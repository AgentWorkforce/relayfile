package mountsync

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestObjectCacheRoundTripAndRejectsCorruption(t *testing.T) {
	cache := &objectCache{root: filepath.Join(t.TempDir(), "objects")}
	file := RemoteFile{Content: "hello", ContentHash: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}
	cache.put(file)

	got, ok := cache.get(file.ContentHash, "")
	if !ok || got.Content != "hello" {
		t.Fatalf("cache get = %#v, %v", got, ok)
	}
	path := filepath.Join(cache.root, file.ContentHash)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode = %v", info.Mode().Perm())
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.get(file.ContentHash, ""); ok {
		t.Fatal("corrupt object was accepted")
	}
}

func TestObjectCacheStoresBytesOnlyNotWorkspaceMetadata(t *testing.T) {
	cache := &objectCache{root: filepath.Join(t.TempDir(), "objects")}
	file := RemoteFile{
		Path:        "/workspace-a/secret-name.txt",
		Revision:    "rev_workspace_a",
		ContentType: "application/x-workspace-a",
		Content:     "hello",
		ContentHash: "sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
	}
	cache.put(file)

	data, err := os.ReadFile(filepath.Join(cache.root, normalizeObjectHash(file.ContentHash)))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("object store must hold raw bytes only, got %q", data)
	}
	got, ok := cache.get(file.ContentHash, "")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if got.Path != "" || got.Revision != "" || got.ContentType != "" {
		t.Fatalf("cache replayed another response's metadata: %#v", got)
	}
}

func TestObjectCacheBase64RoundTrip(t *testing.T) {
	cache := &objectCache{root: filepath.Join(t.TempDir(), "objects")}
	raw := []byte{0xff, 0x00, 0xfe}
	hash := hashBytes(raw)
	cache.put(RemoteFile{Content: base64.StdEncoding.EncodeToString(raw), Encoding: "base64", ContentHash: hash})

	got, ok := cache.get(hash, "")
	if !ok || got.Encoding != "base64" {
		t.Fatalf("binary object should round-trip as base64: %#v, %v", got, ok)
	}
	decoded, err := base64.StdEncoding.DecodeString(got.Content)
	if err != nil || string(decoded) != string(raw) {
		t.Fatalf("decoded = %v, %v", decoded, err)
	}
}

func TestObjectCacheEvictsLeastRecentlyUsedOverByteCap(t *testing.T) {
	cache := &objectCache{root: filepath.Join(t.TempDir(), "objects"), maxBytes: 10}
	put := func(content string) string {
		hash := hashBytes([]byte(content))
		cache.put(RemoteFile{Content: content, ContentHash: hash})
		return hash
	}
	a := put("aaaa")
	b := put("bbbb")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(cache.root, a), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(cache.root, b), old.Add(time.Minute), old.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Touch a so b becomes the least recently used object.
	if _, ok := cache.get(a, ""); !ok {
		t.Fatal("expected a to be cached")
	}
	c := put("cccc")

	if _, ok := cache.get(b, ""); ok {
		t.Fatal("least recently used object should have been evicted")
	}
	for _, hash := range []string{a, c} {
		if _, ok := cache.get(hash, ""); !ok {
			t.Fatalf("object %s should remain cached", hash)
		}
	}
	var total int64
	for _, object := range cache.listObjects() {
		total += object.size
	}
	if total > cache.maxBytes {
		t.Fatalf("store holds %d bytes, cap %d", total, cache.maxBytes)
	}

	// An object larger than the whole cap is never stored.
	big := strings.Repeat("x", 11)
	if _, ok := cache.get(put(big), ""); ok {
		t.Fatal("object larger than the cap must not be stored")
	}
}

func TestSecondMountBootstrapsCachedObjectsWithoutBodyReads(t *testing.T) {
	cacheRoot := filepath.Join(t.TempDir(), "objects")
	shared := "shared bytes"
	sharedHash := hashBytes([]byte(shared))

	first := &fakeClient{files: map[string]RemoteFile{
		"/docs/a.txt": {Path: "/docs/a.txt", Revision: "rev_first", ContentType: "text/plain", Content: shared, ContentHash: sharedHash},
	}}
	firstSyncer, err := NewSyncer(first, SyncerOptions{WorkspaceID: "ws_first", RemoteRoot: "/", LocalRoot: t.TempDir(), ObjectCacheRoot: cacheRoot})
	if err != nil {
		t.Fatal(err)
	}
	if err := firstSyncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first.requestedReadCalls() == 0 {
		t.Fatal("first mount should fetch bodies from the server")
	}

	other := "other bytes"
	second := &fakeClient{files: map[string]RemoteFile{
		"/notes/b.txt": {Path: "/notes/b.txt", Revision: "rev_second", ContentType: "text/plain", Content: shared, ContentHash: sharedHash},
		"/notes/c.txt": {Path: "/notes/c.txt", Revision: "rev_c", ContentType: "text/plain", Content: other, ContentHash: hashBytes([]byte(other))},
	}}
	localDir := t.TempDir()
	secondSyncer, err := NewSyncer(second, SyncerOptions{WorkspaceID: "ws_second", RemoteRoot: "/", LocalRoot: localDir, ObjectCacheRoot: cacheRoot})
	if err != nil {
		t.Fatal(err)
	}
	if err := secondSyncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if got := second.readFileCallsByPath["/notes/b.txt"]; got != 0 {
		t.Fatalf("cached object should not be re-read, got %d reads", got)
	}
	if got := second.readFileCallsByPath["/notes/c.txt"]; got != 1 {
		t.Fatalf("uncached object should be read once, got %d", got)
	}
	data, err := os.ReadFile(filepath.Join(localDir, "notes", "b.txt"))
	if err != nil || string(data) != shared {
		t.Fatalf("materialized content = %q, %v", data, err)
	}
	if got := secondSyncer.state.Files["/notes/b.txt"].Revision; got != "rev_second" {
		t.Fatalf("revision must come from this mount's tree entry, got %q", got)
	}
}
