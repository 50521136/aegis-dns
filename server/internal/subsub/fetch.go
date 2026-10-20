package subsub

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MaxBodySize 是订阅内容的大小上限（文档 6.2）。
//
// 20MB 已经能装下最大的公共规则集（OISD full 约 10MB）。
// 超过就截断并告警，避免一个恶意/异常 URL 把内存吃光。
const MaxBodySize = 20 << 20

// FetchTimeout 是单次拉取的默认超时。
//
// 不能设成 30s：国内机房到公共规则源（OISD、anti-AD 这类放在境外 CDN 后面的）
// 实测只有 20~30 KB/s，OISD 的 domainswild 压缩后 1.7MB 也要 60~90s 才下完，
// 而且波动很大（同一台机器两次实测，一次 62s 一次直接 i/o timeout）。
// 30s / 120s 都会让真实用户的订阅间歇性失败，所以给到 300s。
//
// 拉长超时的代价很小：刷新跑在调度器的后台 goroutine 里，不阻塞任何请求；
// 真正的坏 URL 由 fail_count 与下轮重试兜住。慢线路还可以用
// NewFetcherWithTimeout 单独调。
const FetchTimeout = 300 * time.Second

// FetchResult 是一次拉取的结果。
type FetchResult struct {
	// NotModified 为 true 表示服务端返回 304，内容未变。
	NotModified  bool
	Content      []byte
	ETag         string
	LastModified string
	// Truncated 表示内容被大小上限截断。
	Truncated  bool
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

// NewFetcher 用默认超时创建拉取器。
func NewFetcher() *Fetcher {
	return NewFetcherWithTimeout(FetchTimeout)
}

// NewFetcherWithTimeout 用指定超时创建拉取器；d <= 0 时退回默认值。
func NewFetcherWithTimeout(d time.Duration) *Fetcher {
	if d <= 0 {
		d = FetchTimeout
	}
	return &Fetcher{
		client: &http.Client{
			Timeout: d,
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

// timeout 返回本拉取器的超时，供 ctx 复用。
func (f *Fetcher) timeout() time.Duration {
	if f.client != nil && f.client.Timeout > 0 {
		return f.client.Timeout
	}
	return FetchTimeout
}

// fetchAttempts 是网络层失败的重试次数。
//
// 境外公共规则源的 CDN 会间歇性重置连接（实测 read: connection reset by peer），
// 单次失败不重试的话，用户要等下一个 24 小时周期才可能成功 —— 体感就是「订阅坏了」。
// 只对网络层错误重试；HTTP 4xx/5xx 与内容错误一律不重试（重试也不会变好）。
const fetchAttempts = 3

// githubMirrors 是 raw.githubusercontent.com 的镜像前缀。
//
// 国内机房直连该域名基本不通（实测 IPv4/IPv6 都是 45s 超时、HTTP 000），
// 而推荐列表里的公共规则源大多托管在 GitHub raw —— 没有回退等于全都用不了。
// 顺序按实测速度排；镜像只是公共规则列表的转发，不涉及用户隐私数据。
var githubMirrors = []string{
	"https://gh-proxy.com/",
	"https://ghproxy.net/",
}

// isGitHubRaw 判断是否为 GitHub raw 地址。
//
// 必须按 host 判断而不是子串：已经套了代理的地址（如
// https://cdn.example.com/https://raw.githubusercontent.com/...）里也含这个子串，
// 用 Contains 会把它再套一层镜像，变成双重代理。
func isGitHubRaw(u string) bool {
	pu, err := url.Parse(u)
	if err != nil {
		return false
	}
	switch strings.ToLower(pu.Host) {
	case "raw.githubusercontent.com":
		return true
	case "github.com":
		return strings.Contains(pu.Path, "/raw/")
	}
	return false
}

// retryBackoff 是第 n 次重试前的等待。
func retryBackoff(n int) time.Duration {
	switch n {
	case 1:
		return 2 * time.Second
	case 2:
		return 6 * time.Second
	default:
		return 15 * time.Second
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

	// 与 client.Timeout 保持一致：client 的超时是整轮（含重定向），
	// ctx 的超时覆盖单次请求，两者取小更安全。
	ctx, cancel := context.WithTimeout(ctx, f.timeout())
	defer cancel()

	// GitHub raw 先走镜像：直连在这类机房上是确定不通的，先试直连只会白等几分钟。
	if isGitHubRaw(url) {
		for _, m := range githubMirrors {
			res, err := f.tryWithRetry(ctx, m+url, etag, lastModified)
			if err == nil {
				return res, nil
			}
			// 镜像返回 4xx/5xx 属于确定性失败，换下一个镜像
		}
	}
	return f.tryWithRetry(ctx, url, etag, lastModified)
}

// tryWithRetry 带网络层重试地拉取一次。
//
// 只在请求没拿到响应时重试，避免重复下载已经成功的内容；
// 带状态码的错误（HTTP 4xx/5xx）重试没有意义，直接返回。
func (f *Fetcher) tryWithRetry(ctx context.Context, url, etag, lastModified string) (*FetchResult, error) {
	var lastErr error
	for attempt := 1; attempt <= fetchAttempts; attempt++ {
		res, err := f.fetchOnce(ctx, url, etag, lastModified)
		if err == nil {
			return res, nil
		}
		lastErr = err
		var se *statusError
		if errors.As(err, &se) {
			return nil, err
		}
		if attempt < fetchAttempts {
			select {
			case <-time.After(retryBackoff(attempt)):
			case <-ctx.Done():
				return nil, fmt.Errorf("请求订阅失败: %w", ctx.Err())
			}
		}
	}
	return nil, lastErr
}

// statusError 表示订阅源返回了非 200 状态码，属于确定性失败，不应重试。
type statusError struct {
	Code int
}

func (e *statusError) Error() string { return fmt.Sprintf("订阅返回 HTTP %d", e.Code) }

// fetchOnce 是一次完整的拉取尝试。
func (f *Fetcher) fetchOnce(ctx context.Context, url, etag, lastModified string) (*FetchResult, error) {
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
		return nil, &statusError{Code: resp.StatusCode}
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

	// 超时/连接类错误换成人话：管理员看到 "context deadline exceeded"
	// 根本判断不出是源站被墙、DNS 有问题还是自己网络抖动。
	switch {
	case strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "i/o timeout"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no such host"):
		msg = "拉取超时或连接失败，订阅源可能不可达（境外地址建议换成国内可达的镜像源）"
	}

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
