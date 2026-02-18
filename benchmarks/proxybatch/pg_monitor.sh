#!/bin/bash
# Simple PostgreSQL load monitor - run alongside benchmarks
# Usage: ./pg_monitor.sh <output_prefix> <duration_seconds>
#
# Environment variables:
#   PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE
#   Or use: PG_DSN="postgresql://user:pass@host:port/db"

OUTPUT_PREFIX="${1:-test}"
DURATION="${2:-30}"

# Use standard PostgreSQL environment variables
export PGHOST="${PGHOST:-localhost}"
export PGPORT="${PGPORT:-5432}"
export PGUSER="${PGUSER:-tqdbproxy}"
export PGPASSWORD="${PGPASSWORD:-tqdbproxy}"
export PGDATABASE="${PGDATABASE:-tqdbproxy}"

# Legacy variable support
PG_HOST="${PG_HOST:-$PGHOST}"
PG_PORT="${PG_PORT:-$PGPORT}"
PG_USER="${PG_USER:-$PGUSER}"
PG_DB="${PG_DB:-$PGDATABASE}"

STATS_FILE="${OUTPUT_PREFIX}_pg_stats.txt"
REALTIME_FILE="${OUTPUT_PREFIX}_pg_realtime.csv"

# Build psql command (uses environment variables automatically)
PSQL="psql"

echo "Monitoring PostgreSQL for $DURATION seconds..."
echo "Connection: ${PGUSER}@${PGHOST}:${PGPORT}/${PGDATABASE}"
echo "Output: $STATS_FILE, $REALTIME_FILE"
echo ""

# Test connection
if ! $PSQL -c "SELECT 1" > /dev/null 2>&1; then
    echo "ERROR: Cannot connect to PostgreSQL"
    echo "Set credentials:"
    echo "  export PGHOST=localhost"
    echo "  export PGPORT=5432"  
    echo "  export PGUSER=youruser"
    echo "  export PGPASSWORD=yourpass"
    echo "  export PGDATABASE=tqdbproxy"
    exit 1
fi

# Get initial stats
echo "=== PostgreSQL Stats - $(date) ===" > "$STATS_FILE"
echo "" >> "$STATS_FILE"

echo "--- Before Benchmark ---" >> "$STATS_FILE"
$PSQL >> "$STATS_FILE" 2>&1 << 'SQL'
-- Connection stats
SELECT 'Active Connections:' as metric, COUNT(*) as value
FROM pg_stat_activity WHERE datname = 'tqdbproxy' AND state = 'active'
UNION ALL
SELECT 'Idle Connections:', COUNT(*)
FROM pg_stat_activity WHERE datname = 'tqdbproxy' AND state = 'idle';

-- Database stats snapshot
SELECT 
    'Commits' as metric, xact_commit as value
FROM pg_stat_database WHERE datname = 'tqdbproxy'
UNION ALL
SELECT 'Rollbacks', xact_rollback
FROM pg_stat_database WHERE datname = 'tqdbproxy'
UNION ALL
SELECT 'Blocks Read', blks_read
FROM pg_stat_database WHERE datname = 'tqdbproxy'
UNION ALL
SELECT 'Blocks Hit', blks_hit
FROM pg_stat_database WHERE datname = 'tqdbproxy'
UNION ALL
SELECT 'Rows Inserted', tup_inserted
FROM pg_stat_database WHERE datname = 'tqdbproxy';
SQL

# Start real-time monitoring
echo "timestamp,active_conns,idle_conns,waiting,commits,inserts,rows_per_commit,cache_hit_pct" > "$REALTIME_FILE"

START_TIME=$(date +%s)
END_TIME=$((START_TIME + DURATION))

# Get baseline - use table stats for inserts (more accurate)
PREV_STATS=$($PSQL -t -A -c "
SELECT 
    (SELECT xact_commit FROM pg_stat_database WHERE datname = '$PGDATABASE') || ',' ||
    (SELECT COALESCE(n_tup_ins, 0) FROM pg_stat_user_tables WHERE relname = 'test');")
IFS=',' read -r PREV_COMMITS PREV_INSERTS <<< "$PREV_STATS"

# Track totals for summary
TOTAL_COMMITS=0
TOTAL_INSERTS=0

while [ $(date +%s) -lt $END_TIME ]; do
    TIMESTAMP=$(date +%s)
    
    # Get current stats - use table-level insert count
    STATS=$($PSQL -t -A -c "
    SELECT 
        (SELECT COUNT(*) FROM pg_stat_activity WHERE datname = '$PGDATABASE' AND state = 'active') || ',' ||
        (SELECT COUNT(*) FROM pg_stat_activity WHERE datname = '$PGDATABASE' AND state = 'idle') || ',' ||
        (SELECT COUNT(*) FROM pg_stat_activity WHERE datname = '$PGDATABASE' AND wait_event IS NOT NULL) || ',' ||
        (SELECT xact_commit FROM pg_stat_database WHERE datname = '$PGDATABASE') || ',' ||
        (SELECT COALESCE(n_tup_ins, 0) FROM pg_stat_user_tables WHERE relname = 'test') || ',' ||
        (SELECT ROUND(100.0 * blks_hit / NULLIF(blks_hit + blks_read, 0), 2) FROM pg_stat_database WHERE datname = '$PGDATABASE');
    " 2>/dev/null)
    
    if [ -n "$STATS" ]; then
        IFS=',' read -r ACTIVE IDLE WAITING COMMITS INSERTS CACHE_HIT <<< "$STATS"
        
        # Calculate rates (per second since last measurement)
        COMMITS_RATE=$((COMMITS - PREV_COMMITS))
        INSERTS_RATE=$((INSERTS - PREV_INSERTS))
        
        # Handle table truncate - if inserts decreased, reset baseline
        if [ $INSERTS_RATE -lt 0 ]; then
            PREV_INSERTS=$INSERTS
            INSERTS_RATE=0
        fi
        
        # Calculate rows per commit ratio
        if [ $COMMITS_RATE -gt 0 ]; then
            ROWS_PER_COMMIT=$(awk "BEGIN {printf \"%.2f\", $INSERTS_RATE / $COMMITS_RATE}")
        else
            ROWS_PER_COMMIT="0.00"
        fi
        
        # Track totals
        TOTAL_COMMITS=$((TOTAL_COMMITS + COMMITS_RATE))
        TOTAL_INSERTS=$((TOTAL_INSERTS + INSERTS_RATE))
        
        echo "$TIMESTAMP,$ACTIVE,$IDLE,$WAITING,$COMMITS_RATE,$INSERTS_RATE,$ROWS_PER_COMMIT,$CACHE_HIT" >> "$REALTIME_FILE"
        
        # Show progress with rows/commit ratio and highlight when batches flush
        if [ $INSERTS_RATE -gt 1000 ]; then
            # Highlight batch flush with marker
            printf "\r[%3ds] Active: %3d | Commits/s: %6d | Inserts/s: %6d | Rows/Commit: %5s | Cache: %5s%% ⚡BATCH FLUSH\n" \
                $((TIMESTAMP - START_TIME)) "$ACTIVE" "$COMMITS_RATE" "$INSERTS_RATE" "$ROWS_PER_COMMIT" "$CACHE_HIT"
        else
            printf "\r[%3ds] Active: %3d | Commits/s: %6d | Inserts/s: %6d | Rows/Commit: %5s | Cache: %5s%%           " \
                $((TIMESTAMP - START_TIME)) "$ACTIVE" "$COMMITS_RATE" "$INSERTS_RATE" "$ROWS_PER_COMMIT" "$CACHE_HIT"
        fi
        
        PREV_COMMITS=$COMMITS
        PREV_INSERTS=$INSERTS
    fi
    
    sleep 1
done

echo ""
echo ""

# Calculate and display summary
echo "Summary:"
echo "  Duration:         ${DURATION}s"
echo "  Total Commits:    $TOTAL_COMMITS"
echo "  Total Rows Inserted: $TOTAL_INSERTS"
if [ $TOTAL_COMMITS -gt 0 ]; then
    AVG_ROWS_PER_COMMIT=$(awk "BEGIN {printf \"%.2f\", $TOTAL_INSERTS / $TOTAL_COMMITS}")
    echo "  Avg Rows/Commit:  $AVG_ROWS_PER_COMMIT"
else
    echo "  Avg Rows/Commit:  N/A (no commits)"
fi
echo "  Throughput:       $(awk "BEGIN {printf \"%.1f\", $TOTAL_INSERTS / $DURATION}") rows/sec"
echo "  Commit Rate:      $(awk "BEGIN {printf \"%.1f\", $TOTAL_COMMITS / $DURATION}") commits/sec"
echo ""

# Show batching analysis
if [ $TOTAL_COMMITS -gt 0 ]; then
    AVG_RATIO=$(awk "BEGIN {printf \"%.2f\", $TOTAL_INSERTS / $TOTAL_COMMITS}")
    if (( $(awk "BEGIN {print ($AVG_RATIO > 1.5)}") )); then
        echo "✓ Batching detected: ~$AVG_RATIO rows per transaction"
    elif (( $(awk "BEGIN {print ($AVG_RATIO > 1.1)}") )); then
        echo "~ Light batching: ~$AVG_RATIO rows per transaction"
    else
        echo "⚠ No batching detected: Each INSERT is a separate transaction (auto-commit)"
        echo "  Note: Throughput improvements come from reduced network overhead,"
        echo "        not from transaction-level batching."
    fi
    echo ""
fi

# Get final stats
echo "" >> "$STATS_FILE"
echo "--- After Benchmark ---" >> "$STATS_FILE"
$PSQL >> "$STATS_FILE" 2>&1 << 'SQL'
-- Final stats
SELECT 
    'Total Commits' as metric, xact_commit as value
FROM pg_stat_database WHERE datname = current_database()
UNION ALL
SELECT 'Total Rollbacks', xact_rollback
FROM pg_stat_database WHERE datname = current_database()
UNION ALL
SELECT 'Total Inserts', tup_inserted
FROM pg_stat_database WHERE datname = current_database()
UNION ALL
SELECT 'Cache Hit Ratio %', ROUND(100.0 * blks_hit / NULLIF(blks_hit + blks_read, 0), 2)
FROM pg_stat_database WHERE datname = current_database();

-- Table stats
\echo
\echo 'Table Stats:'
SELECT 
    relname,
    n_tup_ins as inserts,
    n_tup_upd as updates,
    n_tup_del as deletes,
    n_live_tup as live_rows,
    n_dead_tup as dead_rows
FROM pg_stat_user_tables 
WHERE relname = 'test';

-- Lock contention
\echo
\echo 'Lock Stats:'
SELECT mode, COUNT(*) as count
FROM pg_locks 
WHERE database = (SELECT oid FROM pg_database WHERE datname = current_database())
GROUP BY mode;

-- Statement-level stats (if pg_stat_statements available)
\echo
\echo 'Recent INSERT Statements (if pg_stat_statements enabled):'
SELECT 
    LEFT(query, 100) as query_sample,
    calls,
    rows as total_rows,
    CASE WHEN calls > 0 THEN ROUND(rows::numeric / calls, 2) ELSE 0 END as avg_rows_per_call
FROM pg_stat_statements
WHERE query LIKE '%INSERT INTO test%'
  AND query NOT LIKE '%pg_stat_statements%'
ORDER BY calls DESC
LIMIT 5;
SQL

echo ""
echo "Monitoring complete!"
echo "Stats: $STATS_FILE"
echo "Timeline: $REALTIME_FILE"
echo ""
echo "View timeline: column -t -s, $REALTIME_FILE | less"
