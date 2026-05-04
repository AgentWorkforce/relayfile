package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestA10InitialSyncGateExitsZeroOnReady covers productized cloud-mount
// contract A10 (positive path): the cloud reports `cataloging` while the
// initial sync runs, the CLI polls until the provider transitions to
// `ready`, and `waitForInitialSync` returns nil so setup exits 0.
func TestA10InitialSyncGateExitsZeroOnReady(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/ws_demo/sync/status":
			n := atomic.AddInt32(&calls, 1)
			if n == 1 {
				_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"cataloging","lagSeconds":120,"watermarkTs":"2026-05-02T18:00:00Z"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"ready","lagSeconds":2,"watermarkTs":"2026-05-02T18:00:05Z"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := waitForInitialSync(server.URL, "tok", "ws_demo", "notion", localDir, 10*time.Second, &stdout); err != nil {
		t.Fatalf("waitForInitialSync failed: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Fatalf("expected at least 2 sync status polls, got %d", got)
	}
}

// TestA10InitialSyncGateExitsZeroOnTimeout covers productized cloud-mount
// contract A10 (timeout path): when the configured deadline elapses with
// the provider still in `cataloging`, `waitForInitialSync` MUST exit 0
// with the resume hint so the workspace and mount stay usable while sync
// catches up in the background.
func TestA10InitialSyncGateExitsZeroOnTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"cataloging","lagSeconds":120,"watermarkTs":"2026-05-02T18:00:00Z"}]}`))
	}))
	defer server.Close()

	var stdout bytes.Buffer
	start := time.Now()
	if err := waitForInitialSync(server.URL, "tok", "ws_demo", "notion", localDir, 100*time.Millisecond, &stdout); err != nil {
		t.Fatalf("expected nil error on timeout, got: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("expected timeout to surface within 5s, took %s", elapsed)
	}
	got := stdout.String()
	if !strings.Contains(got, "notion still syncing in the background") {
		t.Fatalf("expected resume hint, got: %q", got)
	}
}
