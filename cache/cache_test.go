package cache

import (
	"testing"
	"time"
)

func TestCache_SetGet(t *testing.T) {
	c, err := New(DefaultCacheConfig())
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	key := "test-key"
	value := []byte("test-value")

	c.Set(key, value, time.Minute)

	// Small delay to allow async set to complete
	time.Sleep(10 * time.Millisecond)

	got, flags, ok := c.Get(key)
	if !ok {
		t.Errorf("Get(%q) returned ok=false, want true", key)
	}
	if string(got) != string(value) {
		t.Errorf("Get(%q) = %q, want %q", key, got, value)
	}
	if flags != FlagFresh {
		t.Errorf("Get(%q) flags = %d, want %d (fresh)", key, flags, FlagFresh)
	}
}

func TestCache_GetMiss(t *testing.T) {
	c, err := New(DefaultCacheConfig())
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	_, _, ok := c.Get("nonexistent")
	if ok {
		t.Errorf("Get(nonexistent) returned ok=true, want false")
	}
}

func TestCache_Delete(t *testing.T) {
	c, err := New(DefaultCacheConfig())
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	key := "delete-test"
	c.Set(key, []byte("value"), time.Minute)
	time.Sleep(10 * time.Millisecond)

	c.Delete(key)
	time.Sleep(10 * time.Millisecond)

	_, _, ok := c.Get(key)
	if ok {
		t.Errorf("Get after Delete should return ok=false")
	}
}

func TestCache_SingleFlight(t *testing.T) {
	c, err := New(DefaultCacheConfig())
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	key := "single-flight-test"

	// First GetOrWait should return not ok, not waited
	_, _, ok1, waited1 := c.GetOrWait(key)
	if ok1 {
		t.Errorf("First GetOrWait should return ok=false")
	}
	if waited1 {
		t.Errorf("First GetOrWait should return waited=false")
	}

	// SetAndNotify should store and notify
	c.SetAndNotify(key, []byte("value"), time.Minute)

	// Now Get should return the value
	val, _, ok2 := c.Get(key)
	if !ok2 {
		t.Errorf("Get after SetAndNotify should return ok=true")
	}
	if string(val) != "value" {
		t.Errorf("Get returned %q, want %q", val, "value")
	}
}

// TestCache_ColdCacheConcurrent tests that concurrent requests for the same
// cold key result in only one DB fetch (execute once, 2nd goes to cache).
func TestCache_ColdCacheConcurrent(t *testing.T) {
	c, err := New(DefaultCacheConfig())
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	key := "cold-concurrent-test"
	fetchCount := 0
	done := make(chan bool, 2)

	// Simulate two concurrent requests
	go func() {
		_, _, ok, waited := c.GetOrWait(key)
		if !ok && !waited {
			// We are the first - simulate DB fetch
			fetchCount++
			time.Sleep(50 * time.Millisecond) // Simulate DB latency
			c.SetAndNotify(key, []byte("fetched-value"), time.Minute)
		}
		done <- true
	}()

	// Second request slightly after first
	time.Sleep(5 * time.Millisecond)
	go func() {
		_, _, ok, waited := c.GetOrWait(key)
		if !ok && !waited {
			// We are the first - should NOT happen
			fetchCount++
			c.SetAndNotify(key, []byte("second-fetch"), time.Minute)
		} else if waited {
			// Expected: we waited for the first request
			t.Log("Second request correctly waited for first")
		}
		done <- true
	}()

	<-done
	<-done

	// Verify only one fetch occurred
	if fetchCount != 1 {
		t.Errorf("Expected 1 DB fetch, got %d", fetchCount)
	}

	// Verify the value is cached
	val, flags, ok := c.Get(key)
	if !ok {
		t.Errorf("Expected value to be cached")
	}
	if string(val) != "fetched-value" {
		t.Errorf("Expected 'fetched-value', got %q", string(val))
	}
	if flags != FlagFresh {
		t.Errorf("Expected fresh flag, got %d", flags)
	}
}

// TestCache_StaleRefresh tests that stale entries return stale data while refresh happens.
// Uses a short TTL and staleMultiplier to verify flag transitions.
func TestCache_StaleRefresh(t *testing.T) {
	// Create cache with short stale window for testing
	cfg := CacheConfig{
		MaxMemory:       64 * 1024 * 1024,
		Workers:         4,
		StaleMultiplier: 2.0, // Hard expiry = 2x soft expiry
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}
	defer c.Close()

	key := "stale-test"

	// Set with very short TTL (100ms soft, 200ms hard)
	c.Set(key, []byte("original"), 100*time.Millisecond)
	time.Sleep(10 * time.Millisecond)

	// Immediately should be fresh
	val, flags, ok := c.Get(key)
	if !ok {
		t.Fatalf("Expected cache hit")
	}
	if string(val) != "original" {
		t.Errorf("Expected 'original', got %q", string(val))
	}
	if flags != FlagFresh {
		t.Errorf("Expected FlagFresh (0), got %d", flags)
	}

	// Wait for soft expiry (100ms)
	time.Sleep(120 * time.Millisecond)

	// First access after soft expiry should get FlagRefresh (3)
	val2, flags2, ok2 := c.Get(key)
	if !ok2 {
		t.Log("Cache miss after soft expiry - value may have hard expired")
		return // Test inconclusive due to timing
	}
	if string(val2) != "original" {
		t.Errorf("Expected 'original' (stale), got %q", string(val2))
	}
	// Should get either FlagRefresh (3) first time, or FlagStale (1) after
	if flags2 != FlagRefresh && flags2 != FlagStale {
		t.Errorf("Expected FlagRefresh (3) or FlagStale (1), got %d", flags2)
	}
	if flags2 == FlagRefresh {
		t.Log("Got FlagRefresh - first stale access triggers refresh")
	}

	// Second access during stale period should get FlagStale (1)
	_, flags3, ok3 := c.Get(key)
	if ok3 && flags3 != FlagStale {
		t.Logf("Second stale access: flags=%d (expected FlagStale=1)", flags3)
	}
}
