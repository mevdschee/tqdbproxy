# Write Batching Benchmark

This benchmark demonstrates hint-based write batching performance across
different throughput levels.

## Overview

The benchmark runs write operations at four different target throughput levels
with corresponding batching hints:

- **1,000 ops/sec** (baseline - no batching, batch:0)
- **10,000 ops/sec** (low latency batching, batch:1)
- **100,000 ops/sec** (moderate batching, batch:10)
- **1,000,000 ops/sec** (aggressive batching, batch:100)

For each load level, it tracks:

- Batching window (ms)
- Actual throughput (ops/sec)
- Average latency (ms)

## Running the Benchmark

```bash
cd benchmarks/batching
go run main.go
```

This will:

1. Run benchmarks at each load level (3 seconds each)
2. Generate `bars_postgres.dat` and `bars_mysql.dat` with raw data
3. Generate `plot_bars.gnu` gnuplot script

## Generating the Graph

Requires gnuplot:

```bash
# Install gnuplot (if needed)
# Ubuntu/Debian: apt install gnuplot
# macOS: brew install gnuplot
# Arch: pacman -S gnuplot

# Generate the graph
gnuplot plot_bars.gnu
```

This creates `throughput_latency_comparison.png` showing:

- Throughput (ops/sec) achieved at each target rate
- Latency (ms) for each batching configuration
- Side-by-side comparison of PostgreSQL and MariaDB

## Expected Behavior

### Baseline (1k ops/sec, batch:0)

- No batching - immediate execution
- Lowest throughput baseline
- Lowest latency
- Demonstrates performance without batching overhead

### Low Latency (10k ops/sec, batch:1)

- 1ms batching window
- Good balance of low latency and some batching benefits
- Minimal additional latency from batching

### Moderate Batching (100k ops/sec, batch:10)

- 10ms batching window
- Significant batching efficiency gains
- Moderate latency increase
- Good for high-throughput scenarios

### Aggressive Batching (1M ops/sec, batch:100)

- 100ms batching window
- Maximum batching efficiency
- Higher latency but massive throughput gains
- Best for extreme write loads where latency is less critical

## Interpreting Results

The graph shows **hint-based batching performance**:

1. **Throughput**: How many ops/sec achieved at each target rate
2. **Latency**: The trade-off in latency for batching gains
3. **Predictability**: Consistent performance based on hint values
4. **Control**: Explicit control over batching behavior per query

## Configuration

The benchmark uses these batching configurations:

| Target Rate  | Batch Hint | Purpose                            |
| ------------ | ---------- | ---------------------------------- |
| 1k ops/sec   | batch:0    | Baseline (no batching)             |
| 10k ops/sec  | batch:1    | Low latency batching (1ms window)  |
| 100k ops/sec | batch:10   | Moderate batching (10ms window)    |
| 1M ops/sec   | batch:100  | Aggressive batching (100ms window) |

```go
Config{
    MaxBatchSize: 1000,  // Maximum operations per batch
}
```

## Files Generated

- `bars_postgres.dat` - PostgreSQL performance data
- `bars_mysql.dat` - MariaDB performance data
- `plot_bars.gnu` - Gnuplot script for visualization
- `throughput_latency_comparison.png` - Visual graph (after running gnuplot)

## Understanding the Graph

The graph shows two panels (PostgreSQL and MariaDB), each displaying:

**Blue bars**: Throughput (k ops/sec) - actual achieved rate\
**Red bars**: Latency (ms) - average latency per operation

X-axis labels show the target rates (1k, 10k, 100k, 1M) with their corresponding
batch hints, making it easy to see the latency/throughput trade-off at each
level.
