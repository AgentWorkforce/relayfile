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
// FINDING (see docs/multi-agent-collaboration-assessment.md): the
// .relay/conflicts/<path>.<baseRevision>.local materialization path
// (Syncer.materializeConflict, syncer.go:2624) only fires when the server
// returns ErrConflict for a write. But the mount daemon's local-edit push
// path (fsnotify -> HandleLocalChange -> pushSingleFile ->
// flushOutboxRecordChunk -> client.WriteFilesBulk, syncer.go:2258) goes
// through Store.BulkWrite (internal/relayfile/store.go:1409), which has NO
// If-Match / expected-revision field on BulkWriteFile
// (internal/relayfile/store.go:191-198) and unconditionally calls
// nextRevisionLocked() (store.go:1468) with no compare against the existing
// file's revision. Contrast Store.WriteFile (store.go:1328), the single-file
// PUT path, which REQUIRES IfMatch (store.go:1329) and rejects a stale one.
// So the documented "conflict-safe writeback ... concurrent changes are
// detected instead of silently overwritten" claim
// (docs/guides/collaboration.md, "Conflict Resolution" section) does not
// hold for the default local-edit-to-mount-daemon push path: it's last-write-
// wins with no detection, and the loser's local copy is never reverted or
// flagged — it silently diverges from the server until the next pull
// overwrites it with no warning.
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

	t.Logf("push results: errA=%v errB=%v (both nil == both writes accepted unconditionally)", errA, errB)
	if errA != nil || errB != nil {
		t.Fatalf("expected BOTH pushes to be accepted with no error (BulkWrite has no revision check) got errA=%v errB=%v", errA, errB)
	}

	finalA, _ := os.ReadFile(filepath.Join(localDirA, "Shared.md"))
	finalB, _ := os.ReadFile(filepath.Join(localDirB, "Shared.md"))
	t.Logf("local working copies after push: A=%q B=%q (each sandbox still shows its OWN edit — neither was reverted)", finalA, finalB)
	if string(finalA) != "edit from A" || string(finalB) != "edit from B" {
		t.Fatalf("expected each sandbox's local copy to remain unchanged post-push (no revert), got A=%q B=%q", finalA, finalB)
	}

	conflictsA := listConflictArtifacts(t, localDirA)
	conflictsB := listConflictArtifacts(t, localDirB)
	t.Logf("conflict artifacts: A=%v B=%v (expected: none — BulkWrite path never surfaces ErrConflict)", conflictsA, conflictsB)
	if len(conflictsA) != 0 || len(conflictsB) != 0 {
		t.Fatalf("expected NO conflict artifacts (BulkWrite has no conflict detection), got A=%v B=%v", conflictsA, conflictsB)
	}

	remoteFile, readErr := syncerA.client.ReadFile(ctx, workspaceID, "/notion/Shared.md")
	if readErr != nil {
		t.Fatalf("read final server content: %v", readErr)
	}
	serverContent := remoteFile.Content
	t.Logf("server's final stored content: %q (silent last-write-wins; whichever push reached Store.BulkWrite second)", serverContent)
	if serverContent != "edit from A" && serverContent != "edit from B" {
		t.Fatalf("expected server content to be exactly one writer's content (silent overwrite), got %q", serverContent)
	}
}

func listConflictArtifacts(t *testing.T, localDir string) []string {
	t.Helper()
	dir := filepath.Join(localDir, ".relay", "conflicts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
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
