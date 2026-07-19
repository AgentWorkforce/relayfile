package mountsync

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTombstoneTwoPhaseDelete asserts a missing path is NOT deleted on the
// first observation; only the second consecutive observation against a
// strictly-advancing revision actually deletes.
func TestTombstoneTwoPhaseDelete(t *testing.T) {
	disableWS := false
	localDir := t.TempDir()

	mirrored := filepath.Join(localDir, "keep.md")
	if err := os.WriteFile(mirrored, []byte("# keep"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	other := filepath.Join(localDir, "other.md")
	if err := os.WriteFile(other, []byte("# other"), 0o644); err != nil {
		t.Fatalf("seed other: %v", err)
	}
	stateFile := filepath.Join(localDir, ".relayfile-mount-state.json")
	if err := writeMountState(stateFile, mountState{
		LastAppliedRevision: "rev_1",
		Files: map[string]trackedFile{
			"/keep.md":  {Revision: "rev_1", ContentType: "text/markdown", Hash: hashString("# keep")},
			"/other.md": {Revision: "rev_1", ContentType: "text/markdown", Hash: hashString("# other")},
		},
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	// Cloud advances to rev_5 and removes /keep.md.
	files := map[string]RemoteFile{
		"/other.md": {Path: "/other.md", Revision: "rev_5", ContentType: "text/markdown", Content: "# other"},
	}
	fc := &fakeClient{files: files, eventsUnsupported: true}

	syncer, err := NewSyncer(fc, SyncerOptions{
		WorkspaceID: "ws_tombstone",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		WebSocket:   &disableWS,
		StateFile:   stateFile,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	// First pass: tombstone written, /keep.md still on disk.
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync 1: %v", err)
	}
	if _, err := os.Stat(mirrored); err != nil {
		t.Fatalf("first pass deleted file too early: %v", err)
	}
	if syncer.state.Counters.TombstonesPending == 0 {
		t.Fatalf("expected TombstonesPending to bump on first observation")
	}

	// Second pass: same listing, revision must advance for delete to fire.
	// Bump the cloud revision so the gate allows it.
	files["/other.md"] = RemoteFile{Path: "/other.md", Revision: "rev_6", ContentType: "text/markdown", Content: "# other"}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	if _, err := os.Stat(mirrored); !os.IsNotExist(err) {
		t.Fatalf("second pass should have deleted; stat err=%v", err)
	}
	if syncer.state.Counters.TombstonesConfirmed == 0 {
		t.Fatalf("expected TombstonesConfirmed to bump")
	}
}

// TestRevisionGateRefusesOlderListing asserts that a snapshot listing whose
// observed revision does not advance past lastAppliedRevision cannot fire
// deletes (even after tombstone confirmation, since the gate runs first).
func TestRevisionGateRefusesOlderListing(t *testing.T) {
	disableWS := false
	localDir := t.TempDir()

	mirrored := filepath.Join(localDir, "keep.md")
	if err := os.WriteFile(mirrored, []byte("# keep"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	other := filepath.Join(localDir, "other.md")
	if err := os.WriteFile(other, []byte("# other"), 0o644); err != nil {
		t.Fatalf("seed other: %v", err)
	}
	stateFile := filepath.Join(localDir, ".relayfile-mount-state.json")
	if err := writeMountState(stateFile, mountState{
		LastAppliedRevision: "rev_100", // already past
		Files: map[string]trackedFile{
			"/keep.md":  {Revision: "rev_50", ContentType: "text/markdown", Hash: hashString("# keep")},
			"/other.md": {Revision: "rev_50", ContentType: "text/markdown", Hash: hashString("# other")},
		},
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	// Cloud serves a listing at an older revision; deletes must be refused.
	files := map[string]RemoteFile{
		"/other.md": {Path: "/other.md", Revision: "rev_10", ContentType: "text/markdown", Content: "# other"},
	}
	fc := &fakeClient{files: files, eventsUnsupported: true}

	syncer, err := NewSyncer(fc, SyncerOptions{
		WorkspaceID: "ws_revgate",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		WebSocket:   &disableWS,
		StateFile:   stateFile,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	// Two passes: neither should delete because revision never advances
	// past rev_100.
	for i := 0; i < 2; i++ {
		if err := syncer.SyncOnce(context.Background()); err != nil {
			t.Fatalf("sync %d: %v", i+1, err)
		}
	}
	if _, err := os.Stat(mirrored); err != nil {
		t.Fatalf("revision gate failed: file deleted at older revision: %v", err)
	}
	if syncer.state.Counters.SnapshotDeleteBlocked == 0 {
		t.Fatalf("expected SnapshotDeleteBlocked counter to bump")
	}
}

// TestRevisionGateRefusesUnversionedListingAcrossRepeatedPulls proves that
// an unversioned listing can never authorize destructive snapshots.
// Two partial listings without revisions must neither create nor confirm a
// tombstone for the absent tracked path.
func TestRevisionGateRefusesUnversionedListingAcrossRepeatedPulls(t *testing.T) {
	disableWS := false
	localDir := t.TempDir()

	keepPath := filepath.Join(localDir, "keep.md")
	if err := os.WriteFile(keepPath, []byte("# keep"), 0o644); err != nil {
		t.Fatalf("seed keep: %v", err)
	}
	otherPath := filepath.Join(localDir, "other.md")
	if err := os.WriteFile(otherPath, []byte("# other"), 0o644); err != nil {
		t.Fatalf("seed other: %v", err)
	}
	stateFile := filepath.Join(localDir, ".relayfile-mount-state.json")
	if err := writeMountState(stateFile, mountState{
		Files: map[string]trackedFile{
			"/keep.md":  {ContentType: "text/markdown", Hash: hashString("# keep")},
			"/other.md": {ContentType: "text/markdown", Hash: hashString("# other")},
		},
		BootstrapComplete: true,
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	// The cloud omits /keep.md and emits no revisions for /other.md. This is
	// indistinguishable from an unversioned partial listing, never a delete
	// authorization.
	fc := &fakeClient{files: map[string]RemoteFile{
		"/other.md": {Path: "/other.md", ContentType: "text/markdown", Content: "# other"},
	}, eventsUnsupported: true}
	syncer, err := NewSyncer(fc, SyncerOptions{
		WorkspaceID: "ws_unversioned_gate",
		RemoteRoot:  "/",
		LocalRoot:   localDir,
		WebSocket:   &disableWS,
		StateFile:   stateFile,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := syncer.SyncOnce(context.Background()); err != nil {
			t.Fatalf("sync %d: %v", i+1, err)
		}
	}
	if _, err := os.Stat(keepPath); err != nil {
		t.Fatalf("unversioned listing deleted tracked file: %v", err)
	}
	if syncer.state.Counters.SnapshotDeleteBlocked < 2 {
		t.Fatalf("SnapshotDeleteBlocked = %d, want at least two blocked pulls", syncer.state.Counters.SnapshotDeleteBlocked)
	}
	if _, err := os.Stat(filepath.Join(localDir, ".relay", "pending-deletes")); !os.IsNotExist(err) {
		t.Fatalf("unversioned listing must not create tombstones, stat err=%v", err)
	}
}

// TestRevisionAdvances exercises the numeric and lexicographic paths.
func TestRevisionAdvances(t *testing.T) {
	if !revisionAdvances("", "") {
		t.Fatalf("empty last + empty observed must advance for the first-ever observation")
	}
	if revisionAdvances("rev_5", "") {
		t.Fatalf("empty observed must never advance")
	}
	if !revisionAdvances("", "rev_1") {
		t.Fatalf("empty last + observed must advance")
	}
	if !revisionAdvances("rev_1", "rev_10") {
		t.Fatalf("numeric rev_10 must advance past rev_1")
	}
	if revisionAdvances("rev_10", "rev_2") {
		t.Fatalf("numeric rev_2 must not advance past rev_10")
	}
	if !revisionAdvances("a", "b") {
		t.Fatalf("lexicographic b must advance past a")
	}
	if revisionAdvances("z", "a") {
		t.Fatalf("lexicographic a must not advance past z")
	}
	if revisionAdvances("rev_5", "rev_5") {
		t.Fatalf("equal revision must not advance")
	}
}

// TestPruneStaleTombstonesClearsReappeared exercises the prune pass.
func TestPruneStaleTombstonesClearsReappeared(t *testing.T) {
	localDir := t.TempDir()
	s := &Syncer{
		localRoot: localDir,
		state:     mountState{Files: map[string]trackedFile{}},
	}
	if _, err := s.observePendingDelete("/gone.md", "rev_2"); err != nil {
		t.Fatalf("observe: %v", err)
	}
	if _, err := os.Stat(s.tombstoneFile("/gone.md")); err != nil {
		t.Fatalf("expected tombstone file: %v", err)
	}
	// /gone.md reappears — prune should clear it.
	s.pruneStaleTombstones(map[string]struct{}{})
	if _, err := os.Stat(s.tombstoneFile("/gone.md")); !os.IsNotExist(err) {
		t.Fatalf("expected tombstone to be pruned; err=%v", err)
	}
}

func TestObservePendingDeleteAgedOutReobservesCurrentPass(t *testing.T) {
	localDir := t.TempDir()
	s := &Syncer{
		localRoot: localDir,
		state:     mountState{Files: map[string]trackedFile{}},
	}
	old := time.Now().UTC().Add(-(tombstoneMaxAge + time.Hour))
	if err := s.writeTombstone(&pendingDeleteTombstone{
		Path:             "/gone.md",
		FirstObservedAt:  old,
		LastObservedAt:   old,
		Attempts:         1,
		ObservedRevision: "rev_1",
	}); err != nil {
		t.Fatalf("seed old tombstone: %v", err)
	}

	allow, err := s.observePendingDelete("/gone.md", "rev_9")
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if allow {
		t.Fatalf("aged-out tombstone should reset observation, not allow delete")
	}
	data, err := os.ReadFile(s.tombstoneFile("/gone.md"))
	if err != nil {
		t.Fatalf("expected refreshed tombstone: %v", err)
	}
	var got pendingDeleteTombstone
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode refreshed tombstone: %v", err)
	}
	if got.Attempts != 1 || got.ObservedRevision != "rev_9" || !got.FirstObservedAt.After(old) {
		t.Fatalf("expected tombstone to be reset in current pass, got %#v", got)
	}
	if s.state.Counters.TombstonesAgedOut != 1 || s.state.Counters.TombstonesPending != 1 {
		t.Fatalf("unexpected counters after reset: %#v", s.state.Counters)
	}
}
