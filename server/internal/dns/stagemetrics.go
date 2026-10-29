package dns

import (
	"sync/atomic"
	"time"
)

// 分阶段耗时直方图。
//
// 为什么需要它：线上只能看到「整条查询」的延迟（P50=6ms、P90=64ms、
// P99=260ms），但 64ms 的尾巴到底卡在用户识别、规则匹配、缓存查询、还是
// 上游往返，从外部完全看不出来。之前用临时探测脚本去猜，结果被脚本自身
// 的问题误导过一次（把 Python ssl 客户端的 40ms 当成服务端 Nagle 问题）。
//
// 这里用 log2 桶做亚微秒级计数，热路径上只有一次原子加，没有内存分配：
//
//	桶 i 表示耗时落在 [2^(i-1), 2^i) 微秒
//	桶 0 单独表示 <1µs
//
// 为什么不用精确采集：每条查询都 append 到切片会在 700 QPS 下每秒分配
// 几十 KB，GC 压力反而污染测量结果。桶化后每次只有一次 atomic.Add，
// 且读数时能直接看出「90% 落在哪个量级」，这正好是排查尾部延迟要的信息。
const (
	stageBuckets = 24 // 覆盖到 2^23 µs ≈ 8.4 秒
	stageCount   = 6
)

// 阶段下标。取值必须稳定，Metrics 里按名字输出。
const (
	stageIdentify = iota // 用户识别（纯内存查表）
	stageGate            // 并发闸门 + 配额 + 限流等前置校验
	stageRules           // 规则匹配（含拦截/改写构造应答）
	stageCache           // 缓存查询（含命中直接返回）
	stageUpstream        // 上游往返（含并发合并等待）
	stageTotal           // 整条管线
)

var stageNames = [stageCount]string{
	"identify", "gate", "rules", "cache", "upstream", "total",
}

// stageHist 是一组阶段直方图。所有字段原子操作，可并发读写。
type stageHist struct {
	// counts[stage][bucket]
	counts [stageCount][stageBuckets]atomic.Int64
	// sums[stage] 是累计微秒数，用于算均值
	sums [stageCount]atomic.Int64
	// samples[stage] 是总样本数
	samples [stageCount]atomic.Int64
}

var stages stageHist

// bucketOf 把耗时映射到 log2 桶。
func bucketOf(d time.Duration) int {
	us := d.Microseconds()
	if us < 1 {
		return 0
	}
	b := 1
	for us >= 2 {
		us >>= 1
		b++
	}
	if b >= stageBuckets {
		b = stageBuckets - 1
	}
	return b
}

// observe 记录一次阶段耗时。热路径调用，必须无分配。
func observe(stage int, d time.Duration) {
	if stage < 0 || stage >= stageCount {
		return
	}
	stages.counts[stage][bucketOf(d)].Add(1)
	stages.sums[stage].Add(d.Microseconds())
	stages.samples[stage].Add(1)
}

// stageStats 是某个阶段的读数。
type stageStats struct {
	Count  int64
	AvgUS  int64
	P50US  int64
	P90US  int64
	P99US  int64
	MaxUS  int64
	Bucket map[string]int64 // 量级 -> 次数，便于看长尾
}

// readStage 汇总某个阶段。只在 metricsLoop 里调用（每 5 分钟一次），
// 不需要考虑开销。
func readStage(stage int) stageStats {
	out := stageStats{Bucket: map[string]int64{}}
	if stage < 0 || stage >= stageCount {
		return out
	}
	out.Count = stages.samples[stage].Load()
	if out.Count == 0 {
		return out
	}
	out.AvgUS = stages.sums[stage].Load() / out.Count

	// 从桶里恢复分位数。
	//
	// 不能直接取桶上界：桶是 log2 划分的，跨一个桶就是 2 倍，取上界会把
	// 分位数系统性放大（取整让 P90 从 80ms 报成 8ms 那次就是反过来——
	// 上界落在 8192µs 而真实值是 80000µs，误差一个数量级）。
	//
	// 正确做法是在**目标分位数所在的桶内做线性插值**：
	//   桶 b 的区间是 [lo, hi)，该桶内累计到第 k 个样本时，位置按
	//   lo + (hi-lo) * (k / c) 估算。误差不超过一个桶宽（即最多 2 倍
	//   以内的偏差，实际因为桶内分布通常均匀，误差远小于此）。
	var cumulative int64
	type quant struct {
		q    int64
		slot *int64
	}
	thresholds := []quant{
		{50 * out.Count / 100, &out.P50US},
		{90 * out.Count / 100, &out.P90US},
		{99 * out.Count / 100, &out.P99US},
	}
	ti := 0
	for b := 0; b < stageBuckets; b++ {
		c := stages.counts[stage][b].Load()
		if c == 0 {
			continue
		}
		lo := int64(0)
		hi := int64(1)
		if b > 0 {
			lo = int64(1) << uint(b-1)
			hi = int64(1) << uint(b)
		}
		out.Bucket[bucketLabel(b)] = c
		if hi > out.MaxUS {
			out.MaxUS = hi
		}
		prevCumulative := cumulative
		cumulative += c
		for ti < len(thresholds) && cumulative > thresholds[ti].q {
			// 目标样本在这个桶里的相对位置
			within := thresholds[ti].q - prevCumulative
			frac := float64(within) / float64(c)
			if frac < 0 {
				frac = 0
			}
			if frac > 1 {
				frac = 1
			}
			est := lo + int64(float64(hi-lo)*frac)
			*thresholds[ti].slot = est
			ti++
		}
	}
	// 没填上的分位数（样本太少）用 MaxUS 兜底，避免显示 0 引起误解。
	for ; ti < len(thresholds); ti++ {
		*thresholds[ti].slot = out.MaxUS
	}
	return out
}

// bucketLabel 给桶一个人能读的标签。
func bucketLabel(b int) string {
	if b == 0 {
		return "<1us"
	}
	upper := int64(1) << uint(b)
	switch {
	case upper < 1000:
		return itoa64(upper) + "us"
	case upper < 1000000:
		return itoa64(upper/1000) + "ms"
	default:
		return itoa64(upper/1000000) + "s"
	}
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// StageMetrics 把全部阶段读数导出，供 metricsLoop 打日志。
func StageMetrics() map[string]stageStats {
	out := make(map[string]stageStats, stageCount)
	for i := 0; i < stageCount; i++ {
		out[stageNames[i]] = readStage(i)
	}
	return out
}

// ResetStageMetrics 清空计数（测试用）。
func ResetStageMetrics() {
	for i := 0; i < stageCount; i++ {
		for b := 0; b < stageBuckets; b++ {
			stages.counts[i][b].Store(0)
		}
		stages.sums[i].Store(0)
		stages.samples[i].Store(0)
	}
}
