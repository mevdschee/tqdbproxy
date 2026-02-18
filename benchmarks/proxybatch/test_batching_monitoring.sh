#!/bin/bash
# Test PostgreSQL batching with monitoring

# Start PostgreSQL monitoring in background
./pg_monitor.sh batching_with_wb 30 &
MONITOR_PID=$!

sleep 2

# Run a focused benchmark with batching
cat > quick_batch_test.go << 'GOEOF'
package main

import (
"database/sql"
"fmt"
"sync"
"time"
_ "github.com/lib/pq"
)

func main() {
dsn := "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5433/tqdbproxy?sslmode=disable"
db, err := sql.Open("postgres", dsn)
if err != nil {
panic(err)
}
defer db.Close()

db.SetMaxOpenConns(100)
db.SetMaxIdleConns(20)

// Setup
db.Exec("DROP TABLE IF EXISTS test")
db.Exec("CREATE TABLE test (id SERIAL PRIMARY KEY, value INTEGER, created_at BIGINT)")

// Run high-volume inserts with 10ms batching window
var wg sync.WaitGroup
workers := 100
duration := 25 * time.Second
endTime := time.Now().Add(duration)

for i := 0; i < workers; i++ {
wg.Add(1)
go func(workerID int) {
defer wg.Done()
for time.Now().Before(endTime) {
query := fmt.Sprintf("/* batch:10 */ INSERT INTO test (value, created_at) VALUES (%d, %d)",
workerID, time.Now().UnixNano())
db.Exec(query)
}
}(i)
}

wg.Wait()
fmt.Println("Benchmark complete!")
}
GOEOF

go run quick_batch_test.go

wait $MONITOR_PID

echo ""
echo "=== Results ==="
tail -20 batching_with_wb_pg_stats.txt

rm -f quick_batch_test.go
