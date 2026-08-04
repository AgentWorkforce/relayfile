package mountsync

// Measurement harness for docs/multi-agent-collaboration-assessment.md (Q2).
//
// Sets up a REAL internal/httpapi.Server (real HTTP, real WebSocket endpoint,
// real internal/relayfile.Store) plus real mountsync.Syncer instances with
// real local temp directories, simulating two agent sandboxes mounting the
// same workspace. Run with:
//   go test ./internal/mountsync/ -run TestAssessPropagation -v
//
// TestAssessPropagationLatencyWebSocket and TestAssessPropagationLatencyPollOnly
// deliberately don't use t.Parallel() per .claude/rules/go-httpapi-tests.md:
// they measure wall-clock convergence latency, and running them concurrently
// would have them compete for CPU/scheduler time and skew each other's
// numbers. TestAssessSameFileNearSimultaneousWrite is a pass/fail correctness
// check (not a timing measurement) and does use t.Parallel().

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

func newAssessStore(t *testing.T) *relayfile.Store {
	t.Helper()
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	return store
}

type blockingReadFileClient struct {
	RemoteClient
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingReadFileClient) ReadFile(ctx context.Context, workspaceID, path string) (RemoteFile, error) {
	file, err := c.RemoteClient.ReadFile(ctx, workspaceID, path)
	c.once.Do(func() {
		close(c.started)
		select {
		case <-c.release:
		case <-ctx.Done():
		}
	})
	if err == nil && ctx.Err() != nil {
		return RemoteFile{}, ctx.Err()
	}
	return file, err
}

// TestAssessPropagationLatencyWebSocket measures wall-clock time from a
// remote write landing on the server to sandbox B's local mirror reflecting
// it, with WebSocket push enabled (the default: RELAYFILE_MOUNT_WEBSOCKET
// defaults true per cmd/relayfile-mount/main.go:102).
func TestAssessPropagationLatencyWebSocket(t *testing.T) {
	store := newAssessStore(t)
	workspaceID := "ws_assess_prop_ws"
	handler := newMountsyncAPIHandler(t, store)
	api := httptest.NewServer(handler)
	token := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "SandboxB", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	localDirB := t.TempDir()
	clientB := NewHTTPClient(api.URL, token, api.Client())
	wsEnabled := true
	syncerB, err := NewSyncer(clientB, SyncerOptions{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/notion",
		LocalRoot:   localDirB,
		WebSocket:   &wsEnabled,
	})
	if err != nil {
		t.Fatalf("new syncer B: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Registered after t.TempDir() above, so on cleanup this runs BEFORE
	// TempDir's own removal (t.Cleanup is LIFO). readWebSocketLoop has no
	// exposed Wait/Close, so give its background goroutine a moment to
	// observe the canceled context / closed connection and stop touching
	// localDirB before the directory is removed out from under it —
	// otherwise TempDir's RemoveAll can race a still-running write and fail
	// with "directory not empty".
	t.Cleanup(func() {
		cancel()
		api.Close()
		time.Sleep(200 * time.Millisecond)
	})

	// Bootstrap the (empty) tree, then connect the WebSocket push channel.
	if err := syncerB.SyncOnce(ctx); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}
	if err := syncerB.MaintainWebSocket(ctx); err != nil {
		t.Fatalf("connect websocket: %v", err)
	}
	// Give the WS handshake + server catch-up read time to settle.
	time.Sleep(150 * time.Millisecond)

	const trials = 25
	latencies := make([]time.Duration, 0, trials)
	for i := 0; i < trials; i++ {
		path := fmt.Sprintf("/notion/Latency%d.md", i)
		content := fmt.Sprintf("# latency trial %d", i)
		start := time.Now()
		writeMountsyncRemoteFile(t, api.Client(), api.URL, token, workspaceID, path, "0", content)

		localPath := filepath.Join(localDirB, fmt.Sprintf("Latency%d.md", i))
		deadline := time.Now().Add(5 * time.Second)
		var seen time.Time
		for time.Now().Before(deadline) {
			data, readErr := os.ReadFile(localPath)
			if readErr == nil && string(data) == content {
				seen = time.Now()
				break
			}
			time.Sleep(500 * time.Microsecond)
		}
		if seen.IsZero() {
			t.Fatalf("trial %d: local mirror never converged within 5s", i)
		}
		latencies = append(latencies, seen.Sub(start))
	}

	reportLatencyStats(t, "websocket-push", latencies)
}

// TestAssessPropagationLatencyPollOnly measures the same thing with
// WebSocket disabled, forcing the poll fallback path. Uses a short interval
// (production default is 30s +/-20% jitter, floor 5s per
// cmd/relayfile-mount/main.go:34,96-97) purely to keep the test fast; the
// mechanism measured (poll-driven convergence, not push) is what matters.
func TestAssessPropagationLatencyPollOnly(t *testing.T) {
	store := newAssessStore(t)
	workspaceID := "ws_assess_prop_poll"
	handler := newMountsyncAPIHandler(t, store)
	api := httptest.NewServer(handler)
	defer api.Close()
	token := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "SandboxB", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	localDirB := t.TempDir()
	clientB := NewHTTPClient(api.URL, token, api.Client())
	wsDisabled := false
	pollInterval := 1 * time.Second
	syncerB, err := NewSyncer(clientB, SyncerOptions{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/notion",
		LocalRoot:   localDirB,
		WebSocket:   &wsDisabled,
		Interval:    pollInterval,
	})
	if err != nil {
		t.Fatalf("new syncer B: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := syncerB.SyncOnce(ctx); err != nil {
		t.Fatalf("initial sync failed: %v", err)
	}

	// Write BEFORE starting the poll loop so the write always lands mid-cycle
	// from the poller's perspective (worst case within one interval).
	writeMountsyncRemoteFile(t, api.Client(), api.URL, token, workspaceID, "/notion/PollLatency.md", "0", "# poll trial")
	start := time.Now()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = syncerB.SyncOnce(ctx)
			}
		}
	}()
	defer func() {
		close(stop)
		wg.Wait()
	}()

	localPath := filepath.Join(localDirB, "PollLatency.md")
	deadline := time.Now().Add(5 * time.Second)
	var seen time.Time
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(localPath)
		if readErr == nil && string(data) == "# poll trial" {
			seen = time.Now()
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if seen.IsZero() {
		t.Fatalf("poll mirror never converged within 5s (interval=%s)", pollInterval)
	}
	t.Logf("poll-only convergence: interval=%s observed_latency=%s (production default interval=30s +/-20%% jitter, 5s floor per cmd/relayfile-mount/main.go:34,96-97 — latency scales linearly with interval)",
		pollInterval, seen.Sub(start))
}

// TestAssessSameFileNearSimultaneousWrite exercises Q2's third question:
// two sandboxes editing the SAME path within about one sync cycle of each
// other.
//
// REGRESSION TEST for the Phase 0 fix (see
// docs/multi-agent-collaboration-assessment.md, gap #1): BulkWriteFile
// (internal/relayfile/store.go:191-198) previously had no If-Match /
// expected-revision field, and Store.BulkWrite unconditionally overwrote —
// last-write-wins with no detection, contradicting
// docs/guides/collaboration.md's "concurrent changes are detected instead of
// silently overwritten" claim. Store.BulkWrite now accepts an optional
// per-file IfMatch (checked the same way Store.WriteFile checks it,
// store.go:1328-1329), and mountsync's pushLocal/outbox path
// (ensureOutboxRecord in outbox.go, outboxRecordsAsBulkFiles in
// syncer.go:2438) populates it from the tracked base revision. This test
// asserts the fixed behavior: the losing write is rejected with a
// "conflict" BulkWriteError, Syncer.materializeConflict (syncer.go:2625)
// fires and preserves the loser's edit at
// .relay/conflicts/<path>.<baseRevision>.local, and the loser's working
// copy is reverted to match the server instead of silently diverging.
func TestAssessSameFileNearSimultaneousWrite(t *testing.T) {
	t.Parallel()
	store := newAssessStore(t)
	workspaceID := "ws_assess_same_file"
	handler := newMountsyncAPIHandler(t, store)
	api := httptest.NewServer(handler)
	defer api.Close()

	tokenA := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "SandboxA", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))
	tokenB := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "SandboxB", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	// Seed the shared file so both sandboxes bootstrap from the same revision.
	writeMountsyncRemoteFile(t, api.Client(), api.URL, tokenA, workspaceID, "/notion/Shared.md", "0", "base content")

	localDirA := t.TempDir()
	localDirB := t.TempDir()
	wsDisabled := false

	syncerA, err := NewSyncer(NewHTTPClient(api.URL, tokenA, api.Client()), SyncerOptions{
		WorkspaceID: workspaceID, RemoteRoot: "/notion", LocalRoot: localDirA, WebSocket: &wsDisabled,
	})
	if err != nil {
		t.Fatalf("new syncer A: %v", err)
	}
	syncerB, err := NewSyncer(NewHTTPClient(api.URL, tokenB, api.Client()), SyncerOptions{
		WorkspaceID: workspaceID, RemoteRoot: "/notion", LocalRoot: localDirB, WebSocket: &wsDisabled,
	})
	if err != nil {
		t.Fatalf("new syncer B: %v", err)
	}

	ctx := context.Background()
	if err := syncerA.SyncOnce(ctx); err != nil {
		t.Fatalf("A bootstrap: %v", err)
	}
	if err := syncerB.SyncOnce(ctx); err != nil {
		t.Fatalf("B bootstrap: %v", err)
	}
	assertLocalFileContent(t, filepath.Join(localDirA, "Shared.md"), "base content")
	assertLocalFileContent(t, filepath.Join(localDirB, "Shared.md"), "base content")

	// Both sandboxes edit the SAME path concurrently, based on the same
	// pre-race revision, then race to push.
	if err := os.WriteFile(filepath.Join(localDirA, "Shared.md"), []byte("edit from A"), 0o644); err != nil {
		t.Fatalf("local write A: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDirB, "Shared.md"), []byte("edit from B"), 0o644); err != nil {
		t.Fatalf("local write B: %v", err)
	}

	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		errA = syncerA.HandleLocalChange(ctx, "Shared.md", 0)
	}()
	go func() {
		defer wg.Done()
		errB = syncerB.HandleLocalChange(ctx, "Shared.md", 0)
	}()
	wg.Wait()

	// handleWriteError treats a detected conflict as gracefully handled (like
	// permission denial), not a hard Go error — materializeConflict returns
	// nil on success. So errA/errB nil-ness does NOT distinguish old-buggy
	// (silent overwrite) from fixed (detected + materialized) behavior; the
	// observable outcome (artifact + reverted local copy + server content)
	// is what proves the fix. Both are logged for diagnosis only.
	t.Logf("push results: errA=%v errB=%v", errA, errB)

	remoteFile, readErr := syncerA.client.ReadFile(ctx, workspaceID, "/notion/Shared.md")
	if readErr != nil {
		t.Fatalf("read final server content: %v", readErr)
	}
	serverContent := remoteFile.Content
	if serverContent != "edit from A" && serverContent != "edit from B" {
		t.Fatalf("expected server content to be exactly one writer's content, got %q", serverContent)
	}

	// Store.BulkWrite serializes the two racing pushes under a single mutex
	// (store.go BulkWrite), so exactly one wins outright and the other's
	// If-Match (the shared pre-race base revision) is stale by the time it's
	// checked — a clean win/lose split, not a partial/racy outcome.
	winner, loser := "A", "B"
	winnerDir, loserDir := localDirA, localDirB
	if serverContent == "edit from B" {
		winner, loser = "B", "A"
		winnerDir, loserDir = localDirB, localDirA
	}
	t.Logf("winner=%s loser=%s (server content=%q)", winner, loser, serverContent)

	winnerConflicts := listConflictArtifacts(t, winnerDir)
	loserConflicts := listConflictArtifacts(t, loserDir)
	t.Logf("conflict artifacts: winner(%s)=%v loser(%s)=%v", winner, winnerConflicts, loser, loserConflicts)
	if len(winnerConflicts) != 0 {
		t.Fatalf("winner (%s) should have no conflict artifact, got %v", winner, winnerConflicts)
	}
	if len(loserConflicts) != 1 {
		t.Fatalf("loser (%s) should have exactly one conflict artifact preserving its overwritten edit, got %v", loser, loserConflicts)
	}
	loserArtifactContent, err := os.ReadFile(filepath.Join(loserDir, ".relay", "conflicts", loserConflicts[0]))
	if err != nil {
		t.Fatalf("read loser conflict artifact: %v", err)
	}
	wantLoserArtifact := "edit from " + loser
	if string(loserArtifactContent) != wantLoserArtifact {
		t.Fatalf("conflict artifact should preserve the loser's overwritten edit %q, got %q", wantLoserArtifact, loserArtifactContent)
	}

	// The winner's working copy keeps its own edit; the loser's working copy
	// is reverted to match the server (the winner's content) — data isn't
	// lost, it's moved into the conflict artifact instead of silently gone.
	finalWinner, _ := os.ReadFile(filepath.Join(winnerDir, "Shared.md"))
	finalLoser, _ := os.ReadFile(filepath.Join(loserDir, "Shared.md"))
	t.Logf("local working copies after push: winner(%s)=%q loser(%s)=%q", winner, finalWinner, loser, finalLoser)
	if string(finalWinner) != "edit from "+winner {
		t.Fatalf("winner's local copy should still show its own edit, got %q", finalWinner)
	}
	if string(finalLoser) != serverContent {
		t.Fatalf("loser's local copy should be reverted to the server's (winner's) content %q, got %q", serverContent, finalLoser)
	}
}

// TestAssessSameFileWebSocketBeatsDebouncedWatcher covers the real-daemon
// ordering that TestAssessSameFileNearSimultaneousWrite cannot exercise:
// fsnotify waits 100ms for a local save to settle, while a competing writer's
// WebSocket update may be applied immediately. The remote update must preserve
// the on-disk edit even though HandleLocalChange has not marked it Dirty yet.
func TestAssessSameFileWebSocketBeatsDebouncedWatcher(t *testing.T) {
	t.Parallel()
	store := newAssessStore(t)
	workspaceID := "ws_assess_same_file_ws_debounce"
	handler := newMountsyncAPIHandler(t, store)
	api := httptest.NewServer(handler)
	defer api.Close()

	tokenA := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "SandboxA", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))
	tokenB := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "SandboxB", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))
	const remotePath = "/notion/Shared.md"
	writeMountsyncRemoteFile(t, api.Client(), api.URL, tokenA, workspaceID, remotePath, "0", "base content")

	localDirA := t.TempDir()
	localDirB := t.TempDir()
	wsDisabled := false
	syncerA, err := NewSyncer(NewHTTPClient(api.URL, tokenA, api.Client()), SyncerOptions{
		WorkspaceID: workspaceID, RemoteRoot: "/notion", LocalRoot: localDirA, WebSocket: &wsDisabled,
	})
	if err != nil {
		t.Fatalf("new syncer A: %v", err)
	}
	syncerB, err := NewSyncer(NewHTTPClient(api.URL, tokenB, api.Client()), SyncerOptions{
		WorkspaceID: workspaceID, RemoteRoot: "/notion", LocalRoot: localDirB, WebSocket: &wsDisabled,
	})
	if err != nil {
		t.Fatalf("new syncer B: %v", err)
	}

	ctx := context.Background()
	if err := syncerA.SyncOnce(ctx); err != nil {
		t.Fatalf("A bootstrap: %v", err)
	}
	if err := syncerB.SyncOnce(ctx); err != nil {
		t.Fatalf("B bootstrap: %v", err)
	}
	// Revision identifiers are opaque by contract. Make B's base sort after
	// the incoming store revision so any implementation that incorrectly uses
	// revision ordering to gate preservation fails this regression.
	trackedB := syncerB.state.Files[remotePath]
	trackedB.Revision = "zzzz_opaque_base"
	syncerB.state.Files[remotePath] = trackedB
	localPathA := filepath.Join(localDirA, "Shared.md")
	localPathB := filepath.Join(localDirB, "Shared.md")
	if err := os.WriteFile(localPathA, []byte("edit from A"), 0o644); err != nil {
		t.Fatalf("local write A: %v", err)
	}
	if err := os.WriteFile(localPathB, []byte("edit from B"), 0o644); err != nil {
		t.Fatalf("local write B: %v", err)
	}

	// A's watcher callback wins the server race. On B, model the WebSocket
	// callback arriving before fsnotify's 100ms debounced callback.
	if err := syncerA.HandleLocalChange(ctx, "Shared.md", 0); err != nil {
		t.Fatalf("A push: %v", err)
	}
	if err := syncerB.applyWebSocketEvent(ctx, websocketEvent{
		Type:      "file.updated",
		Path:      remotePath,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("B websocket apply: %v", err)
	}
	if err := syncerB.HandleLocalChange(ctx, "Shared.md", 0); err != nil {
		t.Fatalf("B delayed watcher callback: %v", err)
	}

	assertLocalFileContent(t, localPathB, "edit from A")
	conflicts := listConflictArtifacts(t, localDirB)
	if len(conflicts) != 1 {
		t.Fatalf("B should preserve its debounced local edit in one conflict artifact, got %v", conflicts)
	}
	artifact, err := os.ReadFile(filepath.Join(localDirB, ".relay", "conflicts", conflicts[0]))
	if err != nil {
		t.Fatalf("read B conflict artifact: %v", err)
	}
	if got := string(artifact); got != "edit from B" {
		t.Fatalf("B conflict artifact = %q, want %q", got, "edit from B")
	}
}

// A duplicate or stale WebSocket event can arrive during the same debounce
// window even when the server content has not advanced from the tracked base.
// It must leave the local edit for the watcher to push without manufacturing a
// conflict, because only one side has actually changed.
func TestAssessDuplicateRemoteBasePreservesDebouncedLocalEdit(t *testing.T) {
	t.Parallel()
	store := newAssessStore(t)
	workspaceID := "ws_assess_duplicate_remote_base"
	handler := newMountsyncAPIHandler(t, store)
	api := httptest.NewServer(handler)
	defer api.Close()

	token := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "SandboxB", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))
	const remotePath = "/notion/Shared.md"
	writeMountsyncRemoteFile(t, api.Client(), api.URL, token, workspaceID, remotePath, "0", "base content")

	localDir := t.TempDir()
	wsDisabled := false
	syncer, err := NewSyncer(NewHTTPClient(api.URL, token, api.Client()), SyncerOptions{
		WorkspaceID: workspaceID, RemoteRoot: "/notion", LocalRoot: localDir, WebSocket: &wsDisabled,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	ctx := context.Background()
	if err := syncer.SyncOnce(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	localPath := filepath.Join(localDir, "Shared.md")
	if err := os.WriteFile(localPath, []byte("edit from B"), 0o644); err != nil {
		t.Fatalf("local write: %v", err)
	}
	if err := syncer.applyWebSocketEvent(ctx, websocketEvent{
		Type:      "file.updated",
		Path:      remotePath,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("duplicate websocket apply: %v", err)
	}

	assertLocalFileContent(t, localPath, "edit from B")
	if conflicts := listConflictArtifacts(t, localDir); len(conflicts) != 0 {
		t.Fatalf("duplicate remote base should not create a conflict artifact, got %v", conflicts)
	}
	if err := syncer.HandleLocalChange(ctx, "Shared.md", 0); err != nil {
		t.Fatalf("delayed watcher callback: %v", err)
	}
	assertLocalFileContent(t, localPath, "edit from B")
	remoteFile, err := syncer.client.ReadFile(ctx, workspaceID, remotePath)
	if err != nil {
		t.Fatalf("read server file after delayed watcher: %v", err)
	}
	if got := remoteFile.Content; got != "edit from B" {
		t.Fatalf("server content after delayed watcher = %q, want %q", got, "edit from B")
	}
	if conflicts := listConflictArtifacts(t, localDir); len(conflicts) != 0 {
		t.Fatalf("single-sided local edit should remain conflict-free after push, got %v", conflicts)
	}
}

// A duplicate WebSocket read starts from the tracked base but does not hold
// the Syncer's mutex during network I/O. If the watcher pushes a local edit
// before that read is applied, the stale response must not roll the working
// copy or its tracked state back to the old base.
func TestAssessDuplicateWebSocketReadDoesNotEraseCompletedLocalWrite(t *testing.T) {
	t.Parallel()
	store := newAssessStore(t)
	workspaceID := "ws_assess_duplicate_ws_completed_write"
	handler := newMountsyncAPIHandler(t, store)
	api := httptest.NewServer(handler)
	defer api.Close()

	token := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "SandboxB", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))
	const remotePath = "/notion/Shared.md"
	writeMountsyncRemoteFile(t, api.Client(), api.URL, token, workspaceID, remotePath, "0", "base content")

	localDir := t.TempDir()
	wsDisabled := false
	syncer, err := NewSyncer(NewHTTPClient(api.URL, token, api.Client()), SyncerOptions{
		WorkspaceID: workspaceID, RemoteRoot: "/notion", LocalRoot: localDir, WebSocket: &wsDisabled,
	})
	if err != nil {
		t.Fatalf("new syncer: %v", err)
	}
	ctx := context.Background()
	if err := syncer.SyncOnce(ctx); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	blockedClient := &blockingReadFileClient{
		RemoteClient: syncer.client,
		started:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseBlockedRead := func() {
		releaseOnce.Do(func() { close(blockedClient.release) })
	}
	defer releaseBlockedRead()
	syncer.client = blockedClient
	eventResult := make(chan error, 1)
	go func() {
		eventResult <- syncer.applyWebSocketEvent(ctx, websocketEvent{
			Type:      "file.updated",
			Path:      remotePath,
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		})
	}()

	select {
	case <-blockedClient.started:
	case <-time.After(5 * time.Second):
		t.Fatal("websocket ReadFile did not reach the interleaving barrier")
	}
	localPath := filepath.Join(localDir, "Shared.md")
	if err := os.WriteFile(localPath, []byte("completed local edit"), 0o644); err != nil {
		t.Fatalf("local write: %v", err)
	}
	if err := syncer.HandleLocalChange(ctx, "Shared.md", 0); err != nil {
		t.Fatalf("local watcher push while websocket read was blocked: %v", err)
	}
	releaseBlockedRead()
	select {
	case err := <-eventResult:
		if err != nil {
			t.Fatalf("duplicate websocket apply: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("duplicate websocket apply did not finish")
	}

	assertLocalFileContent(t, localPath, "completed local edit")
	tracked := syncer.state.Files[remotePath]
	if tracked.Hash != hashBytes([]byte("completed local edit")) || tracked.Dirty {
		t.Fatalf("tracked state rolled back after stale websocket read: %+v", tracked)
	}
	remoteFile, err := syncer.client.ReadFile(ctx, workspaceID, remotePath)
	if err != nil {
		t.Fatalf("read server file after interleaving: %v", err)
	}
	if got := remoteFile.Content; got != "completed local edit" {
		t.Fatalf("server content after interleaving = %q, want %q", got, "completed local edit")
	}
	if conflicts := listConflictArtifacts(t, localDir); len(conflicts) != 0 {
		t.Fatalf("duplicate remote base should remain conflict-free, got %v", conflicts)
	}
}

// TestAssessSameFileNearSimultaneousWriteGoMergeAutoMerges is the Go-file
// counterpart to TestAssessSameFileNearSimultaneousWrite. Both sandboxes race
// from one confirmed-clean revision, but their edits target separate
// top-level functions. The losing write must use the mount-rollout merge path
// instead of becoming a conflict artifact.
func TestAssessSameFileNearSimultaneousWriteGoMergeAutoMerges(t *testing.T) {
	t.Parallel()
	store := newAssessStore(t)
	workspaceID := "ws_assess_same_go_file"
	handler := newMountsyncAPIHandler(t, store)
	api := httptest.NewServer(handler)
	defer api.Close()

	tokenA := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "SandboxA", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))
	tokenB := mustMountsyncTestJWT(t, "dev-secret", workspaceID, "SandboxB", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))
	const (
		remotePath  = "/notion/Shared.go"
		baseContent = "package shared\n\nfunc Alpha() string { return \"base alpha\" }\n\nfunc Beta() string { return \"base beta\" }\n"
		editA       = "package shared\n\nfunc Alpha() string { return \"edit from A\" }\n\nfunc Beta() string { return \"base beta\" }\n"
		editB       = "package shared\n\nfunc Alpha() string { return \"base alpha\" }\n\nfunc Beta() string { return \"edit from B\" }\n"
	)
	writeMountsyncRemoteFile(t, api.Client(), api.URL, tokenA, workspaceID, remotePath, "0", baseContent)

	localDirA := t.TempDir()
	localDirB := t.TempDir()
	wsDisabled := false
	syncerA, err := NewSyncer(NewHTTPClient(api.URL, tokenA, api.Client()), SyncerOptions{
		WorkspaceID: workspaceID, RemoteRoot: "/notion", LocalRoot: localDirA, WebSocket: &wsDisabled,
	})
	if err != nil {
		t.Fatalf("new syncer A: %v", err)
	}
	syncerB, err := NewSyncer(NewHTTPClient(api.URL, tokenB, api.Client()), SyncerOptions{
		WorkspaceID: workspaceID, RemoteRoot: "/notion", LocalRoot: localDirB, WebSocket: &wsDisabled,
	})
	if err != nil {
		t.Fatalf("new syncer B: %v", err)
	}

	ctx := context.Background()
	if err := syncerA.SyncOnce(ctx); err != nil {
		t.Fatalf("A bootstrap: %v", err)
	}
	if err := syncerB.SyncOnce(ctx); err != nil {
		t.Fatalf("B bootstrap: %v", err)
	}
	localPathA := filepath.Join(localDirA, "Shared.go")
	localPathB := filepath.Join(localDirB, "Shared.go")
	assertLocalFileContent(t, localPathA, baseContent)
	assertLocalFileContent(t, localPathB, baseContent)

	// A no-op watcher pass is the only place the rollout cache is populated:
	// it proves each local copy exactly matches its tracked server revision.
	if err := syncerA.HandleLocalChange(ctx, "Shared.go", 0); err != nil {
		t.Fatalf("A confirmed-clean pass: %v", err)
	}
	if err := syncerB.HandleLocalChange(ctx, "Shared.go", 0); err != nil {
		t.Fatalf("B confirmed-clean pass: %v", err)
	}
	baseRevisionA := syncerA.state.Files[remotePath].Revision
	baseRevisionB := syncerB.state.Files[remotePath].Revision
	if _, ok := syncerA.readShadowContent(remotePath, baseRevisionA); !ok {
		t.Fatal("A confirmed-clean pass did not populate a Go merge base")
	}
	if _, ok := syncerB.readShadowContent(remotePath, baseRevisionB); !ok {
		t.Fatal("B confirmed-clean pass did not populate a Go merge base")
	}

	if err := os.WriteFile(localPathA, []byte(editA), 0o644); err != nil {
		t.Fatalf("write local A edit: %v", err)
	}
	if err := os.WriteFile(localPathB, []byte(editB), 0o644); err != nil {
		t.Fatalf("write local B edit: %v", err)
	}

	var wg sync.WaitGroup
	var errA, errB error
	wg.Add(2)
	go func() {
		defer wg.Done()
		errA = syncerA.HandleLocalChange(ctx, "Shared.go", 0)
	}()
	go func() {
		defer wg.Done()
		errB = syncerB.HandleLocalChange(ctx, "Shared.go", 0)
	}()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("racing Go pushes failed: A=%v B=%v", errA, errB)
	}

	remoteFile, err := syncerA.client.ReadFile(ctx, workspaceID, remotePath)
	if err != nil {
		t.Fatalf("read merged server file: %v", err)
	}
	if !strings.Contains(remoteFile.Content, "func Alpha() string { return \"edit from A\" }") ||
		!strings.Contains(remoteFile.Content, "func Beta() string { return \"edit from B\" }") {
		t.Fatalf("merged server content should include both edits, got:\n%s", remoteFile.Content)
	}

	// The winner needs one ordinary poll to see the merge that was initiated
	// by the loser. This checks eventual local convergence as well as the
	// server-side merged result.
	if err := syncerA.SyncOnce(ctx); err != nil {
		t.Fatalf("A convergence sync: %v", err)
	}
	if err := syncerB.SyncOnce(ctx); err != nil {
		t.Fatalf("B convergence sync: %v", err)
	}
	for label, localPath := range map[string]string{"A": localPathA, "B": localPathB} {
		content, readErr := os.ReadFile(localPath)
		if readErr != nil {
			t.Fatalf("read final local %s file: %v", label, readErr)
		}
		if !strings.Contains(string(content), "func Alpha() string { return \"edit from A\" }") ||
			!strings.Contains(string(content), "func Beta() string { return \"edit from B\" }") {
			t.Fatalf("final local %s content should include both edits, got:\n%s", label, content)
		}
	}

	if artifacts := listConflictArtifacts(t, localDirA); len(artifacts) != 0 {
		t.Fatalf("A should have no conflict artifact after an auto-merge, got %v", artifacts)
	}
	if artifacts := listConflictArtifacts(t, localDirB); len(artifacts) != 0 {
		t.Fatalf("B should have no conflict artifact after an auto-merge, got %v", artifacts)
	}
}

// listConflictArtifacts returns conflict-artifact paths relative to
// .relay/conflicts/, recursively — per conflictArtifactPath (syncer.go),
// an artifact for remote path "/notion/Shared.md" lands nested at
// "notion/Shared.md.<rev>.local", not flat in the top-level directory.
// Artifacts already moved under conflicts/resolved/ (resolveConflictArtifacts,
// syncer.go) are excluded — those are resolved, not current conflicts.
func listConflictArtifacts(t *testing.T, localDir string) []string {
	t.Helper()
	dir := filepath.Join(localDir, ".relay", "conflicts")
	var names []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "resolved" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		names = append(names, rel)
		return nil
	})
	if err != nil {
		return nil
	}
	sort.Strings(names)
	return names
}

func reportLatencyStats(t *testing.T, label string, latencies []time.Duration) {
	t.Helper()
	if len(latencies) == 0 {
		t.Fatalf("%s: no samples", label)
	}
	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var sum time.Duration
	for _, d := range sorted {
		sum += d
	}
	mean := sum / time.Duration(len(sorted))
	median := sorted[len(sorted)/2]
	p95 := sorted[int(float64(len(sorted))*0.95)]
	if p95idx := int(float64(len(sorted)) * 0.95); p95idx < len(sorted) {
		p95 = sorted[p95idx]
	}
	min := sorted[0]
	max := sorted[len(sorted)-1]

	parts := make([]string, len(sorted))
	for i, d := range sorted {
		parts[i] = d.Round(time.Millisecond).String()
	}
	t.Logf("%s: n=%d min=%s mean=%s median=%s p95=%s max=%s samples=[%s]",
		label, len(sorted), min.Round(time.Millisecond), mean.Round(time.Millisecond),
		median.Round(time.Millisecond), p95.Round(time.Millisecond), max.Round(time.Millisecond),
		strings.Join(parts, ","))
}
