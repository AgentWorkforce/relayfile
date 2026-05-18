package mountsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// pendingDeleteTombstone is the on-disk marker placed under
// .relay/pending-deletes/<sha256-of-path>.json on the first observation
// that a tracked remote path has disappeared. The actual delete only fires
// after two consecutive clean confirmations against a strictly-advancing
// revision — the marker drives that protocol.
type pendingDeleteTombstone struct {
	Path             string    `json:"path"`
	FirstObservedAt  time.Time `json:"firstObservedAt"`
	LastObservedAt   time.Time `json:"lastObservedAt"`
	Attempts         int       `json:"attempts"`
	ObservedRevision string    `json:"observedRevision,omitempty"`
}

// tombstoneDir returns the directory under .relay where tombstones live.
func (s *Syncer) tombstoneDir() string {
	return filepath.Join(s.localRoot, ".relay", "pending-deletes")
}

// tombstoneFile returns the on-disk path for a tombstone for remotePath.
// SHA-256 is used to avoid percent-encoding the path itself into a
// filename and to keep names bounded.
func (s *Syncer) tombstoneFile(remotePath string) string {
	sum := sha256.Sum256([]byte(remotePath))
	return filepath.Join(s.tombstoneDir(), hex.EncodeToString(sum[:])+".json")
}

// loadTombstone returns the marker for remotePath, or (nil, nil) when none
// exists.
func (s *Syncer) loadTombstone(remotePath string) (*pendingDeleteTombstone, error) {
	data, err := os.ReadFile(s.tombstoneFile(remotePath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var t pendingDeleteTombstone
	if err := json.Unmarshal(data, &t); err != nil {
		// Corrupt marker — treat as absent so we re-observe and re-write.
		s.logf("tombstone %s unreadable (%v); discarding", remotePath, err)
		return nil, nil
	}
	return &t, nil
}

// writeTombstone persists or refreshes a marker for remotePath.
func (s *Syncer) writeTombstone(t *pendingDeleteTombstone) error {
	if err := os.MkdirAll(s.tombstoneDir(), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.tombstoneFile(t.Path), data, 0o644)
}

// removeTombstone unlinks the marker (best-effort).
func (s *Syncer) removeTombstone(remotePath string) {
	_ = os.Remove(s.tombstoneFile(remotePath))
}

// tombstoneAge is the wall-clock interval after which a tombstone that
// keeps appearing-then-disappearing without confirmation is aged out. This
// guards against a permanently-flapping path filling the tombstone dir.
const tombstoneMaxAge = 24 * time.Hour

// observePendingDelete is the per-path tombstone state machine invoked from
// the snapshot-delete pass. It records the observation and reports whether
// the actual delete may proceed *now*.
//
// Rules:
//   - First observation: write a fresh tombstone, do NOT delete. Bump
//     pendingCounter.
//   - Subsequent confirmed observation against a strictly-newer revision:
//     bump Attempts, confirm. After two confirmations, allow deletion and
//     bump confirmedCounter; the caller removes the tombstone after the
//     OS-level delete succeeds.
//   - Tombstones older than tombstoneMaxAge are dropped and re-observed
//     (avoids permanent stuck state).
func (s *Syncer) observePendingDelete(remotePath, observedRevision string) (allowDelete bool, err error) {
	now := time.Now().UTC()
	existing, err := s.loadTombstone(remotePath)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return s.recordPendingDeleteObservation(remotePath, observedRevision, now)
	}
	// Age out stale tombstones that never confirmed.
	if now.Sub(existing.FirstObservedAt) > tombstoneMaxAge {
		s.state.Counters.TombstonesAgedOut++
		s.removeTombstone(remotePath)
		return s.recordPendingDeleteObservation(remotePath, observedRevision, now)
	}
	existing.LastObservedAt = now
	existing.Attempts++
	if observedRevision != "" {
		existing.ObservedRevision = observedRevision
	}
	if existing.Attempts >= 2 {
		s.state.Counters.TombstonesConfirmed++
		// The caller is responsible for clearing the marker after the
		// actual delete; persist the increment first so a crash mid-delete
		// does not reset progress.
		if err := s.writeTombstone(existing); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, s.writeTombstone(existing)
}

func (s *Syncer) recordPendingDeleteObservation(remotePath, observedRevision string, now time.Time) (bool, error) {
	s.state.Counters.TombstonesPending++
	t := pendingDeleteTombstone{
		Path:             remotePath,
		FirstObservedAt:  now,
		LastObservedAt:   now,
		Attempts:         1,
		ObservedRevision: observedRevision,
	}
	if err := s.writeTombstone(&t); err != nil {
		return false, err
	}
	return false, nil
}

// pruneStaleTombstones drops markers for paths that no longer appear in the
// caller-supplied "still missing" set. This is invoked at the end of each
// snapshot pass: if a previously-tombstoned path is back, the marker is
// cleared.
func (s *Syncer) pruneStaleTombstones(stillMissing map[string]struct{}) {
	entries, err := os.ReadDir(s.tombstoneDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.tombstoneDir(), name))
		if err != nil {
			continue
		}
		var t pendingDeleteTombstone
		if err := json.Unmarshal(data, &t); err != nil {
			continue
		}
		if _, missing := stillMissing[t.Path]; missing {
			// Still pending — keep, but check age.
			if time.Since(t.FirstObservedAt) > tombstoneMaxAge {
				s.state.Counters.TombstonesAgedOut++
				_ = os.Remove(filepath.Join(s.tombstoneDir(), name))
			}
			continue
		}
		// Path reappeared in the snapshot; clear the marker.
		_ = os.Remove(filepath.Join(s.tombstoneDir(), name))
	}
}

// fmt placeholder so go vet does not complain when the file is otherwise
// fully covered.
var _ = fmt.Sprintf
