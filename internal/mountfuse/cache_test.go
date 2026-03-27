package mountfuse

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func newTestCache(maxEntries int, now func() time.Time) *Cache {
	c := NewCache(maxEntries)
	c.nowFunc = now
	return c
}

func TestCachePutAndGet(t *testing.T) {
	now := time.Now()
	c := newTestCache(100, func() time.Time { return now })

	c.Put("/hello.txt", "content-hello", 5*time.Second)

	got, ok := c.Get("/hello.txt")
	if !ok {
		t.Fatal("expected cache hit for /hello.txt")
	}
	if got != "content-hello" {
		t.Errorf("value = %v, want %q", got, "content-hello")
	}
}

func TestCacheTTLExpiry(t *testing.T) {
	now := time.Now()
	c := newTestCache(100, func() time.Time { return now })

	c.Put("/expire.txt", "data", 2*time.Second)

	if _, ok := c.Get("/expire.txt"); !ok {
		t.Fatal("expected cache hit before TTL expiry")
	}

	// Advance time past the TTL.
	now = now.Add(3 * time.Second)

	if _, ok := c.Get("/expire.txt"); ok {
		t.Fatal("expected cache miss after TTL expiry")
	}
}

func TestCacheLRUEviction(t *testing.T) {
	now := time.Now()
	c := newTestCache(3, func() time.Time { return now })

	// Fill cache to max capacity.
	for i := 0; i < 3; i++ {
		c.Put(fmt.Sprintf("/file%d.txt", i), fmt.Sprintf("data%d", i), 10*time.Second)
	}

	// Access file0 so it moves to the end of the LRU (most recently used).
	c.Get("/file0.txt")

	// Add one more entry, which should evict the least recently used (file1).
	c.Put("/file3.txt", "data3", 10*time.Second)

	// file1 was the LRU entry.
	if _, ok := c.Get("/file1.txt"); ok {
		t.Error("expected file1.txt to be evicted")
	}

	// file0 should still be present (was accessed, moved to end).
	if _, ok := c.Get("/file0.txt"); !ok {
		t.Error("expected file0.txt to remain in cache")
	}

	// file3 should be present.
	if _, ok := c.Get("/file3.txt"); !ok {
		t.Error("expected file3.txt to be in cache")
	}
}

func TestCacheInvalidate(t *testing.T) {
	c := NewCache(100)

	c.Put("/remove-me.txt", "data", 10*time.Second)

	if _, ok := c.Get("/remove-me.txt"); !ok {
		t.Fatal("expected cache hit before invalidation")
	}

	c.Invalidate("/remove-me.txt")

	if _, ok := c.Get("/remove-me.txt"); ok {
		t.Fatal("expected cache miss after invalidation")
	}
}

func TestCacheInvalidatePrefix(t *testing.T) {
	c := NewCache(100)

	c.Put("/docs/a.txt", "a", 10*time.Second)
	c.Put("/docs/b.txt", "b", 10*time.Second)
	c.Put("/docs/sub/c.txt", "c", 10*time.Second)
	c.Put("/other/d.txt", "d", 10*time.Second)

	c.InvalidatePrefix("/docs/")

	for _, p := range []string{"/docs/a.txt", "/docs/b.txt", "/docs/sub/c.txt"} {
		if _, ok := c.Get(p); ok {
			t.Errorf("expected %s to be invalidated", p)
		}
	}

	if _, ok := c.Get("/other/d.txt"); !ok {
		t.Error("expected /other/d.txt to remain in cache")
	}
}

func TestCacheLen(t *testing.T) {
	c := NewCache(100)
	c.Put("/a", 1, 10*time.Second)
	c.Put("/b", 2, 10*time.Second)
	if c.Len() != 2 {
		t.Errorf("Len() = %d, want 2", c.Len())
	}
	c.Invalidate("/a")
	if c.Len() != 1 {
		t.Errorf("Len() = %d, want 1", c.Len())
	}
}

func TestCacheConcurrentAccess(t *testing.T) {
	c := NewCache(1000)

	const goroutines = 20
	const ops = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				key := fmt.Sprintf("/concurrent/%d/%d.txt", id, i)
				c.Put(key, fmt.Sprintf("data-%d-%d", id, i), 5*time.Second)
				c.Get(key)
				if i%3 == 0 {
					c.Invalidate(key)
				}
			}
		}(g)
	}

	wg.Wait()
	// If we get here without a race detector panic, the test passes.
}
