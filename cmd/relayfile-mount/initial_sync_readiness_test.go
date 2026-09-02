package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

// sandboxInitialSyncGuard is a faithful transcription of the completion guard
// AgentWorkforce/sandbox runs after `relayfile-mount --once`
// (src/mount-script.ts, buildRelayfileMountInitialSyncCompletionGuardShell).
// When it reports not-complete the sandbox exits 75 (TEMPFAIL) with
// "relayfile initial sync paused before complete readiness". See relayfile#455.
func sandboxInitialSyncGuard(statePath string) (bool, string) {
	payload, err := os.ReadFile(statePath)
	if err != nil {
		return false, fmt.Sprintf("state file unreadable: %v", err)
	}
	var state map[string]any
	if err := json.Unmarshal(payload, &state); err != nil {
		return false, fmt.Sprintf("state file unparseable: %v", err)
	}
	if raw, ok := state["bootstrap"]; ok && raw != nil {
		return false, "state.bootstrap != null"
	}
	raw, ok := state["lastSuccessfulReconcileAt"]
	if !ok {
		return false, "lastSuccessfulReconcileAt absent"
	}
	value, isString := raw.(string)
	if !isString || strings.TrimSpace(value) == "" {
		return false, "lastSuccessfulReconcileAt empty"
	}
	return true, ""
}

func logPublicState(t *testing.T, statePath string) {
	t.Helper()
	payload, err := os.ReadFile(statePath)
	if err != nil {
		t.Logf("state.json unreadable: %v", err)
		return
	}
	var state map[string]any
	if err := json.Unmarshal(payload, &state); err != nil {
		t.Logf("state.json unparseable: %v", err)
		return
	}
	keys := make([]string, 0, len(state))
	for key := range state {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	bootstrap, _ := json.Marshal(state["bootstrap"])
	t.Logf("state.json bytes=%d keys=%v", len(payload), keys)
	t.Logf("  status=%v lastSuccessfulReconcileAt=%v bootstrap=%s",
		state["status"], state["lastSuccessfulReconcileAt"], string(bootstrap))
}

// budgetedBootstrapRelay serves a flat tree of fileCount files. Paired with a
// small RELAYFILE_BOOTSTRAP_MAX_FILES_PER_CYCLE it reproduces the production
// shape: the traversal exhausts its per-cycle file budget, persists a resume
// cursor and yields with traversal_complete=false.
func budgetedBootstrapRelay(t *testing.T, fileCount int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	entries := make([]mountsync.TreeEntry, 0, fileCount)
	for i := 0; i < fileCount; i++ {
		entries = append(entries, mountsync.TreeEntry{Path: fmt.Sprintf("/github/f/%05d.txt", i), Type: "file"})
	}
	var treeCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/fs/tree"):
			treeCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(mountsync.TreeResponse{Entries: entries})
		case strings.Contains(r.URL.Path, "/fs/file"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(mountsync.RemoteFile{
				Path:        r.URL.Query().Get("path"),
				ContentType: "text/plain",
				Content:     "content",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &treeCalls
}

func onceMountConfig(t *testing.T, baseURL, localDir string) mountConfig {
	t.Helper()
	return mountConfig{
		baseURL:          baseURL,
		token:            "test-token",
		workspaceID:      "ws_initial_sync_455",
		remotePath:       "/",
		localDir:         localDir,
		stateDir:         t.TempDir(),
		mountKind:        mountsync.MountKindDaemon,
		syncMode:         syncModeMirror,
		interval:         time.Hour,
		timeout:          30 * time.Second,
		websocketEnabled: false,
		once:             true,
	}
}

// TestInitialSyncOnceSatisfiesSandboxReadinessGuard is the recreate-then-verify
// probe for relayfile#455. A workspace larger than one per-cycle bootstrap file
// budget used to leave `bootstrap` non-null in .relay/state.json while
// `--once` exited 0, so the sandbox's readiness guard exited 75 on every JIT
// provision. `--once` must now resume the persisted checkpoint until the
// bootstrap completes.
func TestInitialSyncOnceSatisfiesSandboxReadinessGuard(t *testing.T) {
	// 24 files against a 5-file/cycle budget: five budget yields before the
	// traversal can finish, the same shape as 2000/cycle on a real workspace.
	t.Setenv("RELAYFILE_BOOTSTRAP_MAX_FILES_PER_CYCLE", "5")
	server, _ := budgetedBootstrapRelay(t, 24)
	localDir := t.TempDir()

	err := runSinglePollingMount(context.Background(), onceMountConfig(t, server.URL, localDir))
	if err != nil {
		t.Fatalf("mount --once returned an error: %v", err)
	}

	statePath := filepath.Join(localDir, ".relay", "state.json")
	logPublicState(t, statePath)
	if ready, reason := sandboxInitialSyncGuard(statePath); !ready {
		t.Fatalf("sandbox readiness guard failed after a successful --once (exit 75): %s", reason)
	}
	for i := 0; i < 24; i++ {
		path := filepath.Join(localDir, "github", "f", fmt.Sprintf("%05d.txt", i))
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("initial sync reported complete but %s is missing: %v", path, err)
		}
	}
}

// TestInitialSyncOnceStopsWhenRootContextEnds pins the cancellation bound on
// the resume loop: a cancelled root context must return without error rather
// than spin, leaving the persisted checkpoint for the next run.
func TestInitialSyncOnceStopsWhenRootContextEnds(t *testing.T) {
	t.Setenv("RELAYFILE_BOOTSTRAP_MAX_FILES_PER_CYCLE", "2")
	server, _ := budgetedBootstrapRelay(t, 200)
	localDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runSinglePollingMount(ctx, onceMountConfig(t, server.URL, localDir)) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled --once must not change the exit code, got %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("--once did not stop after its root context was cancelled")
	}
}
