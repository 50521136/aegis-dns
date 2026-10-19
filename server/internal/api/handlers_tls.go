package api

import (
	"net/http"
	"strings"
)

// handleAdminGetTLS 返回当前证书状态。
func (s *Server) handleAdminGetTLS(w http.ResponseWriter, r *http.Request) {
	st := s.certs.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"available":   st.Available,
		"self_signed": st.SelfSigned,
		"subject":     st.Subject,
		"issuer":      st.Issuer,
		"dns_names":   orEmptySlice(st.DNSNames),
		"not_before":  st.NotBefore,
		"not_after":   st.NotAfter,
		"days_left":   st.DaysLeft,
		"fingerprint": st.Fingerprint,
		"cert_file":   st.CertFile,
		"key_file":    st.KeyFile,
		// 面板据此提示「DoT/DoH 现在能不能用」。
		"dot_doh_enabled": st.Available,
	})
}

// handleAdminUploadTLS 接收 PEM 文本并热替换证书。
//
// 用 JSON 文本而不是 multipart：前端是一个文本框粘贴 PEM，比选文件更贴合
// 「从证书商那里复制」的实际操作；也避免 multipart 解析的边界问题。
func (s *Server) handleAdminUploadTLS(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CertPEM string `json:"cert_pem"`
		KeyPEM  string `json:"key_pem"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	cert := strings.TrimSpace(req.CertPEM)
	key := strings.TrimSpace(req.KeyPEM)
	if cert == "" || key == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest,
			"cert_pem 与 key_pem 都不能为空", nil)
		return
	}
	if !strings.Contains(cert, "BEGIN CERTIFICATE") {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"cert_pem 里没看到 BEGIN CERTIFICATE，请确认粘贴的是证书（不是私钥或中间证书）", nil)
		return
	}
	if !strings.Contains(key, "PRIVATE KEY") {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"key_pem 里没看到 PRIVATE KEY，请确认粘贴的是私钥", nil)
		return
	}

	if err := s.certs.Replace([]byte(cert+"\n"), []byte(key+"\n")); err != nil {
		writeError(w, http.StatusUnprocessableEntity, CodeValidationFailed, err.Error(), nil)
		return
	}

	// 证书换了不需要重建快照（快照里没有证书），但要把状态回给前端。
	st := s.certs.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"subject":     st.Subject,
		"dns_names":   orEmptySlice(st.DNSNames),
		"not_after":   st.NotAfter,
		"days_left":   st.DaysLeft,
		"fingerprint": st.Fingerprint,
		"self_signed": st.SelfSigned,
	})
}
