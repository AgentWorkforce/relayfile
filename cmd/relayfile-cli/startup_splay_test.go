package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

// `relayfile mount` runs its own loop, not relayfile-mount's. Its first
// (possibly full-tree) cycle must also wait out the startup splay, or every
// scheduled sandbox and every scoped runner bootstraps in the same second.
func TestPublicMountLoopWaitsStartupSplayBeforeFirstRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
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
	previous := startupSplaySample
	startupSplaySample = func() float64 { return 0.5 } // 2.5m of a 5m window
	t.Cleanup(func() { startupSplaySample = previous })
	prevLog := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(prevLog)

	localDir := filepath.Join(t.TempDir(), "mount")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	disableWebSocket := false
	syncer, err := mountsync.NewSyncer(mountsync.NewHTTPClient(server.URL, "test-token", server.Client()), mountsync.SyncerOptions{
		WorkspaceID: "ws_public_startup_splay", RemoteRoot: "/", LocalRoot: localDir,
		Interval: time.Hour, WebSocket: &disableWebSocket, RootCtx: ctx, Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	err = runMountLoopWithAuthLock(ctx, syncer, localDir, "ws_public_startup_splay", server.URL, "",
		time.Second, time.Hour, 0, maxStartupJitter, false, true, false,
		mountPIDFile(localDir), mountLogFile(localDir), &sync.Mutex{})
	if err == nil {
		t.Fatal("one-shot public mount returned success while its startup splay outlasted the deadline")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("public mount made %d request(s) during the startup splay, want 0", got)
	}
}

func TestPublicMountStartupSplayDelayBounds(t *testing.T) {
	if got := startupSplayDelay(0, 0.9); got != 0 {
		t.Fatalf("disabled splay = %s, want 0", got)
	}
	if got := startupSplayDelay(10*time.Second, 0.5); got != 5*time.Second {
		t.Fatalf("half sample = %s, want 5s", got)
	}
	if got := startupSplayDelay(time.Hour, 0.999); got >= maxStartupJitter {
		t.Fatalf("oversized splay = %s, want < %s", got, maxStartupJitter)
	}
}
