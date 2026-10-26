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
	"sync"
	"sync/atomic"
	"time"

	"github.com/50521136/aegis-dns/server/internal/netproxy"
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
	// client 是直连客户端；proxyClient 走代理（未启用时为 nil）。
	//
	// 两个都留着是为了实现「代理优先、直连兜底」：只保留一个 Transport 的话，
	// 代理一挂订阅就彻底拉不动，而很多源其实直连也能通 —— 反过来，只走直连
	// 时 GitHub 这类源在国内机房又完全不通。两条路都握着才能逐次尝试。
	client      *http.Client
	proxyClient *http.Client
	// proxy 是当前出口代理（http/https/socks5），空表示直连。
	// 用 atomic 是因为设置页改完要立刻生效，而拉取跑在后台 goroutine 里 ——
	// 重建 Fetcher 会丢掉正在进行的下载。
	proxy atomic.Pointer[string]
	// mu 护 client.Timeout 的并发读写：SetTimeout 在设置保存时调用，
	// 而 timeout() 在每次拉取时读，两者会并发。
	mu sync.Mutex
}

// SetProxy 设置出口代理；空字符串表示直连。返回错误表示地址不合法（此时不改动）。
func (f *Fetcher) SetProxy(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		f.proxyClient = nil
		empty := ""
		f.proxy.Store(&empty)
		return nil
	}
	tr, err := netproxy.Transport(raw, nil)
	if err != nil {
		return err
	}
	f.proxyClient = &http.Client{
		Timeout:       f.client.Timeout,
		Transport:     tr,
		CheckRedirect: f.client.CheckRedirect,
	}
	f.proxy.Store(&raw)
	return nil
}

// Proxy 返回当前代理地址。
func (f *Fetcher) Proxy() string {
	if v := f.proxy.Load(); v != nil {
		return *v
	}
	return ""
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
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.client != nil && f.client.Timeout > 0 {
		return f.client.Timeout
	}
	return FetchTimeout
}

// SetTimeout 更新默认超时。
//
// 同时改 client.Timeout 与 ctx 预算：这两个都要一致，否则
// 「client 先超时」和「ctx 先超时」会给出不同的错误形态，
// 排查时很难判断是网络问题还是配置问题。
func (f *Fetcher) SetTimeout(d time.Duration) {
	if d <= 0 || f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.client != nil {
		f.client.Timeout = d
	}
	if f.proxyClient != nil {
		f.proxyClient.Timeout = d
	}
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

// githubPagesBranches 是映射 GitHub Pages 时依次尝试的分支名。
//
// Pages 的源分支无从得知，只能试。main 是现在的默认，master 是老仓库。
var githubPagesBranches = []string{"main", "master"}

// githubPagesCandidates 把 GitHub Pages 项目页地址映射成 raw 等价地址。
//
//	<org>.github.io/<repo>/<path> → raw.githubusercontent.com/<org>/<repo>/<branch>/<path>
//
// 为什么必须做：AdGuard 官方规则库整站都在 adguardteam.github.io 上，而国内机房
// 直连 github.io 基本不通（实测 60s 超时、零字节），也不在现有镜像的支持范围里
// （gh-proxy 对 github.io 直接回 404）。但同一份内容换成 raw 形式、走镜像，
// 实测 2.3 秒就能拉完 4.7MB。
//
// 不是项目页（没有 repo 段）时返回 nil：那属于用户主页，映射规则不同，
// 猜错只会拉到 404，不如老实回退到原地址。
func githubPagesCandidates(u string) []string {
	pu, err := url.Parse(u)
	if err != nil {
		return nil
	}
	host := strings.ToLower(pu.Host)
	if !strings.HasSuffix(host, ".github.io") {
		return nil
	}
	org := strings.TrimSuffix(host, ".github.io")
	if org == "" {
		return nil
	}
	parts := strings.Split(strings.Trim(pu.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		return nil
	}
	repo := parts[0]
	rest := strings.Join(parts[1:], "/")
	out := make([]string, 0, len(githubPagesBranches))
	for _, br := range githubPagesBranches {
		out = append(out, fmt.Sprintf(
			"https://raw.githubusercontent.com/%s/%s/%s/%s", org, repo, br, rest))
	}
	return out
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
//
// 代理与直连的兜底在**这一层**做：先让当前客户端（可能带代理）完整跑完一轮
// 候选地址与重试，整轮都失败再换另一个客户端重跑一轮。
//
// 为什么不在 fetchOnce 里逐次兜底：那会让每次网络尝试都翻倍，而这里本身还有
// 「两个镜像 × 两个分支 × 3 次重试」的嵌套，最坏情况会从 12 次尝试膨胀到 24 次，
// 直接把 300 秒的 FetchTimeout 耗尽 —— 表现出来就是订阅永远卡在「同步中」。
func (f *Fetcher) Fetch(ctx context.Context, url, etag, lastModified string) (*FetchResult, error) {
	return f.FetchTimeout(ctx, url, etag, lastModified, 0)
}

// FetchTimeout 用指定超时拉取；timeout <= 0 时用 Fetcher 自己的默认值。
//
// 为什么要能按次覆盖：默认超时改短（20 秒）之后，几个慢速大源必然失败，
// 而它们是全局订阅 —— 失败意味着十几万条规则永远停在旧版本。
// 按订阅单独放宽是这个矛盾的唯一出路（见 migrations/0021）。
func (f *Fetcher) FetchTimeout(ctx context.Context, url, etag, lastModified string, timeout time.Duration) (*FetchResult, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("订阅地址必须以 http:// 或 https:// 开头")
	}

	budget := f.timeout()
	if timeout > 0 {
		budget = timeout
	}

	// 与 client.Timeout 保持一致：client 的超时是整轮（含重定向），
	// ctx 的超时覆盖单次请求，两者取小更安全。
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	// 第一轮用当前客户端；开了代理时它是代理客户端。
	primary, primaryName := f.client, "direct"
	if pc := f.proxyClient; pc != nil {
		primary, primaryName = pc, "proxy"
	}

	// 开代理时把预算对半分：代理卡死不能把整个超时吃光，否则直连兜底永远
	// 轮不上（实测一条代理不响应时，代理轮会把 300 秒耗尽，订阅一直显示
	// 「同步中」直到超时）。对半分的代价是「大文件下载时间减半」，但这比
	// 「兜底形同虚设」好得多 —— 正常代理拉公共规则集只要几秒。
	proxyCtx := ctx
	if f.proxyClient != nil {
		var cancelProxy context.CancelFunc
		proxyCtx, cancelProxy = context.WithTimeout(ctx, budget/2)
		defer cancelProxy()
	}

	res, err := f.fetchRound(proxyCtx, primary, primaryName, url, etag, lastModified)
	if err == nil {
		return res, nil
	}
	// 拿到 HTTP 状态码说明链路是通的，换条路不会变好，还会盖掉真实原因。
	var se *statusError
	if errors.As(err, &se) {
		return nil, err
	}
	if f.proxyClient == nil {
		return nil, err
	}

	// 代理这一轮整轮失败 → 直连再跑一轮（用父 ctx 剩余的时间）。
	res2, err2 := f.fetchRound(ctx, f.client, "direct", url, etag, lastModified)
	if err2 == nil {
		return res2, nil
	}
	return nil, fmt.Errorf("代理与直连均失败（%s: %v；直连: %v）", primaryName, err, err2)
}

// fetchRound 用一个固定客户端跑完一整轮候选地址（镜像、Pages 映射、原地址）。
func (f *Fetcher) fetchRound(ctx context.Context, client *http.Client, via, url, etag, lastModified string) (*FetchResult, error) {
	// GitHub raw 先走镜像：直连在这类机房上是确定不通的，先试直连只会白等几分钟。
	if isGitHubRaw(url) {
		for _, m := range githubMirrors {
			res, err := f.tryWithRetry(ctx, client, via, m+url, etag, lastModified)
			if err == nil {
				return res, nil
			}
			// 镜像返回 4xx/5xx 属于确定性失败，换下一个镜像
		}
	}

	// GitHub Pages（*.github.io）先映射成 raw 再走镜像。
	// 这类地址在公共规则源里极常见，而现有镜像都不支持 github.io 本身。
	for _, cand := range githubPagesCandidates(url) {
		for _, m := range githubMirrors {
			if res, err := f.tryWithRetry(ctx, client, via, m+cand, etag, lastModified); err == nil {
				return res, nil
			}
		}
		// 镜像全挂再试一次 raw 直连（少数机房 raw 反而通）
		if res, err := f.tryWithRetry(ctx, client, via, cand, etag, lastModified); err == nil {
			return res, nil
		}
	}

	return f.tryWithRetry(ctx, client, via, url, etag, lastModified)
}

// tryWithRetry 带网络层重试地拉取一次。
//
// 只在请求没拿到响应时重试，避免重复下载已经成功的内容；
// 带状态码的错误（HTTP 4xx/5xx）重试没有意义，直接返回。
func (f *Fetcher) tryWithRetry(ctx context.Context, client *http.Client, via, url, etag, lastModified string) (*FetchResult, error) {
	var lastErr error
	for attempt := 1; attempt <= fetchAttempts; attempt++ {
		res, err := f.fetchWith(ctx, client, via, url, etag, lastModified)
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

// fetchWith 用指定客户端拉一次；via 只用于错误信息里标明走的哪条路。
func (f *Fetcher) fetchWith(ctx context.Context, client *http.Client, via, url, etag, lastModified string) (*FetchResult, error) {
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

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求订阅失败(%s): %w", via, err)
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
//
// 格式固定为「error: <短码> | <详情>」：短码给列表徽章显示（必须短，否则会把
// 表格挤变形），详情给 toast 展开。两段都用竖线分隔，前端按第一个竖线切。
//
// 之所以把短码和详情都塞进同一个字段而不是拆成两列：last_status 已经落库并且
// 有历史数据，加列要做迁移；而这里只要约定一个分隔符就能同时满足「列表短」
// 和「详情全」两个需求。
func DescribeStatus(err error) string {
	if err == nil {
		return "ok"
	}
	code, detail := classifyError(err)
	detail = truncateRunes(detail, 300)
	return "error: " + code + " | " + detail
}

// classifyError 把错误拆成「短码」与「可读详情」。
func classifyError(err error) (code, detail string) {
	msg := err.Error()
	// 去掉可能很长的包装前缀，只留核心信息。
	msg = strings.TrimPrefix(msg, "请求订阅失败: ")
	msg = strings.TrimPrefix(msg, "请求订阅失败(proxy): ")
	msg = strings.TrimPrefix(msg, "请求订阅失败(direct): ")

	var se *statusError
	if errors.As(err, &se) {
		return fmt.Sprintf("HTTP %d", se.Code), httpStatusHint(se.Code)
	}

	switch {
	case strings.Contains(msg, "代理与直连均失败"):
		// 两条路都挂了：短码点明这点，详情保留两边原因。
		return "代理/直连均失败", msg
	case strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "i/o timeout"):
		return "超时", "拉取超时，订阅源可能不可达。境外地址建议换成国内可达的镜像源。原始错误：" + msg
	case strings.Contains(msg, "connection refused"):
		return "连接被拒", "连接被拒绝：代理端口没在监听，或地址/端口写错。原始错误：" + msg
	case strings.Contains(msg, "no such host"):
		return "解析失败", "域名解析失败：代理主机名写错，或本机 DNS 有问题。原始错误：" + msg
	case strings.Contains(msg, "connection reset"):
		return "连接重置", "连接被重置：链路被中间设备中断。原始错误：" + msg
	case strings.Contains(msg, "tls"), strings.Contains(msg, "x509"):
		return "证书错误", "TLS 握手失败：证书校验不过，或代理做了 TLS 拦截。原始错误：" + msg
	case strings.Contains(msg, "订阅内容为空"):
		return "内容为空", "订阅源返回了 200 但内容为空。原始错误：" + msg
	case strings.Contains(msg, "解压订阅内容失败"):
		return "解压失败", "订阅内容声明 gzip 但无法解压，可能是被中间设备改写。原始错误：" + msg
	}
	return "失败", msg
}

// httpStatusHint 给常见状态码补一句人话。
func httpStatusHint(code int) string {
	switch code {
	case 401, 403:
		return fmt.Sprintf("订阅源返回 HTTP %d：被源站拒绝。多为需要鉴权、UA 被拦或触发了限流，也可能是代理出口 IP 被该源拉黑。", code)
	case 404:
		return fmt.Sprintf("订阅源返回 HTTP %d：地址不存在，检查链接是否失效或仓库改名。", code)
	case 429:
		return fmt.Sprintf("订阅源返回 HTTP %d：请求过于频繁，被限流。建议把拉取间隔调大。", code)
	}
	if code >= 500 {
		return fmt.Sprintf("订阅源返回 HTTP %d：源站自身故障，稍后重试即可。", code)
	}
	return fmt.Sprintf("订阅源返回 HTTP %d。", code)
}

// truncateRunes 按字符（不是字节）截断，避免把中文截成乱码。
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
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
