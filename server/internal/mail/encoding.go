package mail

import (
	"encoding/base64"
	"strings"
)

// base64Raw 返回不带换行的 base64 编码（用于头部编码）。
func base64Raw(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// base64Wrap 返回按 76 列折行的 base64 编码（用于正文）。
//
// 不折行的话整封正文是一行几万字符，部分老式 MTA 会直接拒收，
// 而且报错通常只说「格式错误」，很难定位到是折行问题。
func base64Wrap(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	if enc != "" {
		b.WriteString(enc)
	}
	return b.String()
}
