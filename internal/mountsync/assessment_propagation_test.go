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
