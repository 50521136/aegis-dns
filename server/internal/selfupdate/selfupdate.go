// Package selfupdate 通过 GitHub Releases 实现二进制自更新。
//
// 流程（文档第十四章）：
//
//	查 releases/latest  → 比对 semver → 选平台资产 → 下载到临时文件
//	→ 校验 checksums.txt 中的 SHA-256 → 原子替换二进制 → 由 systemd 拉起
//
// 强制的完整性校验是必须的：更新源是第三方 CDN，没有校验就等于把
// 「谁能改 GitHub Release 谁就能在你服务器上执行代码」当成默认前提。
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/minio/selfupdate"

	"github.com/50521136/aegis-dns/server/internal/version"
)

// httpTimeout 是单次 HTTP 请求的超时。
const httpTimeout = 60 * time.Second

// Release 是一次发布的元信息。
type Release struct {
	Tag         string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	Prerelease  bool   `json:"prerelease"`
	Draft       bool   `json:"draft"`
}

// Asset 是发布里的一个产物。
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

// CheckResult 是更新检查结果。
type CheckResult struct {
	Current         string `json:"current"`
	Latest          string `json:"latest"`
	UpdateAvailable bool   `json:"update_available"`
	Notes           string `json:"notes"`
	URL             string `json:"url"`
	SHA256          string `json:"sha256"`
	PublishedAt     string `json:"published_at"`
	Platform        string `json:"platform"`
	// Reason 说明为什么没有可用更新（平台不匹配、已是最新等）。
	Reason string `json:"reason,omitempty"`
}

// client 是带超时的 HTTP 客户端。
//
// 不用 http.DefaultClient：默认没有超时，GitHub API 偶尔挂起会把
// 6 小时一次的检查协程永久卡住（虽然无害，但会让「上次检查时间」失真）。
func client() *http.Client {
	return &http.Client{Timeout: httpTimeout}
}

// apiBase 可在测试中替换。
var apiBase = "https://api.github.com"

// Check 查询 GitHub Releases 并判断是否有可用更新。
//
// component 用于挑选资产前缀，例如 "aegis-dnsd" 或 "aegis-apid"。
// channel 为 "beta" 时会看 prerelease 版本。
func Check(ctx context.Context, repo, component, channel, current string) (*CheckResult, error) {
	if repo == "" {
		return nil, fmt.Errorf("未配置更新仓库（update.repo 为空）")
	}
	platform := runtime.GOOS + "-" + runtime.GOARCH

	rel, err := fetchRelease(ctx, repo, channel)
	if err != nil {
		return nil, err
	}
	latest := strings.TrimPrefix(rel.Tag, "v")

	res := &CheckResult{
		Current:     current,
		Latest:      latest,
		Notes:       rel.Body,
		PublishedAt: rel.PublishedAt,
		Platform:    platform,
	}
	if compareSemver(latest, current) <= 0 {
		res.Reason = "当前已是最新版本"
		return res, nil
	}

	// 按 GOOS-GOARCH 挑资产。
	want := fmt.Sprintf("%s-%s-%s.tar.gz", component, latest, platform)
	assets, err := fetchAssets(ctx, repo, rel.Tag)
	if err != nil {
		return nil, err
	}
	for _, a := range assets {
		if a.Name == want {
			res.UpdateAvailable = true
			res.URL = a.BrowserDownloadURL
			break
		}
	}
	if !res.UpdateAvailable {
		res.Reason = fmt.Sprintf("发布中没有匹配当前平台的资产 %s", want)
		return res, nil
	}

	// 校验和来自同一次发布的 checksums.txt。
	if sum, err := fetchChecksum(ctx, repo, rel.Tag, assets, want); err == nil {
		res.SHA256 = sum
	} else {
		res.Reason = "已找到新版本，但未能取到校验和"
	}
	return res, nil
}

func fetchRelease(ctx context.Context, repo, channel string) (*Release, error) {
	url := apiBase + "/repos/" + repo + "/releases/latest"
	if channel == "beta" {
		url = apiBase + "/repos/" + repo + "/releases?per_page=10"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	// 可选 token：匿名 60 次/小时，带 token 5000 次/小时。
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	resp, err := client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 GitHub API 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	if channel == "beta" {
		var list []Release
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("解析发布列表失败: %w", err)
		}
		for i := range list {
			if !list[i].Draft {
				return &list[i], nil
			}
		}
		return nil, fmt.Errorf("没有可用的发布")
	}

	var rel Release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("解析发布信息失败: %w", err)
	}
	if rel.Tag == "" {
		return nil, fmt.Errorf("发布信息缺少 tag_name")
	}
	return &rel, nil
}

func fetchAssets(ctx context.Context, repo, tag string) ([]Asset, error) {
	url := apiBase + "/repos/" + repo + "/releases/tags/" + tag
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取发布资产失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}
	var payload struct {
		Assets []Asset `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("解析资产列表失败: %w", err)
	}
	return payload.Assets, nil
}

// fetchChecksum 从发布的 checksums.txt 里取指定文件的 SHA-256。
func fetchChecksum(ctx context.Context, repo, tag string, assets []Asset, want string) (string, error) {
	var checksumURL string
	for _, a := range assets {
		if a.Name == "checksums.txt" {
			checksumURL = a.BrowserDownloadURL
			break
		}
	}
	if checksumURL == "" {
		return "", fmt.Errorf("发布中没有 checksums.txt")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, checksumURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 checksums.txt 返回 %d", resp.StatusCode)
	}

	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) != 2 {
			continue
		}
		// 格式：<sha256>  <filename>（sha256sum 默认带两个空格）
		if strings.TrimPrefix(fields[1], "*") == want {
			return strings.ToLower(fields[0]), nil
		}
	}
	return "", fmt.Errorf("checksums.txt 中没有 %s 的记录", want)
}

// Apply 下载并应用更新。
//
// 返回需要重启才能生效（调用方据此决定是否退出进程让 systemd 拉起）。
func Apply(ctx context.Context, url, wantSHA string) error {
	if url == "" {
		return fmt.Errorf("更新地址为空")
	}
	if wantSHA == "" {
		return fmt.Errorf("缺少校验和，拒绝应用未经验证的更新")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client().Do(req)
	if err != nil {
		return fmt.Errorf("下载更新失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载更新返回 %d", resp.StatusCode)
	}

	// 先落到临时文件并校验，再交给 selfupdate 做原子替换。
	tmp, err := os.CreateTemp("", "aegis-update-*.tar.gz")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, 200<<20)); err != nil {
		tmp.Close()
		return fmt.Errorf("写入更新包失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("校验和不匹配：期望 %s，实际 %s（已中止更新）", wantSHA, got)
	}

	// 发布产物是 tar.gz，需要先解出二进制。
	binPath, cleanup, err := extractBinary(tmpName)
	if err != nil {
		return err
	}
	defer cleanup()

	f, err := os.Open(binPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := selfupdate.Apply(f, selfupdate.Options{}); err != nil {
		return fmt.Errorf("替换二进制失败: %w", err)
	}
	return nil
}

// ApplyFile 把解压出的二进制替换到指定路径（用于 apid 更新 dnsd 的场景）。
func ApplyFile(ctx context.Context, url, wantSHA, targetPath string) error {
	if wantSHA == "" {
		return fmt.Errorf("缺少校验和，拒绝应用未经验证的更新")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client().Do(req)
	if err != nil {
		return fmt.Errorf("下载更新失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载更新返回 %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp("", "aegis-update-*.tar.gz")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, 200<<20)); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("校验和不匹配：期望 %s，实际 %s（已中止更新）", wantSHA, got)
	}

	binPath, cleanup, err := extractBinary(tmpName)
	if err != nil {
		return err
	}
	defer cleanup()

	// 备份旧文件，便于回滚。
	if _, err := os.Stat(targetPath); err == nil {
		_ = copyFile(targetPath, targetPath+".bak")
	}
	return copyFile(binPath, targetPath)
}

// RestartService 通过 systemctl 重启指定服务。
func RestartService(ctx context.Context, unit string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("系统没有 systemctl，请手动重启 %s", unit)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemctl", "restart", unit)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl restart %s 失败: %w (%s)", unit, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// extractBinary 从 tar.gz 里解出可执行文件，返回其临时路径。
func extractBinary(tarPath string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "aegis-extract-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { os.RemoveAll(dir) }

	cmd := exec.Command("tar", "xzf", tarPath, "-C", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("解压更新包失败: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	// 找到解出的可执行文件（发布包结构是 <dir>/<binary>）。
	var found string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if info.Mode()&0o111 != 0 {
			found = p
			return io.EOF // 找到即停
		}
		return nil
	})
	if found == "" {
		cleanup()
		return "", func() {}, fmt.Errorf("更新包里没有可执行文件")
	}
	return found, cleanup, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// compareSemver 比较两个语义化版本，返回 -1 / 0 / 1。
//
// 只比较数字段，忽略预发布后缀的细节差异；够用于「有没有新版本」的判断。
// 非法输入（如 "dev"）视为 0，因此开发版本永远会被判定为「有更新」。
func compareSemver(a, b string) int {
	pa := parseSemver(a)
	pb := parseSemver(b)
	for i := 0; i < 3; i++ {
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return 0
}

func parseSemver(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// 去掉预发布/构建元数据。
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, part := range strings.Split(v, ".") {
		if i >= 3 {
			break
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return out
		}
		out[i] = n
	}
	return out
}

// ComponentBinaryName 返回某组件的资产前缀。
func ComponentBinaryName(component string) string { return "aegis-" + component }

// CurrentVersion 返回当前二进制版本。
func CurrentVersion() string { return version.Version }
