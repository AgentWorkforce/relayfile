package mountsync

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var objectHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// defaultObjectCacheMaxBytes bounds the persistent content-addressed store.
// Objects are evicted least-recently-used (by mtime, refreshed on hit) once
// the directory grows past the cap.
const defaultObjectCacheMaxBytes int64 = 1 << 30

// objectCache is a content-addressed store of raw file bytes keyed by their
// SHA-256. It deliberately stores bytes only: path, revision, content type and
// every other piece of per-workspace metadata comes from the server response
// (tree entry or read) that authorized the caller to see the object, so the
// store never retains or replays metadata from another workspace.
type objectCache struct {
	root     string
	maxBytes int64

	mu         sync.Mutex
	sized      bool
	totalBytes int64
}

func defaultObjectCache() *objectCache {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return &objectCache{root: filepath.Join(home, ".relayfile", "cache", "objects")}
}

func normalizeObjectHash(value string) string {
	value = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "sha256:")
	if !objectHashPattern.MatchString(value) {
		return ""
	}
	return value
}

func (c *objectCache) limit() int64 {
	if c.maxBytes > 0 {
		return c.maxBytes
	}
	return defaultObjectCacheMaxBytes
}

// get returns the cached bytes for hash as a RemoteFile carrying only
// content, encoding and hash. Callers fill in the path metadata from the
// authorizing server response. encoding is the encoding the server reported
// for the path ("" when unknown); binary content is always base64-encoded.
func (c *objectCache) get(hash, encoding string) (RemoteFile, bool) {
	hash = normalizeObjectHash(hash)
	if c == nil || hash == "" {
		return RemoteFile{}, false
	}
	path := filepath.Join(c.root, hash)
	data, err := os.ReadFile(path)
	if err != nil {
		return RemoteFile{}, false
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hash {
		return RemoteFile{}, false
	}
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	file := RemoteFile{ContentHash: hash}
	if normalizeEncoding(encoding) == "base64" || !utf8.Valid(data) {
		file.Content = base64.StdEncoding.EncodeToString(data)
		file.Encoding = "base64"
	} else {
		file.Content = string(data)
	}
	return file, true
}

func (c *objectCache) put(file RemoteFile) {
	hash := normalizeObjectHash(file.ContentHash)
	if c == nil || hash == "" {
		return
	}
	data, ok := remoteFileBytes(file)
	if !ok || int64(len(data)) > c.limit() {
		return
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != hash {
		return
	}
	target := filepath.Join(c.root, hash)
	if _, err := os.Stat(target); err == nil {
		now := time.Now()
		_ = os.Chtimes(target, now, now)
		return
	}
	if os.MkdirAll(c.root, 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(c.root, ".object-*")
	if err != nil {
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	_ = tmp.Chmod(0o600)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil || os.Rename(name, target) != nil {
		return
	}
	c.added(int64(len(data)))
}

// added accounts for a newly stored object and evicts least-recently-used
// objects once the store exceeds its byte cap. The running total is seeded
// from one directory scan and re-measured on every eviction, so objects
// written by concurrent mounts sharing the directory are accounted for.
func (c *objectCache) added(size int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.sized {
		c.totalBytes = 0
		for _, object := range c.listObjects() {
			c.totalBytes += object.size
		}
		c.sized = true
	} else {
		c.totalBytes += size
	}
	if c.totalBytes <= c.limit() {
		return
	}
	objects := c.listObjects()
	total := int64(0)
	for _, object := range objects {
		total += object.size
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].modTime.Before(objects[j].modTime) })
	for _, object := range objects {
		if total <= c.limit() {
			break
		}
		// Removing an object another process is reading is safe: the reader
		// either already holds the bytes (and re-verifies the hash) or misses.
		if err := os.Remove(filepath.Join(c.root, object.name)); err == nil || os.IsNotExist(err) {
			total -= object.size
		}
	}
	c.totalBytes = total
}

type objectCacheEntry struct {
	name    string
	size    int64
	modTime time.Time
}

func (c *objectCache) listObjects() []objectCacheEntry {
	entries, err := os.ReadDir(c.root)
	if err != nil {
		return nil
	}
	objects := make([]objectCacheEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !objectHashPattern.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		objects = append(objects, objectCacheEntry{name: entry.Name(), size: info.Size(), modTime: info.ModTime()})
	}
	return objects
}

func remoteFileBytes(file RemoteFile) ([]byte, bool) {
	if strings.EqualFold(file.Encoding, "base64") {
		decoded, err := base64.StdEncoding.DecodeString(file.Content)
		if err != nil {
			return nil, false
		}
		return decoded, true
	}
	return []byte(file.Content), true
}
