package dns

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/resolver"
)

// Optional production-scale probe: AEGIS_MEMORY_SNAPSHOT points to a local
// gzipped snapshot. Upstreams are replaced with loopback to avoid network I/O.
func TestRegistryMemorySnapshot(t *testing.T) {
	path := os.Getenv("AEGIS_MEMORY_SNAPSHOT")
	if path == "" {
		t.Skip("set AEGIS_MEMORY_SNAPSHOT to profile a real snapshot")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	snap := new(model.Snapshot)
	if err := json.NewDecoder(z).Decode(snap); err != nil {
		t.Fatal(err)
	}
	snap.Defaults.Upstreams = []string{"127.0.0.1:9"}
	snap.Defaults.FallbackUpstreams = nil
	snap.Defaults.UpstreamsOS = nil
	snap.Defaults.BootstrapDNS = nil
	for i := range snap.Users {
		snap.Users[i].Upstreams = nil
	}
	pools := resolver.NewCache()
	reg := NewRegistry(pools)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	if err := reg.Build(snap); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if os.Getenv("AEGIS_MEMORY_HOT_RELOAD") == "1" {
		// Model the real loader: retain the old immutable registry while a
		// separately decoded snapshot and its replacement rules are built.
		old := reg.Load()
		snap = nil
		runtime.GC()
		f2, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		z2, err := gzip.NewReader(f2)
		if err != nil {
			t.Fatal(err)
		}
		snap = new(model.Snapshot)
		if err := json.NewDecoder(z2).Decode(snap); err != nil {
			t.Fatal(err)
		}
		z2.Close()
		f2.Close()
		snap.Version++
		snap.ChangedUsers = nil
		snap.Defaults.Upstreams = []string{"127.0.0.1:9"}
		snap.Defaults.FallbackUpstreams = nil
		snap.Defaults.UpstreamsOS = nil
		snap.Defaults.BootstrapDNS = nil
		for i := range snap.Users {
			snap.Users[i].Upstreams = nil
		}
		start = time.Now()
		if err := reg.Build(snap); err != nil {
			t.Fatal(err)
		}
		elapsed = time.Since(start)
		runtime.KeepAlive(old)
	}
	h := fnv.New64a()
	queries := 0
	for _, u := range snap.Users {
		ur := reg.Load().LookupClientID(u.ClientID)
		if ur == nil {
			continue
		}
		rs := ur.Rules.Load()
		fmt.Fprintf(h, "%s:%+v;", u.UserID, rs.Stats)
		for _, list := range [][]string{u.SubAllow, u.SubBlock, snap.Global.SubAllow, snap.Global.SubBlock} {
			step := len(list)/100 + 1
			for i := 0; i < len(list); i += step {
				for _, q := range []string{list[i], "probe." + list[i]} {
					d := rs.Match(q, 1)
					fmt.Fprintf(h, "%s:%s:%s:%s;", q, d.Action, d.Source, d.Value)
					queries++
				}
			}
		}
	}
	fallback := reg.Load().Fallback().Rules.Load()
	fmt.Fprintf(h, "fallback:%+v", fallback.Stats)
	snap = nil
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&after)
	t.Logf("MEMORY heap_live=%d heap_inuse=%d build_allocated=%d build_ms=%d samples=%d digest=%x", after.HeapAlloc, after.HeapInuse, after.TotalAlloc-before.TotalAlloc, elapsed.Milliseconds(), queries, h.Sum64())
	runtime.KeepAlive(reg)
}
