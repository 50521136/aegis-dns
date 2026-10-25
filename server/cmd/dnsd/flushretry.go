package main

import (
	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

// flushBuffer 是「落库失败后待重试」的缓冲。
//
// 为什么需要：flushOnce 里 c.Flush() 是**取走并清空**语义，取出来的这批数据
// 一旦 WriteStats 失败，旧实现直接 return —— 那批统计与查询日志永久丢失，
// 只留一行 Error 日志。库被锁、磁盘满、apid 同时在写，都会触发这条路径，
// 表现是「统计数字莫名少了一块」，而且无法事后补。
//
// 三个约束：
//
//  1. **有上限**。库长时间不可用时不能让缓冲无限膨胀把 dnsd 内存吃光 ——
//     dnsd 是 DNS 解析进程，宁可丢掉最旧的一批，也不能被统计拖崩。
//     超限时丢最旧的（新数据更可能反映当前状况）。
//  2. **合并而不是逐条重放**。失败的重试批次与下一轮新数据要能一起写，
//     否则库恢复慢的时候会积累几十个小事务，反而更慢。
//  3. **不做无限重试**。连续失败到上限就丢弃并记一次告警，把「丢了多少」
//     明确写进日志 —— 静默丢弃才是最难排查的。
type flushBuffer struct {
	rollups []store.RollupBucket
	tops    []store.TopBucket
	logs    []model.QueryLogRow

	// maxRows 是总行数上限（三类合计）。0 表示不限制（仅测试用）。
	maxRows int
	// dropped 是累计丢弃的行数，用于日志告警与自检展示。
	dropped int
}

const defaultFlushBufferRows = 200000

func newFlushBuffer(maxRows int) *flushBuffer {
	return &flushBuffer{maxRows: maxRows}
}

// add 把一批失败的数据放进缓冲。
func (b *flushBuffer) add(rollups []store.RollupBucket, tops []store.TopBucket, logs []model.QueryLogRow) {
	b.rollups = append(b.rollups, rollups...)
	b.tops = append(b.tops, tops...)
	b.logs = append(b.logs, logs...)
	b.trim()
}

// take 取出全部缓冲内容（调用方负责在成功后丢弃、失败时放回）。
func (b *flushBuffer) take() ([]store.RollupBucket, []store.TopBucket, []model.QueryLogRow) {
	r, t, l := b.rollups, b.tops, b.logs
	b.rollups, b.tops, b.logs = nil, nil, nil
	return r, t, l
}

// len 返回缓冲内的总行数。
func (b *flushBuffer) len() int {
	return len(b.rollups) + len(b.tops) + len(b.logs)
}

// trim 在超过上限时丢弃最旧的部分。
//
// 查询日志按时间顺序追加，丢头部就是丢最旧的；rollup/top 同理。
func (b *flushBuffer) trim() {
	if b.maxRows <= 0 {
		return
	}
	for b.len() > b.maxRows {
		// 一次丢掉超出量的一半或整段最旧的，避免每行都进一次循环。
		switch {
		case len(b.logs) > 0:
			drop := len(b.logs)
			if rest := b.len() - drop; rest >= b.maxRows {
				// 全丢日志还不够，继续丢 rollup
				b.logs = nil
				b.dropped += drop
				continue
			}
			// 只丢一部分即可满足上限
			need := b.len() - b.maxRows
			if need > len(b.logs) {
				need = len(b.logs)
			}
			b.logs = b.logs[need:]
			b.dropped += need
		case len(b.rollups) > 0:
			need := b.len() - b.maxRows
			if need > len(b.rollups) {
				need = len(b.rollups)
			}
			b.rollups = b.rollups[need:]
			b.dropped += need
		case len(b.tops) > 0:
			need := b.len() - b.maxRows
			if need > len(b.tops) {
				need = len(b.tops)
			}
			b.tops = b.tops[need:]
			b.dropped += need
		default:
			return
		}
	}
}

// flushScheduler 封装「带重试与退避的落库」。
//
// 独立成类型而不是塞进 flushOnce，是为了能单独测：退避节奏、丢弃上限、
// 缓冲合并在生产里都只在故障时出现，没有测试就只能靠线上出事才发现。
type flushScheduler struct {
	buf *flushBuffer
	// backoff 是当前退避的倍数（1 = 不停顿，2 = 跳过一轮，4 = 跳过三轮…）。
	backoff int
	// skip 是还要跳过几轮（退避实现：失败一次就多跳几轮）。
	skip int
	// failedRounds 是连续失败轮数。
	failedRounds int
	// maxBackoffRounds 是退避上限（避免库恢复后还要等很久）。
	maxBackoffRounds int
}

func newFlushScheduler(maxRows int) *flushScheduler {
	return &flushScheduler{
		buf:              newFlushBuffer(maxRows),
		backoff:          1,
		maxBackoffRounds: 8,
	}
}

// shouldSkip 判断这一轮是否应当跳过（退避中）。
func (f *flushScheduler) shouldSkip() bool {
	if f.skip > 0 {
		f.skip--
		return true
	}
	return false
}

// onSuccess 重置退避状态。
func (f *flushScheduler) onSuccess() {
	f.backoff = 1
	f.skip = 0
	f.failedRounds = 0
}

// onFailure 记录失败并推进退避。
//
// 退避节奏：1, 2, 4, 8 轮（上限 8）。落库间隔默认 3 秒，最坏多等 24 秒，
// 既不会在库短暂锁住时狂打，也不会让库恢复后还傻等很久。
func (f *flushScheduler) onFailure() {
	f.failedRounds++
	if f.backoff < f.maxBackoffRounds {
		f.backoff *= 2
	}
	f.skip = f.backoff - 1
}
