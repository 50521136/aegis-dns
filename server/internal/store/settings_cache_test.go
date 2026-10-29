package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

// 进程内设置缓存必须满足三件事：读到的值对、写完立刻能读到新值、
// 调用方就地改动返回值不会污染缓存。任意一条破掉都是静默的数据错误，
// 所以三个都钉成测试。
func TestSettingsCache(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer st.Close()

	// 1) 首次读：走库，回填缓存
	got, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatalf("首次读取失败: %v", err)
	}
	if got.CacheMinTTL != model.DefaultSettings().CacheMinTTL {
		t.Fatalf("默认值不对: %d", got.CacheMinTTL)
	}

	// 2) 调用方就地改返回值，不能影响后续读
	got.CacheMinTTL = 99999
	got.Upstreams = append(got.Upstreams[:0], "9.9.9.9")
	again, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatalf("二次读取失败: %v", err)
	}
	if again.CacheMinTTL == 99999 || len(again.Upstreams) == 1 && again.Upstreams[0] == "9.9.9.9" {
		t.Fatalf("调用方改动污染了缓存: %+v", again)
	}

	// 3) 走写路径后必须立刻读到新值（缓存失效）
	want := *again
	want.CacheMinTTL = 300
	if err := st.SaveSettingsAndInvalidate(ctx, &want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	after, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatalf("写后读取失败: %v", err)
	}
	if after.CacheMinTTL != 300 {
		t.Fatalf("写完仍读到旧值: CacheMinTTL=%d（缓存没失效）", after.CacheMinTTL)
	}

	// 4) 绕过写路径直接改库，缓存应当仍然返回旧值 —— 这是有意的：
	//    非 SaveSettings 的写入属于运维手工操作，重启 apid 生效。
	if _, err := st.write.ExecContext(ctx,
		`UPDATE meta SET v = '111' WHERE k = 'setting.cache_min_ttl'`); err != nil {
		t.Fatalf("直写库失败: %v", err)
	}
	stale, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatalf("读失败: %v", err)
	}
	if stale.CacheMinTTL != 300 {
		t.Fatalf("缓存语义和预期不符: %d", stale.CacheMinTTL)
	}
}
