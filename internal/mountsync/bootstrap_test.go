package mountsync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// bootstrapClient is a configurable RemoteClient for the resumable /
// decoupled-timeout bootstrap tests. It paginates ListTree, can sleep per
// page, can fail at a configured page index, and respects ctx
// cancellation. ListEvents can sleep to exercise the independent cursor
// timeout.
type bootstrapClient struct {
	mu sync.Mutex

	files    map[string]RemoteFile
	pageSize int

	listTreePages    int
	listTreeSleep    time.Duration
	listTreeFailAt   int   // 1-based page index to fail at; 0 = never
	listTreeFailErr  error // error returned at failAt
	listTreeFailOnce bool  // clear failAt after firing once
	readFileSleep    time.Duration

	listEventsSleep time.Duration
	events          []FilesystemEvent

	listTreeCalls atomic.Int64
	readFileCalls atomic.Int64
}

func newBootstrapClient(fileCount, pageSize int) *bootstrapClient {
	files := make(map[string]RemoteFile, fileCount)
	for i := 0; i < fileCount; i++ {
		p := fmt.Sprintf("/f/%05d.txt", i)
		files[p] = RemoteFile{
			Path:        p,
			Revision:    fmt.Sprintf("rev_%d", i),
			ContentType: "text/plain",
			Content:     fmt.Sprintf("content-%d", i),
		}
	}
	return &bootstrapClient{files: files, pageSize: pageSize}
}

func (c *bootstrapClient) sortedPaths() []string {
	paths := make([]string, 0, len(c.files))
	for p := range c.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func (c *bootstrapClient) ListTree(ctx context.Context, workspaceID, path string, depth int, cursor string) (TreeResponse, error) {
	c.listTreeCalls.Add(1)
	if c.listTreeSleep > 0 {
		select {
		case <-time.After(c.listTreeSleep):
		case <-ctx.Done():
			return TreeResponse{}, ctx.Err()
		}
	}
	c.mu.Lock()
	failAt := c.listTreeFailAt
	failErr := c.listTreeFailErr
	failOnce := c.listTreeFailOnce
	c.mu.Unlock()

	paths := c.sortedPaths()
	start := 0
	if cursor != "" {
		for i, p := range paths {
			if p == cursor {
				start = i + 1
				break
			}
		}
	}
	pageIdx := start/c.pageSize + 1
	if failAt != 0 && pageIdx == failAt {
		if failOnce {
			c.mu.Lock()
			c.listTreeFailAt = 0
			c.mu.Unlock()
		}
		return TreeResponse{}, failErr
	}
	end := start + c.pageSize
	if end > len(paths) {
		end = len(paths)
	}
	entries := make([]TreeEntry, 0, end-start)
	for _, p := range paths[start:end] {
		f := c.files[p]
		entries = append(entries, TreeEntry{Path: p, Type: "file", Revision: f.Revision})
	}
	resp := TreeResponse{Path: normalizeRemotePath(path), Entries: entries}
	if end < len(paths) {
		next := paths[end-1]
		resp.NextCursor = &next
	}
	return resp, nil
}

func (c *bootstrapClient) ListEvents(ctx context.Context, workspaceID, provider, cursor string, limit int) (EventFeed, error) {
	if c.listEventsSleep > 0 {
		select {
		case <-time.After(c.listEventsSleep):
		case <-ctx.Done():
			return EventFeed{}, ctx.Err()
		}
	}
	if len(c.events) == 0 {
		return EventFeed{Events: []FilesystemEvent{}, NextCursor: nil}, nil
	}
	return EventFeed{Events: append([]FilesystemEvent(nil), c.events...), NextCursor: nil}, nil
}

func (c *bootstrapClient) ReadFile(ctx context.Context, workspaceID, path string) (RemoteFile, error) {
	c.readFileCalls.Add(1)
	if c.readFileSleep > 0 {
		select {
		case <-time.After(c.readFileSleep):
		case <-ctx.Done():
			return RemoteFile{}, ctx.Err()
		}
	}
	p := normalizeRemotePath(path)
	c.mu.Lock()
	f, ok := c.files[p]
	c.mu.Unlock()
	if !ok {
		return RemoteFile{}, &HTTPError{StatusCode: 404, Code: "not_found", Message: "not found"}
	}
	return f, nil
}

func (c *bootstrapClient) WriteFile(ctx context.Context, workspaceID, path, baseRevision, contentType, content string) (WriteResult, error) {
	return WriteResult{TargetRevision: "rev_w"}, nil
}

func (c *bootstrapClient) WriteFilesBulk(ctx context.Context, workspaceID string, files []BulkWriteFile) (BulkWriteResponse, error) {
	return BulkWriteResponse{}, nil
}

func (c *bootstrapClient) DeleteFile(ctx context.Context, workspaceID, path, baseRevision string) error {
	return nil
}

func loadPersistedState(t *testing.T, localDir string) mountState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(localDir, ".relayfile-mount-state.json"))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	var st mountState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	return st
}

func newBootstrapSyncer(t *testing.T, client RemoteClient, localDir string, opts SyncerOptions) *Syncer {
	t.Helper()
	opts.WorkspaceID = defaultIf(opts.WorkspaceID, "ws_bootstrap")
	opts.RemoteRoot = defaultIf(opts.RemoteRoot, "/")
	opts.LocalRoot = localDir
	wsDisabled := false
	opts.WebSocket = &wsDisabled
	s, err := NewSyncer(client, opts)
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	return s
}

func defaultIf(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// TestBootstrapTimeoutDecoupledFromCycleTimeout: a tiny inbound per-cycle
// deadline must NOT starve the bootstrap full pull, because the syncer
// derives the heavy-pull deadline from rootCtx.
func TestBootstrapTimeoutDecoupledFromCycleTimeout(t *testing.T) {
	client := newBootstrapClient(40, 10)
	client.listTreeSleep = 30 * time.Millisecond // many pages, slow
	localDir := t.TempDir()
	s := newBootstrapSyncer(t, client, localDir, SyncerOptions{
		RootCtx: context.Background(),
	})

	// Inbound per-cycle ctx with a 5ms deadline — far shorter than the
	// total ListTree time. Bootstrap must still complete.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := s.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile under tiny per-cycle ctx: %v", err)
	}
	if got, want := countLocalFiles(t, localDir), 40; got != want {
		t.Fatalf("expected %d mirrored files, got %d", want, got)
	}
	st := loadPersistedState(t, localDir)
	if !st.BootstrapComplete {
		t.Fatalf("expected BootstrapComplete=true after full mirror")
	}
}

// TestBootstrapProgressExtension: in progress-extension mode the
// bootstrap survives as long as pages keep landing within the idle
// window; if the client stalls past the idle window it is cancelled.
func TestBootstrapProgressExtension(t *testing.T) {
	t.Run("progress keeps it alive", func(t *testing.T) {
		client := newBootstrapClient(30, 10)
		client.listTreeSleep = 40 * time.Millisecond
		localDir := t.TempDir()
		s := newBootstrapSyncer(t, client, localDir, SyncerOptions{
			RootCtx: context.Background(),
		})
		// idle window 200ms, each page ~40ms -> never idle, total > window.
		s.bootstrapIdleTimeout = 200 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		if err := s.Reconcile(ctx); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if got := countLocalFiles(t, localDir); got != 30 {
			t.Fatalf("expected 30 files mirrored, got %d", got)
		}
	})

	t.Run("stall past idle window cancels", func(t *testing.T) {
		client := newBootstrapClient(30, 10)
		client.listTreeSleep = 400 * time.Millisecond // > idle window
		localDir := t.TempDir()
		s := newBootstrapSyncer(t, client, localDir, SyncerOptions{
			RootCtx: context.Background(),
		})
		s.bootstrapIdleTimeout = 80 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		err := s.Reconcile(ctx)
		if err == nil {
			t.Fatalf("expected cancellation error when client stalls past idle window")
		}
	})
}

// TestBootstrapResumesFromPersistedCursor: a mid-traversal failure
// persists the cursor + BootstrapComplete=false; the next cycle resumes
// from the persisted cursor and completes.
func TestBootstrapResumesFromPersistedCursor(t *testing.T) {
	client := newBootstrapClient(50, 10)
	client.listTreeFailAt = 3 // fail on the 3rd page
	client.listTreeFailErr = &HTTPError{StatusCode: 502, Code: "bad_gateway", Message: "boom"}
	client.listTreeFailOnce = true
	localDir := t.TempDir()
	s := newBootstrapSyncer(t, client, localDir, SyncerOptions{RootCtx: context.Background()})

	// Cycle 1: fails partway. Cursor + progress persisted, not complete.
	if err := s.Reconcile(context.Background()); err == nil {
		t.Fatalf("expected cycle 1 to fail mid-traversal")
	}
	st := loadPersistedState(t, localDir)
	if st.BootstrapComplete {
		t.Fatalf("BootstrapComplete must be false after interrupted traversal")
	}
	if st.BootstrapCursor == "" {
		t.Fatalf("expected a persisted BootstrapCursor after interrupted traversal")
	}
	syncedAfterCycle1 := st.BootstrapFilesSynced
	if syncedAfterCycle1 == 0 {
		t.Fatalf("expected some files synced before the failure")
	}
	readsAfterCycle1 := client.readFileCalls.Load()

	// Cycle 2: must resume from the persisted cursor (no re-read of the
	// already-applied prefix) and complete.
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("cycle 2 resume reconcile: %v", err)
	}
	st = loadPersistedState(t, localDir)
	if !st.BootstrapComplete {
		t.Fatalf("expected BootstrapComplete=true after resumed completion")
	}
	if st.BootstrapCursor != "" {
		t.Fatalf("expected BootstrapCursor cleared on completion, got %q", st.BootstrapCursor)
	}
	totalReads := client.readFileCalls.Load()
	resumedReads := totalReads - readsAfterCycle1
	// Resuming should read only the remaining ~ (50 - syncedAfterCycle1)
	// files, never the full 50 again.
	if int(resumedReads) > 50-syncedAfterCycle1+client.pageSize {
		t.Fatalf("resumed traversal re-read too many files: %d (synced before failure=%d)", resumedReads, syncedAfterCycle1)
	}
	if got := countLocalFiles(t, localDir); got != 50 {
		t.Fatalf("expected 50 files mirrored after resume, got %d", got)
	}
}

// TestBootstrapCompleteGatesFastPath: a hand-seeded state with Files +
// LastEventAt but BootstrapComplete=false MUST NOT short-circuit; the
// full pull runs and the mirror converges (the rw_517d60b6 repro).
func TestBootstrapCompleteGatesFastPath(t *testing.T) {
	client := newBootstrapClient(8, 10)
	localDir := t.TempDir()
	// Seed a partial state: 8 tracked files (but not on disk), LastEventAt
	// set, BootstrapComplete=false (clobber-remnant shape).
	seed := mountState{Files: map[string]trackedFile{}, LastEventAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)}
	for p, f := range client.files {
		seed.Files[p] = trackedFile{Revision: f.Revision, ContentType: f.ContentType, Hash: hashBytes([]byte(f.Content))}
	}
	if err := writeMountState(filepath.Join(localDir, ".relayfile-mount-state.json"), seed); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	s := newBootstrapSyncer(t, client, localDir, SyncerOptions{RootCtx: context.Background()})

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if client.listTreeCalls.Load() == 0 {
		t.Fatalf("expected ListTree to run (fast-path must NOT skip when BootstrapComplete=false)")
	}
	if got := countLocalFiles(t, localDir); got != 8 {
		t.Fatalf("expected mirror to converge to 8 files, got %d", got)
	}
	st := loadPersistedState(t, localDir)
	if !st.BootstrapComplete {
		t.Fatalf("expected BootstrapComplete=true after the forced full reconcile")
	}
}

// TestForceFullReconcileEnvOverridesCompleteFlag: BootstrapComplete=true
// + RELAYFILE_FORCE_FULL_RECONCILE=1 forces one full pull, then the flag
// self-clears so the next cycle uses the fast-path.
func TestForceFullReconcileEnvOverridesCompleteFlag(t *testing.T) {
	t.Setenv("RELAYFILE_FORCE_FULL_RECONCILE", "1")
	client := newBootstrapClient(5, 10)
	localDir := t.TempDir()
	seed := mountState{
		Files:             map[string]trackedFile{},
		BootstrapComplete: true,
		LastEventAt:       time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
	}
	for p, f := range client.files {
		seed.Files[p] = trackedFile{Revision: f.Revision, ContentType: f.ContentType, Hash: hashBytes([]byte(f.Content))}
	}
	if err := writeMountState(filepath.Join(localDir, ".relayfile-mount-state.json"), seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s := newBootstrapSyncer(t, client, localDir, SyncerOptions{RootCtx: context.Background()})
	if !s.forceFullReconcile {
		t.Fatalf("expected forceFullReconcile resolved true from env")
	}

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("forced reconcile: %v", err)
	}
	if client.listTreeCalls.Load() == 0 {
		t.Fatalf("expected forced full pull (ListTree) despite BootstrapComplete=true")
	}
	if s.forceFullReconcile {
		t.Fatalf("expected forceFullReconcile to self-clear after one full reconcile")
	}
	callsAfterForce := client.listTreeCalls.Load()

	// Next cycle: fast-path should now engage (no new ListTree).
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("post-force reconcile: %v", err)
	}
	if client.listTreeCalls.Load() != callsAfterForce {
		t.Fatalf("expected fast-path to engage after force cleared; ListTree calls went %d -> %d", callsAfterForce, client.listTreeCalls.Load())
	}
}

// TestResolveLatestEventCursorIndependentTimeout: ListEvents that sleeps
// longer than cursorTimeout returns a deadline error from its OWN short
// ctx even though the inbound ctx is context.Background().
func TestResolveLatestEventCursorIndependentTimeout(t *testing.T) {
	client := newBootstrapClient(1, 10)
	client.listEventsSleep = 200 * time.Millisecond
	localDir := t.TempDir()
	s := newBootstrapSyncer(t, client, localDir, SyncerOptions{
		RootCtx:       context.Background(),
		CursorTimeout: 30 * time.Millisecond,
	})
	start := time.Now()
	_, err := s.resolveLatestEventCursor(context.Background())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected deadline error from independent cursor timeout")
	}
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("cursor resolution did not honor its own short timeout (took %s)", elapsed)
	}
}

// TestBootstrapStatusSurfacesPhase: .relay/state.json exposes the
// bootstrap block + status:"bootstrapping" mid-bootstrap, and clears to
// ready after completion.
func TestBootstrapStatusSurfacesPhase(t *testing.T) {
	client := newBootstrapClient(20, 10)
	client.listTreeFailAt = 2
	client.listTreeFailErr = &HTTPError{StatusCode: 502, Code: "bad_gateway", Message: "boom"}
	client.listTreeFailOnce = true
	localDir := t.TempDir()
	s := newBootstrapSyncer(t, client, localDir, SyncerOptions{RootCtx: context.Background()})

	// Cycle 1 fails mid-bootstrap -> public state should show bootstrapping.
	_ = s.Reconcile(context.Background())
	pub := readPublicStateFile(t, localDir)
	if pub.Status != "bootstrapping" {
		t.Fatalf("expected status=bootstrapping mid-bootstrap, got %q", pub.Status)
	}
	if pub.Bootstrap == nil || pub.Bootstrap.Phase != "bootstrapping" {
		t.Fatalf("expected bootstrap block with phase=bootstrapping, got %+v", pub.Bootstrap)
	}

	// Cycle 2 completes -> bootstrap cleared.
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	pub = readPublicStateFile(t, localDir)
	if pub.Bootstrap != nil {
		t.Fatalf("expected bootstrap block cleared after completion, got %+v", pub.Bootstrap)
	}
	if pub.Status == "bootstrapping" {
		t.Fatalf("expected status to leave bootstrapping after completion, got %q", pub.Status)
	}
}

func readPublicStateFile(t *testing.T, localDir string) publicState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(localDir, ".relay", "state.json"))
	if err != nil {
		t.Fatalf("read public state: %v", err)
	}
	var ps publicState
	if err := json.Unmarshal(data, &ps); err != nil {
		t.Fatalf("unmarshal public state: %v", err)
	}
	return ps
}

// TestResumedTraversalSkipsSnapshotDeletePass: a partial (resumed)
// traversal must NOT delete locally-mirrored files that fall outside the
// partial listing — protects the #164/#165 mount-root-clobber invariants.
func TestResumedTraversalSkipsSnapshotDeletePass(t *testing.T) {
	client := newBootstrapClient(40, 10)
	client.listTreeFailAt = 3
	client.listTreeFailErr = &HTTPError{StatusCode: 502, Code: "bad_gateway", Message: "boom"}
	client.listTreeFailOnce = true
	localDir := t.TempDir()
	s := newBootstrapSyncer(t, client, localDir, SyncerOptions{RootCtx: context.Background()})

	// Cycle 1: partial, persists cursor.
	if err := s.Reconcile(context.Background()); err == nil {
		t.Fatalf("expected cycle 1 partial failure")
	}
	mirroredAfterPartial := countLocalFiles(t, localDir)
	if mirroredAfterPartial == 0 {
		t.Fatalf("expected some files mirrored before failure")
	}

	// Cycle 2 resumes from the persisted cursor: the listing is partial
	// (only the tail), so the snapshot delete pass MUST be skipped — the
	// already-mirrored prefix files must survive.
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if got := countLocalFiles(t, localDir); got != 40 {
		t.Fatalf("resumed traversal lost mirrored files: expected 40, got %d (prefix was clobbered by an unsafe delete pass)", got)
	}
}

// assertNoGoroutineLeak fails if the goroutine count grew materially
// across fn, after a short settle. Used to prove the bootstrap watchdog
// is torn down on every pullRemote exit path.
func assertNoGoroutineLeak(t *testing.T, fn func()) {
	t.Helper()
	before := runtime.NumGoroutine()
	fn()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before+1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutine leak: before=%d after=%d (bootstrap watchdog not torn down)", before, runtime.NumGoroutine())
}

func TestBootstrapWatchdogNoGoroutineLeak(t *testing.T) {
	assertNoGoroutineLeak(t, func() {
		client := newBootstrapClient(30, 10)
		client.listTreeSleep = 5 * time.Millisecond
		localDir := t.TempDir()
		s := newBootstrapSyncer(t, client, localDir, SyncerOptions{RootCtx: context.Background()})
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		if err := s.Reconcile(ctx); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	})
}
