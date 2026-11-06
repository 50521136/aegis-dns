package diagnose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// 这个文件对接「OpenAI 兼容」的 chat/completions 与 /models 端点。
//
// 只实现一个最小子集：单轮、非流式、JSON 输出。够用且能兼容绝大多数
// 自建与云厂商（DeepSeek / 通义 / Kimi / vLLM / Ollama 的 /v1 都支持这套）。

// LLMConfig 是模型连接参数。
type LLMConfig struct {
	BaseURL string // 例如 https://api.deepseek.com/v1
	APIKey  string
	Model   string // 主模型，例如 deepseek-chat
	Timeout time.Duration
	// MaxPerHour 是每小时诊断次数上限，0 表示不限。
	MaxPerHour int
	// MaxTokens 是单次调用的 max_tokens，<=0 用 defaultMaxReplyTokens。
	//
	// 推理模型（如 Sage-A1.5 / Agents-A1）会先把 token 花在
	// reasoning_content 上，再写 content；给 800 时实测 800 个 token
	// 全被思考吃光、finish_reason=length、content 为空，但额度已经扣了。
	MaxTokens int
	// FallbackModels 是主模型失败后依次尝试的回退模型。
	// 顺序即优先级；会去掉空串、重复项与主模型本身。
	FallbackModels []string
}

const (
	// defaultMaxReplyTokens 比旧版的 800 大得多：推理模型会把 token
	// 先花在 reasoning_content 上，实测 Sage-A1.5 同一份证据的思考量
	// 在 1190~2000+ 之间波动，给 800/1600/2000 都出现过
	// finish_reason=length + content 为空（额度已扣，结论没有）。
	// 4000 是上限不是实际消耗：正常出结论时实测只用了 1253。
	defaultMaxReplyTokens = 4000
	// maxAutoRetryTokens 是「思考耗尽」自动重试时的上限，避免无限加倍。
	maxAutoRetryTokens = 8000
	// modelsTimeout 是拉取 /models 的超时，与诊断超时分开（列表接口应该很快）。
	modelsTimeout = 15 * time.Second
)

// Suggestion 是模型输出的结构化结论。
type Suggestion struct {
	Verdict string `json:"verdict"` // wrong_block | correct | unclear
	Reason  string `json:"reason"`
	Action  Action `json:"action"`
	Reply   string `json:"reply"`
	// Model 是实际给出这个结论的模型名（主模型失败时可能是回退模型）。
	// 不进 JSON：它不是模型输出，是本地记录的来源，供落库与日志用。
	Model string `json:"-"`
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

// modelChain 把主模型与回退模型拼成去重、去空的尝试顺序。
func modelChain(primary string, fallbacks []string) []string {
	out := make([]string, 0, len(fallbacks)+1)
	seen := map[string]bool{}
	for _, m := range append([]string{primary}, fallbacks...) {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// Diagnose 调用模型得到建议。evidence 必须是 Collect 的产物。
//
// 依次尝试「主模型 → 回退模型」。每次调用若因推理 token 耗尽而空输出，
// 自动把 max_tokens 加倍再试一次（上限 maxAutoRetryTokens）——线上
// Sage-A1.5 在 800 时空返回但已扣 800 token，1600 就能正常出 JSON。
func Diagnose(ctx context.Context, cfg LLMConfig, ev *Evidence) (*Suggestion, error) {
	if cfg.BaseURL == "" || cfg.Model == "" {
		return nil, errors.New("未配置模型地址或模型名")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxReplyTokens
	}
	chain := modelChain(cfg.Model, cfg.FallbackModels)

	evJSON, err := json.Marshal(ev)
	if err != nil {
		return nil, fmt.Errorf("序列化证据失败: %w", err)
	}

	var errs []string
	for _, name := range chain {
		sug, err := callModel(ctx, cfg, name, string(evJSON), maxTokens)
		if err == nil {
			sug.Model = name
			return sug, nil
		}
		// 只针对「思考把 token 吃光」做同模型加大重试；HTTP 错误、
		// Key 错、模型不存在这类重试没有意义，直接换下一个模型。
		var trunc *thinkingTruncatedError
		if errors.As(err, &trunc) && maxTokens < maxAutoRetryTokens {
			retryTokens := maxTokens * 2
			if retryTokens > maxAutoRetryTokens {
				retryTokens = maxAutoRetryTokens
			}
			sug, retryErr := callModel(ctx, cfg, name, string(evJSON), retryTokens)
			if retryErr == nil {
				sug.Model = name
				return sug, nil
			}
			err = fmt.Errorf("%w；加大 max_tokens 到 %d 后仍失败: %v", err, retryTokens, retryErr)
		}
		errs = append(errs, fmt.Sprintf("%s: %v", name, err))
	}
	if len(errs) == 1 {
		return nil, fmt.Errorf("模型调用失败: %s", errs[0])
	}
	return nil, fmt.Errorf("模型调用失败（已尝试 %d 个模型）: %s", len(errs), strings.Join(errs, "；"))
}

// thinkingTruncatedError 表示响应合法但推理过程耗尽了 max_tokens，
// 最终 content 为空。单独成类是为了让上层只对这一种情况加大重试。
type thinkingTruncatedError struct {
	completionTokens int
	reasoningTokens  int
	finishReason     string
}

func (e *thinkingTruncatedError) Error() string {
	return fmt.Sprintf("模型返回内容为空：推理耗尽了 max_tokens（finish_reason=%s，completion=%d，reasoning=%d），已消耗额度但没有输出；请调大 max_tokens 或改用非推理模型/回退模型",
		e.finishReason, e.completionTokens, e.reasoningTokens)
}

// callModel 对单个模型发一次请求并解析。
func callModel(ctx context.Context, cfg LLMConfig, name, evJSON string, maxTokens int) (*Suggestion, error) {
	body := map[string]any{
		"model": name,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": evJSON},
		},
		"temperature": 0, // 诊断要可复现，不要创造性
		"max_tokens":  maxTokens,
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
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
			ReasoningTokens  int `json:"reasoning_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析模型响应失败: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, errors.New("模型没有返回任何 choice")
	}
	choice := parsed.Choices[0]
	content := strings.TrimSpace(choice.Message.Content)
	if content == "" {
		// 推理模型把 token 全花在 reasoning_content 上时，content 会是空串。
		// 给出带 usage 的明确错误，别让管理员只看到一句「返回内容为空」。
		if choice.FinishReason == "length" || choice.Message.ReasoningContent != "" {
			return nil, &thinkingTruncatedError{
				completionTokens: parsed.Usage.CompletionTokens,
				reasoningTokens:  parsed.Usage.ReasoningTokens,
				finishReason:     choice.FinishReason,
			}
		}
		return nil, errors.New("模型返回内容为空")
	}

	sug, err := parseSuggestion(content)
	if err != nil {
		return nil, err
	}
	return sug, nil
}

// ModelInfo 是 /models 返回的单个模型。
type ModelInfo struct {
	ID    string `json:"id"`
	Owned string `json:"owned_by,omitempty"`
}

// ListModels 拉取上游 /models 列表，Sorted 去重后返回。
//
// 用独立的短超时：这是面板上的一次性探测，不该被诊断超时（可能几十秒）拖住。
// 上游没实现 /models 时返回明确错误，前端仍保留手填模型名的能力。
func ListModels(ctx context.Context, cfg LLMConfig) ([]ModelInfo, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("未配置模型地址")
	}
	url := strings.TrimSuffix(cfg.BaseURL, "/") + "/models"
	reqCtx, cancel := context.WithTimeout(ctx, modelsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求模型列表失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取模型列表失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("模型列表返回 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var parsed struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %w", err)
	}
	seen := map[string]bool{}
	out := make([]ModelInfo, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, ModelInfo{ID: id, Owned: m.OwnedBy})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ParseFallbackModels 把逗号 / 换行 / 空格分隔的配置串拆成模型名列表。
// 供 api 层从设置字符串构造 LLMConfig。
func ParseFallbackModels(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
