package api

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/50521136/aegis-dns/server/internal/selfupdate"
)

// 这个文件处理「在面板里改 DoH（HTTPS）监听端口」。
//
// 为什么不能只是写个值：
//
//	dnsd 在启动时 bind 端口，改配置必须先过一遍占用检测再重启，
//	否则新端口被别的程序占着时 dnsd 起不来 —— 而那时用户正在用这台
//	DNS 解析域名，停了就是全网断网，连面板都可能打不开。
//
// 所以流程是「先探测 → 再写 → 重启 → 校验 → 失败回滚」：
// 任何一步不成立都把配置还原并重新拉起旧端口，绝不让服务停在半路。

// portCheck 是单个端口的可用性判定结果。
type portCheck struct {
	Port    int    `json:"port"`
	OK      bool   `json:"ok"`
	Reason  string `json:"reason"`
	Current bool   `json:"current"` // 是否就是当前在用的端口
}

// parseListenPort 从 ":443" / "0.0.0.0:443" / "127.0.0.1:443" 里取端口号。
func parseListenPort(addr string) int {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return 0
	}
	i := strings.LastIndexByte(addr, ':')
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(addr[i+1:])
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

// formatListen 保持原有的监听地址形态，只替换端口号。
//
// 保留 host 部分很重要：有人会把 dnsd 绑到 127.0.0.1 前面挂反代，
// 一律重写成 ":port" 会把那种部署改坏。
func formatListen(oldAddr string, port int) string {
	oldAddr = strings.TrimSpace(oldAddr)
	if oldAddr == "" {
		return fmt.Sprintf(":%d", port)
	}
	i := strings.LastIndexByte(oldAddr, ':')
	if i < 0 {
		return fmt.Sprintf(":%d", port)
	}
	host := oldAddr[:i]
	return fmt.Sprintf("%s:%d", host, port)
}

// probePort 判断端口当前能否被绑定。
//
// selfPorts 是「本次重启后我们期望占用的全部端口」—— 那些端口即使
// 现在被占着（被旧的 dnsd 自己占），也不该判成冲突，否则每次保存都会
// 报「端口已被占用」。
func probePort(port int, selfPorts map[int]bool) portCheck {
	res := portCheck{Port: port}
	if port < 1 || port > 65535 {
		res.Reason = "端口必须在 1-65535 之间"
		return res
	}
	if selfPorts[port] {
		res.OK = true
		res.Current = true
		res.Reason = "当前占用（就是本服务自己）"
		return res
	}
	// 试绑 TCP。DoH 是 TCP 服务，这条判定足够。
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		res.Reason = describeBindError(err, port)
		return res
	}
	_ = ln.Close()
	res.OK = true
	res.Reason = "可用"
	return res
}

// describeBindError 把 bind 失败翻译成能直接行动的结论。
func describeBindError(err error, port int) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "address already in use"):
		if owner := portOwner(port); owner != "" {
			return fmt.Sprintf("端口 %d 已被其他程序占用（%s）", port, owner)
		}
		return fmt.Sprintf("端口 %d 已被其他程序占用", port)
	case strings.Contains(msg, "permission denied"):
		return fmt.Sprintf("端口 %d 需要 root 权限才能监听（1024 以下）", port)
	case strings.Contains(msg, "cannot assign requested address"):
		return fmt.Sprintf("端口 %d 的本机地址不可用，换一个端口或检查绑定地址", port)
	default:
		return fmt.Sprintf("端口 %d 不可用：%s", port, msg)
	}
}

// portOwner 尽力找出占用端口的进程名，仅用于提示，查不到就返回空。
func portOwner(port int) string {
	// 用 /proc 而不是调用 ss：apid 里执行外部命令要考虑 PATH 和权限，
	// 而 /proc/net/tcp 一定存在，代价也小。
	f, err := os.Open("/proc/net/tcp")
	if err != nil {
		return ""
	}
	defer f.Close()
	target := fmt.Sprintf(":%04X", port)
	buf := make([]byte, 64*1024)
	n, _ := f.Read(buf)
	lines := strings.Split(string(buf[:n]), "\n")
	for _, ln := range lines[1:] {
		fields := strings.Fields(ln)
		if len(fields) < 4 {
			continue
		}
		// 第 2 列是 local_address(hex:port)，第 4 列是 st（0A = LISTEN）
		if !strings.HasSuffix(fields[1], target) {
			continue
		}
		if fields[3] != "0A" {
			continue
		}
		if len(fields) >= 10 {
			if name := procNameByInode(fields[9]); name != "" {
				return name
			}
		}
		return "未知程序"
	}
	return ""
}

// procNameByInode 通过 socket inode 反查进程名。
func procNameByInode(inode string) string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}
	want := "socket:[" + inode + "]"
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		fdDir := filepath.Join("/proc", e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || link != want {
				continue
			}
			if b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm")); err == nil {
				return strings.TrimSpace(string(b))
			}
			return ""
		}
	}
	return ""
}

// dnsPortSnapshot 返回 dnsd 当前配置的全部监听端口（用于冲突判定）。
func (s *Server) dnsPortSnapshot() map[int]bool {
	out := map[int]bool{}
	for _, addr := range []string{
		s.cfg.DNS.ListenUDP, s.cfg.DNS.ListenTCP,
		s.cfg.DNS.ListenDOT, s.cfg.DNS.ListenDOH, s.cfg.DNS.ListenDOHPlain,
	} {
		if p := parseListenPort(addr); p > 0 {
			out[p] = true
		}
	}
	return out
}

// apiPorts 返回 apid 自己占用的端口，改 dnsd 端口时必须避开。
func (s *Server) apiPorts() map[int]bool {
	out := map[int]bool{}
	for _, addr := range []string{s.cfg.API.Listen, s.cfg.API.TLSListen} {
		if p := parseListenPort(addr); p > 0 {
			out[p] = true
		}
	}
	return out
}

// validateDoHPort 做全部校验，返回错误说明（空表示通过）。
func (s *Server) validateDoHPort(port int) (portCheck, error) {
	if port < 1 || port > 65535 {
		return portCheck{Port: port}, fmt.Errorf("端口必须在 1-65535 之间")
	}
	// 和其它 DNS 端口撞车：dnsd 是自己一个进程绑全部端口，
	// 两个协议用同一个端口必然有一个绑不上，dnsd 直接起不来。
	for name, addr := range map[string]string{
		"明文 UDP": s.cfg.DNS.ListenUDP,
		"明文 TCP": s.cfg.DNS.ListenTCP,
		"DoT":    s.cfg.DNS.ListenDOT,
	} {
		if p := parseListenPort(addr); p == port {
			return portCheck{Port: port},
				fmt.Errorf("端口 %d 已被 %s 使用，同一进程内不能复用", port, name)
		}
	}
	if s.apiPorts()[port] {
		return portCheck{Port: port},
			fmt.Errorf("端口 %d 是面板自己的监听端口，改了会打不开管理界面", port)
	}
	return probePort(port, s.dnsPortSnapshot()), nil
}

// applyDoHPort 写入配置并重启 dnsd，失败则整体回滚。
//
// 返回值是给界面看的结论，不是错误 —— 失败时会带上回滚结果，
// 让管理员知道服务现在处于什么状态（这比他猜「到底停没停」重要）。
type applyPortResult struct {
	OK       bool   `json:"ok"`
	Port     int    `json:"port"`
	Message  string `json:"message"`
	Rollback bool   `json:"rollback"`
}

func (s *Server) applyDoHPort(port int) applyPortResult {
	cfgPath := s.cfg.Path
	if cfgPath == "" {
		return applyPortResult{OK: false, Message: "找不到配置文件路径，无法修改"}
	}
	oldAddr := s.cfg.DNS.ListenDOH
	newAddr := formatListen(oldAddr, port)

	// 备份原文：回滚要按字节还原，不能重新序列化 ——
	// YAML 重新 marshal 会丢掉全部注释，用户的配置文件就废了。
	orig, err := os.ReadFile(cfgPath)
	if err != nil {
		return applyPortResult{OK: false, Port: port, Message: "读取配置文件失败: " + err.Error()}
	}
	if err := replaceListenDO(orig, cfgPath, newAddr); err != nil {
		return applyPortResult{OK: false, Port: port, Message: err.Error()}
	}

	// 重启 dnsd 让新端口生效。只重启 dnsd —— apid 还要负责回滚和报错。
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := selfupdate.RestartService(ctx, "aegis-dnsd"); err != nil {
		// 重启都失败，立刻还原
		_ = os.WriteFile(cfgPath, orig, 0o600)
		_ = selfupdate.RestartService(ctx, "aegis-dnsd")
		return applyPortResult{OK: false, Port: port, Rollback: true,
			Message: "重启 dnsd 失败，已回滚到原端口 " + strconv.Itoa(parseListenPort(oldAddr)) + "：" + err.Error()}
	}

	// 重启成功不代表绑上了：dnsd 可能起来但新端口绑定失败。
	// 给它一点时间再确认端口真的在监听。
	if !waitListening(port, 8*time.Second) {
		_ = os.WriteFile(cfgPath, orig, 0o600)
		_ = selfupdate.RestartService(ctx, "aegis-dnsd")
		return applyPortResult{OK: false, Port: port, Rollback: true,
			Message: fmt.Sprintf("dnsd 已重启但 %d 端口没有进入监听（可能被别的程序抢占了），已回滚到原端口", port)}
	}

	s.cfg.DNS.ListenDOH = newAddr
	return applyPortResult{OK: true, Port: port,
		Message: fmt.Sprintf("DoH 已切到 %d 端口，dnsd 已重启生效", port)}
}

// replaceListenDO 只改 config.yaml 里 listen_doh 那一行。
//
// 用逐行替换而不是 YAML marshal：marshal 会把注释、字段顺序、
// 引号风格全部重写一遍，用户的配置文件会被无声改写。
func replaceListenDO(orig []byte, cfgPath, newAddr string) error {
	lines := strings.Split(string(orig), "\n")
	pat := regexp.MustCompile(`^(\s*listen_doh:\s*)(.*)$`)
	found := false
	for i, ln := range lines {
		m := pat.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		lines[i] = m[1] + strconv.Quote(newAddr)
		found = true
		break
	}
	if !found {
		return fmt.Errorf("配置文件里没有 listen_doh 项，请手工添加后再试")
	}
	out := []byte(strings.Join(lines, "\n"))

	// 先写临时文件再原子替换：直接写原文件的话，写到一半崩溃
	// 会留下半截 YAML，dnsd 下次就起不来了。
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("写入临时配置失败: %w", err)
	}
	if err := os.Rename(tmp, cfgPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	return nil
}

// waitListening 等待端口进入监听状态。
func waitListening(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}
