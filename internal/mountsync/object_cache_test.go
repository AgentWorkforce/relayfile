package mountsync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestObjectCacheRoundTripAndRejectsCorruption(t *testing.T) {
	cache := &objectCache{root: filepath.Join(t.TempDir(), "objects")}
	file := RemoteFile{Content: "hello", ContentHash: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"}
	cache.put(file)

	got, ok := cache.get(file.ContentHash)
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
	if err := os.WriteFile(path, []byte(`{"content":"tampered","contentHash":"`+file.ContentHash+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.get(file.ContentHash); ok {
		t.Fatal("corrupt object was accepted")
	}
}
