# Stack processing performance

Measured on 2026-11-06, macOS arm64, Apple M3 Pro, Go 1.26.0.
Baseline source: commit `1b55e74` with the same benchmark and a constructor-only refactor.

## Change

- Reserve a bounded in-flight slot under the mutex, then capture program counters outside it.
- Retain up to 48 program counters in a pooled fixed-size buffer, rather than formatting every start callsite.
- At span end, symbolize only spans that meet the duration threshold and have no existing `code.stacktrace`.
- Release the buffer for short spans without formatting; avoid map locking when stack capture is disabled.
- Preserve the 4096 in-flight limit, including concurrent reservations, and prevent captures being retained after shutdown.

The callsite is still the span's start location. Capturing PCs has a cost even for short spans;
this does not provide profiling-derived or zero-overhead stacks.

## Local results

Median of three 150 ms runs; complete SDK start/end operations, without a network exporter.
"Parallel / 8" means `RunParallel` at GOMAXPROCS=8. It is not application request latency.

| Scenario | Before ns/op | After ns/op | Reduction | Before allocations | After allocations |
|---|---:|---:|---:|---:|---:|
| Short span / serial | 3856 | 963.3 | 75.0% | 21 | 3 |
| Short span / parallel / 8 | 3161 | 1035 | 67.3% | 19 | 3 |
| Stack on every span / parallel / 8 | 2978 | 1647 | 44.7% | 21 | 8 |

Short parallel spans used 2754 B/op before and 945 B/op after. These are local measurements,
not a statistically established speedup or a production performance guarantee. Compiler inlining,
call depth, GC, CPU scheduling, and concurrent workload affect results. Disabled-mode timing also
varied between runs, so no disabled-mode speedup is claimed.

Raw measurements: `testdata/benchmarks/before.txt` and `testdata/benchmarks/after.txt`.

```bash
go test -run '^$' -bench '^BenchmarkStackProcessor$' -benchmem -benchtime=150ms -count=3 -cpu=1,4,8
```

CI records benchmarks independently and does not fail builds on timing differences between shared runners.
