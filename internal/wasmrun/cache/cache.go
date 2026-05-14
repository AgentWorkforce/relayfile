package cache

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultAlgo     = "sha256"
	wasmExt         = ".wasm"
	sumExt          = ".wasm.sum"
	corruptExt      = ".corrupt"
	defaultMaxBytes = 256 << 20
)

var (
	ErrInvalidHash = errors.New("invalid content hash")
	ErrNotFound    = errors.New("wasm cache entry not found")
	ErrCorrupt     = errors.New("wasm cache entry corrupt")
)

// Hash is the cloud-produced content hash that addresses a cached module.
// It is opaque to this cache: the algorithm and digest bytes come from the
// caller. The cache does not re-derive the hash from the stored bytes;
// integrity of the bytes is checked via a sidecar checksum written at Put time.
type Hash struct {
	Algo string
	Hex  string
}

type Entry struct {
	Hash       Hash
	SizeBytes  int64
	AccessedAt time.Time
}

type Cache interface {
	Get(h Hash) (io.ReadCloser, error)
	Put(h Hash, src io.Reader) error
	Forget(h Hash) error
	Stat(h Hash) (Entry, error)
	List() ([]Entry, error)
}

type Options struct {
	Dir      string
	MaxBytes int64
	Verify   bool
}

type FileCache struct {
	dir      string
	maxBytes int64
	verify   bool
	now      func() time.Time

	mu sync.Mutex
}

func Open(opts Options) (*FileCache, error) {
	dir := opts.Dir
	if dir == "" {
		var err error
		dir, err = DefaultDir()
		if err != nil {
			return nil, err
		}
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultMaxBytes
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create wasm cache dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure wasm cache dir: %w", err)
	}

	c := &FileCache{
		dir:      dir,
		maxBytes: opts.MaxBytes,
		verify:   true,
		now:      time.Now,
	}
	if opts.Verify {
		c.verify = true
	}
	if err := c.recover(); err != nil {
		return nil, err
	}
	return c, nil
}

func DefaultDir() (string, error) {
	if dir := os.Getenv("RELAYFILE_CACHE_DIR"); dir != "" {
		return filepath.Join(dir, "wasm"), nil
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		return filepath.Join(home, ".relayfile", "cache", "wasm"), nil
	}
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "relayfile", "wasm"), nil
	}
	return "", errors.New("resolve relayfile cache dir: HOME and XDG_CACHE_HOME are unset")
}

func NewHash(s string) (Hash, error) {
	algo, hex, ok := strings.Cut(s, ":")
	if !ok {
		algo = defaultAlgo
		hex = s
	}
	h := Hash{Algo: algo, Hex: hex}
	if err := h.validate(); err != nil {
		return Hash{}, err
	}
	return h, nil
}

func (h Hash) String() string {
	if h.Algo == "" {
		return h.Hex
	}
	return h.Algo + ":" + h.Hex
}

func (h Hash) validate() error {
	if err := validateAlgo(h.Algo); err != nil {
		return err
	}
	if len(h.Hex) != sha256.Size*2 {
		return fmt.Errorf("%w: want 64 lowercase hex characters", ErrInvalidHash)
	}
	for _, r := range h.Hex {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			continue
		}
		return fmt.Errorf("%w: want 64 lowercase hex characters", ErrInvalidHash)
	}
	return nil
}

func (c *FileCache) Dir() string {
	return c.dir
}

func (c *FileCache) Path(h Hash) (string, error) {
	return c.wasmCachePath(h)
}

func (c *FileCache) wasmCachePath(h Hash) (string, error) {
	if err := h.validate(); err != nil {
		return "", err
	}
	return filepath.Join(c.dir, h.Algo+"-"+h.Hex+wasmExt), nil
}

func (c *FileCache) legacyWasmCachePath(hex string) (string, error) {
	h := Hash{Algo: defaultAlgo, Hex: hex}
	if err := h.validate(); err != nil {
		return "", err
	}
	return filepath.Join(c.dir, hex+wasmExt), nil
}

func (c *FileCache) sumPath(h Hash) (string, error) {
	return c.wasmCacheSumPath(h)
}

func (c *FileCache) wasmCacheSumPath(h Hash) (string, error) {
	if err := h.validate(); err != nil {
		return "", err
	}
	return filepath.Join(c.dir, h.Algo+"-"+h.Hex+sumExt), nil
}

func (c *FileCache) legacyWasmCacheSumPath(hex string) (string, error) {
	h := Hash{Algo: defaultAlgo, Hex: hex}
	if err := h.validate(); err != nil {
		return "", err
	}
	return filepath.Join(c.dir, hex+sumExt), nil
}

func (c *FileCache) Get(h Hash) (io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	path, err := c.wasmCachePath(h)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if h.Algo != defaultAlgo {
				return nil, ErrNotFound
			}
			legacyPath, legacyErr := c.legacyWasmCachePath(h.Hex)
			if legacyErr != nil {
				return nil, legacyErr
			}
			if _, legacyErr := os.Stat(legacyPath); legacyErr != nil {
				if errors.Is(legacyErr, os.ErrNotExist) {
					return nil, ErrNotFound
				}
				return nil, legacyErr
			}
			path = legacyPath
		} else {
			return nil, err
		}
	}
	if c.verify {
		if err := c.verifyFile(h, path); err != nil {
			return nil, err
		}
	}
	if err := c.touch(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (c *FileCache) Put(h Hash, src io.Reader) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	path, err := c.wasmCachePath(h)
	if err != nil {
		return err
	}
	sumPath, err := c.wasmCacheSumPath(h)
	if err != nil {
		return err
	}
	if err := c.ensureDir(); err != nil {
		return err
	}
	// Idempotent: if entry exists and matches its sidecar, no-op.
	if err := c.verifyFile(h, path); err == nil {
		return c.evict()
	} else if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrCorrupt) {
		return err
	}

	tmp, err := os.CreateTemp(c.dir, filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	hasher := sha256.New()
	if _, err := io.Copy(tmp, io.TeeReader(src, hasher)); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	bytesSum := fmt.Sprintf("%x", hasher.Sum(nil))

	tmpSum, err := os.CreateTemp(c.dir, filepath.Base(sumPath)+".tmp-")
	if err != nil {
		return err
	}
	tmpSumPath := tmpSum.Name()
	defer os.Remove(tmpSumPath)
	if _, err := io.WriteString(tmpSum, bytesSum); err != nil {
		tmpSum.Close()
		return err
	}
	if err := tmpSum.Sync(); err != nil {
		tmpSum.Close()
		return err
	}
	if err := tmpSum.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err := os.Rename(tmpSumPath, sumPath); err != nil {
		return err
	}
	if err := c.touch(path); err != nil {
		return err
	}
	return c.evict()
}

func (c *FileCache) Forget(h Hash) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	path, err := c.wasmCachePath(h)
	if err != nil {
		return err
	}
	sumPath, err := c.wasmCacheSumPath(h)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(sumPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	corrupt := path + corruptExt
	if err := os.Remove(corrupt); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if h.Algo == defaultAlgo {
		legacyPath, err := c.legacyWasmCachePath(h.Hex)
		if err != nil {
			return err
		}
		legacySumPath, err := c.legacyWasmCacheSumPath(h.Hex)
		if err != nil {
			return err
		}
		if err := os.Remove(legacyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Remove(legacySumPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Remove(legacyPath + corruptExt); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (c *FileCache) Stat(h Hash) (Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	path, err := c.wasmCachePath(h)
	if err != nil {
		return Entry{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Entry{}, ErrNotFound
		}
		return Entry{}, err
	}
	return Entry{
		Hash:       h,
		SizeBytes:  info.Size(),
		AccessedAt: info.ModTime(),
	}, nil
}

func (c *FileCache) List() ([]Entry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listLocked()
}

func (c *FileCache) recover() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	entries, err := c.listLocked()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path, err := c.wasmCachePath(entry.Hash)
		if err != nil {
			return err
		}
		if err := c.verifyFile(entry.Hash, path); err != nil && !errors.Is(err, ErrNotFound) {
			if !errors.Is(err, ErrCorrupt) {
				return err
			}
		}
	}
	return c.evict()
}

func (c *FileCache) ensureDir() error {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return fmt.Errorf("create wasm cache dir: %w", err)
	}
	return nil
}

// verifyFile compares the on-disk bytes against the integrity sidecar that
// was written at Put time. It does NOT compare against the content_hash
// filename — per the parent spec, content_hash is sha256(bundled JS ||
// sdk-version) produced by the cloud, not the hash of the compiled WASM
// bytes stored here.
func (c *FileCache) verifyFile(h Hash, path string) error {
	if err := h.validate(); err != nil {
		return err
	}
	sumPath, err := c.wasmCacheSumPath(h)
	if err != nil {
		return err
	}
	if filepath.Base(path) == h.Hex+wasmExt {
		sumPath, err = c.legacyWasmCacheSumPath(h.Hex)
		if err != nil {
			return err
		}
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	wantBytes, err := os.ReadFile(sumPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// File present without sidecar — treat as corrupt and quarantine.
			if qerr := c.quarantine(path); qerr != nil {
				return qerr
			}
			return fmt.Errorf("%w: missing integrity sidecar", ErrCorrupt)
		}
		return err
	}
	want := strings.TrimSpace(string(wantBytes))

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	defer f.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return err
	}
	got := fmt.Sprintf("%x", sum.Sum(nil))
	if got != want {
		if err := c.quarantine(path); err != nil {
			return err
		}
		// Drop the now-stale sidecar so a future Put can re-populate cleanly.
		_ = os.Remove(sumPath)
		return fmt.Errorf("%w: got %s want %s", ErrCorrupt, got, want)
	}
	return nil
}

func (c *FileCache) quarantine(path string) error {
	target := path + corruptExt
	if _, err := os.Stat(target); err == nil {
		target = fmt.Sprintf("%s.%d", target, c.now().UnixNano())
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(path, target); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return nil
}

func (c *FileCache) listLocked() ([]Entry, error) {
	dirents, err := os.ReadDir(c.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	entries := make([]Entry, 0, len(dirents))
	for _, dirent := range dirents {
		name := dirent.Name()
		if dirent.IsDir() || !strings.HasSuffix(name, wasmExt) || strings.HasSuffix(name, sumExt) {
			continue
		}
		stem := strings.TrimSuffix(name, wasmExt)
		algo, hex, ok := strings.Cut(stem, "-")
		if !ok {
			continue
		}
		h, err := NewHash(algo + ":" + hex)
		if err != nil {
			continue
		}
		info, err := dirent.Info()
		if err != nil {
			return nil, err
		}
		entries = append(entries, Entry{
			Hash:       h,
			SizeBytes:  info.Size(),
			AccessedAt: info.ModTime(),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].AccessedAt.Equal(entries[j].AccessedAt) {
			return entries[i].Hash.String() < entries[j].Hash.String()
		}
		return entries[i].AccessedAt.Before(entries[j].AccessedAt)
	})
	return entries, nil
}

func (c *FileCache) evict() error {
	entries, err := c.listLocked()
	if err != nil {
		return err
	}
	var total int64
	for _, entry := range entries {
		total += entry.SizeBytes
	}
	for _, entry := range entries {
		if total <= c.maxBytes {
			return nil
		}
		path, err := c.wasmCachePath(entry.Hash)
		if err != nil {
			return err
		}
		sumPath, err := c.wasmCacheSumPath(entry.Hash)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Remove(sumPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		total -= entry.SizeBytes
	}
	return nil
}

func (c *FileCache) touch(path string) error {
	now := c.now()
	return os.Chtimes(path, now, now)
}

func validateAlgo(algo string) error {
	if algo == "" {
		return fmt.Errorf("%w: missing digest algorithm", ErrInvalidHash)
	}
	for _, r := range algo {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return fmt.Errorf("%w: invalid digest algorithm", ErrInvalidHash)
	}
	return nil
}
