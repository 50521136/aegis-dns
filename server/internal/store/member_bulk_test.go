package store

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/50521136/aegis-dns/server/internal/model"
)

func openBulkMemberTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedBulkMember(t *testing.T, s *Store, name, role, tier string, until int64) string {
	t.Helper()
	ctx := context.Background()
	u, err := s.CreateUser(ctx, name, "", "x", role)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMembership(ctx, u.ID, tier, until); err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func bulkMemberState(t *testing.T, s *Store, uid string) (int64, string) {
	t.Helper()
	u, err := s.GetUser(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	return u.MemberUntil, u.TierID
}

func TestBulkMembershipPreviewAndGrant(t *testing.T) {
	ctx := context.Background()
	s := openBulkMemberTestStore(t, filepath.Join(t.TempDir(), "members.db"))
	for _, tier := range []model.TierDef{{ID: "plus", Name: "Plus", Sort: 5}, {ID: "gold", Name: "Gold", Sort: 20}} {
		if err := s.UpsertTier(ctx, tier); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Unix()
	active := now + 86400
	const delta int64 = 2 * 86400
	cases := []struct {
		name, role, tier, wantTier string
		until, wantUntil           int64 // zero wantUntil means now + delta
	}{
		{"new", model.RoleUser, "", "plus", 0, 0},
		{"expired", model.RoleUser, "gold", "plus", now - 1, 0},
		{"active higher", model.RoleUser, "gold", "gold", active, active + delta},
		{"active implicit vip", model.RoleUser, "", "vip", active, active + delta},
		{"active stale tier", model.RoleUser, "removed", "vip", active, active + delta},
		{"active lower", model.RoleUser, "free", "plus", active, active + delta},
		{"permanent higher", model.RoleUser, "gold", "gold", model.MemberForever, model.MemberForever},
		{"permanent lower", model.RoleUser, "free", "plus", model.MemberForever, model.MemberForever},
		{"near permanent", model.RoleUser, "plus", "plus", model.MemberForever - 1, model.MemberForever},
		{"above permanent", model.RoleUser, "plus", "plus", math.MaxInt64, model.MemberForever},
		{"admin", model.RoleAdmin, "free", "free", active, active},
	}
	ids := make([]string, len(cases))
	for i, tc := range cases {
		ids[i] = seedBulkMember(t, s, tc.name, tc.role, tc.tier, tc.until)
	}
	req := BulkMemberRequest{TierID: " plus ", Days: 2, DryRun: true, IdempotencyKey: "batch-1",
		ActorID: ids[len(ids)-1], IP: "192.0.2.3"}
	preview, err := s.BulkGrantMembership(ctx, req)
	const changed = 9
	if err != nil || preview != (BulkMemberResult{Users: changed, Tier: "plus", Days: 2, DryRun: true}) {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	for i, tc := range cases {
		if until, tier := bulkMemberState(t, s, ids[i]); until != tc.until || tier != tc.tier {
			t.Fatalf("%s changed during preview: %d, %q", tc.name, until, tier)
		}
	}
	if audits, err := s.RecentAudit(ctx, 10); err != nil || len(audits) != 0 {
		t.Fatalf("preview audit = %+v, %v", audits, err)
	}
	req.DryRun = false
	before := time.Now().Unix()
	result, err := s.BulkGrantMembership(ctx, req)
	after := time.Now().Unix()
	if err != nil || result != (BulkMemberResult{Users: changed, Tier: "plus", Days: 2}) {
		t.Fatalf("grant = %+v, %v", result, err)
	}
	for i, tc := range cases {
		until, tier := bulkMemberState(t, s, ids[i])
		lo, hi := tc.wantUntil, tc.wantUntil
		if tc.wantUntil == 0 {
			lo, hi = before+delta, after+delta
		}
		if until < lo || until > hi || tier != tc.wantTier {
			t.Errorf("%s = (%d, %q), want expiry [%d,%d] and tier %q", tc.name, until, tier, lo, hi, tc.wantTier)
		}
	}
	audits, err := s.RecentAudit(ctx, 10)
	if err != nil || len(audits) != 1 || audits[0].UserID != req.ActorID || audits[0].IP != req.IP ||
		audits[0].Action != "admin_bulk_grant_membership" || audits[0].Detail != "tier=plus days=2 users=9" {
		t.Fatalf("grant audit = %+v, %v", audits, err)
	}
}

func TestBulkMembershipConcurrentReplay(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "members.db")
	s1, s2 := openBulkMemberTestStore(t, path), openBulkMemberTestStore(t, path)
	start := time.Now().Unix() + 86400
	uid := seedBulkMember(t, s1, "user", model.RoleUser, "vip", start)
	req := BulkMemberRequest{TierID: "vip", Days: 7, IdempotencyKey: "batch-1"}
	type attempt struct {
		result BulkMemberResult
		err    error
	}
	out := make(chan attempt, 2)
	ready := make(chan struct{})
	for _, s := range []*Store{s1, s2} {
		go func() {
			<-ready
			result, err := s.BulkGrantMembership(ctx, req)
			out <- attempt{result, err}
		}()
	}
	close(ready)
	a, b := <-out, <-out
	if a.err != nil || b.err != nil || a.result.Replayed == b.result.Replayed || a.result.Users != 1 || b.result.Users != 1 {
		t.Fatalf("concurrent attempts = %+v, %+v", a, b)
	}
	if until, _ := bulkMemberState(t, s1, uid); until != start+7*86400 {
		t.Fatalf("expiry = %d, want a single extension from %d", until, start)
	}
	newUser := seedBulkMember(t, s1, "late", model.RoleUser, "", 0)
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
	s3 := openBulkMemberTestStore(t, path)
	result, err := s3.BulkGrantMembership(ctx, req)
	if err != nil || !result.Replayed || result.Users != 1 {
		t.Fatalf("replay after reopen = %+v, %v", result, err)
	}
	if until, _ := bulkMemberState(t, s3, newUser); until != 0 {
		t.Fatal("replay changed a user who joined after the original grant")
	}
	for _, changed := range []BulkMemberRequest{
		{TierID: "vip", Days: 8, IdempotencyKey: req.IdempotencyKey},
		{TierID: "different", Days: 7, IdempotencyKey: req.IdempotencyKey},
	} {
		if _, err := s3.BulkGrantMembership(ctx, changed); !errors.Is(err, ErrBulkMemberConflict) {
			t.Fatalf("changed request returned %v, want conflict", err)
		}
	}
	if audits, err := s3.RecentAudit(ctx, 10); err != nil || len(audits) != 1 {
		t.Fatalf("replay and conflict should not audit again: %+v, %v", audits, err)
	}
}

func TestBulkMembershipAuditFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	s := openBulkMemberTestStore(t, filepath.Join(t.TempDir(), "members.db"))
	u1 := seedBulkMember(t, s, "first", model.RoleUser, "", 0)
	u2 := seedBulkMember(t, s, "second", model.RoleUser, "", 0)
	if _, err := s.write.ExecContext(ctx, `CREATE TRIGGER fail_audit BEFORE INSERT ON audit_log
		BEGIN SELECT RAISE(ABORT, 'forced audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	req := BulkMemberRequest{TierID: "vip", Days: 7, IdempotencyKey: "batch-1"}
	if _, err := s.BulkGrantMembership(ctx, req); err == nil || !strings.Contains(err.Error(), "forced audit failure") {
		t.Fatalf("grant returned %v, want injected audit failure", err)
	}
	for _, uid := range []string{u1, u2} {
		if until, tier := bulkMemberState(t, s, uid); until != 0 || tier != "" {
			t.Fatalf("user %s escaped rollback: %d, %q", uid, until, tier)
		}
	}
	if _, err := s.write.ExecContext(ctx, `DROP TRIGGER fail_audit`); err != nil {
		t.Fatal(err)
	}
	result, err := s.BulkGrantMembership(ctx, req)
	if err != nil || result.Replayed || result.Users != 2 {
		t.Fatalf("retry should commit a new grant: %+v, %v", result, err)
	}
}
