// Package webdist 以 go:embed 携带前端构建产物。
//
// 这是「前端挂了不影响 DNS」的最后一块拼图（文档 R7）：
// 前端不再是独立部署物，它随 apid 二进制一起发布，
// 部署一个版本 = 换两个二进制文件，没有「忘记上传前端」这个故障面。
//
// 仓库里提交的是一个占位 index.html；发布流程会把 web/dist 的内容
// 覆盖进来再编译（见 .github/workflows/release.yml）。
package webdist

import (
	"embed"
	"io/fs"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// FS 返回前端静态资源的文件系统（已剥掉 dist 前缀）。
func FS() (fs.FS, error) {
	return fs.Sub(embedded, "dist")
}

// HasIndex 判断构建产物里是否真的带上了前端页面。
//
// 仓库里只有 dist/.gitkeep 占位，因此从源码直接编译出的 apid 会返回 false，
// 此时 HTTP 层会给出一个可操作的提示页，而不是让用户看到 404 空白页
// 然后去查 nginx 配置（本项目根本不用 nginx）。
func HasIndex() bool {
	sub, err := FS()
	if err != nil {
		return false
	}
	f, err := sub.Open("index.html")
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	if n == 0 {
		return false
	}
	body := string(buf[:n])
	// 注意不能只按字节截断找 "assets/"：真实 index.html 开头是一段内联的
	// 主题初始化脚本，资源引用在它后面。这里同时接受几个稳定的特征串。
	return strings.Contains(body, "assets/") ||
		strings.Contains(body, `id="root"`) ||
		strings.Contains(body, "DNSForge")
}
