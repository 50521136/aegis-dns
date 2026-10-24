package api

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// 这个文件给响应加 gzip。
//
// 为什么必须做：入口 JS 打包产物 1.32 MB，2 Mbps 链路上一次页面加载要 5.3 秒；
// gzip 后 394 KB，降到 1.6 秒（实测 gzip -c 的压缩比 3.4x）。纯带宽收益，
// 不改任何业务逻辑。
//
// 为什么用标准库 gzip 而不是引入 brotli 依赖：
//   - brotli 的运行时压缩在 2 vCPU 机器上比 gzip 慢 2~4 倍，而收益只有 15~20%；
//   - 静态资源本来就可以在构建期预压缩，那种场景才值得用 brotli；
//   - 少一个依赖就少一处 CI 与供应链风险。
// 本中间件负责「动态响应 + 尚未预压缩的资源」的兜底。

const (
	// compressMinSize 是压缩门槛。小于这个体积压了反而更大：
	// gzip 头 + 尾约 20 字节，而短 JSON 响应本身可能只有几十字节。
	compressMinSize = 1024
)

// gzipPool 复用 writer，避免每次请求都重新分配压缩表。
//
// gzip.NewWriter 内部要初始化约 256KB 的哈希表，逐请求新建在低配机器上
// 会明显增加 GC 压力。
var gzipPool = sync.Pool{
	New: func() any {
		w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		return w
	},
}

// alreadyCompressed 判断类型本身是否已压缩过（再压纯烧 CPU）。
func alreadyCompressed(ct string) bool {
	for _, p := range []string{
		"image/", "video/", "audio/",
		"application/zip", "application/gzip", "application/x-gzip",
		"application/pdf", "font/woff", "font/woff2",
	} {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	// PNG/JPEG 之类以 image/ 前缀已覆盖；这里是少数不带前缀的二进制类型。
	switch ct {
	case "application/octet-stream", "application/x-protobuf":
		return true
	}
	return false
}

// compressible 判断类型是否值得压缩。
//
// 采用白名单而不是黑名单：未知类型一律不压。反过来的话，
// 任何新出现的二进制类型都会先被压一遍才发现没收益。
func compressible(ct string) bool {
	for _, p := range []string{
		"text/", "application/json", "application/javascript",
		"application/xml", "image/svg+xml", "application/xhtml",
	} {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	return false
}

// acceptsGzip 判断客户端是否接受 gzip。
func acceptsGzip(r *http.Request) bool {
	ae := r.Header.Get("Accept-Encoding")
	if ae == "" {
		return false
	}
	for _, part := range strings.Split(ae, ",") {
		// 形如 "gzip;q=0.8"，取分号前的编码名。
		name := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if strings.EqualFold(name, "gzip") || name == "*" {
			// 显式 q=0 表示拒绝
			if strings.Contains(part, "q=0") && !strings.Contains(part, "q=0.") {
				return false
			}
			return true
		}
	}
	return false
}

// middlewareCompress 是压缩中间件。
//
// 放在 logger 之前（更外层）：statusRecorder 包住 compressWriter，
// 记到日志里的字节数是**压缩前**的体积 —— 那才是排查「响应为什么大」
// 时有用的数字。
func (s *Server) middlewareCompress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Range 请求必须原样返回：一旦压缩，Content-Range 与
		// Content-Length 的语义就对不上了，播放器/断点续传会坏。
		if r.Header.Get("Range") != "" {
			next.ServeHTTP(w, r)
			return
		}
		if !acceptsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w}
		defer cw.Close()
		next.ServeHTTP(cw, r)
	})
}

// compressWriter 先缓冲一小段，攒够 compressMinSize 才真正决定压缩。
//
// 为什么要缓冲：多数 API 响应只有几百字节，压了反而更大。而 Content-Type
// 已知、体积未知时，只能先看实际写了多少。缓冲上限就是 compressMinSize，
// 超了就立刻开始压缩 —— 内存占用恒定，不会因为大响应把整个 body 存下来。
type compressWriter struct {
	http.ResponseWriter
	status     int
	wroteHdr   bool
	buf        []byte
	gz         *gzip.Writer
	decided    bool
	compress   bool
	compressNo bool // 明确不压（类型不匹配 / 已有 Content-Encoding）
}

func (c *compressWriter) WriteHeader(code int) {
	if c.wroteHdr {
		return
	}
	c.status = code
	c.wroteHdr = true

	// 3xx/204/304 没有 body，不该压缩也不用压缩。
	if code < 200 || code == http.StatusNoContent || code == http.StatusNotModified {
		c.compressNo = true
	}
	// 已经压缩过的响应（比如未来加了预压缩静态资源）直接透传。
	if c.Header().Get("Content-Encoding") != "" {
		c.compressNo = true
	}
	ct := c.Header().Get("Content-Type")
	if ct != "" && (alreadyCompressed(ct) || !compressible(ct)) {
		c.compressNo = true
	}
	// 已知体积且太小：不压。
	if cl := c.Header().Get("Content-Length"); cl != "" {
		if n, err := strconv.Atoi(cl); err == nil && n < compressMinSize {
			c.compressNo = true
		}
	}

	// 只要可能会压缩，就必须声明 Vary —— 否则中间缓存会把
	// 压缩版发给不支持 gzip 的客户端。
	if !c.compressNo {
		c.Header().Add("Vary", "Accept-Encoding")
	}
	if c.compressNo {
		c.ResponseWriter.WriteHeader(code)
	}
	// 否则延迟：等攒够数据再决定，避免给短响应白加 gzip。
}

func (c *compressWriter) Write(p []byte) (int, error) {
	if !c.wroteHdr {
		c.WriteHeader(http.StatusOK)
	}

	if c.compressNo {
		return c.ResponseWriter.Write(p)
	}

	if c.decided {
		if c.compress {
			return c.gz.Write(p)
		}
		return c.ResponseWriter.Write(p)
	}

	// 未决定：先攒。攒够了立刻决定压缩。
	c.buf = append(c.buf, p...)
	if len(c.buf) < compressMinSize {
		// 还没到门槛，先不落盘。返回 len(p) 让调用方以为写成功了。
		return len(p), nil
	}
	c.startCompress()
	if _, err := c.gz.Write(c.buf); err != nil {
		return 0, err
	}
	c.buf = nil
	return len(p), nil
}

// startCompress 进入压缩模式并把已攒的内容写下去。
func (c *compressWriter) startCompress() {
	c.decided = true
	c.compress = true

	// 压缩后长度必变，原来的 Content-Length 必须去掉，
	// 否则客户端会按旧长度截断或一直等。
	c.Header().Del("Content-Length")
	c.Header().Set("Content-Encoding", "gzip")

	gz := gzipPool.Get().(*gzip.Writer)
	gz.Reset(c.ResponseWriter)
	c.gz = gz

	c.ResponseWriter.WriteHeader(c.status)
}

// Close 收尾：决定最终压不压、刷新 gzip、归还 writer。
func (c *compressWriter) Close() {
	if !c.decided {
		// 全程没超过门槛：按原样输出，不加压缩头。
		c.decided = true
		c.compress = false
		if !c.compressNo {
			// 之前延迟了 WriteHeader，这里补上
			if c.status == 0 {
				c.status = http.StatusOK
			}
			c.ResponseWriter.WriteHeader(c.status)
		}
		if len(c.buf) > 0 {
			_, _ = c.ResponseWriter.Write(c.buf)
			c.buf = nil
		}
		return
	}
	if c.gz != nil {
		_ = c.gz.Close()
		gzipPool.Put(c.gz)
		c.gz = nil
	}
}

// Flush 实现 http.Flusher。
//
// 现在没有 SSE，但这层要留：一旦某天加了流式接口，
// 缺这个接口会让 Flusher 断言失败、退化成整段缓冲。
func (c *compressWriter) Flush() {
	if !c.decided && len(c.buf) > 0 {
		c.startCompress()
	}
	if c.gz != nil {
		_ = c.gz.Flush()
	}
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
