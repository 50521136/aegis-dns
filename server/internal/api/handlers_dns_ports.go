package api

import (
	"net/http"
)

// DoH 端口的面板接口。
//
// 分成三个端点而不是一个：
//   - GET  查当前值（页面加载）
//   - POST /check 单独做占用探测（输入框失焦就查，别等点保存才知道冲突）
//   - PUT  真正写入 + 重启
//
// 分开的理由很实际：改 DNS 端口是「改错就全网断网」的操作，
// 必须在动手之前就能看到「这个端口能不能用」，而不是保存后才报错。

// handleGetDNSPorts 返回 dnsd 当前的监听端口。
func (s *Server) handleGetDNSPorts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"listen_udp":       s.cfg.DNS.ListenUDP,
		"listen_tcp":       s.cfg.DNS.ListenTCP,
		"listen_dot":       s.cfg.DNS.ListenDOT,
		"listen_doh":       s.cfg.DNS.ListenDOH,
		"listen_doh_plain": s.cfg.DNS.ListenDOHPlain,
		"doh_port":         parseListenPort(s.cfg.DNS.ListenDOH),
		"dot_port":         parseListenPort(s.cfg.DNS.ListenDOT),
		// 面板自己的监听端口：界面要把它列进「不能占用」的清单
		"panel_listen":     s.cfg.API.Listen,
		"panel_tls_listen": s.cfg.API.TLSListen,
		// 当前生效的图：告诉界面「这几个端口是本服务自己的，不算冲突」
		"in_use_by_dnsd":  s.dnsPortSnapshot(),
		"in_use_by_panel": s.apiPorts(),
	})
}

// handleCheckDNSPort 探测一个候选 DoH 端口是否可用。
//
// 只读：不改配置、不重启，可以随便调。
func (s *Server) handleCheckDNSPort(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Port int `json:"port"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	check, err := s.validateDoHPort(req.Port)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"port": req.Port, "ok": false, "reason": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"port": check.Port, "ok": check.OK,
		"current": check.Current, "reason": check.Reason,
	})
}

// handleUpdateDNSPort 修改 DoH（HTTPS）监听端口。
//
// 校验在写入之前全部做完：占用、与其它协议撞车、面板自己占用。
// 任何一条不过就直接 4xx 拒绝，绝不留一个「配置改了但服务起不来」的中间态。
func (s *Server) handleUpdateDNSPort(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DoHPort int `json:"doh_port"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}

	cur := parseListenPort(s.cfg.DNS.ListenDOH)
	if req.DoHPort == cur {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "port": cur, "message": "端口未变化，无需重启",
		})
		return
	}

	// 先做完整校验；不通过一律不动配置
	check, err := s.validateDoHPort(req.DoHPort)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}
	if !check.OK {
		writeError(w, http.StatusConflict, CodeValidationFailed, check.Reason, nil)
		return
	}

	res := s.applyDoHPort(req.DoHPort)
	if !res.OK {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok": false, "port": req.DoHPort, "message": res.Message, "rollback": res.Rollback,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "port": req.DoHPort, "message": res.Message,
	})
}
