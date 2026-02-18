# ProxyBatch Benchmark Analysis - CPU Load Study

**Date:** February 18, 2026\
**Duration:** 32.66s\
**Total CPU Samples:** 39.56s (121% due to multiple cores)

## Executive Summary

The proxybatch benchmark reveals that **syscalls and I/O operations dominate CPU
usage** (40.62%), with database driver operations consuming another ~23% of CPU
time. The application successfully tested both MySQL and PostgreSQL with
batching enabled.

---

## Performance Results

### MySQL Performance

| Batch Hint (ms) | Throughput (k ops/s) | Avg Latency (ms) |
| --------------- | -------------------- | ---------------- |
| 0 (no batching) | 42.6                 | 2.35             |
| 1               | 42.4                 | 2.36             |
| 10              | **82.6**             | 12.08            |
| 100             | 58.7                 | 83.45            |

**Best:** 10ms batch window achieves **82.6k ops/sec** (~94% improvement over no
batching)

### PostgreSQL Performance

| Batch Hint (ms) | Throughput (k ops/s) | Avg Latency (ms) |
| --------------- | -------------------- | ---------------- |
| 0 (no batching) | 44.5                 | 2.24             |
| 1               | 45.5                 | 2.19             |
| 10              | **66.9**             | 14.71            |
| 100             | 52.9                 | 89.54            |

**Best:** 10ms batch window achieves **66.9k ops/sec** (~50% improvement over no
batching)

---

## CPU Profile Analysis

### Top CPU Consumers (by category)

#### 1. System Calls & I/O (40.62% - 16.07s)

```
internal/runtime/syscall.Syscall6    16.07s  40.62%  ← DOMINANT
syscall.RawSyscall6                  14.44s  36.50%
syscall.Write                        10.53s  26.62%
syscall.Read                          3.18s   8.04%
```

**Finding:** Network I/O syscalls consume the largest share of CPU time. This is
expected for a database benchmark doing heavy I/O operations.

#### 2. Database Drivers (~23% combined)

```
MySQL Driver:
  mysql.(*mysqlConn).Exec             9.09s  22.98%
  mysql.(*mysqlConn).ExecContext      9.16s  23.15%
  mysql.(*mysqlConn).readPacket       3.18s   8.04%
  mysql.(*buffer).fill                3.04s   7.68%
  mysql.(*mysqlConn).writePacket      5.50s  13.90%

PostgreSQL Driver:
  pq.(*conn).simpleExec               8.70s  21.99%
  pq.(*conn).Exec                     8.88s  22.45%
  pq.(*conn).recvMessage              2.65s   6.70%
```

**Finding:** Both drivers show similar CPU consumption patterns. MySQL spends
slightly more time on packet I/O operations.

#### 3. Runtime Scheduler (27.65% - 10.94s)

```
runtime.findRunnable                 10.94s  27.65%
runtime.schedule                     11.55s  29.20%
runtime.netpoll                       2.10s   5.31%
```

**Finding:** Significant scheduler activity due to high concurrency and
goroutine context switching.

#### 4. Database/SQL Package (62.89% cumulative)

```
database/sql.(*DB).ExecContext       24.88s  62.89%  (cumulative)
database/sql.(*DB).execDC            21.11s  53.36%
database/sql.withLock                18.59s  46.99%
```

**Finding:** The standard library's database/sql package coordinates all
operations but adds minimal direct overhead.

#### 5. Synchronization (13.35% - 5.28s)

```
runtime.futex                         5.28s  13.35%
runtime.lock2                         2.20s   5.56%
runtime.procyield                     1.23s   3.11%
```

**Finding:** Lock contention is present but not excessive. Connection pool
management requires synchronization.

---

## Memory Profile Analysis

**Total Allocated:** 12.5 MB (in-use at profile time)

### Top Memory Consumers

```
runtime.allocm                       4104 kB  32.81%  ← Thread allocation
runtime.malg                         3073 kB  24.57%  ← Goroutine stacks
mysql.newBuffer                      2056 kB  16.44%  ← MySQL I/O buffers
database/sql.(*DB).conn              4145 kB  33.14%  ← Connection pool
```

**Finding:** Memory usage is dominated by runtime overhead (goroutines/threads)
and database connection management. Very low application-level allocations
indicate efficient operation.

---

## Key Insights

### ✅ Strengths

1. **Batching Effectiveness**
   - 10ms batch window provides optimal throughput gains
   - MySQL: 94% throughput improvement (42.6k → 82.6k ops/s)
   - PostgreSQL: 50% throughput improvement (44.5k → 66.9k ops/s)

2. **Low Application Overhead**
   - Application code (`main.runBenchmark.func1`) uses only 0.43% direct CPU
   - Most time spent in syscalls (expected for I/O-bound workload)

3. **Efficient Memory Usage**
   - Only 12.5 MB in-use memory
   - No obvious memory leaks or excessive allocations
   - Minimal per-operation allocation overhead

4. **Driver Performance**
   - Both MySQL and PostgreSQL drivers show similar efficiency
   - Network I/O dominates, not driver parsing/encoding logic

### ⚠️ Areas for Optimization

1. **Syscall Overhead (40.62%)**
   - Consider using buffered writes to reduce syscall frequency
   - Batch network operations where possible
   - Investigate kernel-level optimizations (TCP_NODELAY, buffer sizes)

2. **Scheduler Overhead (27.65%)**
   - High goroutine context switching
   - Could benefit from reducing concurrency or using worker pools
   - Consider adjusting GOMAXPROCS for CPU-bound scenarios

3. **Connection Pool Locking**
   - `database/sql.withLock` appears frequently in profile
   - May benefit from connection pool tuning or sharding

4. **100ms Batching Degrades Performance**
   - Excessive batching increases latency without throughput gains
   - Sweet spot is 10ms for both databases
   - Suggests optimal batch size is ~800-900 operations

### 🎯 Recommendations

1. **Production Configuration**
   - Use 10ms batch window for optimal throughput/latency balance
   - Configure connection pool: 100-200 max connections
   - Monitor CPU usage under sustained load

2. **Further Profiling**
   - Test with varying concurrency levels
   - Profile under sustained load (not just 32s duration)
   - Compare proxy vs direct database connection overhead

3. **Optimization Opportunities**
   - Implement write coalescing at network layer
   - Tune TCP parameters (SO_SNDBUF, SO_RCVBUF)
   - Consider connection pooling per-backend strategy

---

## Visualization Files Generated

- **[cpu_profile.svg](cpu_profile.svg)** - Full CPU call graph
- **[proxybatch_performance.png](proxybatch_performance.png)** - Performance
  comparison chart
- **[cpu.prof](cpu.prof)** - Raw CPU profile data
- **[mem.prof](mem.prof)** - Raw memory profile data

To interactively explore profiles:

```bash
# Interactive CPU analysis
go tool pprof cpu.prof

# Web-based visualization
go tool pprof -http=:8080 cpu.prof
```

---

## Conclusion

The proxybatch benchmark demonstrates that **write batching with a 10ms window
provides substantial throughput improvements** while keeping latency reasonable.
CPU usage is dominated by expected I/O syscalls, with minimal application
overhead. The system scales well with proper connection pool configuration.

**Next Steps:** Run sustained load tests and compare proxy overhead vs direct
database connections under production-like conditions.
