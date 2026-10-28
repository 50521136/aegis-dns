package api

import (
	"strings"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这组测试锁住「用户协议」的几条规则。
//
// 写错的代价都是静默的：
//   - 开了协议却不校验 agree_terms → 勾选框形同虚设，脚本直连 API 就能绕过；
//   - 正文为空却允许保存 → 用户被要求同意一份空白协议；
//   - 标题为空时提示语拼成「请先阅读并同意」→ 读起来像话没说完。

func TestTermsTitleOrDefault(t *testing.T) {
	// 管理员填了标题就用它
	if got := termsTitleOrDefault("  服务条款 "); got != "服务条款" {
		t.Errorf("标题应去掉首尾空白，实际 %q", got)
	}
	// 标题为空时兜底：错误提示里不能出现空标题
	for _, empty := range []string{"", "   ", "\n\t"} {
		if got := termsTitleOrDefault(empty); got != "用户协议" {
			t.Errorf("空标题 %q 应兜底成「用户协议」，实际 %q", empty, got)
		}
	}
}

func TestDefaultTermsContentIsUsable(t *testing.T) {
	c := model.DefaultTermsContent
	if strings.TrimSpace(c) == "" {
		t.Fatal("默认协议正文不能为空：管理员一开开关就会撞上「正文为空」的保存校验")
	}
	// 正文要覆盖本程序实际具备的能力，否则这份协议跟实现对不上，出事时没用。
	for _, want := range []string{
		"DoH", "DoT", // 接入协议
		"配额",    // 免费/会员两档
		"查询日志",  // 数据与保留
		"开放解析器", // 禁止行为
		"来源 IP", // 租户识别方式
		"ECS",   // 上游网段透传
		"SLA",   // 可用性免责
		"协议变更",  // 变更条款
	} {
		if !strings.Contains(c, want) {
			t.Errorf("默认协议正文缺少关键内容 %q —— 协议必须和程序实际能力对得上", want)
		}
	}
	// 默认稿要能在 10 万字符上限之内，且不该短到像占位符。
	if n := len([]rune(c)); n < 500 || n > 100000 {
		t.Errorf("默认协议正文长度异常：%d 字符", n)
	}
}

func TestValidateSettingsTermsRules(t *testing.T) {
	base := func() *model.GlobalSettings {
		s := model.DefaultSettings()
		return &s
	}

	// 默认关闭 → 正文为空也允许保存（此时根本不展示协议）
	s := base()
	s.TermsEnabled = false
	s.TermsContent = ""
	if err := validateSettings(s); err != nil {
		t.Fatalf("协议关闭时不该校验正文，实际 %v", err)
	}

	// 开启 + 正文为空 → 必须拒绝：用户会被要求同意一份空白协议
	s = base()
	s.TermsEnabled = true
	s.TermsContent = "   \n\t "
	if err := validateSettings(s); err == nil {
		t.Fatal("开启协议但正文为空，应拒绝保存")
	}

	// 开启 + 标题为空 → 拒绝
	s = base()
	s.TermsEnabled = true
	s.TermsContent = "正文"
	s.TermsTitle = "  "
	if err := validateSettings(s); err == nil {
		t.Fatal("开启协议但标题为空，应拒绝保存")
	}

	// 开启 + 标题正文都有 → 通过
	s = base()
	s.TermsEnabled = true
	s.TermsTitle = "用户协议"
	s.TermsContent = "正文"
	if err := validateSettings(s); err != nil {
		t.Fatalf("标题与正文齐全时应允许保存，实际 %v", err)
	}

	// 正文超长 → 拒绝（基本是把整本法律文本误粘贴进来）
	s = base()
	s.TermsEnabled = true
	s.TermsTitle = "用户协议"
	s.TermsContent = strings.Repeat("长", 100001)
	if err := validateSettings(s); err == nil {
		t.Fatal("正文超过 10 万字符应拒绝保存")
	}
}

// applySettingsPatch 要能显式把正文清空（指针语义），
// 否则管理员想删掉正文时前端传空串会被当成「没传」。
func TestApplySettingsPatchClearsTerms(t *testing.T) {
	dst := model.DefaultSettings()
	if dst.TermsContent == "" {
		t.Fatal("默认设置里应有协议正文")
	}
	empty := ""
	p := settingsPatch{TermsContent: &empty}
	applySettingsPatch(&dst, &p)
	if dst.TermsContent != "" {
		t.Errorf("显式传空串应清空正文，实际 %q", dst.TermsContent)
	}

	// 不传（nil）时保持原值不动
	before := dst.TermsTitle
	applySettingsPatch(&dst, &settingsPatch{})
	if dst.TermsTitle != before {
		t.Error("未传的字段不该被改动")
	}
}
