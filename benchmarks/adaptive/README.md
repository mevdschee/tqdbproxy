# Adaptive Write Batching Benchmark

This benchmark demonstrates how the adaptive delay system automatically adjusts
under different load levels.

## Overview

The benchmark runs write operations at four different target throughput levels:

- **1,000 ops/sec** (low load)
- **10,000 ops/sec** (medium load)
- **100,000 ops/sec** (high load)
- **1,000,000 ops/sec** (very high load)

For each load level, it tracks:

- Current delay (ms)
- Actual throughput (ops/sec)
- How the delay adapts over time

## Running the Benchmark

```bash
cd benchmarks/adaptive
go run main.go
```

This will:

1. Run benchmarks at each load level (10 seconds each)
2. Generate `adaptive_data.csv` with raw data
3. Generate `adaptive_plot.gnu` gnuplot script
4. Generate `adaptive_summary.txt` with results

## Generating the Graph

Requires gnuplot:

```bash
# Install gnuplot (if needed)
# Ubuntu/Debian: apt install gnuplot
# macOS: brew install gnuplot
# Arch: pacman -S gnuplot

# Generate the graph
gnuplot adaptive_plot.gnu
```

This creates `adaptive_benchmark.png` showing:

- **Left Y-axis (blue)**: Delay in milliseconds
- **Right Y-axis (red)**: Throughput in ops/sec
- **X-axis**: Time in seconds
- **Four lines**: One for each load level

## Expected Behavior

### Low Load (1k ops/sec)

- Delay should decrease over time
- System detects low throughput and reduces delay
- Final delay near minimum (1ms)

### Medium Load (10k ops/sec)

- Delay should stabilize around initial value
- Throughput matches the threshold setting
- Balanced between latency and batching

### High Load (100k ops/sec)

- Delay should increase significantly
- System detects high throughput and increases delay
- More batching opportunities

### Very High Load (1M ops/sec)

- Delay should reach maximum (100ms)
- Maximum batching efficiency
- Trade-off: higher latency for better throughput

## Interpreting Results

The graph shows the **adaptive behavior**:

1. **Delay adjustments**: Watch how each line (delay) changes over time
2. **Responsiveness**: Lower loads → lower delays (more responsive)
3. **Efficiency**: Higher loads → higher delays (more batching)
4. **Adaptation speed**: How quickly the system responds to load changes

## Configuration

The benchmark uses these adaptive settings:

```go
InitialDelayMs:  10      // Start at 10ms
MaxDelayMs:      100     // Cap at 100ms
MinDelayMs:      1       // Floor at 1ms
MaxBatchSize:    1000    // Max operations per batch
WriteThreshold:  <varies> // Target ops/sec (matches test load)
AdaptiveStep:    1.5     // Adjustment multiplier
MetricsInterval: 1       // Adjust every 1 second
```

## Files Generated

- `adaptive_data.csv` - Time-series data (timestamp, throughput, delay, etc.)
- `adaptive_plot.gnu` - Gnuplot script for visualization
- `adaptive_summary.txt` - Human-readable summary with insights
- `adaptive_benchmark.png` - Visual graph (after running gnuplot)

## Understanding the Graph

**Solid lines**: Delay (ms) - shows how batching delay adapts\
**Dashed lines**: Throughput (ops/sec) - shows actual performance

Colors:

- Blue: 1k ops/sec
- Red: 10k ops/sec
- Green: 100k ops/sec
- Orange: 1M ops/sec

The graph visually demonstrates that the system automatically finds the optimal
delay for each load level!
