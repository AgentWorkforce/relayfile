package mountfuse

import (
	"strings"
	"sync"
	"time"
)

type cacheEntry struct {
	value     any
	expiresAt time.Time
	key       string
}

type MetadataEntry struct {
	Path        string
	Type        string
	Revision    string
	ContentType string
	Size        int64
	Mode        uint32
	ModTime     time.Time
}

type ContentEntry struct {
	Path        string
	Revision    string
	ContentType string
	Data        []byte
}

// Cache is a thread-safe LRU cache with per-entry TTL expiration.
// The order slice tracks LRU ordering with the most recently used
// entry at the end.
type Cache struct {
	mu         sync.Mutex
	entries    map[string]*cacheEntry
	order      []string // LRU order, most recent at end
	maxEntries int
	nowFunc    func() time.Time // for testing
	refreshTTL time.Duration
}

// NewCache creates a cache that holds at most maxEntries items.
func NewCache(maxEntries int) *Cache {
	if maxEntries <= 0 {
		maxEntries = 1000
	}
	return &Cache{
		entries:    make(map[string]*cacheEntry),
		order:      make([]string, 0),
		maxEntries: maxEntries,
		nowFunc:    time.Now,
		refreshTTL: time.Second,
	}
}

func (c *Cache) now() time.Time {
	if c.nowFunc != nil {
		return c.nowFunc()
	}
	return time.Now()
}

// Get returns the cached value for key if it exists and has not expired.
// Expired entries are deleted on access. On a hit the key is moved to the
// end of the LRU order (most-recently-used position).
func (c *Cache) Get(key string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if c.now().After(entry.expiresAt) {
		c.removeLocked(key)
		return nil, false
	}
	c.moveToEnd(key)
	return entry.value, true
}

// Put adds or updates a cache entry with the given TTL. If the cache is at
// capacity, the least-recently-used entry (front of order) is evicted first.
func (c *Cache) Put(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.entries[key]; exists {
		c.entries[key].value = value
		c.entries[key].expiresAt = c.now().Add(ttl)
		c.moveToEnd(key)
		return
	}

	// Evict oldest if at capacity.
	for len(c.entries) >= c.maxEntries && len(c.order) > 0 {
		oldest := c.order[0]
		c.removeLocked(oldest)
	}

	c.entries[key] = &cacheEntry{
		value:     value,
		expiresAt: c.now().Add(ttl),
		key:       key,
	}
	c.order = append(c.order, key)
}

// Invalidate removes a specific key from the cache.
func (c *Cache) Invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(key)
}

// InvalidatePrefix removes all entries whose key starts with the given prefix.
func (c *Cache) InvalidatePrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key := range c.entries {
		if strings.HasPrefix(key, prefix) {
			c.removeLocked(key)
		}
	}
}

// Len returns the current number of entries in the cache.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *Cache) RemovePath(path string) {
	c.InvalidatePrefix("meta:" + path)
	c.InvalidatePrefix("content:" + path)
}

func (c *Cache) InvalidatePath(path string) {
	c.RemovePath(path)
	c.Invalidate("meta:" + path)
	c.Invalidate("content:" + path)
}

func (c *Cache) HotPaths(window time.Duration) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if window <= 0 {
		window = c.refreshTTL
	}
	cutoff := c.now().Add(-window)
	seen := map[string]struct{}{}
	paths := make([]string, 0)
	for key, entry := range c.entries {
		if entry.expiresAt.Before(cutoff) {
			continue
		}
		path := strings.TrimPrefix(strings.TrimPrefix(key, "meta:"), "content:")
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	return paths
}

func (c *Cache) RefreshInterval() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.refreshTTL
}

func (c *Cache) PutMetadata(entry MetadataEntry) {
	c.Put("meta:"+entry.Path, entry, c.refreshTTL)
}

func (c *Cache) PutContent(entry ContentEntry) {
	c.Put("content:"+entry.Path, entry, c.refreshTTL)
}

// removeLocked removes a key from both the entries map and the order slice.
// Caller must hold c.mu.
func (c *Cache) removeLocked(key string) {
	if _, ok := c.entries[key]; !ok {
		return
	}
	delete(c.entries, key)
	c.removeFromOrder(key)
}

// removeFromOrder removes the first occurrence of key from the order slice.
func (c *Cache) removeFromOrder(key string) {
	for i, k := range c.order {
		if k == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// moveToEnd moves key to the end of the order slice (most-recently-used).
func (c *Cache) moveToEnd(key string) {
	c.removeFromOrder(key)
	c.order = append(c.order, key)
}
