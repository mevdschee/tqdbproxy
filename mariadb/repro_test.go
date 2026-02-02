package mariadb

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/mevdschee/tqdbproxy/cache"
	"github.com/mevdschee/tqdbproxy/config"
	"github.com/mevdschee/tqdbproxy/replica"
)

func TestProxyLifecycle(t *testing.T) {
	// 1. Start mock backend
	l, addr, stop := mockBackend(t, "127.0.0.1:0")
	defer func() { stop <- true }() // Runs second (LIFO)
	defer l.Close()                 // Runs first - unblocks Accept()

	// 2. Start Proxy
	pcfg := config.ProxyConfig{
		Listen:  "127.0.0.1:3309",
		Default: "main",
	}
	pools := map[string]*replica.Pool{
		"main": replica.NewPool(addr, nil),
	}

	queryCache, _ := cache.New(cache.DefaultCacheConfig())
	proxy := New(pcfg, pools, queryCache)

	err := proxy.Start()
	if err != nil {
		t.Fatalf("Failed to start proxy: %v", err)
	}
	defer proxy.Stop()
	// Give it a moment to start
	time.Sleep(100 * time.Millisecond)

	// 3. Connect as a client
	dsn := fmt.Sprintf("tqdbproxy:tqdbproxy@tcp(127.0.0.1:3309)/tqdbproxy")
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("Failed to open db: %v", err)
	}
	defer db.Close()

	err = db.Ping()
	if err != nil {
		t.Fatalf("Failed to ping proxy: %v", err)
	}

	rows, err := db.Query("SELECT 1")
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	defer rows.Close()

	if rows.Next() {
		var val int
		if err := rows.Scan(&val); err != nil {
			t.Fatal(err)
		}
		if val != 1 {
			t.Errorf("Expected 1, got %d", val)
		}
	}
}
