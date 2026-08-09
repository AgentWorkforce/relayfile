package mountsync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

type pendingLocalChange struct {
	RelativePath string `json:"relativePath"`
	Generation   string `json:"generation"`
}

// recordPendingLocalChange durably remembers a watcher observation before its
// debounce timer starts. The record is deliberately one small file per path:
// a fresh teardown process can enumerate O(pending) work without walking the
// mounted tree, while repeated events for one path coalesce naturally.
func (s *Syncer) recordPendingLocalChange(relativePath string) error {
	relativePath, err := normalizePendingLocalPath(relativePath)
	if err != nil {
		return err
	}
	record := pendingLocalChange{
		RelativePath: relativePath,
		Generation: fmt.Sprintf(
			"%d-%d",
			time.Now().UTC().UnixNano(),
			s.pendingLocalSequence.Add(1),
		),
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.pendingLocalDir, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(s.pendingLocalChangePath(relativePath), payload, 0o644)
}

func (s *Syncer) pendingLocalChangePath(relativePath string) string {
	sum := sha256.Sum256([]byte(filepath.ToSlash(relativePath)))
	return filepath.Join(s.pendingLocalDir, hex.EncodeToString(sum[:])+".json")
}

func normalizePendingLocalPath(relativePath string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(relativePath))
	if cleaned == "" || cleaned == "." || filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid pending local path %q", relativePath)
	}
	return filepath.ToSlash(cleaned), nil
}

func (s *Syncer) pendingLocalChangeToken(relativePath string) []byte {
	payload, err := os.ReadFile(s.pendingLocalChangePath(relativePath))
	if err != nil {
		return nil
	}
	return payload
}

// clearPendingLocalChange removes only the generation the handler observed.
// If another filesystem event rewrites the record while the upload is in
// flight, its new generation survives for a later callback or teardown drain.
func (s *Syncer) clearPendingLocalChange(relativePath string, observed []byte) error {
	if len(observed) == 0 {
		return nil
	}
	path := s.pendingLocalChangePath(relativePath)
	current, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(current, observed) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Syncer) pendingLocalChanges() ([]pendingLocalChange, error) {
	entries, err := os.ReadDir(s.pendingLocalDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	records := make([]pendingLocalChange, 0, len(entries))
	var readErrors []error
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.pendingLocalDir, entry.Name())
		payload, readErr := os.ReadFile(path)
		if readErr != nil {
			readErrors = append(readErrors, fmt.Errorf("read pending local change %s: %w", entry.Name(), readErr))
			continue
		}
		var record pendingLocalChange
		if decodeErr := json.Unmarshal(payload, &record); decodeErr != nil {
			readErrors = append(readErrors, fmt.Errorf("decode pending local change %s: %w", entry.Name(), decodeErr))
			continue
		}
		relativePath, normalizeErr := normalizePendingLocalPath(record.RelativePath)
		if normalizeErr != nil {
			readErrors = append(readErrors, fmt.Errorf("decode pending local change %s: %w", entry.Name(), normalizeErr))
			continue
		}
		record.RelativePath = relativePath
		if filepath.Base(s.pendingLocalChangePath(relativePath)) != entry.Name() {
			readErrors = append(readErrors, fmt.Errorf("pending local change %s does not match recorded path %q", entry.Name(), relativePath))
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].RelativePath < records[j].RelativePath
	})
	return records, errors.Join(readErrors...)
}

// FlushOutboxOnce is the single bounded teardown path. It ingests only
// paths durably observed by the watcher, then force-flushes the durable outbox.
// It never calls scanLocalFiles, pullRemote, digest generation, or websocket
// work, so its local detection cost is O(pending) rather than O(tree).
func (s *Syncer) FlushOutboxOnce(ctx context.Context) error {
	if err := s.assertMountRootInvariant(); err != nil {
		return err
	}
	records, journalErr := s.pendingLocalChanges()
	errs := []error{journalErr}
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := s.handleLocalChange(ctx, record.RelativePath, fsnotify.Op(0), true); err != nil {
			errs = append(errs, fmt.Errorf("ingest pending local change %s: %w", record.RelativePath, err))
		}
	}
	// Existing durable commands must still get their teardown attempt even if
	// one journal entry is malformed or one local draft cannot be ingested.
	if err := s.flushPersistedOutboxOnce(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
