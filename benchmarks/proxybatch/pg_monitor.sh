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
echo "timestamp,active_conns,idle_conns,waiting,inserts_per_sec,total_rows,queries_executed,cache_hit_pct" > "$REALTIME_FILE"

START_TIME=$(date +%s)
END_TIME=$((START_TIME + DURATION))

# Get baseline - track inserts and query count
PREV_STATS=$($PSQL -t -A -c "
SELECT 
    COALESCE(n_tup_ins, 0) || ',' ||
    (SELECT sum(calls) FROM pg_stat_statements WHERE query LIKE '%INSERT INTO test%' AND query NOT LIKE '%pg_stat%')
FROM pg_stat_user_tables WHERE relname = 'test';")
IFS=',' read -r PREV_INSERTS PREV_QUERIES <<< "$PREV_STATS"
# Handle NULL for queries if pg_stat_statements not available
PREV_QUERIES=${PREV_QUERIES:-0}

# Track totals for summary
TOTAL_INSERTS=0
TOTAL_QUERIES=0

while [ $(date +%s) -lt $END_TIME ]; do
    TIMESTAMP=$(date +%s)
    
    # Get current stats - track test table inserts and backend query count
    STATS=$($PSQL -t -A -c "
    SELECT 
        (SELECT COUNT(*) FROM pg_stat_activity WHERE datname = '$PGDATABASE' AND state = 'active') || ',' ||
        (SELECT COUNT(*) FROM pg_stat_activity WHERE datname = '$PGDATABASE' AND state = 'idle') || ',' ||
        (SELECT COUNT(*) FROM pg_stat_activity WHERE datname = '$PGDATABASE' AND wait_event IS NOT NULL) || ',' ||
        (SELECT COALESCE(n_tup_ins, 0) FROM pg_stat_user_tables WHERE relname = 'test') || ',' ||
        (SELECT COALESCE(sum(calls), 0) FROM pg_stat_statements WHERE query LIKE '%INSERT INTO test%' AND query NOT LIKE '%pg_stat%') || ',' ||
        (SELECT ROUND(100.0 * blks_hit / NULLIF(blks_hit + blks_read, 0), 2) FROM pg_stat_database WHERE datname = '$PGDATABASE');
    " 2>/dev/null)
    
    if [ -n "$STATS" ]; then
        IFS=',' read -r ACTIVE IDLE WAITING INSERTS QUERIES CACHE_HIT <<< "$STATS"
        
        # Calculate rates (per second since last measurement)
        INSERTS_RATE=$((INSERTS - PREV_INSERTS))
        QUERIES_RATE=$((QUERIES - PREV_QUERIES))
        
        # Handle table truncate/delete - if inserts decreased, reset baseline
        if [ $INSERTS_RATE -lt 0 ]; then
            PREV_INSERTS=$INSERTS
            INSERTS_RATE=0
        fi
        
        # Track totals
        TOTAL_INSERTS=$((TOTAL_INSERTS + INSERTS_RATE))
        TOTAL_QUERIES=$((TOTAL_QUERIES + QUERIES_RATE))
        
        # Calculate batch efficiency (rows per query)
        if [ $QUERIES_RATE -gt 0 ]; then
            BATCH_EFF=$(awk "BEGIN {printf \"%.1f\", $INSERTS_RATE / $QUERIES_RATE}")
        else
            BATCH_EFF="0.0"
        fi
        
        echo "$TIMESTAMP,$ACTIVE,$IDLE,$WAITING,$INSERTS_RATE,$INSERTS,$QUERIES,$CACHE_HIT" >> "$REALTIME_FILE"
        
        # Show progress - highlight when batch flushes happen
        if [ $INSERTS_RATE -gt 1000 ]; then
            # Highlight batch flush with marker
            printf "\r[%3ds] Active: %3d | Inserts/s: %6d | Queries/s: %5d | BatchEff: %5s | Cache: %5s%% ⚡\n" \
                $((TIMESTAMP - START_TIME)) "$ACTIVE" "$INSERTS_RATE" "$QUERIES_RATE" "$BATCH_EFF" "$CACHE_HIT"
        else
            printf "\r[%3ds] Active: %3d | Inserts/s: %6d | Queries/s: %5d | BatchEff: %5s | Cache: %5s%%  " \
                $((TIMESTAMP - START_TIME)) "$ACTIVE" "$INSERTS_RATE" "$QUERIES_RATE" "$BATCH_EFF" "$CACHE_HIT"
        fi
        
        PREV_INSERTS=$INSERTS
        PREV_QUERIES=$QUERIES
    fi
    
    sleep 1
done

echo ""
echo ""

# Calculate and display summary
echo "Summary:"
echo "  Duration:         ${DURATION}s"
echo "  Total Rows Inserted: $TOTAL_INSERTS"
echo "  Total Backend Queries: $TOTAL_QUERIES"
if [ $TOTAL_QUERIES -gt 0 ]; then
    AVG_BATCH=$(awk "BEGIN {printf \"%.1f\", $TOTAL_INSERTS / $TOTAL_QUERIES}")
    echo "  Avg Batch Size:   $AVG_BATCH rows/query"
    if (( $(awk "BEGIN {print ($AVG_BATCH > 1.5)}") )); then
        echo "  ✓ Batching is working!"
    else
        echo "  ✗ No effective batching detected"
    fi
fi
echo "  Throughput:       $(awk "BEGIN {printf \"%.1f\", $TOTAL_INSERTS / $DURATION}") rows/sec"
echo ""

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
