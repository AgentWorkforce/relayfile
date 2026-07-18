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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

func TestFloatEnvParsesValue(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_FLOAT", "0.35")
	got := floatEnv("RELAYFILE_TEST_FLOAT", 0.1)
	if got != 0.35 {
		t.Fatalf("expected 0.35, got %f", got)
	}
}

func TestFloatEnvFallsBackOnInvalid(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_FLOAT_BAD", "oops")
	got := floatEnv("RELAYFILE_TEST_FLOAT_BAD", 0.25)
	if got != 0.25 {
		t.Fatalf("expected fallback 0.25, got %f", got)
	}
}

func TestLazyReposEnvDefaultsFalse(t *testing.T) {
	t.Setenv("RELAYFILE_LAZY_REPOS", "")
	t.Setenv("RELAYFILE_MOUNT_LAZY_GITHUB_REPOS", "")

	if lazyReposEnv() {
		t.Fatal("expected lazy repos to default false")
	}
}

func TestLazyReposEnvParsesOptIn(t *testing.T) {
	t.Setenv("RELAYFILE_LAZY_REPOS", "true")
	t.Setenv("RELAYFILE_MOUNT_LAZY_GITHUB_REPOS", "")

	if !lazyReposEnv() {
		t.Fatal("expected RELAYFILE_LAZY_REPOS=true to opt in")
	}
}

func TestLazyReposEnvSupportsLegacyName(t *testing.T) {
	t.Setenv("RELAYFILE_LAZY_REPOS", "")
	t.Setenv("RELAYFILE_MOUNT_LAZY_GITHUB_REPOS", "true")

	if !lazyReposEnv() {
		t.Fatal("expected legacy lazy repos env var to opt in")
	}
}

func TestClampJitterRatio(t *testing.T) {
	if got := clampJitterRatio(-0.1); got != 0 {
		t.Fatalf("expected clamp to 0, got %f", got)
	}
	if got := clampJitterRatio(1.5); got != 1 {
		t.Fatalf("expected clamp to 1, got %f", got)
	}
	if got := clampJitterRatio(0.4); got != 0.4 {
		t.Fatalf("expected passthrough 0.4, got %f", got)
	}
}

func TestJitteredIntervalWithSample(t *testing.T) {
	base := 10 * time.Second
	if got := jitteredIntervalWithSample(base, 0, 0.2); got != base {
		t.Fatalf("expected no jitter interval %s, got %s", base, got)
	}
	if got := jitteredIntervalWithSample(base, 0.2, 0); got != 8*time.Second {
		t.Fatalf("expected min jitter interval 8s, got %s", got)
	}
	if got := jitteredIntervalWithSample(base, 0.2, 0.5); got != 10*time.Second {
		t.Fatalf("expected midpoint jitter interval 10s, got %s", got)
	}
	if got := jitteredIntervalWithSample(base, 0.2, 1); got != 12*time.Second {
		t.Fatalf("expected max jitter interval 12s, got %s", got)
	}
}

func TestEnforcePollIntervalFloor(t *testing.T) {
	if got := enforcePollIntervalFloor(time.Second); got != minMountPollInterval {
		t.Fatalf("expected interval floor %s, got %s", minMountPollInterval, got)
	}
	if got := enforcePollIntervalFloor(30 * time.Second); got != 30*time.Second {
		t.Fatalf("expected long interval passthrough, got %s", got)
	}
	if got := jitteredIntervalWithSample(minMountPollInterval, 0.2, 0); got != minMountPollInterval {
		t.Fatalf("expected jittered interval floor %s, got %s", minMountPollInterval, got)
	}
	if got := jitteredIntervalWithSample(time.Second, 0, 0.5); got != minMountPollInterval {
		t.Fatalf("expected non-jittered interval floor %s, got %s", minMountPollInterval, got)
	}
}

func TestWebSocketMaintenanceDoesNotLowerReconcileCadence(t *testing.T) {
	for cycle := 1; cycle < websocketReconcileEvery; cycle++ {
		if shouldReconcileMountCycle(true, cycle) {
			t.Fatalf("websocket-enabled cycle %d reconciled before cadence floor", cycle)
		}
	}
	if !shouldReconcileMountCycle(true, websocketReconcileEvery) {
		t.Fatalf("expected websocket-enabled cycle %d to reconcile", websocketReconcileEvery)
	}
	for cycle := 1; cycle <= websocketReconcileEvery; cycle++ {
		if !shouldReconcileMountCycle(false, cycle) {
			t.Fatalf("expected websocket-disabled cycle %d to reconcile", cycle)
		}
	}
}

func TestWriteOnlyMountDisablesWebSocketCadence(t *testing.T) {
	cfg := mountConfig{websocketEnabled: true, syncMode: syncModeWriteOnly}
	if mountWebSocketEnabled(cfg) {
		t.Fatal("write-only mount should not maintain websocket connections")
	}
	if !shouldReconcileMountCycle(mountWebSocketEnabled(cfg), 1) {
		t.Fatal("write-only mount should keep regular reconcile cadence")
	}
}

func TestWatcherUnavailableDisablesWebSocketReconcileCadence(t *testing.T) {
	cfg := mountConfig{websocketEnabled: true, syncMode: syncModeMirror}
	if !mountReconcileUsesWebSocketCadence(cfg, true) {
		t.Fatal("active watcher should allow websocket reconcile cadence")
	}
	if mountReconcileUsesWebSocketCadence(cfg, false) {
		t.Fatal("missing watcher must keep regular reconcile cadence for local scans")
	}
}

func TestResolveMountMode(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		fuse    bool
		want    string
		wantErr bool
	}{
		{name: "default empty mode uses poll", want: mountModePoll},
		{name: "explicit poll", mode: "poll", want: mountModePoll},
		{name: "explicit fuse", mode: "fuse", want: mountModeFuse},
		{name: "case and whitespace normalized", mode: " FUSE ", want: mountModeFuse},
		{name: "fuse flag overrides mode", mode: "poll", fuse: true, want: mountModeFuse},
		{name: "invalid mode errors", mode: "sync", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveMountMode(tc.mode, tc.fuse)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got mode %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMountMode returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected mode %q, got %q", tc.want, got)
			}
		})
	}
}

func TestResolveLocalLayout(t *testing.T) {
	tests := []struct {
		name    string
		layout  string
		want    string
		wantErr bool
	}{
		{name: "default empty layout uses exact", want: localLayoutExact},
		{name: "explicit exact", layout: "exact", want: localLayoutExact},
		{name: "explicit scoped", layout: "scoped", want: localLayoutScoped},
		{name: "case and whitespace normalized", layout: " SCOPED ", want: localLayoutScoped},
		{name: "invalid layout errors", layout: "auto", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveLocalLayout(tc.layout)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got layout %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveLocalLayout returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected layout %q, got %q", tc.want, got)
			}
		})
	}
}

func TestResolveSyncMode(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		want    string
		wantErr bool
	}{
		{name: "default empty sync mode uses mirror", want: syncModeMirror},
		{name: "explicit mirror", mode: "mirror", want: syncModeMirror},
		{name: "explicit write-only", mode: "write-only", want: syncModeWriteOnly},
		{name: "case and whitespace normalized", mode: " WRITE-ONLY ", want: syncModeWriteOnly},
		{name: "invalid sync mode errors", mode: "push", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSyncMode(tc.mode)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got sync mode %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSyncMode returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected sync mode %q, got %q", tc.want, got)
			}
		})
	}
}

func TestExecuteMountDispatchesPollMode(t *testing.T) {
	cfg := mountConfig{mode: mountModePoll}
	pollCalled := false
	fuseCalled := false

	err := executeMount(context.Background(), cfg,
		func(context.Context, mountConfig) error {
			pollCalled = true
			return nil
		},
		func(context.Context, mountConfig) error {
			fuseCalled = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("executeMount returned error: %v", err)
	}
	if !pollCalled {
		t.Fatal("expected poll runner to be called")
	}
	if fuseCalled {
		t.Fatal("did not expect fuse runner to be called")
	}
}

func TestExecuteMountDispatchesFuseMode(t *testing.T) {
	cfg := mountConfig{mode: mountModeFuse}
	pollCalled := false
	fuseCalled := false

	err := executeMount(context.Background(), cfg,
		func(context.Context, mountConfig) error {
			pollCalled = true
			return nil
		},
		func(context.Context, mountConfig) error {
			fuseCalled = true
			return nil
		},
	)
	if err != nil {
		t.Fatalf("executeMount returned error: %v", err)
	}
	if !fuseCalled {
		t.Fatal("expected fuse runner to be called")
	}
	if pollCalled {
		t.Fatal("did not expect poll runner to be called")
	}
}

func TestExecuteMountReturnsRunnerError(t *testing.T) {
	wantErr := errors.New("boom")
	cfg := mountConfig{mode: mountModeFuse}

	err := executeMount(context.Background(), cfg,
		func(context.Context, mountConfig) error { return nil },
		func(context.Context, mountConfig) error { return wantErr },
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v, got %v", wantErr, err)
	}
}

func TestExecuteMountRejectsUnsupportedMode(t *testing.T) {
	err := executeMount(context.Background(), mountConfig{mode: "invalid"},
		func(context.Context, mountConfig) error { return nil },
		func(context.Context, mountConfig) error { return nil },
	)
	if err == nil {
		t.Fatal("expected unsupported mode error")
	}
}

func TestReadMountCredsTokenSupportsAdvisoryFields(t *testing.T) {
	credsFile := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(credsFile, []byte(`{
		"token": " relay_pa_new ",
		"mintedAt": "2026-06-06T14:00:00Z",
		"expiresAt": null
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	token, err := readMountCredsToken(credsFile)
	if err != nil {
		t.Fatalf("read creds token: %v", err)
	}
	if token != "relay_pa_new" {
		t.Fatalf("expected trimmed token, got %q", token)
	}
}

func TestReadMountCredsTokenRejectsMissingToken(t *testing.T) {
	credsFile := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(credsFile, []byte(`{"mintedAt":"2026-06-06T14:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := readMountCredsToken(credsFile); err == nil || !strings.Contains(err.Error(), "missing token") {
		t.Fatalf("expected missing-token error, got %v", err)
	}
}

func TestInstallCredsFileRefreshReloadsChangedToken(t *testing.T) {
	credsFile := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(credsFile, []byte(`{"token":"new-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		switch call {
		case 1:
			if got := r.Header.Get("Authorization"); got != "Bearer old-token" {
				t.Fatalf("expected first request to use old token, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized","message":"Token has expired"}`))
		case 2:
			if got := r.Header.Get("Authorization"); got != "Bearer new-token" {
				t.Fatalf("expected retry to use creds-file token, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"path":"/slack","entries":[],"nextCursor":null}`))
		default:
			t.Fatalf("unexpected call %d", call)
		}
	}))
	defer server.Close()

	client := mountsync.NewHTTPClient(server.URL, "old-token", server.Client())
	installCredsFileRefresh(client, mountConfig{credsFile: credsFile})

	if _, err := client.ListTree(context.Background(), "ws_auth", "/slack", 1, ""); err != nil {
		t.Fatalf("expected creds-file refresh to recover request: %v", err)
	}
	if got := client.Token(); got != "new-token" {
		t.Fatalf("expected client token to update, got %q", got)
	}
}

func TestInstallCredsFileRefreshToleratesParseFailureWithoutRetry(t *testing.T) {
	credsFile := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(credsFile, []byte(`{"token":`), 0o600); err != nil {
		t.Fatal(err)
	}

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthorized","message":"Token has expired"}`))
	}))
	defer server.Close()

	client := mountsync.NewHTTPClient(server.URL, "old-token", server.Client())
	installCredsFileRefresh(client, mountConfig{credsFile: credsFile})

	_, err := client.ListTree(context.Background(), "ws_auth", "/slack", 1, "")
	var httpErr *mountsync.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected original unauthorized error, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected no retry after parse failure, got %d calls", got)
	}
	if got := client.Token(); got != "old-token" {
		t.Fatalf("expected old token to stay installed, got %q", got)
	}
}

// TestRunSinglePollingMountStopsOnBootstrapStall proves the typed hard failure
// leaves the polling runner immediately. main turns this returned error into a
// nonzero process exit, so the ticker cannot retry the same checkpoint.
func TestRunSinglePollingMountStopsOnBootstrapStall(t *testing.T) {
	t.Setenv("RELAYFILE_BOOTSTRAP_STALL_CYCLES", "1")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "stuck", http.StatusBadGateway)
	}))
	defer server.Close()

	err := runSinglePollingMount(context.Background(), mountConfig{
		baseURL:          server.URL,
		token:            "test-token",
		workspaceID:      "ws_bootstrap_stall",
		remotePath:       "/",
		localDir:         t.TempDir(),
		stateDir:         t.TempDir(),
		mountKind:        mountsync.MountKindDaemon,
		syncMode:         syncModeMirror,
		interval:         time.Hour,
		timeout:          time.Second,
		websocketEnabled: false,
	})
	var stalled *mountsync.BootstrapStalledError
	if !errors.As(err, &stalled) {
		t.Fatalf("expected bootstrap stall to escape polling runner, got %v", err)
	}
	if got := calls.Load(); got == 0 {
		t.Fatal("expected initial full-tree request before runner exited")
	}
}

// TestRunSinglePollingMountKeepsNormalCycleFailuresNonFatal verifies that a
// normal cloud error keeps its historical per-cycle retry behavior rather
// than terminating the mount runner.
func TestRunSinglePollingMountKeepsNormalCycleFailuresNonFatal(t *testing.T) {
	t.Setenv("RELAYFILE_BOOTSTRAP_STALL_CYCLES", "2")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "transient", http.StatusBadGateway)
	}))
	defer server.Close()

	err := runSinglePollingMount(context.Background(), mountConfig{
		baseURL:          server.URL,
		token:            "test-token",
		workspaceID:      "ws_normal_retry",
		remotePath:       "/",
		localDir:         t.TempDir(),
		stateDir:         t.TempDir(),
		mountKind:        mountsync.MountKindDaemon,
		syncMode:         syncModeMirror,
		interval:         time.Hour,
		timeout:          time.Second,
		websocketEnabled: false,
		once:             true,
	})
	if err != nil {
		t.Fatalf("normal cycle failure must remain nonfatal to the runner, got %v", err)
	}
}

// TestRunSinglePollingMountStopsOnTimerBootstrapStall exercises the polling
// timer path, not just the initial cycle. The first page commits a partial
// checkpoint and its next-page error remains nonfatal; the following timer
// reconcile fails at that unchanged cursor and terminates the runner.
func TestRunSinglePollingMountStopsOnTimerBootstrapStall(t *testing.T) {
	t.Setenv("RELAYFILE_BOOTSTRAP_STALL_CYCLES", "1")
	var rootTreeCalls atomic.Int32
	var cursorTreeCalls atomic.Int32
	var readCalls atomic.Int32
	entries := make([]mountsync.TreeEntry, 0, 10)
	for i := 0; i < 10; i++ {
		entries = append(entries, mountsync.TreeEntry{Path: fmt.Sprintf("/f/%05d.txt", i), Type: "file"})
	}
	nextCursor := entries[len(entries)-1].Path
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/fs/tree"):
			if r.URL.Query().Get("cursor") != "" {
				cursorTreeCalls.Add(1)
				// A 400 is intentionally not retried by HTTPClient, so exactly
				// two calls prove the initial nonfatal cycle plus one timer cycle.
				http.Error(w, "stuck cursor", http.StatusBadRequest)
				return
			}
			rootTreeCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(mountsync.TreeResponse{Entries: entries, NextCursor: &nextCursor})
		case strings.Contains(r.URL.Path, "/fs/file"):
			readCalls.Add(1)
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
	defer server.Close()

	started := time.Now()
	err := runSinglePollingMount(context.Background(), mountConfig{
		baseURL:          server.URL,
		token:            "test-token",
		workspaceID:      "ws_timer_bootstrap_stall",
		remotePath:       "/",
		localDir:         t.TempDir(),
		stateDir:         t.TempDir(),
		mountKind:        mountsync.MountKindDaemon,
		syncMode:         syncModeMirror,
		interval:         time.Millisecond, // enforced to the 5s poll floor
		timeout:          time.Second,
		websocketEnabled: false,
	})
	var stalled *mountsync.BootstrapStalledError
	if !errors.As(err, &stalled) {
		t.Fatalf("expected timer-path bootstrap stall to escape runner, got %v", err)
	}
	if got := rootTreeCalls.Load(); got != 1 {
		t.Fatalf("root tree calls = %d, want one initial partial traversal", got)
	}
	if got := readCalls.Load(); got != int32(len(entries)) {
		t.Fatalf("ReadFile calls = %d, want %d first-page files", got, len(entries))
	}
	if got := cursorTreeCalls.Load(); got != 2 {
		t.Fatalf("cursor tree calls = %d, want initial nonfatal + timer hard-stop", got)
	}
	if elapsed := time.Since(started); elapsed < minMountPollInterval {
		t.Fatalf("runner returned before a timer cycle (%s < %s)", elapsed, minMountPollInterval)
	}
}

func TestNormalizeRemotePathsDedupesRepeatedFlagValues(t *testing.T) {
	got := normalizeRemotePaths(
		[]string{"/github/repos/acme/cloud", "github/repos/acme/cloud/", "/slack/channels/proj-cloud"},
		"/",
	)
	want := []string{"/github/repos/acme/cloud", "/slack/channels/proj-cloud"}
	if len(got) != len(want) {
		t.Fatalf("expected %d paths, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("path %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestScopedLocalDirKeepsProviderPrefixUnderMountRoot(t *testing.T) {
	got := scopedLocalDir("/workspace", "/github/repos/acme/cloud")
	want := filepath.Join("/workspace", "github", "repos", "acme", "cloud")
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestRunScopedPollingMountsKeepsSharedStateDirForHashResolver(t *testing.T) {
	stateDir := t.TempDir()
	var gotMu sync.Mutex
	var got []mountConfig

	err := runScopedPollingMountsWithRunner(
		context.Background(),
		mountConfig{localDir: t.TempDir(), stateDir: stateDir},
		[]string{"/github", "/slack"},
		func(_ context.Context, cfg mountConfig) error {
			gotMu.Lock()
			defer gotMu.Unlock()
			got = append(got, cfg)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("runScopedPollingMountsWithRunner returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 scoped mounts, got %d", len(got))
	}
	for _, cfg := range got {
		if cfg.stateDir != stateDir {
			t.Fatalf("expected state dir %q, got %q", stateDir, cfg.stateDir)
		}
		if cfg.stateFile != "" {
			t.Fatalf("expected state-file to stay empty so mountsync derives hashed path, got %q", cfg.stateFile)
		}
	}
}

func TestRunPollingMountSingleNonRootDefaultsToExactLocalDir(t *testing.T) {
	localDir := t.TempDir()
	var got []mountConfig

	err := runPollingMountWithRunner(
		context.Background(),
		mountConfig{
			localDir:    localDir,
			stateDir:    t.TempDir(),
			remotePath:  "/slack/channels/C123",
			remotePaths: []string{"/slack/channels/C123"},
		},
		func(_ context.Context, cfg mountConfig) error {
			got = append(got, cfg)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("runPollingMountWithRunner returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one mount, got %d", len(got))
	}
	if got[0].localDir != localDir {
		t.Fatalf("expected exact local dir %q, got %q", localDir, got[0].localDir)
	}
}

func TestRunPollingMountScopedLayoutAppendsRemotePath(t *testing.T) {
	localRoot := t.TempDir()
	var got []mountConfig

	err := runPollingMountWithRunner(
		context.Background(),
		mountConfig{
			localDir:    localRoot,
			localLayout: localLayoutScoped,
			stateDir:    t.TempDir(),
			remotePath:  "/slack/channels/C123",
			remotePaths: []string{"/slack/channels/C123"},
		},
		func(_ context.Context, cfg mountConfig) error {
			got = append(got, cfg)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("runPollingMountWithRunner returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one mount, got %d", len(got))
	}
	want := filepath.Join(localRoot, "slack", "channels", "C123")
	if got[0].localDir != want {
		t.Fatalf("expected scoped local dir %q, got %q", want, got[0].localDir)
	}
}

func TestRunPollingMountMultiPathRequiresExplicitScopedLayout(t *testing.T) {
	err := runPollingMountWithRunner(
		context.Background(),
		mountConfig{
			localDir:    t.TempDir(),
			stateDir:    t.TempDir(),
			remotePaths: []string{"/github", "/slack"},
		},
		func(_ context.Context, cfg mountConfig) error {
			t.Fatalf("runner should not start with implicit multi-path layout: %+v", cfg)
			return nil
		},
	)
	if err == nil {
		t.Fatal("expected multi-path exact layout to fail")
	}
	if !strings.Contains(err.Error(), "--local-layout=scoped") {
		t.Fatalf("expected scoped-layout guidance, got %v", err)
	}
}

func TestMountStartupLogLineIncludesResolvedLayoutAndSyncContract(t *testing.T) {
	localDir := t.TempDir()
	got := mountStartupLogLine(mountConfig{
		localDir:    localDir,
		localLayout: localLayoutExact,
		remotePath:  "/slack/channels/C123",
		syncMode:    syncModeWriteOnly,
		mode:        mountModePoll,
	})

	for _, want := range []string{
		"layout=exact",
		"remote=/slack/channels/C123",
		"local=" + localDir,
		"sync=write-only",
		"mode=poll",
		"state=" + filepath.Join(localDir, ".relay", "state.json"),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("startup log %q missing %q", got, want)
		}
	}
}

func TestRunScopedPollingMountsRejectsSharedExactStateFileOverride(t *testing.T) {
	stateFile := filepath.Join(t.TempDir(), "state.json")

	err := runScopedPollingMountsWithRunner(
		context.Background(),
		mountConfig{localDir: t.TempDir(), stateDir: t.TempDir(), stateFile: stateFile},
		[]string{"/github", "/slack"},
		func(_ context.Context, cfg mountConfig) error {
			t.Fatalf("runner should not start with shared state-file override: %+v", cfg)
			return nil
		},
	)
	if err == nil {
		t.Fatal("expected shared state-file override to be rejected")
	}
	if !strings.Contains(err.Error(), "use --state-dir") {
		t.Fatalf("expected state-dir guidance, got %v", err)
	}
}

func TestRunScopedPollingMountsCancelsSiblingsOnFirstError(t *testing.T) {
	wantErr := errors.New("boom")
	var canceled atomic.Bool
	started := make(chan string, 2)
	releaseFailingMount := make(chan struct{})
	var once sync.Once

	err := runScopedPollingMountsWithRunner(
		context.Background(),
		mountConfig{localDir: t.TempDir(), stateDir: t.TempDir()},
		[]string{"/github", "/slack"},
		func(ctx context.Context, cfg mountConfig) error {
			started <- cfg.remotePath
			if strings.HasSuffix(cfg.remotePath, "/github") {
				<-releaseFailingMount
				return wantErr
			}
			once.Do(func() { close(releaseFailingMount) })
			<-ctx.Done()
			canceled.Store(true)
			return nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected first error %v, got %v", wantErr, err)
	}
	if !canceled.Load() {
		t.Fatal("expected sibling mount to observe context cancellation")
	}
	close(started)
	seen := map[string]bool{}
	for path := range started {
		seen[path] = true
	}
	if !seen["/github"] || !seen["/slack"] {
		t.Fatalf("expected both scoped mounts to start, saw %v", seen)
	}
}

func TestReadRemotePathsFileSupportsJSONAndLines(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "paths.json")
	if err := os.WriteFile(jsonPath, []byte(`["/github","/linear/issues"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonPaths, err := readRemotePathsFile(jsonPath)
	if err != nil {
		t.Fatalf("read json paths: %v", err)
	}
	if want := []string{"/github", "/linear/issues"}; !stringSlicesEqual(jsonPaths, want) {
		t.Fatalf("expected json paths %v, got %v", want, jsonPaths)
	}

	linesPath := filepath.Join(dir, "paths.txt")
	if err := os.WriteFile(linesPath, []byte("\n# comment\n/github/repos/acme/cloud\n/slack/channels/proj-cloud\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linePaths, err := readRemotePathsFile(linesPath)
	if err != nil {
		t.Fatalf("read line paths: %v", err)
	}
	if want := []string{"/github/repos/acme/cloud", "/slack/channels/proj-cloud"}; !stringSlicesEqual(linePaths, want) {
		t.Fatalf("expected line paths %v, got %v", want, linePaths)
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
