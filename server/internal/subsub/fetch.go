package subsub

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxBodySize 是订阅内容的大小上限（文档 6.2）。
//
// 20MB 已经能装下最大的公共规则集（OISD full 约 10MB）。
// 超过就截断并告警，避免一个恶意/异常 URL 把内存吃光。
const MaxBodySize = 20 << 20

// FetchTimeout 是单次拉取的超时。
const FetchTimeout = 30 * time.Second

// FetchResult 是一次拉取的结果。
type FetchResult struct {
	// NotModified 为 true 表示服务端返回 304，内容未变。
	NotModified bool
	Content     []byte
	ETag        string
	LastModified string
	// Truncated 表示内容被大小上限截断。
	Truncated bool
	StatusCode int
}

// userAgent 是拉取订阅时使用的 UA。
//
// 用真实浏览器 UA 而不是 "aegis-dns/1.0"：不少公共规则源（尤其放在
// CDN 后面的）会对未知 UA 返回 403 或挑战页。这与部署侧踩过的坑一致。
const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36"

// Fetcher 拉取订阅内容。
type Fetcher struct {
	client *http.Client
}

// NewFetcher 创建拉取器。
func NewFetcher() *Fetcher {
	return &Fetcher{
		client: &http.Client{
			Timeout: FetchTimeout,
			// 不自动跟随无限重定向，最多 5 跳。
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("重定向次数过多")
				}
				return nil
			},
		},
	}
}

// Fetch 拉取一个订阅。
//
// etag / lastModified 非空时会带上条件请求头，服务端返回 304 就跳过解析
// （文档 6.2 的增量更新前置条件）。
func (f *Fetcher) Fetch(ctx context.Context, url, etag, lastModified string) (*FetchResult, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("订阅地址必须以 http:// 或 https:// 开头")
	}

	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/plain,text/*,*/*")
	// 声明接受 gzip：公共规则集动辄几 MB，压缩能省一大截带宽与时间。
	req.Header.Set("Accept-Encoding", "gzip")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if lastModified != "" {
		req.Header.Set("If-Modified-Since", lastModified)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求订阅失败: %w", err)
	}
	defer resp.Body.Close()

	out := &FetchResult{
		StatusCode:   resp.StatusCode,
		ETag:         resp.Header.Get("ETag"),
		LastModified: resp.Header.Get("Last-Modified"),
	}

	if resp.StatusCode == http.StatusNotModified {
		out.NotModified = true
		return out, nil
	}
	if resp.StatusCode != http.StatusOK {
		// 4xx/5xx 一律视为失败，让调用方保留旧数据（文档 6.4）。
		return nil, fmt.Errorf("订阅返回 HTTP %d", resp.StatusCode)
	}

	var reader io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("解压订阅内容失败: %w", err)
		}
		defer gz.Close()
		reader = gz
	}

	// 多读 1 字节用来判断是否被截断。
	body, err := io.ReadAll(io.LimitReader(reader, MaxBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("读取订阅内容失败: %w", err)
	}
	if len(body) > MaxBodySize {
		body = body[:MaxBodySize]
		out.Truncated = true
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("订阅内容为空")
	}
	out.Content = body
	return out, nil
}

// DescribeStatus 把拉取结果转成入库用的 last_status 文本。
func DescribeStatus(err error) string {
	if err == nil {
		return "ok"
	}
	msg := err.Error()
	// 去掉可能很长的包装前缀，只留核心信息。
	msg = strings.TrimPrefix(msg, "请求订阅失败: ")
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return "error: " + msg
}

// ParseRetryAfter 解析 Retry-After 头（秒）。
func ParseRetryAfter(v string) int {
	if v == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return n
	}
	return 0
}
