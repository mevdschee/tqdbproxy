#!/bin/bash
# Run benchmark with monitoring

# Start monitor in background
./pg_monitor.sh benchmark_monitor 40 &
MONITOR_PID=$!

# Wait for monitor to initialize
sleep 2

# Run a single short test (just the first PostgreSQL test)
echo "Running short benchmark test..."
timeout 35 ./proxybatch 2>&1 | head -20

# Wait for monitor
wait $MONITOR_PID

echo ""
echo "Check benchmark_monitor_pg_realtime.csv for details"
