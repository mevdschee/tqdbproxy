#!/bin/bash
# Measure PostgreSQL server load under different scenarios
# Usage: ./measure_postgres_load.sh

set -e

# Configuration
PG_HOST="${PG_HOST:-localhost}"
PG_PORT="${PG_PORT:-5432}"
PG_USER="${PG_USER:-postgres}"
PG_DB="${PG_DB:-tqdbproxy}"
RESULTS_DIR="postgres_load_results"
DURATION=30  # seconds per test

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

mkdir -p "$RESULTS_DIR"

echo -e "${GREEN}=== PostgreSQL Load Testing Framework ===${NC}"
echo "Results will be saved to: $RESULTS_DIR/"
echo ""

# Function to enable pg_stat_statements if not already enabled
enable_pg_stats() {
    echo -e "${YELLOW}Checking pg_stat_statements...${NC}"
    
    psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements;" 2>/dev/null || {
        echo -e "${YELLOW}Note: pg_stat_statements extension not available. Install with:${NC}"
        echo "  sudo apt-get install postgresql-contrib"
        echo "  Add 'shared_preload_libraries = pg_stat_statements' to postgresql.conf"
        echo "  Restart PostgreSQL"
        echo ""
        echo -e "${YELLOW}Continuing with basic stats only...${NC}"
        return 1
    }
    
    # Reset stats
    psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -c "SELECT pg_stat_statements_reset();" >/dev/null 2>&1 || true
    
    echo -e "${GREEN}✓ pg_stat_statements enabled${NC}"
    return 0
}

# Function to collect PostgreSQL stats
collect_pg_stats() {
    local scenario=$1
    local output_file="${RESULTS_DIR}/${scenario}_stats.txt"
    
    echo -e "${YELLOW}Collecting PostgreSQL stats for: $scenario${NC}"
    
    {
        echo "=== PostgreSQL Stats for: $scenario ==="
        echo "Timestamp: $(date)"
        echo ""
        
        # Database activity
        echo "--- Active Connections ---"
        psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -c "
            SELECT 
                state, 
                COUNT(*) as connections,
                COUNT(*) FILTER (WHERE wait_event IS NOT NULL) as waiting
            FROM pg_stat_activity 
            WHERE datname = '$PG_DB'
            GROUP BY state;" 2>/dev/null || echo "Failed to get connections"
        
        echo ""
        echo "--- Transaction Stats ---"
        psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -c "
            SELECT 
                xact_commit as commits,
                xact_rollback as rollbacks,
                blks_read as blocks_read,
                blks_hit as blocks_hit,
                ROUND(100.0 * blks_hit / NULLIF(blks_hit + blks_read, 0), 2) as cache_hit_ratio,
                tup_inserted as rows_inserted,
                tup_updated as rows_updated,
                tup_deleted as rows_deleted
            FROM pg_stat_database 
            WHERE datname = '$PG_DB';" 2>/dev/null || echo "Failed to get transaction stats"
        
        echo ""
        echo "--- Table Stats ---"
        psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -c "
            SELECT 
                schemaname,
                relname as table,
                seq_scan as seq_scans,
                idx_scan as index_scans,
                n_tup_ins as inserts,
                n_tup_upd as updates,
                n_tup_del as deletes,
                n_live_tup as live_rows,
                n_dead_tup as dead_rows
            FROM pg_stat_user_tables 
            WHERE relname = 'test'
            ORDER BY n_tup_ins DESC 
            LIMIT 10;" 2>/dev/null || echo "Failed to get table stats"
        
        echo ""
        echo "--- Index Stats ---"
        psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -c "
            SELECT 
                schemaname,
                tablename,
                indexname,
                idx_scan as scans,
                idx_tup_read as tuples_read,
                idx_tup_fetch as tuples_fetched
            FROM pg_stat_user_indexes 
            WHERE tablename = 'test'
            ORDER BY idx_scan DESC;" 2>/dev/null || echo "Failed to get index stats"
        
        echo ""
        echo "--- Query Stats (if pg_stat_statements available) ---"
        psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -c "
            SELECT 
                LEFT(query, 80) as query_preview,
                calls,
                ROUND(total_exec_time::numeric, 2) as total_ms,
                ROUND(mean_exec_time::numeric, 2) as mean_ms,
                ROUND(stddev_exec_time::numeric, 2) as stddev_ms,
                rows
            FROM pg_stat_statements 
            WHERE query LIKE '%INSERT INTO test%'
            ORDER BY total_exec_time DESC 
            LIMIT 10;" 2>/dev/null || echo "pg_stat_statements not available"
        
        echo ""
        echo "--- Lock Stats ---"
        psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -c "
            SELECT 
                mode,
                COUNT(*) as lock_count
            FROM pg_locks 
            WHERE database = (SELECT oid FROM pg_database WHERE datname = '$PG_DB')
            GROUP BY mode
            ORDER BY lock_count DESC;" 2>/dev/null || echo "Failed to get lock stats"
        
        echo ""
        echo "--- Wait Events ---"
        psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -c "
            SELECT 
                wait_event_type,
                wait_event,
                COUNT(*) as count
            FROM pg_stat_activity 
            WHERE datname = '$PG_DB' AND wait_event IS NOT NULL
            GROUP BY wait_event_type, wait_event
            ORDER BY count DESC;" 2>/dev/null || echo "No wait events"
        
    } > "$output_file"
    
    echo -e "${GREEN}✓ Stats saved to: $output_file${NC}"
}

# Function to get real-time PostgreSQL metrics
monitor_pg_realtime() {
    local scenario=$1
    local duration=$2
    local output_file="${RESULTS_DIR}/${scenario}_realtime.csv"
    
    echo -e "${YELLOW}Starting real-time monitoring for $duration seconds...${NC}"
    
    # CSV header
    echo "timestamp,active_connections,idle_connections,commits_per_sec,cache_hit_ratio,rows_inserted_per_sec" > "$output_file"
    
    local end_time=$(($(date +%s) + duration))
    local prev_commits=0
    local prev_inserts=0
    local first_run=true
    
    while [ $(date +%s) -lt $end_time ]; do
        local stats=$(psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -t -A -c "
            SELECT 
                (SELECT COUNT(*) FROM pg_stat_activity WHERE datname = '$PG_DB' AND state = 'active'),
                (SELECT COUNT(*) FROM pg_stat_activity WHERE datname = '$PG_DB' AND state = 'idle'),
                xact_commit,
                ROUND(100.0 * blks_hit / NULLIF(blks_hit + blks_read, 0), 2),
                tup_inserted
            FROM pg_stat_database 
            WHERE datname = '$PG_DB';" 2>/dev/null)
        
        if [ -n "$stats" ]; then
            IFS='|' read -r active idle commits cache_hit inserts <<< "$stats"
            
            if [ "$first_run" = false ]; then
                local commits_per_sec=$((commits - prev_commits))
                local inserts_per_sec=$((inserts - prev_inserts))
                echo "$(date +%s),$active,$idle,$commits_per_sec,$cache_hit,$inserts_per_sec" >> "$output_file"
            fi
            
            prev_commits=$commits
            prev_inserts=$inserts
            first_run=false
        fi
        
        sleep 1
    done
    
    echo -e "${GREEN}✓ Real-time data saved to: $output_file${NC}"
}

# Function to run a benchmark scenario
run_scenario() {
    local scenario=$1
    local ops_per_sec=$2
    local description=$3
    
    echo ""
    echo -e "${GREEN}================================================${NC}"
    echo -e "${GREEN}Running Scenario: $scenario${NC}"
    echo -e "${GREEN}Description: $description${NC}"
    echo -e "${GREEN}Target: ${ops_per_sec}k ops/sec for ${DURATION}s${NC}"
    echo -e "${GREEN}================================================${NC}"
    echo ""
    
    # Reset stats
    psql -h "$PG_HOST" -p "$PG_PORT" -U "$PG_USER" -d "$PG_DB" -c "SELECT pg_stat_statements_reset();" >/dev/null 2>&1 || true
    
    # Collect baseline stats
    collect_pg_stats "${scenario}_before"
    
    # Start real-time monitoring in background
    monitor_pg_realtime "$scenario" "$DURATION" &
    local monitor_pid=$!
    
    # Build and run benchmark
    cd /home/maurits/projects/tqdbproxy/benchmarks/proxybatch
    
    # Modify main.go temporarily to run specific scenario
    # Or run with custom duration
    echo "Building benchmark..."
    go build -o proxybatch
    
    echo "Starting benchmark..."
    # Run the benchmark - it will execute for its configured duration
    timeout $((DURATION + 10)) ./proxybatch > "${RESULTS_DIR}/${scenario}_benchmark.log" 2>&1 || true
    
    # Wait for monitoring to complete
    wait $monitor_pid 2>/dev/null || true
    
    # Collect post-test stats
    sleep 2
    collect_pg_stats "${scenario}_after"
    
    echo -e "${GREEN}✓ Scenario completed${NC}"
}

# Function to generate comparison report
generate_report() {
    local report_file="${RESULTS_DIR}/COMPARISON_REPORT.md"
    
    echo -e "${YELLOW}Generating comparison report...${NC}"
    
    cat > "$report_file" << 'EOF'
# PostgreSQL Load Comparison Report

## Test Configuration

- **Database:** PostgreSQL
- **Duration:** 30 seconds per scenario
- **Test Date:** $(date)

## Scenarios Tested

EOF
    
    # Add scenario summaries
    for scenario_dir in ${RESULTS_DIR}/*_after_stats.txt; do
        if [ -f "$scenario_dir" ]; then
            local scenario=$(basename "$scenario_dir" | sed 's/_after_stats.txt//')
            echo "### $scenario" >> "$report_file"
            echo "" >> "$report_file"
            
            # Extract key metrics
            grep -A 5 "Transaction Stats" "$scenario_dir" >> "$report_file" 2>/dev/null || true
            echo "" >> "$report_file"
        fi
    done
    
    echo -e "${GREEN}✓ Report generated: $report_file${NC}"
}

# Main execution
main() {
    # Enable pg_stat_statements
    enable_pg_stats
    
    echo ""
    echo -e "${GREEN}Choose test scenario:${NC}"
    echo "1. Direct PostgreSQL (no proxy) - baseline"
    echo "2. Via tqdbproxy - no batching"
    echo "3. Via tqdbproxy - 1ms batching"
    echo "4. Via tqdbproxy - 10ms batching"
    echo "5. Via tqdbproxy - 100ms batching"
    echo "6. Run all scenarios sequentially"
    echo ""
    read -p "Enter choice [1-6]: " choice
    
    case $choice in
        1)
            run_scenario "direct_postgres" 50 "Direct PostgreSQL connection, no batching"
            ;;
        2)
            run_scenario "proxy_no_batch" 50 "Via proxy without batching"
            ;;
        3)
            run_scenario "proxy_1ms" 50 "Via proxy with 1ms batch window"
            ;;
        4)
            run_scenario "proxy_10ms" 100 "Via proxy with 10ms batch window"
            ;;
        5)
            run_scenario "proxy_100ms" 200 "Via proxy with 100ms batch window"
            ;;
        6)
            echo -e "${YELLOW}Running all scenarios...${NC}"
            run_scenario "direct_postgres" 50 "Direct PostgreSQL connection"
            sleep 5
            run_scenario "proxy_no_batch" 50 "Via proxy without batching"
            sleep 5
            run_scenario "proxy_1ms" 50 "Via proxy with 1ms batch window"
            sleep 5
            run_scenario "proxy_10ms" 100 "Via proxy with 10ms batch window"
            sleep 5
            run_scenario "proxy_100ms" 200 "Via proxy with 100ms batch window"
            
            generate_report
            ;;
        *)
            echo "Invalid choice"
            exit 1
            ;;
    esac
    
    echo ""
    echo -e "${GREEN}=== Testing Complete ===${NC}"
    echo "Results saved to: $RESULTS_DIR/"
    echo ""
    echo "To view results:"
    echo "  cat ${RESULTS_DIR}/*_stats.txt"
    echo "  cat ${RESULTS_DIR}/COMPARISON_REPORT.md"
    echo ""
    echo "To analyze real-time data:"
    echo "  column -t -s, ${RESULTS_DIR}/*_realtime.csv | less"
}

# Run main
main
