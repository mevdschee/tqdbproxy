#!/bin/bash
# Proof that batching actually works - compare batch:0 vs batch:10

echo "=== Test 1: batch:0 (NO batching - baseline) ==="
psql "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5433/tqdbproxy?sslmode=disable" -c "DELETE FROM test"

# Get baseline stats
BEFORE=$(psql "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5432/tqdbproxy?sslmode=disable" -t -A -c "SELECT n_tup_ins FROM pg_stat_user_tables WHERE relname = 'test'")

# Send 100 INSERTs with batch:0 (no batching) - each should execute immediately
echo "Sending 100 INSERTs with batch:0 (should execute immediately, no batching)..."
START=$(date +%s%N)
for i in {1..100}; do
  psql "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5433/tqdbproxy?sslmode=disable" -c "/* batch:0 */ INSERT INTO test (value, created_at) VALUES ($i, 123456)" > /dev/null 2>&1 &
done
wait
END=$(date +%s%N)
ELAPSED_NO_BATCH=$(( (END - START) / 1000000 ))

AFTER=$(psql "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5432/tqdbproxy?sslmode=disable" -t -A -c "SELECT n_tup_ins FROM pg_stat_user_tables WHERE relname = 'test'")
INSERTED_NO_BATCH=$((AFTER - BEFORE))

echo "  Time: ${ELAPSED_NO_BATCH}ms"
echo "  Rows inserted: $INSERTED_NO_BATCH"
echo ""

echo "=== Test 2: batch:10 (WITH batching) ==="
psql "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5433/tqdbproxy?sslmode=disable" -c "DELETE FROM test"

# Get baseline stats
BEFORE=$(psql "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5432/tqdbproxy?sslmode=disable" -t -A -c "SELECT n_tup_ins FROM pg_stat_user_tables WHERE relname = 'test'")

# Send 100 INSERTs with batch:10 (batching enabled)
echo "Sending 100 INSERTs with batch:10 (should batch together)..."
START=$(date +%s%N)
for i in {1..100}; do
  psql "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5433/tqdbproxy?sslmode=disable" -c "/* batch:10 */ INSERT INTO test (value, created_at) VALUES ($i, 123456)" > /dev/null 2>&1 &
done
wait
# Wait for batches to flush
sleep 1
END=$(date +%s%N)
ELAPSED_WITH_BATCH=$(( (END - START) / 1000000 ))

AFTER=$(psql "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5432/tqdbproxy?sslmode=disable" -t -A -c "SELECT n_tup_ins FROM pg_stat_user_tables WHERE relname = 'test'")
INSERTED_WITH_BATCH=$((AFTER - BEFORE))

echo "  Time: ${ELAPSED_WITH_BATCH}ms"
echo "  Rows inserted: $INSERTED_WITH_BATCH"
echo ""

echo "=== Comparison ==="
echo "batch:0  - ${INSERTED_NO_BATCH} rows in ${ELAPSED_NO_BATCH}ms"
echo "batch:10 - ${INSERTED_WITH_BATCH} rows in ${ELAPSED_WITH_BATCH}ms"

if [ $INSERTED_NO_BATCH -eq 100 ] && [ $INSERTED_WITH_BATCH -eq 100 ]; then
  echo "✓ Both inserted 100 rows correctly"
else
  echo "✗ Insert count mismatch!"
fi

if [ $ELAPSED_WITH_BATCH -lt $ELAPSED_NO_BATCH ]; then
  SPEEDUP=$(awk "BEGIN {printf \"%.1f\", $ELAPSED_NO_BATCH / $ELAPSED_WITH_BATCH}")
  echo "✓ Batching was ${SPEEDUP}x faster"
else
  echo "✗ Batching was NOT faster (may need more concurrent load)"
fi
