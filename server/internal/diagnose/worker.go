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
	timeout := time.Duration(w.cfgFn().Timeout)
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout+10*time.Second)
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

	cfg := w.cfgFn()
	sug, err := Diagnose(ctx, cfg, ev)
	if err != nil {
		w.fail(ctx, fb.ID, "模型调用失败: "+err.Error())
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
	out.AIGuardNotes = strings.Join(guard.Notes, "；")
	out.AIEvidence = MarshalEvidence(ev)
	out.AIModel = cfg.Model

	if err := w.st.WriteAIDiagnosis(ctx, &out); err != nil {
		w.log.Error("写入诊断结果失败", "feedback", fb.ID, "err", err)
		return
	}
	w.log.Info("反馈诊断完成",
		"feedback", fb.ID, "verdict", sug.Verdict,
		"action", guard.Action.Type, "domains", out.AIDomains)
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
