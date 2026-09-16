package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

// TestOnceRecoveryStateDeadlineIsResumableBootstrap pins the Cloud hosted
// proof #1754 / relayfile bootstrap-owner flush blocker.
//
// The workspace is in the "detected non-empty state without completed
// bootstrap" recovery shape: it already tracks files, BootstrapComplete is
// false, and the traversal cursor is empty, so no public `bootstrap` block has
// been published yet. A per-cycle context deadline in that state is an
// in-progress bootstrap yield, not a fatal cycle failure: the heavy pull is
// deadline-decoupled and derives its own rootCtx window. Classifying it as
// fatal made `relayfile-mount --once` exit 1 with "mount sync cycle failed",
// which the Cloud bootstrap-owner flush treats as a hard failure and aborts the
// run instead of accepting the partial bootstrap.
func TestOnceRecoveryStateDeadlineIsResumableBootstrap(t *testing.T) {
	const blockedPath = "/github/f/00001.txt"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/fs/file"):
			// Block until the caller's per-cycle deadline fires, reproducing
			// the steady-state per-cycle timeout landing mid-reconcile.
			<-r.Context().Done()
		case strings.Contains(r.URL.Path, "/fs/tree"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(mountsync.TreeResponse{})
		case strings.Contains(r.URL.Path, "/fs/events"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"events":[]}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	localDir := t.TempDir()
	cfg := onceMountConfig(t, server.URL, localDir)
	cfg.timeout = 50 * time.Millisecond

	statePath := seedRecoveryState(t, cfg, localDir, blockedPath)

	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)

	err := runSinglePollingMount(context.Background(), cfg)
	if err != nil {
		t.Fatalf("recovery-state deadline must be a resumable bootstrap yield, got: %v", err)
	}
	if !strings.Contains(buf.String(), "mount bootstrapping: bootstrap incomplete (in progress)") {
		t.Fatalf("expected the resumable bootstrap diagnostic; log:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "mount sync cycle failed") {
		t.Fatalf("recovery-state deadline must not be reported as a fatal cycle failure; log:\n%s", buf.String())
	}
	// The public bootstrap block must still be absent so this test exercised the
	// authoritative-state branch rather than the published-checkpoint branch.
	if synced, total, ok := readBootstrapProgress(localDir); ok {
		t.Fatalf("test setup unexpectedly published a public bootstrap block: %d/%d", synced, total)
	}
	assertIncompleteRecoveryState(t, statePath, blockedPath)
}

func TestOnceRecoveryStateRootDeadlineRemainsFatal(t *testing.T) {
	const blockedPath = "/github/f/00001.txt"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/fs/file") {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	localDir := t.TempDir()
	cfg := onceMountConfig(t, server.URL, localDir)
	cfg.timeout = time.Second
	statePath := seedRecoveryState(t, cfg, localDir, blockedPath)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := runSinglePollingMount(ctx, cfg)
	if err == nil {
		t.Fatal("root deadline during incomplete bootstrap must not report success")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("root deadline cause = %v, want context.DeadlineExceeded", err)
	}
	if got := mountProcessExitCode(cfg, err); got == 0 {
		t.Fatalf("root deadline exit code = %d, want nonzero", got)
	}
	assertIncompleteRecoveryState(t, statePath, blockedPath)
}

func assertIncompleteRecoveryState(t *testing.T, statePath, blockedPath string) {
	t.Helper()
	payload, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read private state after recovery cycle: %v", err)
	}
	var state struct {
		BootstrapComplete       bool                       `json:"bootstrapComplete"`
		SkippedMaterializations map[string]json.RawMessage `json:"skippedMaterializations"`
	}
	if err := json.Unmarshal(payload, &state); err != nil {
		t.Fatalf("decode private recovery state: %v", err)
	}
	if state.BootstrapComplete {
		t.Fatal("private recovery state marked bootstrap complete")
	}
	if _, ok := state.SkippedMaterializations[blockedPath]; !ok {
		t.Fatalf("private recovery state lost blocked path %q", blockedPath)
	}
}

// seedRecoveryState writes the private mount state for a workspace that tracks
// files but never completed, or published a checkpoint for, its bootstrap.
func seedRecoveryState(t *testing.T, cfg mountConfig, localDir, blockedPath string) string {
	t.Helper()
	resolved, err := mountsync.ResolveMountStatePath(mountsync.MountStatePathOptions{
		WorkspaceID: cfg.workspaceID,
		RemoteRoot:  cfg.remotePath,
		LocalRoot:   localDir,
		StateDir:    cfg.stateDir,
		MountKind:   cfg.mountKind,
	})
	if err != nil {
		t.Fatalf("resolve state path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(resolved.StateFile), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	state := fmt.Sprintf(`{
		"workspaceId": %q,
		"remoteRoot": "/",
		"localRoot": %q,
		"files": {"/github/f/00000.txt": {"revision": "rev_0", "contentType": "text/plain", "hash": "abc"}},
		"bootstrapComplete": false,
		"skippedMaterializations": {%q: {"operation": "bootstrap read", "firstSeenAt": "2026-09-13T00:00:00Z", "lastAttemptAt": "2026-09-13T00:00:00Z", "attemptCount": 1, "lastError": "prior failure"}}
	}`, cfg.workspaceID, localDir, blockedPath)
	if err := os.WriteFile(resolved.StateFile, []byte(state), 0o644); err != nil {
		t.Fatalf("write recovery state: %v", err)
	}
	return resolved.StateFile
}
