package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	// rand.Float64() is almost never below 150ms/5m; retry the rare draw that
	// is, so the assertion never flakes.
	for attempt := 0; attempt < 3; attempt++ {
		requests.Store(0)
		err := runSinglePollingMount(ctx, cfg)
		if err == nil {
			ctx, cancel = context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			continue
		}
		if got := requests.Load(); got != 0 {
			t.Fatalf("mount made %d request(s) during the startup splay, want 0", got)
		}
		return
	}
	t.Fatal("startup splay never delayed the first cycle")
}

func TestWaitStartupSplayZeroDelayRunsImmediately(t *testing.T) {
	start := time.Now()
	if err := waitStartupSplay(context.Background(), 0); err != nil {
		t.Fatalf("zero splay: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("zero splay waited %s", elapsed)
	}
}
