package rules

import (
	"fmt"
	"testing"
	"unsafe"
)

func TestSuffixSetFinishCompactsDuplicateStorage(t *testing.T) {
	const unique, entries = 4096, 16384
	s := newSuffixSet()
	input := make([]string, entries)
	for i := range input {
		input[i] = fmt.Sprintf("d%04d.example", i%unique)
	}
	s.add(input)
	s.finish()
	if len(s.sorted) != unique || s.size != unique {
		t.Fatalf("size=%d len=%d, want %d", s.size, len(s.sorted), unique)
	}
	if cap(s.sorted) != unique {
		t.Fatalf("retained capacity=%d, want %d after dedupe", cap(s.sorted), unique)
	}
	for _, q := range []string{"d0000.example", "x.d4095.example"} {
		if ok, _, _ := s.lookup(q, 1); !ok {
			t.Errorf("missing %q after compaction", q)
		}
	}
	if ok, _, _ := s.lookup("not-listed.example", 1); ok {
		t.Error("unexpected match after compaction")
	}
	s.finish()
	if cap(s.sorted) != unique || s.size != unique {
		t.Fatal("finish is not idempotent")
	}
	s.add([]string{"new.example"})
	s.finish()
	if ok, _, _ := s.lookup("x.new.example", 1); !ok {
		t.Fatal("add/finish after compaction lost new entry")
	}
}

func TestSuffixSetFinishReleasesFilteredStorage(t *testing.T) {
	s := newSuffixSet()
	invalid := make([]string, 8192)
	for i := range invalid {
		invalid[i] = "bad domain"
	}
	s.add(invalid)
	s.finish()
	if s.sorted != nil || s.size != 0 {
		t.Fatalf("empty filtered set retained capacity=%d, size=%d", cap(s.sorted), s.size)
	}
}

func TestSuffixSetFinishCompactionThresholds(t *testing.T) {
	for _, tc := range []struct {
		name             string
		unique, capacity int
		compact          bool
	}{
		{"small-savings", 1024, 4096, false},
		{"below-eighth", 28673, 32768, false},
		{"eighth-at-minimum", 28672, 32768, true},
		{"quarter-at-minimum", 12288, 16384, true},
		{"unique", 16384, 16384, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &suffixSet{sorted: make([]string, tc.unique, tc.capacity)}
			for i := range s.sorted {
				s.sorted[i] = fmt.Sprintf("d%05d.example", i)
			}
			old := &s.sorted[0]
			s.finish()
			if tc.compact {
				if cap(s.sorted) != tc.unique || &s.sorted[0] == old {
					t.Fatal("large spare capacity was not compacted")
				}
			} else if cap(s.sorted) != tc.capacity || &s.sorted[0] != old {
				t.Fatal("unnecessary copy for small savings")
			}
		})
	}
}

var compactBenchmarkSink *suffixSet

// B/op includes both the initial array and any compact copy; retained-header-B
// measures the final string-header backing array, not transient allocations.
func BenchmarkSuffixSetFinishStorage(b *testing.B) {
	for _, tc := range []struct {
		name            string
		entries, unique int
	}{
		{"unique", 600000, 600000},
		{"low-duplicates", 600000, 550000},
		{"high-duplicates", 2596988, 657827},
	} {
		b.Run(tc.name, func(b *testing.B) {
			pool := make([]string, tc.unique)
			for i := range pool {
				pool[i] = fmt.Sprintf("d%07d.example", i)
			}
			input := make([]string, tc.entries)
			for i := range input {
				input[i] = pool[i%len(pool)]
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				s := &suffixSet{sorted: append([]string(nil), input...)}
				s.finish()
				compactBenchmarkSink = s
			}
			b.ReportMetric(float64(cap(compactBenchmarkSink.sorted))*float64(unsafe.Sizeof("")), "retained-header-B")
		})
	}
}
