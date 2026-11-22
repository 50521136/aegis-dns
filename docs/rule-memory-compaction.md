# Rule-array memory compaction

`suffixSet.finish` sorts and deduplicates subscription entries. Reslicing to the
unique prefix does not release the original backing array. The compact path
copies to an exact-sized array only when at least 1/8 of capacity and 64 KiB of
string headers can be released. Smaller savings use the existing array, with
unused slots cleared. A fully filtered list releases its reserved array.

This trades one temporary compact allocation for less retained storage. It does
not reduce domain-string storage or DNS answer-cache capacity. It does not force
GC or change service memory limits.

## Reproduction

Run from `server/` (use the Go executable installed on the host):

```sh
go test ./internal/rules -run '^TestSuffixSetFinish' -count=1
go test ./internal/rules -run '^$' -bench '^BenchmarkSuffixSetFinishStorage$' -benchmem -benchtime=3x -count=3
AEGIS_MEMORY_SNAPSHOT=/secure/path/snapshot.json.gz go test ./internal/dns -run '^TestRegistryMemorySnapshot$' -count=1 -v
AEGIS_MEMORY_SNAPSHOT=/secure/path/snapshot.json.gz AEGIS_MEMORY_HOT_RELOAD=1 GOMEMLIMIT=800MiB go test ./internal/dns -run '^TestRegistryMemorySnapshot$' -count=1 -v
```

The snapshot is private and must not be committed. The optional probe replaces
upstream addresses with loopback to avoid external requests. It drops the input
snapshot, forces GC, measures the surviving registry heap, and logs a digest of
sampled decisions and statistics. Hot mode retains the old registry while a
separately decoded snapshot builds replacement rules. For peak RSS, compile a
matching test binary and run it under `/usr/bin/time -v`; compiler RSS is not part
of the measurement. B/op means cumulative allocation, not retained heap.

## Measured results

On a production snapshot with 216 tenants and fallback policy `refused`, three
independent runs for each version gave:

- Baseline mean live heap: 129.655 MiB.
- Compact mean live heap: 125.796 MiB (3.859 MiB less).
- 93,060 sampled decisions per run; identical digest `fa0065e58309c375`.
- Single cold-build process peak RSS: 419,072 vs 421,120 KiB.
- Two hot-rebuild peak RSS runs: baseline 597,084 / 597,716 KiB;
  compact 579,192 / 597,364 KiB, with GOMEMLIMIT=800MiB.
- Cold-build cumulative allocation increased from roughly 64.6 to 79.5 MiB;
  copy cost is intentional. Peak figures are observations, not a universal bound.

A synthetic high-duplicate case (2,596,988 entries, 657,827 unique) reduced
retained string-header storage from 41,558,016 to 10,525,232 bytes. It adds one
allocation and about 10.5 MB of cumulative allocation per build. Unique and
low-duplicate cases keep their existing backing array.

Verification includes threshold boundaries, invalid-only inputs, membership,
idempotent finish, add-after-finish, the full race-enabled Go test suite and the
local E2E suite (433 passed, 0 failed). This is a modest live-heap improvement for
the current production dataset, not a promise to remove hundreds of MiB of RSS.
