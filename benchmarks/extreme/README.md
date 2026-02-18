# Extreme Throughput Test

Tests the absolute maximum throughput of the write batching system with a no-op
backend.

## Results

With pure no-op backend (no actual database I/O):

- **Peak Throughput**: ~600k ops/sec
- **Average Throughput**: ~560-575k ops/sec
- **Configuration**: 1ms delay, 1000 batch size, 10000 workers

## Why Not >1M ops/sec?

The system achieves ~600k ops/sec maximum, which is 60% of the 1M target. The
bottlenecks are:

1. **Channel/Mutex Contention**: 10,000 goroutines competing for batch queues
2. **Batching Delay**: Even at 1ms, this limits throughput to ~1000 batches/sec
   max
3. **Go Runtime Overhead**: Context switching, scheduling 10k goroutines
4. **Atomic Operations**: Metrics collection and counters add overhead

## Throughput Analysis

| Delay | Workers | Avg Throughput | Peak         |
| ----- | ------- | -------------- | ------------ |
| 100ms | 1000    | 486k ops/sec   | 522k ops/sec |
| 100ms | 2000    | 533k ops/sec   | 569k ops/sec |
| 50ms  | 4000    | 558k ops/sec   | 592k ops/sec |
| 10ms  | 6000    | 575k ops/sec   | 611k ops/sec |
| 1ms   | 10000   | 564k ops/sec   | 600k ops/sec |

**Conclusion**: Optimal is ~10ms delay with 6000 workers = ~600k ops/sec peak

## Running the Test

```bash
cd benchmarks/extreme
go build
./extreme
```

The test runs for 30 seconds and reports throughput every second.

## Real-World Performance

With actual database I/O, expect:

- **SQLite**: 50-100k ops/sec (limited by disk I/O)
- **MariaDB/PostgreSQL (local)**: 100-200k ops/sec
- **MariaDB/PostgreSQL (network)**: 50-150k ops/sec

The write batching system provides **2-3x improvement** over individual writes.

## Optimization Notes

The current implementation prioritizes:

- ✓ Correctness (ACID properties)
- ✓ Latency (bounded delay)
- ✓ Fairness (all requests processed)
- ○ Absolute max throughput

To reach >1M ops/sec would require:

- Lock-free data structures
- Batching across multiple goroutines simultaneously
- Zero-copy batch building
- Reduced metrics collection overhead

These optimizations add significant complexity and may not be worth the
trade-off for real-world database workloads where I/O is the limiting factor.
