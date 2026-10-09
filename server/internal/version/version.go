// Package version 保存编译期注入的版本信息。
//
// 发布流程通过 -ldflags "-X main.version=..." 注入，这里再做一层兜底，
// 保证本地 `go run` 时也有可用值（自更新比对依赖它）。
package version

import "runtime"

// 由 -ldflags "-X github.com/50521136/aegis-dns/server/internal/version.Version=..." 注入。
var (
	// Version 语义化版本号，例如 1.0.0。
	Version = "dev"
	// Commit 构建时的 git commit 短哈希。
	Commit = "none"
	// BuildTime 构建时间（RFC3339）。
	BuildTime = "unknown"
)

// Component 标识当前二进制是 dnsd 还是 apid，由 main 包在启动时设置。
var Component = "unknown"

// Info 返回版本信息的结构化表示。
type Info struct {
	Component string `json:"component"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Get 返回当前二进制的版本信息。
func Get() Info {
	return Info{
		Component: Component,
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// IsDev 表示这是未经 ldflags 注入的本地构建。
func IsDev() bool { return Version == "dev" || Version == "" }
