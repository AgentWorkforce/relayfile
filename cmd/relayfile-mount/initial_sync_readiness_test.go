package main

import (
	"context"
	"encoding/json"
	"errors"
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
	return budgetedBootstrapRelayWithDelay(t, fileCount, 0)
}

func budgetedBootstrapRelayWithDelay(t *testing.T, fileCount int, readDelay time.Duration) (*httptest.Server, *atomic.Int32) {
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
			if readDelay > 0 {
				select {
				case <-time.After(readDelay):
				case <-r.Context().Done():
					return
				}
			}
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

func TestInitialSyncOnceRejectsEmptyUnmaterializedSource(t *testing.T) {
	root := "/github/repos/acme/project/contents"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/fs/tree"):
			_ = json.NewEncoder(w).Encode(mountsync.TreeResponse{Path: root, Entries: []mountsync.TreeEntry{}})
		case strings.Contains(r.URL.Path, "/fs/file") && strings.HasSuffix(r.URL.Query().Get("path"), "/meta.json"):
			_ = json.NewEncoder(w).Encode(mountsync.RemoteFile{Path: r.URL.Query().Get("path"), Content: `{"default_branch":"main"}`, ContentType: "application/json"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	localDir := t.TempDir()
	cfg := onceMountConfig(t, server.URL, localDir)
	cfg.remotePath = root
	err := runSinglePollingMount(context.Background(), cfg)
	var emptyTree *mountsync.EmptyRemoteTreeError
	if !errors.As(err, &emptyTree) || !strings.Contains(err.Error(), "missing headSha") {
		t.Fatalf("--once should fail with source and manifest evidence: %v", err)
	}
	if ready, _ := sandboxInitialSyncGuard(filepath.Join(localDir, ".relay", "state.json")); ready {
		t.Fatal("empty source mount incorrectly satisfies readiness guard")
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
//
// The workspace is deliberately sized so the bootstrap cannot finish inside the
// window — 400 files at 2 per cycle, each read delayed — and the test asserts
// that cancellation actually fired and that the bootstrap is still incomplete.
// Without those assertions the test would pass by finishing normally and could
// not catch a regression in the bound at all.
func TestInitialSyncOnceStopsWhenRootContextEnds(t *testing.T) {
	t.Setenv("RELAYFILE_BOOTSTRAP_MAX_FILES_PER_CYCLE", "2")
	server, _ := budgetedBootstrapRelayWithDelay(t, 400, 20*time.Millisecond)
	localDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- runSinglePollingMount(ctx, onceMountConfig(t, server.URL, localDir)) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("cancelled --once must report an error, got nil")
		}
		var incomplete *initialBootstrapIncompleteError
		if !errors.As(err, &incomplete) {
			t.Fatalf("unexpected error type: %v", err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded cause, got %v", err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("--once did not stop after its root context was cancelled")
	}
	if ctx.Err() == nil {
		t.Fatal("--once returned before its root context was cancelled; this run did not exercise the cancellation bound")
	}
	if state := readBootstrapResumeState(localDir); !state.inProgress {
		t.Fatal("bootstrap completed inside the cancellation window; this run did not exercise the cancellation bound")
	}
}

// TestFinishInitialBootstrapReturnsOnCancelledContext pins the rootCtx branch
// deterministically, without depending on where a timeout happens to land: an
// already-cancelled context must stop the loop before it runs another cycle.
func TestFinishInitialBootstrapReturnsOnCancelledContext(t *testing.T) {
	localDir := bootstrapInProgressDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cycles := 0
	err := finishInitialBootstrap(ctx, mountConfig{localDir: localDir},
		func(bool) error { cycles++; return nil },
		func() error { return nil },
		false,
	)
	if err == nil {
		t.Fatalf("expected cancellation to produce an error")
	}
	var incomplete *initialBootstrapIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("unexpected error type for cancellation: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled cause, got %v", err)
	}
	if cycles != 0 {
		t.Errorf("ran %d cycles on an already-cancelled context, want 0", cycles)
	}
}

// TestFinishInitialBootstrapDoesNotRetryAFailedFirstCycle pins the pre-loop
// gate: when the cycle that ran before this function failed, --once keeps its
// historical single-attempt behavior so one transient cloud error cannot be
// escalated into a bootstrap stall.
func TestFinishInitialBootstrapDoesNotRetryAFailedFirstCycle(t *testing.T) {
	localDir := bootstrapInProgressDir(t)

	cycles := 0
	cause := errors.New("transient cloud error")
	err := finishInitialBootstrap(context.Background(), mountConfig{localDir: localDir},
		func(bool) error { cycles++; return nil },
		func() error { return cause },
		false,
	)
	if err == nil {
		t.Fatalf("expected error for failed first cycle")
	}
	var incomplete *initialBootstrapIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("unexpected error type: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("expected cause %v, got %v", cause, err)
	}
	if cycles != 0 {
		t.Errorf("ran %d resume cycles after a failed first cycle, want 0", cycles)
	}
}

// TestFinishInitialBootstrapStopsAfterAFailedResumeCycle covers the in-loop
// branch, which the pre-loop test above cannot reach: the loop is entered
// because the first cycle succeeded, and a resume cycle then fails. It must
// stop there rather than keep retrying a failing cycle.
func TestFinishInitialBootstrapStopsAfterAFailedResumeCycle(t *testing.T) {
	localDir := bootstrapInProgressDir(t)

	cycles := 0
	cause := errors.New("transient cloud error")
	err := finishInitialBootstrap(context.Background(), mountConfig{localDir: localDir},
		func(bool) error { cycles++; return nil },
		// nil for the pre-loop check, then an error once a resume cycle has run.
		func() error {
			if cycles == 0 {
				return nil
			}
			return cause
		},
		false,
	)
	if err == nil {
		t.Fatalf("expected error after failed resume cycle")
	}
	var incomplete *initialBootstrapIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("unexpected error type: %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("expected cause %v, got %v", cause, err)
	}
	if cycles != 1 {
		t.Errorf("ran %d resume cycles, want exactly 1 before the failure stopped the loop", cycles)
	}
}

// TestFinishInitialBootstrapResumesAfterInProgressTimeoutYield reproduces the
// low per-cycle timeout race: a non-traversal step (for example outbox or
// digest work) times out after the bootstrap has persisted its cursor. That
// timeout is a healthy yield while the checkpoint is in progress, so --once
// must keep resuming until a later stable cycle removes the checkpoint.
func TestFinishInitialBootstrapResumesAfterInProgressTimeoutYield(t *testing.T) {
	localDir := bootstrapInProgressDir(t)
	timeout := fmt.Errorf("non-traversal step: %w", context.DeadlineExceeded)
	cycles := 0
	err := finishInitialBootstrap(context.Background(), mountConfig{localDir: localDir},
		func(bool) error {
			cycles++
			switch cycles {
			case 1:
				writeBootstrapProgressState(t, localDir, 6, 100, 6)
			case 2:
				writeBootstrapCompleteState(t, localDir)
			}
			return nil
		},
		func() error {
			if cycles == 0 {
				return &cycleOutcomeError{cause: timeout, yielded: true}
			}
			return nil
		},
		false,
	)
	if err != nil {
		t.Fatalf("expected resumable timeout yield to continue to stable completion, got %v", err)
	}
	if cycles != 2 {
		t.Fatalf("ran %d resume cycles after a non-traversal timeout yield, want 2", cycles)
	}
}

// TestFinishInitialBootstrapDoesNotTreatFatalDeadlineAsYield keeps the other
// half of the distinction explicit: a deadline without a persisted-yield
// marker remains fatal and retains its typed error chain.
func TestFinishInitialBootstrapDoesNotTreatFatalDeadlineAsYield(t *testing.T) {
	localDir := bootstrapInProgressDir(t)
	cause := fmt.Errorf("non-traversal step failed: %w", context.DeadlineExceeded)
	err := finishInitialBootstrap(context.Background(), mountConfig{localDir: localDir},
		func(bool) error { return nil },
		func() error { return &cycleOutcomeError{cause: cause} },
		false,
	)
	if err == nil {
		t.Fatal("expected fatal deadline to stop the bootstrap attempt")
	}
	var incomplete *initialBootstrapIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("unexpected error type: %v", err)
	}
	if errors.Is(err, context.DeadlineExceeded) == false {
		t.Fatalf("fatal deadline lost its typed chain: %v", err)
	}
	if cycleYielded(cause) {
		t.Fatal("plain fatal cause was marked as a resumable yield")
	}
}

// TestFinishInitialBootstrapPrefersMidCycleCancellation pins the race where
// the root deadline fires inside run. Cancellation is the authoritative reason
// even if the cycle also records a transient provider failure before returning.
func TestFinishInitialBootstrapPrefersMidCycleCancellation(t *testing.T) {
	localDir := bootstrapInProgressDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	cause := errors.New("transient cloud error")

	cycles := 0
	err := finishInitialBootstrap(ctx, mountConfig{localDir: localDir},
		func(bool) error {
			cycles++
			cancel()
			return nil
		},
		func() error {
			if cycles == 0 {
				return nil
			}
			return cause
		},
		false,
	)
	if err == nil {
		t.Fatalf("expected cancellation to produce an error")
	}
	var incomplete *initialBootstrapIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("unexpected error type: %v", err)
	}
	if incomplete.reason != "context cancelled before bootstrap completed" {
		t.Fatalf("expected mid-cycle cancellation reason, got %q", incomplete.reason)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled cause, got %v", err)
	}
	if errors.Is(err, cause) {
		t.Fatalf("cancellation must take precedence over the concurrent cycle failure: %v", err)
	}
	if cycles != 1 {
		t.Fatalf("ran %d resume cycles, want exactly 1", cycles)
	}
}

// TestFinishInitialBootstrapPrefersTerminalCycleErrorOverConcurrentCancellation
// pins the other half of the mid-cycle race pinned above: when a resume
// cycle returns a terminal cycleErr (mountsync.IsBootstrapTerminalError,
// e.g. BootstrapStalledError) in the same cycle that also races a rootCtx
// cancellation, the terminal error is authoritative. It must propagate
// unwrapped -- not get demoted to a generic *initialBootstrapIncompleteError
// "context cancelled" message -- so a caller's errors.As match on the real,
// operator-actionable cause still succeeds.
func TestFinishInitialBootstrapPrefersTerminalCycleErrorOverConcurrentCancellation(t *testing.T) {
	localDir := bootstrapInProgressDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	terminalErr := &mountsync.BootstrapStalledError{Cycles: 3, Limit: 3, Path: "/"}

	cycles := 0
	err := finishInitialBootstrap(ctx, mountConfig{localDir: localDir},
		func(bool) error {
			cycles++
			cancel()
			return terminalErr
		},
		func() error { return nil },
		false,
	)
	if err == nil {
		t.Fatalf("expected the terminal cycle error to propagate")
	}
	var incomplete *initialBootstrapIncompleteError
	if errors.As(err, &incomplete) {
		t.Fatalf("terminal cycle error must propagate unwrapped, not as *initialBootstrapIncompleteError: %v", err)
	}
	if !errors.Is(err, terminalErr) {
		t.Fatalf("expected terminal error %v, got %v", terminalErr, err)
	}
	if !mountsync.IsBootstrapTerminalError(err) {
		t.Fatalf("expected err to still classify as a terminal bootstrap error: %v", err)
	}
	if cycles != 1 {
		t.Fatalf("ran %d resume cycles, want exactly 1", cycles)
	}
}

// bootstrapInProgressDir writes a public state with a non-null bootstrap block
// so finishInitialBootstrap enters its resume loop instead of returning early.
func bootstrapInProgressDir(t *testing.T) string {
	t.Helper()
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, ".relay"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "state.json"),
		[]byte(`{"bootstrap":{"phase":"bootstrapping","filesSynced":5,"filesTotal":100,"pageOffset":5}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return localDir
}

func writeBootstrapProgressState(t *testing.T, localDir string, synced, total, offset int) {
	t.Helper()
	payload := fmt.Sprintf(`{"bootstrap":{"phase":"bootstrapping","filesSynced":%d,"filesTotal":%d,"pageOffset":%d}}`, synced, total, offset)
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "state.json"), []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bootstrapAlreadyCompleteDir writes a public state with no bootstrap block
// and a non-empty lastSuccessfulReconcileAt, mirroring a mount whose full-tree
// bootstrap already finished in a previous process (see bootstrapAlreadyComplete).
func bootstrapAlreadyCompleteDir(t *testing.T) string {
	t.Helper()
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, ".relay"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "state.json"),
		[]byte(`{"status":"ready","lastSuccessfulReconcileAt":"2026-09-08T00:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return localDir
}

// writeBootstrapCompleteState overwrites localDir's public state with a
// completed-bootstrap snapshot, simulating a resume cycle that finishes the
// tree walk.
func writeBootstrapCompleteState(t *testing.T, localDir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "state.json"),
		[]byte(`{"status":"ready","lastSuccessfulReconcileAt":"2026-09-08T00:00:05Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestFinishInitialBootstrapKeepsSuccessAfterPriorCompletion pins the fix for
// the false-failure counterpart of relayfile#455: a mount whose bootstrap
// already finished before this process ran its first cycle must not be
// reported as an incomplete bootstrap just because that unrelated cycle hit
// a transient error or a cancelled root context.
func TestFinishInitialBootstrapKeepsSuccessAfterPriorCompletion(t *testing.T) {
	t.Run("unrelated cycle failure", func(t *testing.T) {
		localDir := bootstrapAlreadyCompleteDir(t)
		cycles := 0
		err := finishInitialBootstrap(context.Background(), mountConfig{localDir: localDir},
			func(bool) error { cycles++; return nil },
			func() error { return errors.New("transient cloud error unrelated to bootstrap") },
			true,
		)
		if err != nil {
			t.Fatalf("expected success for an already-complete checkpoint despite an unrelated cycle failure, got %v", err)
		}
		if cycles != 0 {
			t.Errorf("ran %d resume cycles for an already-complete checkpoint, want 0", cycles)
		}
	})

	t.Run("cancelled root context", func(t *testing.T) {
		localDir := bootstrapAlreadyCompleteDir(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cycles := 0
		err := finishInitialBootstrap(ctx, mountConfig{localDir: localDir},
			func(bool) error { cycles++; return nil },
			func() error { return nil },
			true,
		)
		if err != nil {
			t.Fatalf("expected success for an already-complete checkpoint despite a cancelled root context, got %v", err)
		}
		if cycles != 0 {
			t.Errorf("ran %d resume cycles for an already-complete checkpoint, want 0", cycles)
		}
	})

	t.Run("not already bootstrapped still fails", func(t *testing.T) {
		// Control: the same failure, without alreadyBootstrapped, must still
		// be reported -- the guard must not become unconditional.
		localDir := bootstrapAlreadyCompleteDir(t)
		cause := errors.New("transient cloud error unrelated to bootstrap")
		err := finishInitialBootstrap(context.Background(), mountConfig{localDir: localDir},
			func(bool) error { return nil },
			func() error { return cause },
			false,
		)
		if err == nil {
			t.Fatalf("expected error when alreadyBootstrapped is false, got nil")
		}
		if !errors.Is(err, cause) {
			t.Fatalf("expected cause %v, got %v", cause, err)
		}
	})

	t.Run("forceFullRecon still fails despite prior completion", func(t *testing.T) {
		// Control: cfg.forceFullRecon disables the alreadyBootstrapped
		// shortcut -- --full-reconcile is an explicit request for THIS
		// cycle to succeed, and a checkpoint that merely predates this
		// process must not exempt it from a real cycle failure.
		localDir := bootstrapAlreadyCompleteDir(t)
		cause := errors.New("provider tree request failed")
		cycles := 0
		err := finishInitialBootstrap(context.Background(), mountConfig{localDir: localDir, forceFullRecon: true},
			func(bool) error { cycles++; return nil },
			func() error { return cause },
			true,
		)
		if err == nil {
			t.Fatalf("expected error when cfg.forceFullRecon is set despite an already-complete checkpoint, got nil")
		}
		if !errors.Is(err, cause) {
			t.Fatalf("expected cause %v, got %v", cause, err)
		}
		// This is finishInitialBootstrap's own first-cycle check, not a
		// resume cycle: run itself is called by the caller (runSinglePollingMount)
		// before finishInitialBootstrap, so no additional resume cycles run here.
		if cycles != 0 {
			t.Errorf("ran %d resume cycles for a checkpoint that read as already complete, want 0", cycles)
		}
	})
}

// TestFinishInitialBootstrapPrefersFreshCompletionOverPreLoopCancellation pins
// the pre-loop counterpart of the mid-loop race already covered by
// TestFinishInitialBootstrapPrefersCompletionOverMidCycleSignals: a SIGTERM
// or other rootCtx cancellation landing exactly as the caller's own first
// cycle finishes a *fresh* bootstrap (not alreadyBootstrapped -- the
// checkpoint was still in progress, or nonexistent, before this process's
// first cycle ran) must not turn that genuine completion into a reported
// failure. The on-disk checkpoint, not the concurrently observed
// cancellation, is authoritative for whether the first cycle succeeded.
func TestFinishInitialBootstrapPrefersFreshCompletionOverPreLoopCancellation(t *testing.T) {
	t.Run("cancellation lands as the first cycle completes", func(t *testing.T) {
		// The state on disk already reads complete -- as it would immediately
		// after the caller's own first cycle (run before finishInitialBootstrap
		// is invoked) persisted a finished checkpoint -- while rootCtx is
		// already cancelled, reproducing a SIGTERM racing that same return.
		localDir := bootstrapAlreadyCompleteDir(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		cycles := 0
		err := finishInitialBootstrap(ctx, mountConfig{localDir: localDir},
			func(bool) error { cycles++; return nil },
			func() error { return nil }, // the first cycle itself succeeded outright
			false,                       // NOT already complete before this process's first cycle ran
		)
		if err != nil {
			t.Fatalf("expected success when the first cycle itself completed the bootstrap despite a concurrent cancellation, got %v", err)
		}
		if cycles != 0 {
			t.Errorf("ran %d resume cycles for a checkpoint that already read complete, want 0", cycles)
		}
	})

	t.Run("real failure still reported despite the same cancellation", func(t *testing.T) {
		// Control: the checkpoint reads not-in-progress because the first
		// cycle failed outright before any bootstrap could start, not
		// because it completed one. That must still be reported, even with
		// rootCtx cancelled the same way -- completion, not cancellation, is
		// what the fix gives precedence to.
		localDir := bootstrapAlreadyCompleteDir(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		cause := errors.New("transient cloud error")

		err := finishInitialBootstrap(ctx, mountConfig{localDir: localDir},
			func(bool) error { return nil },
			func() error { return cause },
			false,
		)
		if err == nil {
			t.Fatalf("expected error for a genuinely failed first cycle, got nil")
		}
		if !errors.Is(err, cause) {
			t.Fatalf("expected cause %v, got %v", cause, err)
		}
	})
}

// TestFinishInitialBootstrapPrefersCompletionOverMidCycleSignals pins the
// in-loop counterpart: when a resume cycle actually finishes the persisted
// checkpoint, a rootCtx cancellation or an unrelated lastCycleErr recorded by
// that same cycle must not turn the completed bootstrap into a reported
// failure. The checkpoint (read immediately after the cycle returns) is the
// authoritative signal, checked before either.
func TestFinishInitialBootstrapPrefersCompletionOverMidCycleSignals(t *testing.T) {
	t.Run("mid-cycle cancellation", func(t *testing.T) {
		localDir := bootstrapInProgressDir(t)
		ctx, cancel := context.WithCancel(context.Background())
		cycles := 0
		err := finishInitialBootstrap(ctx, mountConfig{localDir: localDir},
			func(bool) error {
				cycles++
				writeBootstrapCompleteState(t, localDir)
				cancel()
				return nil
			},
			func() error { return nil },
			false,
		)
		if err != nil {
			t.Fatalf("expected success when the cycle that raced cancellation finished the checkpoint, got %v", err)
		}
		if cycles != 1 {
			t.Fatalf("ran %d resume cycles, want exactly 1", cycles)
		}
	})

	t.Run("mid-cycle unrelated lastCycleErr", func(t *testing.T) {
		localDir := bootstrapInProgressDir(t)
		cause := errors.New("transient cloud error unrelated to bootstrap")
		cycles := 0
		err := finishInitialBootstrap(context.Background(), mountConfig{localDir: localDir},
			func(bool) error {
				cycles++
				writeBootstrapCompleteState(t, localDir)
				return nil
			},
			// nil for the pre-loop check (mirrors the caller's already-run
			// first cycle succeeding), then the unrelated error once the
			// resume cycle above has actually run.
			func() error {
				if cycles == 0 {
					return nil
				}
				return cause
			},
			false,
		)
		if err != nil {
			t.Fatalf("expected success when the cycle that recorded an unrelated lastCycleErr finished the checkpoint, got %v", err)
		}
		if cycles != 1 {
			t.Fatalf("ran %d resume cycles, want exactly 1", cycles)
		}
	})
}

// TestFinishInitialBootstrapStopsWhenCheckpointStopsAdvancing pins the
// no-progress bound: a cycle that keeps succeeding without moving any
// resumable coordinate must not spin to the cycle ceiling.
func TestFinishInitialBootstrapStopsWhenCheckpointStopsAdvancing(t *testing.T) {
	localDir := bootstrapInProgressDir(t)

	cycles := 0
	err := finishInitialBootstrap(context.Background(), mountConfig{localDir: localDir},
		func(bool) error { cycles++; return nil }, // never advances the checkpoint
		func() error { return nil },
		false,
	)
	if err == nil {
		t.Fatalf("expected error when checkpoint stops advancing")
	}
	var incomplete *initialBootstrapIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("unexpected error type: %v", err)
	}
	if incomplete.reason != "bootstrap checkpoint stopped advancing" {
		t.Fatalf("expected stalled reason, got %q", incomplete.reason)
	}
	if cycles != onceBootstrapStableCycleLimit {
		t.Errorf("ran %d cycles on a stalled checkpoint, want %d", cycles, onceBootstrapStableCycleLimit)
	}
}

// TestInitialSyncAtProductionBudget runs the readiness probe at the condition
// that made relayfile#455 structural: a workspace larger than
// defaultBootstrapMaxFilesPerCycle (2000) with no budget override, so one
// --once cycle cannot mirror it.
func TestInitialSyncAtProductionBudget(t *testing.T) {
	server, _ := budgetedBootstrapRelay(t, 2500)
	localDir := t.TempDir()

	err := runSinglePollingMount(context.Background(), onceMountConfig(t, server.URL, localDir))
	if err != nil {
		t.Fatalf("mount --once returned an error: %v", err)
	}
	statePath := filepath.Join(localDir, ".relay", "state.json")
	logPublicState(t, statePath)
	ready, reason := sandboxInitialSyncGuard(statePath)
	t.Logf("PRODUCTION-BUDGET RESULT: --once exit=0 guard_ready=%v reason=%q => sandbox exit code %d",
		ready, reason, map[bool]int{true: 0, false: 75}[ready])
	if !ready {
		t.Fatalf("sandbox readiness guard failed (exit 75): %s", reason)
	}
}
