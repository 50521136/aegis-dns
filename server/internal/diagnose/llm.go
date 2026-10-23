package diagnose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 这个文件对接「OpenAI 兼容」的 chat/completions 端点。
//
// 只实现一个最小子集：单轮、非流式、JSON 输出。够用且能兼容绝大多数
// 自建与云厂商（DeepSeek / 通义 / Kimi / vLLM / Ollama 的 /v1 都支持这套）。

// LLMConfig 是模型连接参数。
type LLMConfig struct {
	BaseURL string // 例如 https://api.deepseek.com/v1
	APIKey  string
	Model   string // 例如 deepseek-chat
	Timeout time.Duration
	// MaxPerHour 是每小时诊断次数上限，0 表示不限。
	MaxPerHour int
}

// maxReplyTokens 限制输出长度。
//
// 诊断结论本身很短（一个判定 + 一句话），给多了模型容易开始长篇解释，
// 反而更容易跑偏；也对费用友好。
const maxReplyTokens = 800

// Suggestion 是模型输出的结构化结论。
type Suggestion struct {
	Verdict string `json:"verdict"` // wrong_block | correct | unclear
	Reason  string `json:"reason"`
	Action  Action `json:"action"`
	Reply   string `json:"reply"`
}

// Action 是建议执行的动作。类型是枚举，具体值由 guard.go 校验。
type Action struct {
	Type    string   `json:"type"` // add_global_allow | add_user_allow | no_action | need_human
	Domains []string `json:"domains,omitempty"`
}

func (s *Suggestion) validate() error {
	switch s.Verdict {
	case "wrong_block", "correct", "unclear":
	default:
		return fmt.Errorf("verdict 非法: %q", s.Verdict)
	}
	switch s.Action.Type {
	case "add_global_allow", "add_user_allow", "no_action", "need_human":
	default:
		return fmt.Errorf("action.type 非法: %q", s.Action.Type)
	}
	if strings.TrimSpace(s.Reply) == "" && s.Action.Type != "need_human" {
		return errors.New("reply 不能为空")
	}
	return nil
}

// systemPrompt 是诊断质量的关键。
//
// 三条硬性约束写在最前面，因为模型最常见的两种跑偏是：
//   - 无视「已经 allow」的事实，硬说被拦了；
//   - 给出粗暴建议（整站放行、放行顶级域），一条就能让拦截率归零。
const systemPrompt = `你是 DNS 拦截规则的诊断助手。你会收到一份 JSON 证据，其中 domains 里每个域名的
action / layer 都是规则引擎确定性计算的结果，不要质疑、不要重新推导，直接采信。

你的任务：判断用户反馈是否属于误杀，并给出处置建议。

硬性要求：
1. 只依据证据下结论，不得推测证据里没有的信息。
2. 如果某域名的 action 已经是 allow，说明规则层面没有拦它；这种情况下必须如实
   指出问题可能在客户端缓存、上游解析或应用本身，不能编造「被某订阅拦截」。
3. 只有 action=block 的域名才可能算误杀。判为误杀时要指出是哪一层拦的
   （layer 字段），以及来自哪个订阅（sub_sources）。
4. action.type 只能取以下四者之一：
   - add_global_allow：确认是公共规则集误杀、且该域名对多数用户都该放行
   - add_user_allow：只该对这个用户放行（个人用途）
   - no_action：规则正确，无需处理
   - need_human：证据不足或有冲突，需要人工判断
5. 建议放行的域名必须是最小必要集合：只放行证据里 action=block 的那几个具体域名，
   绝不放行顶级域、公共后缀或整站根域（例如不能建议放行 com、net、github.io 之外
   的宽泛后缀）。最多 5 个。
6. reply 是给用户看的一句话，不超过 80 个汉字，不要堆技术术语，不要提订阅名。

只输出 JSON，不要任何解释文字或 Markdown 代码块。格式：
{"verdict":"wrong_block|correct|unclear","reason":"给管理员看的判断依据，一句话","action":{"type":"add_global_allow","domains":["a.example.com"]},"reply":"给用户看的一句话"}`

// Diagnose 调用模型得到建议。evidence 必须是 Collect 的产物。
func Diagnose(ctx context.Context, cfg LLMConfig, ev *Evidence) (*Suggestion, error) {
	if cfg.BaseURL == "" || cfg.Model == "" {
		return nil, errors.New("未配置模型地址或模型名")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	evJSON, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("序列化证据失败: %w", err)
	}

	body := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": string(evJSON)},
		},
		"temperature": 0, // 诊断要可复现，不要创造性
		"max_tokens":  maxReplyTokens,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	url := strings.TrimSuffix(cfg.BaseURL, "/") + "/chat/completions"
	reqCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求模型失败: %w", err)
	}
	defer resp.Body.Close()

	// 限制读取体积：模型返回再离谱也不该超过 1MB。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取模型响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("模型返回 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析模型响应失败: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, errors.New("模型没有返回任何 choice")
	}
	content := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if content == "" {
		return nil, errors.New("模型返回内容为空")
	}

	sug, err := parseSuggestion(content)
	if err != nil {
		return nil, err
	}
	return sug, nil
}

// parseSuggestion 容忍模型常见的输出瑕疵：Markdown 代码块包裹、
// JSON 前后的解释性文字。生产上这两种情况出现频率不低，
// 直接 json.Unmarshal 会整条失败。
func parseSuggestion(content string) (*Suggestion, error) {
	c := content
	if i := strings.Index(c, "```"); i >= 0 {
		c = c[i+3:]
		c = strings.TrimPrefix(c, "json")
		c = strings.TrimPrefix(c, "JSON")
		if j := strings.Index(c, "```"); j >= 0 {
			c = c[:j]
		}
	}
	c = strings.TrimSpace(c)
	// 退一步：截取第一个 { 到最后一个 }。
	if a := strings.Index(c, "{"); a > 0 || (a == 0 && !strings.HasPrefix(c, "{")) {
		if b := strings.LastIndex(c, "}"); b > a {
			c = c[a : b+1]
		}
	}

	var sug Suggestion
	if err := json.Unmarshal([]byte(c), &sug); err != nil {
		return nil, fmt.Errorf("模型输出不是合法 JSON（%w）: %s", err, truncate(content, 300))
	}
	if err := sug.validate(); err != nil {
		return nil, fmt.Errorf("模型输出不符合约定（%w）: %s", err, truncate(content, 300))
	}
	return &sug, nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
