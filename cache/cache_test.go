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
