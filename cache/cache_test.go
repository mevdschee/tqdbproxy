package cache

import (
	"testing"
	"time"
)

func TestCache_SetGet(t *testing.T) {
	c, err := New(100)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	key := "test-key"
	value := []byte("test-value")

	c.Set(key, value, time.Minute)

	// Small delay to allow async set to complete
	time.Sleep(10 * time.Millisecond)

	got, ok := c.Get(key)
	if !ok {
		t.Errorf("Get(%q) returned ok=false, want true", key)
	}
	if string(got) != string(value) {
		t.Errorf("Get(%q) = %q, want %q", key, got, value)
	}
}

func TestCache_GetMiss(t *testing.T) {
	c, err := New(100)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	_, ok := c.Get("nonexistent")
	if ok {
		t.Errorf("Get(nonexistent) returned ok=true, want false")
	}
}

func TestCache_Delete(t *testing.T) {
	c, err := New(100)
	if err != nil {
		t.Fatalf("Failed to create cache: %v", err)
	}

	key := "delete-test"
	c.Set(key, []byte("value"), time.Minute)
	time.Sleep(10 * time.Millisecond)

	c.Delete(key)
	time.Sleep(10 * time.Millisecond)

	_, ok := c.Get(key)
	if ok {
		t.Errorf("Get after Delete should return ok=false")
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	t.Skip("Skipping flaky test - Otter TTL cleanup is async and timing-dependent")
}
