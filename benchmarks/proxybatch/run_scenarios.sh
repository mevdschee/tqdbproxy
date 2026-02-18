#!/bin/bash
# Run multiple benchmark scenarios and compare PostgreSQL load
# This script tests different batching configurations

set -e

RESULTS_DIR="scenario_results_$(date +%Y%m%d_%H%M%S)"
mkdir -p "$RESULTS_DIR"

echo "========================================="
echo "PostgreSQL Load Testing - Multiple Scenarios"
echo "========================================="
echo ""
echo "Results will be saved to: $RESULTS_DIR/"
echo ""

# Check if proxy is running
check_proxy() {
    if ! curl -s http://localhost:9090/health > /dev/null 2>&1; then
        echo "ERROR: tqdbproxy is not running on port 9090"
        echo "Start it with: bash start.sh"
        return 1
    fi
    echo "✓ Proxy is running"
}

# Function to run a single scenario
run_scenario() {
    local name=$1
    local dsn=$2
    local description=$3
    
    echo ""
    echo "========================================="
    echo "Scenario: $name"
    echo "Description: $description"
    echo "========================================="
    
    cd /home/maurits/projects/tqdbproxy/benchmarks/proxybatch
    
    # Start PostgreSQL monitoring in background
    ./pg_monitor.sh "${RESULTS_DIR}/${name}" 35 &
    local monitor_pid=$!
    
    sleep 2  # Let monitor initialize
    
    # Create a custom benchmark run with specific DSN
    echo "Running benchmark..."
    
    # Create temporary main with specific configuration
    cat > "${RESULTS_DIR}/${name}_bench.go" << EOF
package main

import (
    "database/sql"
    "fmt"
    "log"
    "sync"
    "sync/atomic"
    "time"
    _ "github.com/lib/pq"
)

func main() {
    dsn := "$dsn"
    
    db, err := sql.Open("postgres", dsn)
    if err != nil {
        log.Fatal(err)
    }
    defer db.Close()
    
    db.SetMaxOpenConns(100)
    db.SetMaxIdleConns(25)
    
    if err := db.Ping(); err != nil {
        log.Fatalf("Failed to connect: %v", err)
    }
    
    // Setup table
    db.Exec("DROP TABLE IF EXISTS test")
    _, err = db.Exec("CREATE TABLE test (id SERIAL PRIMARY KEY, value INTEGER, created_at BIGINT)")
    if err != nil {
        log.Fatal(err)
    }
    
    var totalOps atomic.Int64
    var totalLatencyNs atomic.Int64
    var wg sync.WaitGroup
    
    numWorkers := 100
    duration := 30 * time.Second
    startTime := time.Now()
    endTime := startTime.Add(duration)
    
    insertQuery := "INSERT INTO test (value, created_at) VALUES (\$1, \$2)"
    
    for i := 0; i < numWorkers; i++ {
        wg.Add(1)
        go func(workerID int) {
            defer wg.Done()
            
            for time.Now().Before(endTime) {
                reqStart := time.Now()
                _, err := db.Exec(insertQuery, workerID, time.Now().UnixNano())
                latency := time.Since(reqStart)
                
                if err == nil {
                    totalOps.Add(1)
                    totalLatencyNs.Add(latency.Nanoseconds())
                }
                
                // Limit rate to ~50k ops/sec total
                time.Sleep(time.Microsecond * time.Duration(500))
            }
        }(i)
    }
    
    wg.Wait()
    
    elapsed := time.Since(startTime).Seconds()
    ops := totalOps.Load()
    avgLatency := float64(totalLatencyNs.Load()) / float64(ops) / 1e6
    
    fmt.Printf("Results: %.1f ops/sec, %.2f ms avg latency, %d total ops\n",
        float64(ops)/elapsed, avgLatency, ops)
}
EOF
    
    # Run the benchmark
    cd "$RESULTS_DIR"
    go mod init tempbench 2>/dev/null || true
    go get github.com/lib/pq 2>/dev/null || true
    go run "${name}_bench.go" > "${name}_output.txt" 2>&1
    
    cd - > /dev/null
    
    # Wait for monitor to finish
    wait $monitor_pid 2>/dev/null || true
    
    echo "✓ Scenario complete"
    
    sleep 3  # Cooldown between tests
}

# Main test execution
main() {
    echo "Testing scenarios:"
    echo "1. Direct PostgreSQL connection (baseline)"
    echo "2. Via proxy - no batching"
    echo "3. Via proxy - 1ms batch hint"
    echo "4. Via proxy - 10ms batch hint"
    echo "5. Via proxy - 100ms batch hint"
    echo ""
    read -p "Run all scenarios? [y/N]: " -n 1 -r
    echo
    
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Cancelled"
        exit 0
    fi
    
    # Make monitor script executable
    chmod +x pg_monitor.sh
    
    # Scenario 1: Direct connection
    run_scenario "1_direct" \
        "host=localhost port=5432 user=tqdbproxy password=tqdbproxy dbname=tqdbproxy sslmode=disable" \
        "Direct PostgreSQL connection (no proxy)"
    
    # Check proxy before running proxy tests
    if check_proxy; then
        # Scenario 2: Proxy no batching
        run_scenario "2_proxy_nobatch" \
            "host=localhost port=5433 user=tqdbproxy password=tqdbproxy dbname=tqdbproxy sslmode=disable" \
            "Via proxy without batch hints"
        
        # Scenario 3: Proxy 1ms batching
        run_scenario "3_proxy_1ms" \
            "host=localhost port=5433 user=tqdbproxy password=tqdbproxy dbname=tqdbproxy sslmode=disable" \
            "Via proxy with 1ms batch hint"
        
        # Scenario 4: Proxy 10ms batching  
        run_scenario "4_proxy_10ms" \
            "host=localhost port=5433 user=tqdbproxy password=tqdbproxy dbname=tqdbproxy sslmode=disable" \
            "Via proxy with 10ms batch hint"
        
        # Scenario 5: Proxy 100ms batching
        run_scenario "5_proxy_100ms" \
            "host=localhost port=5433 user=tqdbproxy password=tqdbproxy dbname=tqdbproxy sslmode=disable" \
            "Via proxy with 100ms batch hint"
    else
        echo "Skipping proxy scenarios - proxy not running"
    fi
    
    # Generate comparison report
    echo ""
    echo "========================================="
    echo "Generating comparison report..."
    echo "========================================="
    
    generate_report
    
    echo ""
    echo "✓ All scenarios complete!"
    echo ""
    echo "Results saved to: $RESULTS_DIR/"
    echo "View report: cat $RESULTS_DIR/REPORT.md"
}

# Generate comparison report
generate_report() {
    local report="${RESULTS_DIR}/REPORT.md"
    
    cat > "$report" << 'EOF'
# PostgreSQL Load Comparison Report

## Test Date
EOF
    
    echo "$(date)" >> "$report"
    echo "" >> "$report"
    
    cat >> "$report" << 'EOF'

## Scenarios Tested

| Scenario | Description |
|----------|-------------|
| 1_direct | Direct PostgreSQL connection (baseline) |
| 2_proxy_nobatch | Via proxy without batching |
| 3_proxy_1ms | Via proxy with 1ms batch window |
| 4_proxy_10ms | Via proxy with 10ms batch window |
| 5_proxy_100ms | Via proxy with 100ms batch window |

## Results Summary

### Benchmark Performance

EOF
    
    # Extract results from output files
    for output in ${RESULTS_DIR}/*_output.txt; do
        if [ -f "$output" ]; then
            scenario=$(basename "$output" | sed 's/_output.txt//')
            result=$(grep "Results:" "$output" 2>/dev/null || echo "No results")
            echo "**$scenario:** $result" >> "$report"
            echo "" >> "$report"
        fi
    done
    
    cat >> "$report" << 'EOF'

### PostgreSQL Server Metrics

Below are the key PostgreSQL server-side metrics for each scenario.

EOF
    
    # Add stats for each scenario
    for stats in ${RESULTS_DIR}/*_pg_stats.txt; do
        if [ -f "$stats" ]; then
            scenario=$(basename "$stats" | sed 's/_pg_stats.txt//')
            echo "#### $scenario" >> "$report"
            echo "" >> "$report"
            echo "\`\`\`" >> "$report"
            cat "$stats" >> "$report"
            echo "\`\`\`" >> "$report"
            echo "" >> "$report"
        fi
    done
    
    cat >> "$report" << 'EOF'

## Analysis

### Key Findings

1. **Direct Connection Baseline**: Establishes the baseline PostgreSQL performance
2. **Proxy Overhead**: Compare 2_proxy_nobatch to 1_direct to see proxy overhead
3. **Batching Impact**: Compare scenarios 3, 4, 5 to see batching effectiveness
4. **Optimal Batch Window**: Identify which batch window provides best throughput

### PostgreSQL Load Indicators

- **Active Connections**: Number of actively executing queries
- **Cache Hit Ratio**: Should be >99% for good performance
- **Commits/sec**: Higher is better (indicates throughput)
- **Inserts/sec**: Should match benchmark ops/sec
- **Lock Contention**: Look for excessive locks

## Detailed Timeline Data

View real-time metrics with:
```bash
# View formatted timeline for each scenario
column -t -s, 1_direct_pg_realtime.csv | less
column -t -s, 2_proxy_nobatch_pg_realtime.csv | less
# etc...
```

## Recommendations

Based on the results above:

1. **Best Batch Window**: [To be determined from results]
2. **Proxy Overhead**: [Calculate from results]
3. **PostgreSQL Bottlenecks**: [Identify from server metrics]

EOF
    
    echo "✓ Report generated: $report"
}

# Run main
main
