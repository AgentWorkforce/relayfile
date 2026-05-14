package cache

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// opaqueHash returns a deterministic 64-character lowercase hex string seeded
// from label. It is intentionally NOT a hash of the bytes that will be stored
// under it — the cache treats content_hash as opaque per the parent spec
// (Stage 3: sha256(bundled JS || sdk-version)), and the cached bytes are the
// downstream compiled WASM module.
func opaqueHash(label string) Hash {
	sum := sha256.Sum256([]byte("opaque-fixture:" + label))
	return Hash{Algo: "sha256", Hex: fmt.Sprintf("%x", sum[:])}
}

func opaqueHashWithAlgo(algo, label string) Hash {
	sum := sha256.Sum256([]byte("opaque-fixture:" + label))
	return Hash{Algo: algo, Hex: fmt.Sprintf("%x", sum[:])}
}

func openTestCache(t *testing.T, opts Options) *FileCache {
	t.Helper()
	opts.Dir = t.TempDir()
	c, err := Open(opts)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return c
}

func TestPathStability(t *testing.T) {
	c := openTestCache(t, Options{})
	h := opaqueHash("path-stable")

	got, err := c.Path(h)
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	want := filepath.Join(c.Dir(), h.Algo+"-"+h.Hex+".wasm")
	if got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
	if strings.Contains(filepath.Base(got), string(os.PathSeparator)) {
		t.Fatalf("Path() escaped cache dir: %q", got)
	}
}

func TestPathIncludesDigestAlgorithm(t *testing.T) {
	c := openTestCache(t, Options{})
	h := opaqueHashWithAlgo("blake3", "path-algo")

	got, err := c.Path(h)
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	want := filepath.Join(c.Dir(), "blake3-"+h.Hex+".wasm")
	if got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestNewHashParsesAlgorithmAndLegacySHA256(t *testing.T) {
	hex := opaqueHash("parse").Hex
	parsed, err := NewHash("blake3:" + hex)
	if err != nil {
		t.Fatalf("NewHash(blake3) error = %v", err)
	}
	if parsed.Algo != "blake3" || parsed.Hex != hex {
		t.Fatalf("NewHash(blake3) = %#v", parsed)
	}

	legacy, err := NewHash(hex)
	if err != nil {
		t.Fatalf("NewHash(legacy) error = %v", err)
	}
	if legacy.Algo != "sha256" || legacy.Hex != hex {
		t.Fatalf("NewHash(legacy) = %#v", legacy)
	}
}

func TestGetFallsBackToLegacyBareSHA256Path(t *testing.T) {
	c := openTestCache(t, Options{})
	h := opaqueHash("legacy")
	data := []byte("legacy wasm")
	legacyPath, err := c.legacyWasmCachePath(h.Hex)
	if err != nil {
		t.Fatalf("legacyWasmCachePath() error = %v", err)
	}
	legacySumPath, err := c.legacyWasmCacheSumPath(h.Hex)
	if err != nil {
		t.Fatalf("legacyWasmCacheSumPath() error = %v", err)
	}
	sum := sha256.Sum256(data)
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatalf("write legacy wasm: %v", err)
	}
	if err := os.WriteFile(legacySumPath, []byte(fmt.Sprintf("%x", sum[:])), 0o600); err != nil {
		t.Fatalf("write legacy sum: %v", err)
	}

	rc, err := c.Get(h)
	if err != nil {
		t.Fatalf("Get(legacy) error = %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Get(legacy) = %q, want %q", got, data)
	}
}

func TestGetDoesNotFallbackForNonSHA256(t *testing.T) {
	c := openTestCache(t, Options{})
	shaHash := opaqueHash("legacy-non-sha")
	h := Hash{Algo: "blake3", Hex: shaHash.Hex}
	data := []byte("legacy wasm")
	legacyPath, err := c.legacyWasmCachePath(h.Hex)
	if err != nil {
		t.Fatalf("legacyWasmCachePath() error = %v", err)
	}
	legacySumPath, err := c.legacyWasmCacheSumPath(h.Hex)
	if err != nil {
		t.Fatalf("legacyWasmCacheSumPath() error = %v", err)
	}
	sum := sha256.Sum256(data)
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatalf("write legacy wasm: %v", err)
	}
	if err := os.WriteFile(legacySumPath, []byte(fmt.Sprintf("%x", sum[:])), 0o600); err != nil {
		t.Fatalf("write legacy sum: %v", err)
	}

	if _, err := c.Get(h); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(non-sha legacy) error = %v, want ErrNotFound", err)
	}
}

func TestDefaultDirUsesRelayfileWasmCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("RELAYFILE_CACHE_DIR", "")
	t.Setenv("XDG_CACHE_HOME", "")

	got, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir() error = %v", err)
	}
	want := filepath.Join(home, ".relayfile", "cache", "wasm")
	if got != want {
		t.Fatalf("DefaultDir() = %q, want %q", got, want)
	}
}

func TestGetVerifiesIntegrityAndQuarantinesTampering(t *testing.T) {
	c := openTestCache(t, Options{})
	data := []byte("verified")
	h := opaqueHash("verified")
	if err := c.Put(h, bytes.NewReader(data)); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	path, err := c.Path(h)
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("tamper file: %v", err)
	}

	rc, err := c.Get(h)
	if rc != nil {
		rc.Close()
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Get() error = %v, want ErrCorrupt", err)
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("corrupt file was not quarantined: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt original still present, stat err = %v", err)
	}
}

func TestPutAcceptsOpaqueHashIndependentOfBytes(t *testing.T) {
	// The cache must store WASM bytes addressed by an opaque cloud-produced
	// content_hash. It must NOT re-derive the key from the bytes and reject
	// a mismatch — that would conflict with the parent spec's hash source.
	c := openTestCache(t, Options{})
	h := opaqueHash("opaque-key")
	data := []byte("arbitrary wasm payload — not a sha256 of the key")

	if err := c.Put(h, bytes.NewReader(data)); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	rc, err := c.Get(h)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Get() returned %q, want %q", got, data)
	}
}

func TestPutIsAtomicEnoughForReaders(t *testing.T) {
	c := openTestCache(t, Options{MaxBytes: 8 << 20})
	original := bytes.Repeat([]byte("a"), 4<<20)
	replacement := bytes.Repeat([]byte("b"), 4<<20)
	originalHash := opaqueHash("original")
	replacementHash := opaqueHash("replacement")
	if err := c.Put(originalHash, bytes.NewReader(original)); err != nil {
		t.Fatalf("Put(original) error = %v", err)
	}

	start := make(chan struct{})
	slow := &slowReader{r: bytes.NewReader(replacement), start: start}
	putDone := make(chan error, 1)
	go func() {
		putDone <- c.Put(replacementHash, slow)
	}()
	<-start

	rc, err := c.Get(originalHash)
	if err != nil {
		t.Fatalf("Get(original during Put) error = %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatal("reader observed partial or wrong bytes during concurrent Put")
	}
	if err := <-putDone; err != nil {
		t.Fatalf("Put(replacement) error = %v", err)
	}
}

func TestPutIsIdempotent(t *testing.T) {
	c := openTestCache(t, Options{})
	data := []byte("idempotent")
	h := opaqueHash("idempotent")
	c.now = fixedClock(time.Unix(10, 0))
	if err := c.Put(h, bytes.NewReader(data)); err != nil {
		t.Fatalf("first Put() error = %v", err)
	}
	path, err := c.Path(h)
	if err != nil {
		t.Fatalf("Path() error = %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	c.now = fixedClock(time.Unix(20, 0))
	if err := c.Put(h, bytes.NewReader(data)); err != nil {
		t.Fatalf("second Put() error = %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("idempotent Put touched mtime: before %s after %s", before.ModTime(), after.ModTime())
	}
}

func TestLRUEvictsOldest(t *testing.T) {
	c := openTestCache(t, Options{MaxBytes: 3})
	first := []byte("aa")
	second := []byte("bb")
	firstHash := opaqueHash("first")
	secondHash := opaqueHash("second")
	c.now = fixedClock(time.Unix(1, 0))
	if err := c.Put(firstHash, bytes.NewReader(first)); err != nil {
		t.Fatalf("Put(first) error = %v", err)
	}
	c.now = fixedClock(time.Unix(2, 0))
	if err := c.Put(secondHash, bytes.NewReader(second)); err != nil {
		t.Fatalf("Put(second) error = %v", err)
	}

	if _, err := c.Stat(firstHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("first Stat() error = %v, want ErrNotFound", err)
	}
	if _, err := c.Stat(secondHash); err != nil {
		t.Fatalf("second Stat() error = %v", err)
	}
}

func TestForgetRemovesEntry(t *testing.T) {
	c := openTestCache(t, Options{})
	data := []byte("forget")
	h := opaqueHash("forget")
	if err := c.Put(h, bytes.NewReader(data)); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := c.Forget(h); err != nil {
		t.Fatalf("Forget() error = %v", err)
	}
	if _, err := c.Get(h); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() error = %v, want ErrNotFound", err)
	}
	// Sidecar must be removed too.
	sumPath, err := c.sumPath(h)
	if err != nil {
		t.Fatalf("sumPath() error = %v", err)
	}
	if _, err := os.Stat(sumPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Forget left integrity sidecar behind, stat err = %v", err)
	}
}

func TestColdStartRebuildsIndex(t *testing.T) {
	dir := t.TempDir()
	c1, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatalf("Open(c1) error = %v", err)
	}
	data := []byte("cold-start")
	h := opaqueHash("cold-start")
	if err := c1.Put(h, bytes.NewReader(data)); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	c2, err := Open(Options{Dir: dir})
	if err != nil {
		t.Fatalf("Open(c2) error = %v", err)
	}
	rc, err := c2.Get(h)
	if err != nil {
		t.Fatalf("Get() after reopen error = %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read after reopen: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Get() after reopen = %q, want %q", got, data)
	}

	// A pre-existing .wasm without an integrity sidecar must be quarantined
	// on cold start: we cannot trust bytes we did not write ourselves.
	badHash := opaqueHash("orphan")
	badPath, err := c2.Path(badHash)
	if err != nil {
		t.Fatalf("Path(bad) error = %v", err)
	}
	if err := os.WriteFile(badPath, []byte("orphan"), 0o600); err != nil {
		t.Fatalf("write bad fixture: %v", err)
	}
	if _, err := Open(Options{Dir: dir}); err != nil {
		t.Fatalf("Open(c3) with orphaned preexisting file error = %v", err)
	}
	if _, err := os.Stat(badPath + ".corrupt"); err != nil {
		t.Fatalf("orphaned cold-start file was not quarantined: %v", err)
	}
}

func TestCacheIsWorkspaceAgnostic(t *testing.T) {
	var _ Cache = (*FileCache)(nil)
	c := openTestCache(t, Options{})
	data := []byte("workspace-free")
	h := opaqueHash("workspace-free")
	if err := c.Put(h, bytes.NewReader(data)); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if _, err := c.Get(h); err != nil {
		t.Fatalf("Get() error = %v", err)
	}
}

func TestConcurrentAccessRace(t *testing.T) {
	c := openTestCache(t, Options{MaxBytes: 2 << 20})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				data := []byte(strings.Repeat(string(rune('a'+id%26)), 128) + time.Now().String())
				h := opaqueHash(fmt.Sprintf("race-%d-%d-%d", id, j, time.Now().UnixNano()))
				if err := c.Put(h, bytes.NewReader(data)); err != nil {
					t.Errorf("Put() error = %v", err)
					return
				}
				rc, err := c.Get(h)
				if err == nil {
					rc.Close()
				} else if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrCorrupt) {
					t.Errorf("Get() error = %v", err)
					return
				}
				if j%7 == 0 {
					if err := c.Forget(h); err != nil {
						t.Errorf("Forget() error = %v", err)
						return
					}
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestInvalidHashRejected(t *testing.T) {
	c := openTestCache(t, Options{})
	if _, err := c.Get(Hash{Algo: "sha256", Hex: "../bad"}); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("Get(invalid) error = %v, want ErrInvalidHash", err)
	}
	if err := c.Put(Hash{Algo: "sha256", Hex: strings.Repeat("A", 64)}, bytes.NewReader(nil)); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("Put(uppercase hash) error = %v, want ErrInvalidHash", err)
	}
	if err := c.Put(Hash{Algo: "sha-256", Hex: opaqueHash("invalid-algo").Hex}, bytes.NewReader(nil)); !errors.Is(err, ErrInvalidHash) {
		t.Fatalf("Put(invalid algo) error = %v, want ErrInvalidHash", err)
	}
}

type slowReader struct {
	r     io.Reader
	start chan<- struct{}
	once  sync.Once
}

func (r *slowReader) Read(p []byte) (int, error) {
	r.once.Do(func() {
		close(r.start)
		time.Sleep(25 * time.Millisecond)
	})
	return r.r.Read(p)
}

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}
