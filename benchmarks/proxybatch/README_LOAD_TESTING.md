# Quick Reference: PostgreSQL Load Testing

## Available Tools

| Tool                         | Purpose                                        | Usage                              |
| ---------------------------- | ---------------------------------------------- | ---------------------------------- |
| **pg_monitor.sh**            | Simple real-time monitoring                    | `./pg_monitor.sh <name> <seconds>` |
| **example_usage.sh**         | Interactive tutorial                           | `./example_usage.sh`               |
| **run_scenarios.sh**         | Automated multi-scenario testing               | `./run_scenarios.sh`               |
| **measure_postgres_load.sh** | Advanced monitoring (needs pg_stat_statements) | `./measure_postgres_load.sh`       |

## Quick Start (3 options)

### Option 1: Easiest - Interactive Example

```bash
./example_usage.sh
```

This will guide you through the process step-by-step.

### Option 2: Manual Monitoring

```bash
# Set your PostgreSQL credentials
export PGHOST=localhost
export PGPORT=5432
export PGUSER=$USER
export PGDATABASE=tqdbproxy

# Start monitoring in background (30 seconds)
./pg_monitor.sh my_test 30 &

# Wait a moment, then run your benchmark
sleep 2
./proxybatch

# View results
cat my_test_pg_stats.txt
column -t -s, my_test_pg_realtime.csv | less
```

### Option 3: Automated Scenarios

```bash
# Make sure proxy is running
cd /home/maurits/projects/tqdbproxy && bash start.sh

# Run all scenarios
cd benchmarks/proxybatch
./run_scenarios.sh

# View comparison
cat scenario_results_*/REPORT.md
```

## Environment Setup

### Set PostgreSQL Connection

```bash
export PGHOST=localhost
export PGPORT=5432
export PGUSER=youruser
export PGPASSWORD=yourpass  # or use ~/.pgpass
export PGDATABASE=tqdbproxy
```

### Verify Connection

```bash
psql -c "SELECT version();"
```

## What Gets Measured

- **Active/Idle Connections** - Connection pool usage
- **Commits/Second** - Transaction throughput
- **Inserts/Second** - Row write throughput
- **Rows per Commit** - Batching effectiveness (higher = better batching)

| File                | Contains                                  |
| ------------------- | ----------------------------------------- |
| `*_pg_stats.txt`    | Before/after database statistics          |
| `*_pg_realtime.csv` | Second-by-second time series data         |
| `*_output.txt`      | Benchmark output                          |
| `REPORT.md`         | Comparison report (run_scenarios.sh only) |

## Example Scenarios to Test

1. **Direct PostgreSQL** - Baseline without proxy
2. **Proxy without batching** - Proxy overhead measurement
3. **Proxy with 1ms batching** - Light batching
4. **Proxy with 10ms batching** - Medium batching (optimal?)
5. **Proxy with 100ms batching** - Heavy batching

## Common Analysis Commands

```bash
# View timeline data nicely formatted
column -t -s, test_pg_realtime.csv | less

# Calculate average inserts/sec
awk -F, 'NR>1 {sum+=$6; count++} END {print "Avg inserts/sec:", sum/count}' test_pg_realtime.csv

# Calculate average rows per commit (batching effectiveness)
awk -F, 'NR>1 {sum+=$7; count++} END {print "Avg rows/commit:", sum/count}' test_pg_realtime.csv

# Find peak throughput
awk -F, 'NR>1 {if($6>max) max=$6} END {print "Peak:", max, "inserts/sec"}' test_pg_realtime.csv

# Compare cache hit ratios
grep "Cache Hit" *_pg_stats.txt

# Check connection counts
grep "Active Connections" *_pg_stats.txt
```

## Troubleshooting

**"psql: command not found"**

```bash
sudo apt-get install postgresql-client
```

**"password authentication failed"**

```bash
export PGPASSWORD=yourpassword
# Or create ~/.pgpass file:
echo "localhost:5432:*:youruser:yourpass" > ~/.pgpass
chmod 600 ~/.pgpass
```

**"connection refused"**

```bash
# Check PostgreSQL is running
sudo systemctl status postgresql
# Check port
sudo ss -tlnp | grep 5432
```

## Next Steps

1. Read [POSTGRES_LOAD_TESTING.md](POSTGRES_LOAD_TESTING.md) for detailed
   documentation
2. See [ANALYSIS.md](ANALYSIS.md) for CPU profiling analysis
3. Run `./example_usage.sh` for an interactive tutorial

## Questions?

- What batch window gives the best throughput?
- What is the proxy overhead compared to direct connection?
- Does PostgreSQL show any bottlenecks (locks, cache misses)?
- How many connections are actively used vs idle?

Run the tests and find out!
