package mariadb

import (
	"testing"

	"github.com/mevdschee/tqdbproxy/parser"
)

func TestPreparedStatement_CacheKey(t *testing.T) {
	// Simulate how cache keys are built for prepared statements
	// Cache key = query + params (raw bytes)

	query := "/* ttl:60 */ SELECT * FROM users WHERE id = ?"
	parsed := parser.Parse(query)

	if !parsed.IsCacheable() {
		t.Fatal("Query should be cacheable")
	}

	// Different params produce different cache keys
	params1 := []byte{0x01, 0x00, 0x00, 0x00} // id = 1
	params2 := []byte{0x02, 0x00, 0x00, 0x00} // id = 2

	cacheKey1 := parsed.Query + string(params1)
	cacheKey2 := parsed.Query + string(params2)

	if cacheKey1 == cacheKey2 {
		t.Errorf("Prepared statements with different params should have different cache keys")
	}

	// Same params produce same cache key
	params3 := []byte{0x01, 0x00, 0x00, 0x00} // id = 1 again
	cacheKey3 := parsed.Query + string(params3)

	if cacheKey1 != cacheKey3 {
		t.Errorf("Prepared statements with same params should have same cache key")
	}
}
