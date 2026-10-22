package mail

import (
	"fmt"
	"strings"
)

// 邮件模板。
//
// 全部用内联样式 + table 布局：邮件客户端（尤其是 QQ 邮箱、网易、Outlook）
// 对 <style> 和 flex/grid 的支持一塌糊涂，外链 CSS 更是直接被剥掉。
// 想「好看」只有一条路：把样式写在每个元素的 style 属性里，用 table 排。
//
// 配色沿用面板的主题色，用户一眼能认出是这套系统发的。

const (
	brandName   = "DNSForge"
	colorInk    = "#1a1a1f" // 正文
	colorMute   = "#6b7280" // 次要文字
	colorLine   = "#e5e7eb" // 分隔线
	colorBg     = "#f4f5f7" // 页面底色
	colorCard   = "#ffffff"
	colorBrand  = "#4f46e5" // 主色（靛蓝）
	colorDanger = "#dc2626"
	colorOk     = "#059669"
	colorWarn   = "#d97706"
)

// wrapEmail 把正文塞进统一的外壳：底色、居中卡片、页脚。
func wrapEmail(title, inner string) string {
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="UTF-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width,initial-scale=1">`)
	b.WriteString(`<title>` + htmlEscape(title) + `</title></head>`)
	b.WriteString(`<body style="margin:0;padding:0;background:` + colorBg + `;">`)
	// 外层 100% 宽的表，内层固定 560px：这是邮件客户端里最稳的居中方式，
	// max-width + margin:auto 在 Outlook 里会失效。
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:` + colorBg + `;padding:24px 12px;">`)
	b.WriteString(`<tr><td align="center">`)
	b.WriteString(`<table role="presentation" width="560" cellpadding="0" cellspacing="0" border="0" style="width:100%;max-width:560px;background:` + colorCard + `;border-radius:14px;overflow:hidden;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Hiragino Sans GB','Microsoft YaHei',sans-serif;">`)

	// 顶部色条 + 品牌
	b.WriteString(`<tr><td style="background:` + colorBrand + `;padding:18px 24px;">`)
	b.WriteString(`<span style="color:#fff;font-size:16px;font-weight:600;letter-spacing:.5px;">` + brandName + `</span>`)
	b.WriteString(`</td></tr>`)

	// 正文
	b.WriteString(`<tr><td style="padding:24px;">`)
	b.WriteString(inner)
	b.WriteString(`</td></tr>`)

	// 页脚
	b.WriteString(`<tr><td style="padding:16px 24px 22px;border-top:1px solid ` + colorLine + `;">`)
	b.WriteString(`<p style="margin:0;color:` + colorMute + `;font-size:12px;line-height:1.7;">`)
	b.WriteString(`这封邮件由 ` + brandName + ` 自动发送，请不要直接回复。<br>`)
	b.WriteString(`如果你没有使用过本服务，忽略即可。`)
	b.WriteString(`</p></td></tr>`)

	b.WriteString(`</table></td></tr></table></body></html>`)
	return b.String()
}

// CodeEmail 生成验证码邮件（HTML 版）。
//
// 验证码是这个页面唯一要传达的信息，所以把它做成整封信最显眼的东西：
// 大字号、等宽字体、字间距拉开、单独一块浅色底。
// 原来的纯文本只有一行「验证码：123456」，在手机通知栏里根本认不出来。
func CodeEmail(code, purpose, domain string, ttlMinutes int) string {
	action := map[string]string{
		"register": "注册账号",
		"reset":    "重置密码",
		"change":   "更换邮箱",
	}[purpose]
	if action == "" {
		action = "身份验证"
	}

	inner := `
<p style="margin:0 0 6px;color:` + colorInk + `;font-size:18px;font-weight:600;">验证码</p>
<p style="margin:0 0 20px;color:` + colorMute + `;font-size:13px;line-height:1.7;">
你正在 <strong style="color:` + colorInk + `;">` + htmlEscape(domain) + `</strong> 上` + action + `，请在页面中输入下面的验证码。
</p>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:0 0 20px;">
<tr><td align="center" style="background:#f5f4ff;border:1px solid #ddd9ff;border-radius:12px;padding:22px 12px;">
<div style="font-family:'SF Mono',Menlo,Consolas,monospace;font-size:34px;font-weight:700;letter-spacing:10px;color:` + colorBrand + `;padding-left:10px;">` + htmlEscape(code) + `</div>
<div style="margin-top:8px;color:` + colorMute + `;font-size:12px;">` + fmt.Sprintf("%d 分钟内有效，仅可使用一次", ttlMinutes) + `</div>
</td></tr></table>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#fffbeb;border-radius:10px;">
<tr><td style="padding:12px 14px;color:#92400e;font-size:12px;line-height:1.7;">
如果这不是你本人的操作，请忽略这封邮件，你的账号不会有任何变化。请勿把验证码告诉任何人。
</td></tr></table>`

	return wrapEmail("验证码 · "+action, inner)
}

// DailyReport 是一封日报需要的全部数据。
type DailyReport struct {
	Username     string
	Domain       string
	Date         string // 例如 2026-10-20
	Total        int64  // 总查询量
	Blocked      int64  // 拦截量
	Cached       int64  // 缓存命中
	AvgLatencyMS int64  // 平均延迟
	TopDomains   []ReportRow
	// DailyQuota 为 0 表示不限
	DailyQuota int64
}

// ReportRow 是日报表格里的一行。
type ReportRow struct {
	Name  string
	Count int64
}

// DailyReportEmail 生成日报邮件（HTML 版）。
//
// 做成「报表」而不是一段文字：四个关键数字并排成卡片，
// 下面是占比条和 TOP 域名表。手机上单列堆叠，桌面上两列。
func DailyReportEmail(r DailyReport) string {
	blockRate := pct(r.Blocked, r.Total)
	cacheRate := pct(r.Cached, r.Total)

	var b strings.Builder

	b.WriteString(`<p style="margin:0 0 4px;color:` + colorInk + `;font-size:18px;font-weight:600;">昨日解析日报</p>`)
	b.WriteString(`<p style="margin:0 0 20px;color:` + colorMute + `;font-size:13px;line-height:1.7;">`)
	b.WriteString(htmlEscape(r.Date) + ` · ` + htmlEscape(r.Username) + ` · ` + htmlEscape(r.Domain))
	b.WriteString(`</p>`)

	// 四个关键指标：两列网格（用 table 实现，邮件客户端不认 grid）
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:0 0 22px;">`)
	b.WriteString(metricRow(
		metricCell("总查询量", fmtInt(r.Total), colorBrand, ""),
		metricCell("已拦截", fmtInt(r.Blocked), colorDanger, blockRate+" 拦截率"),
	))
	b.WriteString(`<tr><td style="height:10px;"></td><td></td></tr>`)
	b.WriteString(metricRow(
		metricCell("缓存命中", fmtInt(r.Cached), colorOk, cacheRate+" 命中率"),
		metricCell("平均延迟", fmt.Sprintf("%d ms", r.AvgLatencyMS), colorWarn, ""),
	))
	b.WriteString(`</table>`)

	// 占比条：一眼看出拦截和缓存各占多少
	b.WriteString(sectionTitle("构成"))
	b.WriteString(bar("已拦截", r.Blocked, r.Total, colorDanger))
	b.WriteString(bar("缓存命中", r.Cached, r.Total, colorOk))
	normal := r.Total - r.Blocked - r.Cached
	if normal < 0 {
		normal = 0
	}
	b.WriteString(bar("走上游", normal, r.Total, colorBrand))

	// TOP 域名
	if len(r.TopDomains) > 0 {
		b.WriteString(sectionTitle("查询最多的域名"))
		b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="font-size:13px;">`)
		for i, d := range r.TopDomains {
			bg := "#ffffff"
			if i%2 == 1 {
				bg = "#fafafa"
			}
			b.WriteString(`<tr style="background:` + bg + `;">`)
			b.WriteString(`<td style="padding:8px 10px;color:` + colorMute + `;width:22px;font-family:monospace;">` + fmt.Sprint(i+1) + `</td>`)
			b.WriteString(`<td style="padding:8px 10px;color:` + colorInk + `;font-family:monospace;word-break:break-all;">` + htmlEscape(d.Name) + `</td>`)
			b.WriteString(`<td style="padding:8px 10px;color:` + colorMute + `;text-align:right;font-family:monospace;white-space:nowrap;">` + fmtInt(d.Count) + `</td>`)
			b.WriteString(`</tr>`)
		}
		b.WriteString(`</table>`)
	}

	// 配额使用情况
	if r.DailyQuota > 0 {
		b.WriteString(sectionTitle("每日配额"))
		used := pct(r.Total, r.DailyQuota)
		b.WriteString(`<p style="margin:0 0 8px;color:` + colorMute + `;font-size:12px;">`)
		b.WriteString(fmtInt(r.Total) + " / " + fmtInt(r.DailyQuota) + " 次（" + used + "）")
		b.WriteString(`</p>`)
		b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#eef0f4;border-radius:999px;height:8px;"><tr>`)
		w := int(ratio(r.Total, r.DailyQuota) * 100)
		if w > 100 {
			w = 100
		}
		if w > 0 {
			b.WriteString(fmt.Sprintf(`<td width="%d%%" style="background:%s;border-radius:999px;height:8px;"></td>`, w, colorBrand))
		}
		if w < 100 {
			b.WriteString(fmt.Sprintf(`<td width="%d%%"></td>`, 100-w))
		}
		b.WriteString(`</tr></table>`)
	}

	b.WriteString(`<p style="margin:22px 0 0;color:` + colorMute + `;font-size:12px;line-height:1.7;">`)
	b.WriteString(`想关掉这封日报？在控制台「我的账号 → 通知设置」里可以随时关闭。`)
	b.WriteString(`</p>`)

	return wrapEmail("昨日解析日报", b.String())
}

// DailyReportText 是日报的纯文本版本，给屏蔽 HTML 的客户端兜底。
func DailyReportText(r DailyReport) string {
	var b strings.Builder
	b.WriteString("【" + brandName + " 昨日解析日报】\n")
	b.WriteString(r.Date + " · " + r.Username + " · " + r.Domain + "\n\n")
	b.WriteString(fmt.Sprintf("总查询量：%s\n", fmtInt(r.Total)))
	b.WriteString(fmt.Sprintf("已拦截：%s（%s）\n", fmtInt(r.Blocked), pct(r.Blocked, r.Total)))
	b.WriteString(fmt.Sprintf("缓存命中：%s（%s）\n", fmtInt(r.Cached), pct(r.Cached, r.Total)))
	b.WriteString(fmt.Sprintf("平均延迟：%d ms\n", r.AvgLatencyMS))
	if len(r.TopDomains) > 0 {
		b.WriteString("\n查询最多的域名：\n")
		for i, d := range r.TopDomains {
			b.WriteString(fmt.Sprintf("  %d. %s  %s\n", i+1, d.Name, fmtInt(d.Count)))
		}
	}
	if r.DailyQuota > 0 {
		b.WriteString(fmt.Sprintf("\n每日配额：%s / %s（%s）\n", fmtInt(r.Total), fmtInt(r.DailyQuota), pct(r.Total, r.DailyQuota)))
	}
	b.WriteString("\n想关掉这封日报？在控制台「我的账号 → 通知设置」里可以随时关闭。\n")
	return b.String()
}

// --- 下面是排版小工具 ---

func metricRow(a, b string) string {
	return `<tr>` + a + b + `</tr>`
}

// metricCell 是一个指标卡片。sub 为空时不渲染副标题。
func metricCell(label, value, color, sub string) string {
	s := `<td width="50%" valign="top" style="padding:0 5px;">`
	s += `<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#f8f9fb;border:1px solid ` + colorLine + `;border-radius:12px;">`
	s += `<tr><td style="padding:14px 16px;">`
	s += `<div style="color:` + colorMute + `;font-size:12px;">` + htmlEscape(label) + `</div>`
	s += `<div style="margin-top:6px;color:` + color + `;font-size:24px;font-weight:700;font-family:'SF Mono',Menlo,Consolas,monospace;">` + htmlEscape(value) + `</div>`
	if sub != "" {
		s += `<div style="margin-top:4px;color:` + colorMute + `;font-size:11px;">` + htmlEscape(sub) + `</div>`
	}
	s += `</td></tr></table></td>`
	return s
}

func sectionTitle(t string) string {
	return `<p style="margin:0 0 10px;color:` + colorInk + `;font-size:13px;font-weight:600;">` + htmlEscape(t) + `</p>`
}

// bar 是一行占比条：左边标签，中间进度，右边数值。
func bar(label string, v, total int64, color string) string {
	w := int(ratio(v, total) * 100)
	if w > 100 {
		w = 100
	}
	var b strings.Builder
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin:0 0 8px;font-size:12px;">`)
	b.WriteString(`<tr>`)
	b.WriteString(`<td style="width:64px;color:` + colorMute + `;">` + htmlEscape(label) + `</td>`)
	b.WriteString(`<td style="padding:0 8px;">`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#eef0f4;border-radius:999px;height:8px;"><tr>`)
	if w > 0 {
		b.WriteString(fmt.Sprintf(`<td width="%d%%" style="background:%s;border-radius:999px;height:8px;"></td>`, w, color))
	}
	if w < 100 {
		b.WriteString(fmt.Sprintf(`<td width="%d%%"></td>`, 100-w))
	}
	b.WriteString(`</tr></table></td>`)
	b.WriteString(`<td style="width:76px;text-align:right;color:` + colorMute + `;font-family:monospace;white-space:nowrap;">` + fmtInt(v) + `</td>`)
	b.WriteString(`</tr></table>`)
	return b.String()
}

func ratio(v, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(v) / float64(total)
}

func pct(v, total int64) string {
	if total <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.1f%%", float64(v)*100/float64(total))
}

// fmtInt 加千分位。邮件里 1234567 和 1,234,567 的可读性差很多。
func fmtInt(n int64) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}
