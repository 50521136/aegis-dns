package dns

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/miekg/dns"

	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/resolver"
	"github.com/50521136/aegis-dns/server/internal/stats"
)

// A canceled protocol context must stop before doing work or writing a reply.
// This is the contract used by DoT/DoH connection handlers.
func TestHandleContextHonoursCancellation(t *testing.T) {
	snap := &model.Snapshot{
		Version: 1,
		Schema:  model.SnapshotSchema,
		Defaults: model.Defaults{
			MaxInflight:  10,
			QueryTimeout: 3000,
			BlockingMode: model.BlockingNullIP,
			Upstreams:    []string{"127.0.0.1:9"},
		},
		Users: []model.UserSnap{{UserID: "u1", ClientID: "aaaaaaaaaa", Enabled: true}},
	}
	reg := NewRegistry(resolver.NewCache())
	if err := reg.Build(snap); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(reg, NewCache(16, 60, 3600), stats.New(16, false), slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.ApplyDefaults(snap.Defaults)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := new(dns.Msg)
	req.SetQuestion("example.com.", dns.TypeA)
	w := &captureWriter{remote: &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5353}}
	h.HandleContext(ctx, w, req, &IdentifyHint{ClientID: "aaaaaaaaaa", Proto: "dot"})
	if w.msg != nil {
		t.Fatal("已取消的请求不应写响应")
	}
}
