package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/50521136/aegis-dns/server/internal/model"
)

func TestAtomicWriteJSONRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	want := &model.Snapshot{
		Version: 7,
		Schema:  model.SnapshotSchema,
		Defaults: model.Defaults{
			Domain:       "dns.example",
			Upstreams:    []string{"223.5.5.5"},
			QueryTimeout: 3000,
		},
	}
	if err := AtomicWriteJSON(path, want); err != nil {
		t.Fatalf("AtomicWriteJSON failed: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if got.Version != want.Version || got.Defaults.Domain != want.Defaults.Domain || got.Defaults.QueryTimeout != want.Defaults.QueryTimeout {
		t.Fatalf("round trip mismatch: got %#v, want %#v", got, want)
	}
	if strings.Contains(string(mustReadFile(t, path)), "\n\n") {
		t.Fatal("JSON writer emitted an unexpected blank line")
	}
}

func TestReadRejectsMultipleJSONValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	first := `{"version":1,"schema":1}`
	if err := os.WriteFile(path, []byte(first+"\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil || !strings.Contains(err.Error(), "多个 JSON 值") {
		t.Fatalf("Read error = %v, want multiple-value error", err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
