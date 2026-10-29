package dns

import (
	"testing"
	"time"
)

// 分桶必须准确，否则 p90/p99 报出来的数字是假的 —— 而排查尾部延迟
// 唯一依赖的就是这几个数。
func TestBucketOfBoundaries(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want int
	}{
		{0, 0},
		{500 * time.Nanosecond, 0},   // <1µs
		{1 * time.Microsecond, 1},    // 1µs  -> 桶1 [1,2)
		{1500 * time.Nanosecond, 1},  // 1.5µs
		{2 * time.Microsecond, 2},    // [2,4)
		{3 * time.Microsecond, 2},
		{4 * time.Microsecond, 3},    // [4,8)
		{1000 * time.Microsecond, 10}, // 1000µs=2^10 附近
		{6 * time.Millisecond, 13},   // 6000µs，2^12=4096, 2^13=8192
		{64 * time.Millisecond, 16},  // 64000µs，2^15=32768, 2^16=65536
	}
	for _, c := range cases {
		got := bucketOf(c.d)
		if got != c.want {
			t.Errorf("bucketOf(%v) = %d, 期望 %d", c.d, got, c.want)
		}
	}
}

// 分位数必须能从桶里恢复出合理的量级。
func TestStageQuantiles(t *testing.T) {
	ResetStageMetrics()
	defer ResetStageMetrics()

	// 造一个「均值小、尾部大」的分布，正是线上 P50=6ms / P90=64ms 的形状。
	// 900 条 5ms + 100 条 80ms。
	for i := 0; i < 900; i++ {
		observe(stageCache, 5*time.Millisecond)
	}
	for i := 0; i < 100; i++ {
		observe(stageCache, 80*time.Millisecond)
	}

	ss := readStage(stageCache)
	if ss.Count != 1000 {
		t.Fatalf("样本数 %d, 期望 1000", ss.Count)
	}

	// 均值应当落在 5ms 与 80ms 之间，且偏向 5ms（900:100）。
	if ss.AvgUS < 5000 || ss.AvgUS > 20000 {
		t.Errorf("均值 %dµs 不合理", ss.AvgUS)
	}
	// P50 应该还在 5ms 量级（桶上界 8192µs）。
	if ss.P50US > 8192 {
		t.Errorf("P50=%dµs 偏大，应该 <=8192", ss.P50US)
	}
	// P90 必须已经跳到 80ms 量级，否则尾部问题会被掩盖。
	if ss.P90US < 60000 {
		t.Errorf("P90=%dµs 偏小，没有反映出尾部", ss.P90US)
	}
	if ss.P99US < ss.P90US {
		t.Errorf("P99(%d) 应当 >= P90(%d)", ss.P99US, ss.P90US)
	}
	t.Logf("count=%d avg=%dµs p50=%dµs p90=%dµs p99=%dµs max=%dµs",
		ss.Count, ss.AvgUS, ss.P50US, ss.P90US, ss.P99US, ss.MaxUS)
}

// observe 是热路径，必须在无锁竞争下也能正确累加。
func TestStageMetricsConcurrent(t *testing.T) {
	ResetStageMetrics()
	defer ResetStageMetrics()

	const goroutines = 8
	const perG = 1000
	done := make(chan struct{}, goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			for i := 0; i < perG; i++ {
				observe(stageTotal, 2*time.Millisecond)
			}
			done <- struct{}{}
		}()
	}
	for g := 0; g < goroutines; g++ {
		<-done
	}

	ss := readStage(stageTotal)
	want := int64(goroutines * perG)
	if ss.Count != want {
		t.Fatalf("并发累加后样本数 %d, 期望 %d —— 有丢失", ss.Count, want)
	}
	t.Logf("并发 %d×%d 累计正确: count=%d", goroutines, perG, ss.Count)
}
