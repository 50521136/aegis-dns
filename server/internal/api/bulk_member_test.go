package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/50521136/aegis-dns/server/internal/coord"
	"github.com/50521136/aegis-dns/server/internal/model"
	"github.com/50521136/aegis-dns/server/internal/store"
)

const bulkMemberPath = "/api/v1/admin/users/bulk/member"

func bulkMemberToken(t *testing.T, s *Server, name, role string) string {
	t.Helper()
	u, err := s.store.CreateUser(context.Background(), name, "", "x", role)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := s.tokens.Issue(u.ID, role)
	if err != nil {
		t.Fatal(err)
	}
	return pair.AccessToken
}

func postBulkMember(h http.Handler, token, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, bulkMemberPath, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestBulkMemberRouteAuthAndPreview(t *testing.T) {
	s := newAutoTestServer(t)
	admin := bulkMemberToken(t, s, "admin", model.RoleAdmin)
	user := bulkMemberToken(t, s, "user", model.RoleUser)
	h := s.Handler()
	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"anonymous", "", http.StatusUnauthorized},
		{"regular user", user, http.StatusForbidden},
		{"admin", admin, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := postBulkMember(h, tc.token, "", `{"days":7,"tier_id":"vip","dry_run":true}`)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body)
			}
			if tc.status == http.StatusOK {
				var result struct {
					Users  int  `json:"users"`
					DryRun bool `json:"dry_run"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Users != 1 || !result.DryRun {
					t.Fatalf("unexpected preview: %s (%v)", w.Body, err)
				}
			}
		})
	}
}

func TestBulkMemberValidation(t *testing.T) {
	s := newAutoTestServer(t)
	admin := bulkMemberToken(t, s, "admin", model.RoleAdmin)
	if err := s.store.UpsertTier(context.Background(), model.TierDef{ID: "base", Name: "Base", IsDefault: true}); err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for _, tc := range []struct{ name, key, body string }{
		{"missing key", "", `{"days":7,"tier_id":"vip"}`},
		{"blank key", "  ", `{"days":7,"tier_id":"vip","dry_run":false}`},
		{"combined key", "batch-1,batch-2", `{"days":7,"tier_id":"vip"}`},
		{"oversize key", strings.Repeat("x", 129), `{"days":7,"tier_id":"vip"}`},
		{"zero days", "", `{"days":0,"tier_id":"vip","dry_run":true}`},
		{"negative days", "batch-1", `{"days":-7,"tier_id":"vip"}`},
		{"too many days", "", `{"days":36501,"tier_id":"vip","dry_run":true}`},
		{"missing tier", "batch-1", `{"days":7}`},
		{"unknown tier", "batch-1", `{"days":7,"tier_id":"missing"}`},
		{"free tier", "batch-1", `{"days":7,"tier_id":"free"}`},
		{"default tier", "", `{"days":7,"tier_id":"base","dry_run":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := postBulkMember(h, admin, tc.key, tc.body)
			var got errorEnvelope
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusBadRequest || got.Error.Code != CodeValidationFailed {
				t.Fatalf("status = %d, body = %s, decode err = %v", w.Code, w.Body, err)
			}
		})
	}
}

func TestBulkMemberCommitQueuesSnapshotOnce(t *testing.T) {
	s := newAutoTestServer(t)
	s.coord = coord.New(s.store, filepath.Join(t.TempDir(), "snapshot.json"), s.log)
	admin := bulkMemberToken(t, s, "admin", model.RoleAdmin)
	uid := seedUser(t, s, "user")
	h := s.Handler()
	call := func(key, body string) store.BulkMemberResult {
		t.Helper()
		w := postBulkMember(h, admin, key, body)
		var got store.BulkMemberResult
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s, decode err = %v", w.Code, w.Body, err)
		}
		return got
	}
	const key = "release-batch-7"
	const body = `{"tier_id":"vip","days":7}`
	version, err := s.coord.CurrentVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := call(key, `{"tier_id":"vip","days":7,"dry_run":true}`); !got.DryRun || got.Replayed || got.Users != 1 {
		t.Fatalf("preview = %+v", got)
	}
	assertNoRebuild := func() {
		t.Helper()
		select {
		case <-s.coord.Notify():
			t.Fatal("unexpected rebuild")
		case <-time.After(350 * time.Millisecond): // More than the coordinator debounce.
		}
	}
	assertNoRebuild()
	if got := call(key, body); got.DryRun || got.Replayed || got.Users != 1 || got.Days != 7 || got.Tier != "vip" {
		t.Fatalf("grant = %+v", got)
	}
	select {
	case <-s.coord.Notify():
	case <-time.After(5 * time.Second):
		_, _, err := s.coord.Stats()
		t.Fatalf("no snapshot after grant; coordinator error: %v", err)
	}
	snapshot, err := s.coord.Snapshot()
	if err != nil || snapshot.Version != version+1 || len(snapshot.ChangedUsers) != 0 {
		t.Fatalf("want one full snapshot after grant: %+v, %v", snapshot, err)
	}
	var until int64
	for _, u := range snapshot.Users {
		if u.UserID == uid {
			until = u.MemberUntil
		}
	}
	if until <= time.Now().Unix() {
		t.Fatalf("grant missing from snapshot for %s", uid)
	}
	if got := call(key, `{"days":7,"tier_id":" vip ","dry_run":false}`); !got.Replayed || got.Users != 1 {
		t.Fatalf("replay = %+v", got)
	}
	w := postBulkMember(h, admin, key, `{"days":8,"tier_id":"vip"}`)
	var conflict errorEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &conflict); err != nil || w.Code != http.StatusConflict || conflict.Error.Code != CodeConflict {
		t.Fatalf("conflict: status = %d, body = %s, decode err = %v", w.Code, w.Body, err)
	}
	u, err := s.store.GetUser(context.Background(), uid)
	if err != nil || u.MemberUntil != until {
		t.Fatalf("replay changed expiry: user = %+v, err = %v", u, err)
	}
	assertNoRebuild()
}
