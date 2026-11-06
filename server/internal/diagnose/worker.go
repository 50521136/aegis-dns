package diagnose

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 这个文件是异步诊断的执行器。
//
// 为什么必须异步且单 worker：
//   - 模型响应通常几秒到几十秒，阻塞 HTTP 会让面板以为卡死；
//   - 一次上来十条反馈就并发十条模型请求，既费钱也容易被限流；
//   - 单 worker + 缓冲队列，最坏情况是排队变慢，不会雪崩。

// Store 是 worker 需要的写入能力（便于单测替身）。
type Store interface {
	WriteAIDiagnosis(ctx context.Context, f *model.Feedback) error
	MarkAIDiagnosisFailed(ctx context.Context, fid, reason string) error
}

// Worker 串行消费诊断任务。
type Worker struct {
	src    Source
	st     Store
	log    *slog.Logger
	cfgFn  func() LLMConfig
	modeFn func() string // 返回 off / on_submit / manual

	// onDone 在诊断结果落库**之后**被调用，用于自动处理反馈
	// （放行白名单、回复结单、转人工）。
	//
	// 为什么用回调而不是让 worker 直接做：
	// 自动处理要写规则、改反馈状态、发通知，这些能力都在 api/store 层，
	// 而 diagnose 包只负责「问模型 + 过闸门」。反过来依赖会让
	// diagnose → api 成环，也让这个包的测试被拖进整个栈。
	onDone func(fb *model.Feedback)

	queue chan model.Feedback

	// hourWindow 是简易限流：当前小时的计数与起点。
	hourStart atomic.Int64
	hourCount atomic.Int64

	mu     sync.Mutex
	closed bool

	// window 是查询近期记录的时间窗（秒）。
	window int64
}

// NewWorker 创建并启动诊断 worker。
//
// 队列容量取 64：反馈本身是低频事件（线上一天几条），
// 64 的缓冲足以吸收突发，又不会在模型长期不可用时无限堆积内存。
func NewWorker(src Source, st Store, log *slog.Logger, cfgFn func() LLMConfig, modeFn func() string) *Worker {
	w := &Worker{
		src:    src,
		st:     st,
		log:    log,
		cfgFn:  cfgFn,
		modeFn: modeFn,
		queue:  make(chan model.Feedback, 64),
		window: 3600, // 近 1 小时
	}
	w.hourStart.Store(time.Now().Unix() / 3600)
	go w.loop()
	return w
}

// SetOnDone 注册「诊断完成后」的回调。
//
// 必须在 NewWorker 之后、Enqueue 之前调用；这里不做并发保护，
// 因为它在启动装配阶段设置一次，运行期只读。
func (w *Worker) SetOnDone(fn func(fb *model.Feedback)) {
	if w == nil {
		return
	}
	w.onDone = fn
}

// Enqueue 投递一条待诊断反馈。队列满时丢弃并记日志，不阻塞调用方。
func (w *Worker) Enqueue(fb model.Feedback) {
	if w == nil {
		return
	}
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if closed {
		return
	}
	select {
	case w.queue <- fb:
	default:
		// 队列满：标记失败，避免这条反馈永远停在 pending 上。
		w.log.Warn("诊断队列已满，丢弃任务", "feedback", fb.ID)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = w.st.MarkAIDiagnosisFailed(ctx, fb.ID, "诊断队列已满，请稍后重试")
	}
}

// Close 停止 worker（进程退出时调用）。
func (w *Worker) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	close(w.queue)
}

func (w *Worker) loop() {
	for fb := range w.queue {
		w.runOne(fb)
	}
}

// runOne 执行一次完整诊断：收证据 → 调模型 → 过闸门 → 落库。
func (w *Worker) runOne(fb model.Feedback) {
	// 用独立的超时 ctx：与 HTTP 请求解耦，不受提交方断开影响。
	//
	// 预算要按回退链的最坏情况算：每个模型可能「思考耗尽」再自动重试一次，
	// 主模型 + N 个回退 = 2*(1+N) 次调用。旧版只给 timeout+10s，
	// 主模型一慢就把父级 deadline 吃光，回退模型一发请求就超时。
	cfg := w.cfgFn()
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	attempts := 2 * (1 + len(cfg.FallbackModels))
	budget := timeout*time.Duration(attempts) + 10*time.Second
	if budget > 3*time.Minute {
		budget = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	if !w.allowRate() {
		w.fail(ctx, fb.ID, "超过每小时诊断次数上限，已跳过")
		return
	}

	ev, err := Collect(ctx, w.src, fb, time.Now().Unix()-w.window)
	if err != nil {
		w.fail(ctx, fb.ID, "收集证据失败: "+err.Error())
		return
	}

	cfg = w.cfgFn()
	sug, err := Diagnose(ctx, cfg, ev)
	if err != nil {
		// Diagnose 的错误已带「模型调用失败」前缀，别再叠一层。
		w.fail(ctx, fb.ID, err.Error())
		return
	}

	// 闸门：收紧模型给出的动作。allowedRuleDomains 传 nil ——
	// 「已被更宽规则覆盖」的商业判断留给管理员肉眼确认，
	// 这里只做安全相关的硬校验。
	guard := Guard(sug, ev, nil)

	out := fb // 复制，避免改写调用方持有的值
	out.AIStatus = model.AIDiagDone
	out.AIVerdict = sug.Verdict
	out.AIReason = sug.Reason
	out.AIAction = guard.Action.Type
	out.AIReply = sug.Reply
	out.AIDomains = strings.Join(guard.Action.Domains, ",")
	// 按内容查模式下，模型只给「建议核查」的候选域名，与可执行的
	// 放行清单严格分开：前者给管理员人工排查，后者点采纳才写规则。
	out.AICandidates = strings.Join(sug.Candidates, ",")
	// 落库取证方式：管理员列表与自动处理回调据此区分「按域名查」（可自动
	// 处理）与「按内容查」（只给建议、必须等人工）。
	out.AILookupMode = string(ev.Mode)
	out.AIGuardNotes = strings.Join(guard.Notes, "；")
	out.AIEvidence = MarshalEvidence(ev)
	// 记录实际给出结论的模型：主模型失败时这里会是回退模型，
	// 写主模型名会让事后复盘查错对象。
	out.AIModel = sug.Model

	if err := w.st.WriteAIDiagnosis(ctx, &out); err != nil {
		w.log.Error("写入诊断结果失败", "feedback", fb.ID, "err", err)
		return
	}
	w.log.Info("反馈诊断完成",
		"feedback", fb.ID, "mode", ev.Mode,
		"verdict", sug.Verdict, "action", guard.Action.Type,
		"domains", out.AIDomains, "candidates", out.AICandidates)

	// 诊断结果已落库，交给上层决定「要不要自动处理」。
	//
	// 按内容查模式（用户没填域名）把 mode 传给上层：结论是从正文和近期
	// 拦截记录推断的，上层必须只标记「需人工」、不自动放行或结单。
	// 放在这里而不是让上层自己猜域名字段，是因为域名字段可能填了
	// 「上不了网」这类非域名内容 —— Collect 的判断才是准的。
	//
	// 放在写入之后：自动处理会读这条反馈的 ai_* 字段，若先处理再落库，
	// 它会读到旧值（或空值），结论就是错的。
	// 用独立 ctx：onDone 里有写库操作，可能比本次诊断超时略长，
	// 不该被诊断的 ctx 取消牵连。
	if w.onDone != nil {
		done := out // 传副本：onDone 不得改写 worker 持有的值
		go func() {
			// 用独立 ctx：onDone 里有写库操作，可能比本次诊断超时略长，
			// 不该被诊断的 ctx 取消牵连；这里只做超时兜底，不传下去
			// （回调签名固定为 func(*model.Feedback)，内部自建 ctx）。
			_ = 60 * time.Second
			w.onDone(&done)
		}()
	}
}

func (w *Worker) fail(ctx context.Context, fid, reason string) {
	w.log.Warn("反馈诊断失败", "feedback", fid, "reason", reason)
	if err := w.st.MarkAIDiagnosisFailed(ctx, fid, reason); err != nil {
		w.log.Error("标记诊断失败也失败了", "feedback", fid, "err", err)
	}
}

// allowRate 做每小时次数限制。
//
// 用「当前小时序号」而不是滑动窗口：足够防住跑飞，实现只有几行。
// limit 为 0 表示不限制。
func (w *Worker) allowRate() bool {
	limit := w.cfgFn().MaxPerHour
	if limit <= 0 {
		return true
	}
	hour := time.Now().Unix() / 3600
	if w.hourStart.Load() != hour {
		w.hourStart.Store(hour)
		w.hourCount.Store(0)
	}
	return w.hourCount.Add(1) <= int64(limit)
}

// ShouldAutoRun 判断当前是否应在「用户提交反馈时」自动诊断。
func (w *Worker) ShouldAutoRun() bool {
	if w == nil {
		return false
	}
	return w.modeFn() == "on_submit"
}
