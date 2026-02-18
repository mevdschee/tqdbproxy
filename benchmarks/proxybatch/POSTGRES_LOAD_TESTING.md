# PostgreSQL Load Testing Tools

This directory contains tools for measuring PostgreSQL server load under
different scenarios.

## Quick Start

### Option 1: Simple Monitoring (Recommended)

Monitor PostgreSQL while running any benchmark:

```bash
# Terminal 1: Start monitoring
./pg_monitor.sh my_test 30

# Terminal 2: Run your benchmark
./proxybatch
```

This creates:

- `my_test_pg_stats.txt` - Before/after statistics
- `my_test_pg_realtime.csv` - Second-by-second metrics

### Option 2: Automated Scenario Testing

Run all scenarios automatically:

```bash
# Make sure proxy is running first
cd /home/maurits/projects/tqdbproxy
bash start.sh

# In another terminal, run scenarios
cd /home/maurits/projects/tqdbproxy/benchmarks/proxybatch
./run_scenarios.sh
```

This tests:

1. Direct PostgreSQL (baseline)
2. Via proxy - no batching
3. Via proxy - 1ms batch window
4. Via proxy - 10ms batch window
5. Via proxy - 100ms batch window

Results saved to `scenario_results_TIMESTAMP/`

### Option 3: Advanced Monitoring

Full-featured monitoring with pg_stat_statements:

```bash
./measure_postgres_load.sh
```

## Scripts

### pg_monitor.sh

Simple real-time PostgreSQL monitoring.

**Usage:**

```bash
./pg_monitor.sh <output_prefix> <duration_seconds>
```

**Example:**

```bash
./pg_monitor.sh test1 30
```

**Output:**

- `test1_pg_stats.txt` - Database statistics
- `test1_pg_realtime.csv` - Time-series data

**View timeline:**

```bash
column -t -s, test1_pg_realtime.csv | less
```

### run_scenarios.sh

Automated multi-scenario testing with PostgreSQL monitoring.

**Usage:**

```bash
./run_scenarios.sh
```

**Requirements:**

- PostgreSQL running on localhost:5432
- (Optional) tqdbproxy running on localhost:5433

**Output:**

- `scenario_results_TIMESTAMP/` directory
- `REPORT.md` with comparison

### measure_postgres_load.sh

Interactive load testing framework with pg_stat_statements support.

**Usage:**

```bash
./measure_postgres_load.sh
```

**Requirements:**

- PostgreSQL with pg_stat_statements extension (optional but recommended)

**Setup pg_stat_statements:**

```bash
# Ubuntu/Debian
sudo apt-get install postgresql-contrib

# Edit postgresql.conf
echo "shared_preload_libraries = 'pg_stat_statements'" | sudo tee -a /etc/postgresql/*/main/postgresql.conf

# Restart PostgreSQL
sudo systemctl restart postgresql

# Enable extension
psql -U postgres -d tqdbproxy -c "CREATE EXTENSION pg_stat_statements;"
```

## PostgreSQL Metrics Collected

### Connection Metrics

- Active connections
- Idle connections
- Waiting/blocked connections

### Performance Metrics

- Commits per second
- Rollbacks per second
- Rows inserted per second
- Cache hit ratio (%)

### I/O Metrics

- Blocks read (disk)
- Blocks hit (cache)
- Cache hit percentage

### Lock Metrics

- Lock types and counts
- Wait events

### Table Statistics

- Insert/update/delete counts
- Live vs dead tuples
- Index usage

## Analyzing Results

### Quick Comparison

```bash
# Compare commits/sec across scenarios
grep "Commits/s" scenario_results_*/REPORT.md

# View cache hit ratios
grep "Cache Hit" scenario_results_*/*_pg_stats.txt

# Check connection counts
grep "Active Connections" scenario_results_*/*_pg_stats.txt
```

### Time-Series Analysis

```bash
# Plot with gnuplot
gnuplot << EOF
set datafile separator ","
set terminal png size 1200,800
set output 'timeline.png'
set xlabel "Time (s)"
set ylabel "Operations/sec"
plot 'scenario_results_*/1_direct_pg_realtime.csv' using 1:5 with lines title 'Direct', \
     'scenario_results_*/4_proxy_10ms_pg_realtime.csv' using 1:5 with lines title 'Proxy 10ms'
EOF
```

### CSV Analysis

```bash
# Calculate average commits/sec
awk -F, 'NR>1 {sum+=$5; count++} END {print sum/count}' test_pg_realtime.csv

# Find peak operations
awk -F, 'NR>1 {if($5>max) max=$5} END {print max}' test_pg_realtime.csv
```

## Environment Variables

Configure PostgreSQL connection:

```bash
export PG_HOST=localhost
export PG_PORT=5432
export PG_USER=postgres
export PG_DB=tqdbproxy

./pg_monitor.sh mytest 30
```

## Troubleshooting

### "psql: command not found"

Install PostgreSQL client:

```bash
sudo apt-get install postgresql-client
```

### "permission denied for database"

Use postgres superuser or configure authentication:

```bash
# Edit pg_hba.conf to allow local connections
sudo nano /etc/postgresql/*/main/pg_hba.conf

# Add line:
local   all   postgres   trust
```

### "extension pg_stat_statements does not exist"

Install and enable the extension (see Setup section above).

### High CPU on PostgreSQL

Check:

- Active connections:
  `SELECT COUNT(*) FROM pg_stat_activity WHERE state = 'active';`
- Long queries:
  `SELECT query, state FROM pg_stat_activity WHERE state != 'idle';`
- Lock contention: `SELECT * FROM pg_locks WHERE NOT granted;`

## Examples

### Example 1: Quick Test

```bash
# Start monitor
./pg_monitor.sh baseline 30 &

# Run benchmark
sleep 2 && ./proxybatch

# View results
cat baseline_pg_stats.txt
column -t -s, baseline_pg_realtime.csv | less
```

### Example 2: Compare Two Configurations

```bash
# Test without batching
./pg_monitor.sh nobatch 30 &
sleep 2 && ./proxybatch  # configured for no batching

# Wait and test with batching
sleep 5
./pg_monitor.sh batch10ms 30 &
sleep 2 && ./proxybatch  # configured for 10ms batching

# Compare
diff -y nobatch_pg_stats.txt batch10ms_pg_stats.txt
```

### Example 3: Full Scenario Testing

```bash
# Ensure proxy is running
cd /home/maurits/projects/tqdbproxy
bash start.sh

# Run all scenarios
cd benchmarks/proxybatch
./run_scenarios.sh

# View report
cat scenario_results_*/REPORT.md
```

## See Also

- [ANALYSIS.md](ANALYSIS.md) - CPU profiling analysis
- [Main README](../../README.md) - Project documentation
- [Write Batching Story](../../docs/stories/WRITE_BATCHING.md) - Batching
  details
