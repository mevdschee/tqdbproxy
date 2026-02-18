#!/bin/bash
# Quick example: measure PostgreSQL load during a benchmark run

echo "=== PostgreSQL Load Measurement Example ==="
echo ""
echo "This script will:"
echo "1. Set up PostgreSQL connection"
echo "2. Run pg_monitor.sh in the background"
echo "3. Run the proxybatch benchmark"
echo "4. Display results"
echo ""

# Step 1: Configure PostgreSQL connection
echo "Step 1: Configure connection..."
echo ""
read -p "PostgreSQL username [tqdbproxy]: " pg_user
pg_user=${pg_user:-tqdbproxy}

read -p "PostgreSQL password [tqdbproxy]: " -s pg_pass
echo ""
pg_pass=${pg_pass:-tqdbproxy}

read -p "PostgreSQL database [tqdbproxy]: " pg_db
pg_db=${pg_db:-tqdbproxy}

read -p "PostgreSQL host [localhost]: " pg_host
pg_host=${pg_host:-localhost}

read -p "PostgreSQL port [5432]: " pg_port
pg_port=${pg_port:-5432}

# Export for psql
export PGHOST=$pg_host
export PGPORT=$pg_port
export PGUSER=$pg_user
export PGPASSWORD=$pg_pass
export PGDATABASE=$pg_db

# Test connection
echo ""
echo "Testing connection to PostgreSQL..."
if psql -c "SELECT version();" > /dev/null 2>&1; then
    echo "✓ Connected successfully"
else
    echo "✗ Connection failed"
    echo ""
    echo "Try setting password:"
    echo "  export PGPASSWORD=yourpassword"
    echo "Or configure ~/.pgpass file"
    exit 1
fi

# Step 2: Start monitoring
echo ""
echo "Step 2: Starting PostgreSQL monitoring (30 seconds)..."
./pg_monitor.sh example_test 30 &
MONITOR_PID=$!

sleep 2

# Step 3: Run benchmark
echo ""
echo "Step 3: Running benchmark..."
echo ""

go build -o proxybatch && ./proxybatch

# Wait for monitor to finish
echo ""
echo "Waiting for monitoring to complete..."
wait $MONITOR_PID 2>/dev/null

# Step 4: Display results
echo ""
echo "=== Results ==="
echo ""

echo "PostgreSQL Stats:"
echo "-----------------"
cat example_test_pg_stats.txt
echo ""

echo "Real-time Timeline:"
echo "-------------------"
echo "First 10 seconds:"
column -t -s, example_test_pg_realtime.csv | head -15

echo ""
echo "Full results saved to:"
echo "  - example_test_pg_stats.txt"
echo "  - example_test_pg_realtime.csv"
echo ""
echo "View full timeline:"
echo "  column -t -s, example_test_pg_realtime.csv | less"
