#!/bin/bash
# Test batching with single connection

echo "Truncating test table..."
psql "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5433/tqdbproxy?sslmode=disable" -c "TRUNCATE test"

echo "Getting before stats..."
psql "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5432/tqdbproxy?sslmode=disable" -t -A -c \
  "SELECT xact_commit, tup_inserted FROM pg_stat_database WHERE datname='tqdbproxy'" > /tmp/before.txt

echo "Sending 100 INSERTs with batch:10 hint via single connection..."
psql "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5433/tqdbproxy?sslmode=disable" <<'EOF'
\set i 1
/* batch:10 */ INSERT INTO test (value, created_at) VALUES (1, 123456);
/* batch:10 */ INSERT INTO test (value, created_at) VALUES (2, 123456);
/* batch:10 */ INSERT INTO test (value, created_at) VALUES (3, 123456);
/* batch:10 */ INSERT INTO test (value, created_at) VALUES (4, 123456);
/* batch:10 */ INSERT INTO test (value, created_at) VALUES (5, 123456);
/* batch:10 */ INSERT INTO test (value, created_at) VALUES (6, 123456);
/* batch:10 */ INSERT INTO test (value, created_at) VALUES (7, 123456);
/* batch:10 */ INSERT INTO test (value, created_at) VALUES (8, 123456);
/* batch:10 */ INSERT INTO test (value, created_at) VALUES (9, 123456);
/* batch:10 */ INSERT INTO test (value, created_at) VALUES (10, 123456);
EOF

echo "Getting after stats..."
psql "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5432/tqdbproxy?sslmode=disable" -t -A -c \
  "SELECT xact_commit, tup_inserted FROM pg_stat_database WHERE datname='tqdbproxy'" > /tmp/after.txt

echo ""
echo "Results:"
BEFORE=$(cat /tmp/before.txt)
AFTER=$(cat /tmp/after.txt)
BEFORE_COMMITS=$(echo $BEFORE | cut -d'|' -f1)
BEFORE_INSERTS=$(echo $BEFORE | cut -d'|' -f2)
AFTER_COMMITS=$(echo $AFTER | cut -d'|' -f1)
AFTER_INSERTS=$(echo $AFTER | cut -d'|' -f2)

DELTA_COMMITS=$((AFTER_COMMITS - BEFORE_COMMITS))
DELTA_INSERTS=$((AFTER_INSERTS - BEFORE_INSERTS))

echo "Commits: $DELTA_COMMITS"
echo "Inserts: $DELTA_INSERTS"
if [ $DELTA_COMMITS -gt 0 ]; then
  RATIO=$(awk "BEGIN {printf \"%.2f\", $DELTA_INSERTS / $DELTA_COMMITS}")
  echo "Rows/Commit: $RATIO"
  
  if (( $(awk "BEGIN {print ($RATIO > 1.5)}") )); then
    echo "✓ BATCHING DETECTED!"
  else
    echo "✗ NO BATCHING - each INSERT is a separate transaction"
  fi
fi
