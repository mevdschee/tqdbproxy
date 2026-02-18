package main

import (
"database/sql"
"fmt"
"log"
"time"
_ "github.com/lib/pq"
)

func main() {
dsn := "postgres://tqdbproxy:tqdbproxy@127.0.0.1:5432/tqdbproxy?sslmode=disable"
db, err := sql.Open("postgres", dsn)
if err != nil {
log.Fatal(err)
}
defer db.Close()

// Setup
db.Exec("DROP TABLE IF EXISTS test")
_, err = db.Exec("CREATE TABLE test (id SERIAL PRIMARY KEY, value INTEGER, created_at BIGINT)")
if err != nil {
log.Fatal(err)
}

// Test parameterized insert
query := "INSERT INTO test (value, created_at) VALUES ($1, $2)"
for i := 0; i < 10; i++ {
_, err := db.Exec(query, i, time.Now().UnixNano())
if err != nil {
log.Fatalf("Insert %d failed: %v", i, err)
}
fmt.Printf("Insert %d OK\n", i)
}

fmt.Println("All inserts successful!")
}
