# Hydra Performance & Benchmark Report

This document records the baseline performance characteristics, throughput benchmarks, memory profiles, and rate-limiting accuracy metrics measured for Hydra.

---

## 1. Transfer Throughput

Measured on Linux (x86_64, loopback HTTP server with chunking):

| Benchmark Test | Operations / Throughput | Memory Allocated / Op | Allocations / Op | Notes |
| :--- | :--- | :--- | :--- | :--- |
| `BenchmarkSingleStreamDownload` | **2,311 MB/s** | 6.3 KB / op | 68 allocs/op | Single HTTP connection stream read into discard buffer |
| `BenchmarkRangedDownload` | **69.4 MB/s** | 457 KB / op | 506 allocs/op | 4 parallel workers with dynamic work stealing, range requests, and concurrent filesystem positional disk writes |

---

## 2. Rate Limiting Accuracy

Hydra employs a thread-safe token bucket algorithm with microsecond timestamp reconciliation.

### Accuracy Verification
- **Target Rate**: 2.00 MB/s (2,097,152 bytes/sec)
- **Transfer Volume**: 1,048,576 bytes (1.00 MB in 64 KB chunks)
- **Expected Duration**: 500.00 ms
- **Actual Elapsed Duration**: **500.88 ms**
- **Accuracy Deviation**: **< 0.18%** error margin under standard OS thread scheduling.

### Lock Contention & Token Calculation Overhead
- `BenchmarkRateLimiterThroughput`: **15,966 MB/s** (2,052 ns/op, 0 allocs/op)
- Mutex lock duration per chunk request is negligible (< 2.1 µs), ensuring the rate limiter adds no latency bottleneck even under high thread counts.

---

## 3. Memory Footprint & Resource Consumption

### Buffer Recycling
- Workers pull transfer buffers from a shared `sync.Pool` (`32 KB` buffers) rather than allocating fresh slices per read.
- `BenchmarkMemoryUse`: **21.36 ns/op**, **0 B/op**, **0 allocs/op**.
- Heap pressure remains completely flat regardless of whether the file being downloaded is 10 MB or 100 GB.

### Large File Tracking Overhead
- In-memory tracker footprint for large files (e.g. 50 GB split into 32 dynamic chunk segments) consumes **< 5 KB** total heap space.
- Chunk states are updated in-place via atomic pointers (`atomic.LoadInt64`, `atomic.StoreInt64`), avoiding GC churn during active downloads.

---

## 4. Running Benchmarks Locally

To reproduce the performance validation benchmarks:

```bash
# Run throughput and memory benchmarks
go test -bench=. -benchmem -run=^$ ./pkg/downloader/...

# Run rate limiting and large-file memory footprint verification tests
go test -v -run "TestRateLimitingAccuracy|TestLargeFileMemoryFootprint" ./pkg/downloader/...
```

