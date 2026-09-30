package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountlease"
	"github.com/agentworkforce/relayfile/internal/mountsync"
)

func TestStartupSplayDelayBounds(t *testing.T) {
	if got := startupSplayDelay(0, 0.9); got != 0 {
		t.Fatalf("disabled splay = %s, want 0", got)
	}
	if got := startupSplayDelay(-time.Second, 0.9); got != 0 {
		t.Fatalf("negative splay = %s, want 0", got)
	}
	if got := startupSplayDelay(10*time.Second, 0.5); got != 5*time.Second {
		t.Fatalf("half sample = %s, want 5s", got)
	}
	if got := startupSplayDelay(10*time.Second, 1); got >= 10*time.Second {
		t.Fatalf("sample 1 = %s, want < 10s", got)
	}
	if got := startupSplayDelay(time.Hour, 0.999); got >= maxStartupJitter {
		t.Fatalf("oversized splay = %s, want < %s", got, maxStartupJitter)
	}
}

// The first cycle of a mount (a full-tree bootstrap for a fresh sandbox) must
// wait out the startup splay before touching the workspace, so mounts
// launched in the same instant do not bootstrap in lockstep.
func TestRunSinglePollingMountWaitsStartupSplayBeforeFirstRequest(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch {
		case strings.Contains(r.URL.Path, "/fs/tree"):
			_, _ = w.Write([]byte(`{"path":"/","entries":[]}`))
		case strings.Contains(r.URL.Path, "/fs/events"):
			_, _ = w.Write([]byte(`{"events":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := mountConfig{
		baseURL:       server.URL,
		token:         "test-token",
		workspaceID:   "ws_startup_splay",
		remotePath:    "/",
		localDir:      t.TempDir(),
		stateDir:      t.TempDir(),
		mountKind:     mountsync.MountKindDaemon,
		syncMode:      syncModeMirror,
		interval:      time.Hour,
		timeout:       time.Second,
		startupJitter: maxStartupJitter,
		once:          true,
	}
	// A pinned sample (2.5m of a 5m window) outlasts the 150ms deadline, so
	// the mount must still be waiting when the context expires.
	pinStartupSplaySample(t, 0.5)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := runSinglePollingMount(ctx, cfg)
	if err == nil {
		t.Fatal("one-shot mount returned success while its startup splay outlasted the deadline")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("mount made %d request(s) during the startup splay, want 0", got)
	}
}

// pinStartupSplaySample fixes the splay draw for one test.
func pinStartupSplaySample(t *testing.T, sample float64) {
	t.Helper()
	previous := startupSplaySample
	startupSplaySample = func() float64 { return sample }
	t.Cleanup(func() { startupSplaySample = previous })
}

func TestWaitStartupSplayZeroDelayRunsImmediately(t *testing.T) {
	start := time.Now()
	if _, err := waitStartupSplay(context.Background(), 0, nil); err != nil {
		t.Fatalf("zero splay: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("zero splay waited %s", elapsed)
	}
}

// A `--notify-flush` against a daemon still inside its startup splay must be
// serviced, not left queued until the splay ends: the notifier gives up after
// notifyFlushWait (2x the cycle timeout), far below the up-to-5m splay.
func TestFlushRequestDuringStartupSplayIsAcknowledgedPromptly(t *testing.T) {
	cacheHome := t.TempDir()
	t.Setenv("HOME", cacheHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/fs/tree"):
			_, _ = w.Write([]byte(`{"path":"/","entries":[]}`))
		case strings.Contains(r.URL.Path, "/fs/events"):
			_, _ = w.Write([]byte(`{"events":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	pinStartupSplaySample(t, 0.5) // 2.5m splay: far past this test's deadline

	flushReq := make(chan struct{}, 1)
	localDir := t.TempDir()
	cfg := mountConfig{
		baseURL:       server.URL,
		token:         "test-token",
		workspaceID:   "ws_startup_splay_flush",
		remotePath:    "/",
		localDir:      localDir,
		stateDir:      t.TempDir(),
		mountKind:     mountsync.MountKindDaemon,
		mode:          mountModePoll,
		syncMode:      syncModeMirror,
		interval:      time.Hour,
		timeout:       2 * time.Second,
		startupJitter: maxStartupJitter,
		flushReq:      flushReq,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runSinglePollingMount(ctx, cfg) }()
	defer func() { cancel(); <-done }()

	flushReq <- struct{}{}
	deadline := time.Now().Add(notifyFlushWait(cfg))
	for time.Now().Before(deadline) {
		ack, err := mountlease.ReadFlushAck(cfg.baseURL, cfg.workspaceID, localDir)
		if err == nil && ack.Seq > 0 {
			if !ack.OK {
				t.Fatalf("flush ack recorded a failure: %s", ack.Error)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("flush request during the startup splay was not acknowledged within %s", notifyFlushWait(cfg))
}
