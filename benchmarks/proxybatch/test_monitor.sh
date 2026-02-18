#!/bin/bash
# Test pg_monitor during a benchmark run

# Start the monitor in the background
./pg_monitor.sh monitor_test 20 &
MONITOR_PID=$!

# Wait a bit for monitor to initialize
sleep 2

# Run a simple INSERT test directly
echo "Running INSERT test for 15 seconds..."
psql "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5433/tqdbproxy?sslmode=disable" -c "
DO \$\$
DECLARE
    start_time TIMESTAMP := clock_timestamp();
    i INTEGER := 0;
BEGIN
    WHILE clock_timestamp() < start_time + interval '15 seconds' LOOP
        EXECUTE '/* batch:10 */ INSERT INTO test (value, created_at) VALUES (' || i || ', ' || extract(epoch from now())::bigint || ')';
        i := i + 1;
    END LOOP;
    RAISE NOTICE 'Inserted % rows', i;
END;
\$\$;
"

# Wait for monitor to finish
wait $MONITOR_PID

echo ""
echo "Test complete. Check monitor_test_pg_realtime.csv for results"
