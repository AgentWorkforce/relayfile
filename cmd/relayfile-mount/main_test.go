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

func TestHealthyWebSocketAvoidsStopTheWorldReconcile(t *testing.T) {
	for cycle := 1; cycle <= 100; cycle++ {
		if shouldReconcileMountCycle(true, cycle) {
			t.Fatalf("healthy real-time cycle %d unexpectedly reconciled", cycle)
		}
	}
	for cycle := 1; cycle <= 100; cycle++ {
		if !shouldReconcileMountCycle(false, cycle) {
			t.Fatalf("unhealthy real-time cycle %d did not fall back to reconcile", cycle)
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

func TestPullOnlyMountKeepsWebSocketButDisablesLocalWatcher(t *testing.T) {
	cfg := mountConfig{websocketEnabled: true, syncMode: syncModePullOnly}
	if !mountWebSocketEnabled(cfg) {
		t.Fatal("pull-only mount should keep remote websocket events")
	}
	if mountWatchesLocalChanges(cfg) {
		t.Fatal("pull-only mount must not watch local changes for writeback")
	}
	if !mountReconcileUsesWebSocketCadence(cfg, false) {
		t.Fatal("pull-only mount should use websocket cadence without a local watcher")
	}
	if !mountWatchesLocalChanges(mountConfig{syncMode: syncModeMirror}) {
		t.Fatal("mirror mount should keep its local watcher")
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

func TestCheckpointAndSealCLIValidationFailsBeforeStartingMount(t *testing.T) {
	tests := []struct {
		name string
		cfg  mountConfig
		want string
	}{
		{
			name: "fuse stays mounted",
			cfg:  mountConfig{checkpointAndSeal: true, mode: mountModeFuse, checkpointSession: "session-1", checkpointGeneration: 1, checkpointSealTTL: time.Minute},
			want: "requires --mode=poll",
		},
		{
			name: "identity required",
			cfg:  mountConfig{checkpointAndSeal: true, mode: mountModePoll, checkpointSealTTL: time.Minute},
			want: "requires --checkpoint-session",
		},
		{
			name: "ttl bounded",
			cfg:  mountConfig{checkpointAndSeal: true, mode: mountModePoll, checkpointSession: "session-1", checkpointGeneration: 1, checkpointSealTTL: mountsync.MaxCheckpointSealTTL + time.Second},
			want: "--checkpoint-seal-ttl must be between",
		},
		{
			name: "one-shot modes are exclusive",
			cfg:  mountConfig{checkpointAndSeal: true, once: true, mode: mountModePoll, checkpointSession: "session-1", checkpointGeneration: 1, checkpointSealTTL: time.Minute},
			want: "cannot be combined",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pollCalled := false
			fuseCalled := false
			err := executeMount(
				context.Background(),
				tc.cfg,
				func(context.Context, mountConfig) error { pollCalled = true; return nil },
				func(context.Context, mountConfig) error { fuseCalled = true; return nil },
			)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
			if pollCalled || fuseCalled {
				t.Fatal("invalid checkpoint request must fail before starting a mount")
			}
		})
	}
}

func TestCheckpointOperationTimeoutHasThirtySecondFloor(t *testing.T) {
	for _, tc := range []struct {
		configured time.Duration
		want       time.Duration
	}{
		{configured: 0, want: 30 * time.Second},
		{configured: 15 * time.Second, want: 30 * time.Second},
		{configured: 30 * time.Second, want: 30 * time.Second},
		{configured: 45 * time.Second, want: 45 * time.Second},
	} {
		if got := checkpointOperationTimeout(tc.configured); got != tc.want {
			t.Fatalf("checkpointOperationTimeout(%s) = %s, want %s", tc.configured, got, tc.want)
		}
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

func TestValidateCLIRequestedLocalLayoutRefusesScopedUntilOperatorSurfacesReady(t *testing.T) {
	if err := validateCLIRequestedLocalLayout(localLayoutExact); err != nil {
		t.Fatalf("exact layout should remain available: %v", err)
	}
	err := validateCLIRequestedLocalLayout(localLayoutScoped)
	if err == nil || !strings.Contains(err.Error(), "operator surfaces") || !strings.Contains(err.Error(), "--local-layout=exact") {
		t.Fatalf("expected scoped-layout refusal with exact-layout remedy, got %v", err)
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
		{name: "explicit pull-only", mode: "pull-only", want: syncModePullOnly},
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

func TestExecuteMountRejectsPullOnlyFuseMode(t *testing.T) {
	cfg := mountConfig{mode: mountModeFuse, syncMode: syncModePullOnly}
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
	if err == nil {
		t.Fatal("expected pull-only FUSE mount to be rejected")
	}
	for _, want := range []string{syncModePullOnly, mountModeFuse, "--mode=" + mountModePoll} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
	if pollCalled || fuseCalled {
		t.Fatalf("unsupported mount dispatched a runner: poll=%t fuse=%t", pollCalled, fuseCalled)
	}
}

func TestExecuteMountRejectsMultipleFusePaths(t *testing.T) {
	cfg := mountConfig{
		mode:        mountModeFuse,
		localLayout: localLayoutScoped,
		remotePaths: []string{"/github", "/slack"},
	}
	fuseCalled := false
	err := executeMount(
		context.Background(),
		cfg,
		func(context.Context, mountConfig) error { return nil },
		func(context.Context, mountConfig) error {
			fuseCalled = true
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "--mode=poll") {
		t.Fatalf("expected poll-mode guidance, got %v", err)
	}
	if fuseCalled {
		t.Fatal("FUSE runner must not receive a multi-path config it cannot honor")
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

func TestInstallCredsFileRefreshPrefersExternallyRotatedDelegatedToken(t *testing.T) {
	credsFile := filepath.Join(t.TempDir(), "creds.json")
	refreshCalls := int32(0)
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&refreshCalls, 1)
		http.Error(w, "refresh must not be called", http.StatusServiceUnavailable)
	}))
	defer refreshServer.Close()
	if err := os.WriteFile(credsFile, []byte(fmt.Sprintf(`{
		"accessToken":"new-token",
		"refreshToken":"rotated-refresh-token",
		"refreshUrl":%q
	}`, refreshServer.URL)), 0o600); err != nil {
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
				t.Fatalf("expected retry to use rotated file token, got %q", got)
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
		t.Fatalf("expected externally rotated token to recover request: %v", err)
	}
	if got := client.Token(); got != "new-token" {
		t.Fatalf("expected client token to update, got %q", got)
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 0 {
		t.Fatalf("expected no RelayAuth refresh request, got %d", got)
	}
}

func TestInstallCredsFileRefreshRenewsExpiredRotatedFileToken(t *testing.T) {
	credsFile := filepath.Join(t.TempDir(), "creds.json")
	refreshServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode refresh request: %v", err)
		}
		if payload["refreshToken"] != "rotated-refresh-token" {
			t.Fatalf("refresh token = %q", payload["refreshToken"])
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"accessToken":           "fresh-token",
			"refreshToken":          "fresh-refresh-token",
			"accessTokenExpiresAt":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"refreshTokenExpiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer refreshServer.Close()
	if err := os.WriteFile(credsFile, []byte(fmt.Sprintf(`{
		"accessToken":"expired-file-token",
		"accessTokenExpiresAt":%q,
		"refreshToken":"rotated-refresh-token",
		"refreshUrl":%q
	}`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), refreshServer.URL)), 0o600); err != nil {
		t.Fatal(err)
	}

	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&calls, 1)
		switch call {
		case 1:
			if got := r.Header.Get("Authorization"); got != "Bearer old-token" {
				t.Fatalf("first authorization = %q", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized"}`))
		case 2:
			if got := r.Header.Get("Authorization"); got != "Bearer fresh-token" {
				t.Fatalf("retry authorization = %q, expired file token was reused", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"path":"/slack","entries":[],"nextCursor":null}`))
		default:
			t.Fatalf("unexpected request %d", call)
		}
	}))
	defer server.Close()

	client := mountsync.NewHTTPClient(server.URL, "old-token", server.Client())
	installCredsFileRefresh(client, mountConfig{credsFile: credsFile})
	if _, err := client.ListTree(context.Background(), "ws_auth", "/slack", 1, ""); err != nil {
		t.Fatalf("expected refresh-token renewal to recover request: %v", err)
	}
	if got := client.Token(); got != "fresh-token" {
		t.Fatalf("client token = %q, want fresh-token", got)
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

func TestRunSinglePollingMountRejectsUnsafeCorrelationWithoutEchoingIt(t *testing.T) {
	const unsafeCorrelation = "mount qualification private value"
	err := runSinglePollingMount(context.Background(), mountConfig{
		baseURL:              "http://127.0.0.1",
		token:                "test-token",
		requestCorrelationID: unsafeCorrelation,
	})
	if err == nil || !strings.Contains(err.Error(), "RELAYFILE_MOUNT_CORRELATION_ID") {
		t.Fatalf("expected bounded mount correlation validation, got %v", err)
	}
	if strings.Contains(err.Error(), unsafeCorrelation) {
		t.Fatal("mount startup error exposed the raw correlation")
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

// TestRunSinglePollingMountReportsErrorForNormalCycleFailure asserts that
// a normal cloud error during --once is surfaced so the process exits nonzero instead
// of pretending the bootstrap completed.
func TestRunSinglePollingMountReportsErrorForNormalCycleFailure(t *testing.T) {
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
	if err == nil {
		t.Fatalf("expected --once to return an error when bootstrap is incomplete")
	}
	var incomplete *initialBootstrapIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("unexpected error type: %v", err)
	}
	var httpErr *mountsync.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected a 502 HTTP cause, got %v", err)
	}
}

// TestRunSinglePollingMountKeepsSuccessOnUnrelatedFailureAfterBootstrapComplete
// is the end-to-end counterpart of
// TestFinishInitialBootstrapKeepsSuccessAfterPriorCompletion: it exercises the
// real syncer against a persistent localDir/stateDir (the ensureRelayfileMount
// pattern of reusing a mount point across invocations), rather than injected
// callbacks. A workspace that already finished its full-tree bootstrap in one
// --once run must not have a later, unrelated --once cycle failure reported
// as an incomplete bootstrap.
func TestRunSinglePollingMountKeepsSuccessOnUnrelatedFailureAfterBootstrapComplete(t *testing.T) {
	entries := []mountsync.TreeEntry{
		{Path: "/f/one.txt", Type: "file"},
		{Path: "/f/two.txt", Type: "file"},
	}
	var failing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			http.Error(w, "transient", http.StatusBadGateway)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/fs/tree"):
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
	defer server.Close()

	localDir := t.TempDir()
	cfg := mountConfig{
		baseURL:          server.URL,
		token:            "test-token",
		workspaceID:      "ws_already_bootstrapped",
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

	if err := runSinglePollingMount(context.Background(), cfg); err != nil {
		t.Fatalf("initial bootstrap run failed: %v", err)
	}
	statePath := filepath.Join(localDir, ".relay", "state.json")
	if ready, reason := sandboxInitialSyncGuard(statePath); !ready {
		t.Fatalf("expected the first --once run to leave bootstrap complete: %s", reason)
	}

	failing.Store(true)
	if err := runSinglePollingMount(context.Background(), cfg); err != nil {
		t.Fatalf("expected --once to succeed on an already-bootstrapped mount despite an unrelated cycle failure, got %v", err)
	}
}

// A completed write-only mount has never hydrated provider history. Switching
// it to mirror re-arms bootstrap in private state. If the first provider call
// then fails before any new public bootstrap progress is published, the old
// public success marker must not make --once exit successfully.
func TestRunSinglePollingMountRejectsStalePublicCompletionAfterWriteOnlyToMirrorTransition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "transient", http.StatusBadGateway)
	}))
	defer server.Close()

	localDir := t.TempDir()
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, ".relay"), 0o755); err != nil {
		t.Fatalf("create public state dir: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(localDir, ".relay", "state.json"),
		[]byte(`{"lastSuccessfulReconcileAt":"2026-09-08T00:00:00Z"}`),
		0o644,
	); err != nil {
		t.Fatalf("seed stale public completion: %v", err)
	}

	privatePath, err := mountsync.ResolveMountStatePath(mountsync.MountStatePathOptions{
		WorkspaceID:     "ws_write_only_to_mirror",
		RemoteRoot:      "/",
		LocalRoot:       localDir,
		StateDir:        stateDir,
		MountKind:       mountsync.MountKindDaemon,
		ValidateOutside: true,
	})
	if err != nil {
		t.Fatalf("resolve private state path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(privatePath.StateFile), 0o755); err != nil {
		t.Fatalf("create private state dir: %v", err)
	}
	if err := os.WriteFile(
		privatePath.StateFile,
		[]byte(`{"files":{},"bootstrapComplete":true,"syncMode":"write-only"}`),
		0o644,
	); err != nil {
		t.Fatalf("seed completed write-only private state: %v", err)
	}

	cfg := mountConfig{
		baseURL:          server.URL,
		token:            "test-token",
		workspaceID:      "ws_write_only_to_mirror",
		remotePath:       "/",
		localDir:         localDir,
		stateDir:         stateDir,
		mountKind:        mountsync.MountKindDaemon,
		syncMode:         syncModeMirror,
		interval:         time.Hour,
		timeout:          time.Second,
		websocketEnabled: false,
		once:             true,
	}

	err = runSinglePollingMount(context.Background(), cfg)
	if err == nil {
		t.Fatal("stale public completion must not suppress the required mirror backfill failure")
	}
	var incomplete *initialBootstrapIncompleteError
	if !errors.As(err, &incomplete) {
		t.Fatalf("expected typed incomplete bootstrap error, got %T: %v", err, err)
	}
	var httpErr *mountsync.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected the first-cycle provider failure to remain in the chain, got %v", err)
	}
}

func TestMountProcessExitCodeOnlyMarksTypedOnceBootstrapIncompleteRetryable(t *testing.T) {
	incomplete := newResumableInitialBootstrapIncompleteError(
		bootstrapResumeState{inProgress: true, synced: 10, total: 20},
		"resume bound reached",
		context.DeadlineExceeded,
	)

	if got := mountProcessExitCode(mountConfig{once: true}, incomplete); got != initialBootstrapIncompleteExitCode {
		t.Fatalf("typed --once incomplete exit = %d, want %d", got, initialBootstrapIncompleteExitCode)
	}
	if got := mountProcessExitCode(mountConfig{}, incomplete); got != 1 {
		t.Fatalf("daemon typed incomplete exit = %d, want generic failure", got)
	}
	if got := mountProcessExitCode(mountConfig{once: true}, errors.New("initial bootstrap incomplete")); got != 1 {
		t.Fatalf("text-only --once failure exit = %d, want generic failure", got)
	}
	if got := mountProcessExitCode(mountConfig{once: true}, fmt.Errorf("wrapped: %w", incomplete)); got != initialBootstrapIncompleteExitCode {
		t.Fatalf("wrapped typed --once incomplete exit = %d, want %d", got, initialBootstrapIncompleteExitCode)
	}
	secondIncomplete := newResumableInitialBootstrapIncompleteError(bootstrapResumeState{}, "another scope", nil)
	if got := mountProcessExitCode(mountConfig{once: true}, errors.Join(incomplete, secondIncomplete)); got != initialBootstrapIncompleteExitCode {
		t.Fatalf("all-incomplete scoped aggregate exit = %d, want %d", got, initialBootstrapIncompleteExitCode)
	}
	if got := mountProcessExitCode(mountConfig{once: true}, errors.Join(incomplete, errors.New("fatal sibling"))); got != 1 {
		t.Fatalf("mixed scoped aggregate exit = %d, want generic failure", got)
	}
	fatalProvider := newInitialBootstrapIncompleteError(
		bootstrapResumeState{inProgress: true, synced: 10, total: 20},
		"initial cycle failed",
		&mountsync.HTTPError{StatusCode: http.StatusBadGateway, Message: "bad gateway"},
	)
	if got := mountProcessExitCode(mountConfig{once: true}, fatalProvider); got != 1 {
		t.Fatalf("fatal provider --once exit = %d, want generic failure", got)
	}
	if got := mountProcessExitCode(mountConfig{once: true}, fmt.Errorf("wrapped provider: %w", fatalProvider)); got != 1 {
		t.Fatalf("wrapped fatal provider --once exit = %d, want generic failure", got)
	}
	if got := mountProcessExitCode(mountConfig{once: true}, errors.Join(incomplete, fatalProvider)); got != 1 {
		t.Fatalf("mixed resumable/fatal provider aggregate exit = %d, want generic failure", got)
	}
}

// TestRunSinglePollingMountForceFullReconcileFailsOnceOnProviderErrorAfterPriorCompletion
// is the forceFullRecon counterpart of
// TestRunSinglePollingMountKeepsSuccessOnUnrelatedFailureAfterBootstrapComplete:
// --full-reconcile is an explicit request for a real full-tree reconcile to
// run and succeed on THIS invocation, so an already-complete checkpoint from
// an earlier, unrelated --once run must NOT exempt this run's own forced
// reconcile from a genuine tree/provider request failure. Without the
// cfg.forceFullRecon carve-out in finishInitialBootstrap, this second run
// would report success (err == nil) purely because bootstrap had already
// finished at some prior point -- even though the full reconcile it was
// explicitly asked to run never got past a 502 from the provider.
func TestRunSinglePollingMountForceFullReconcileFailsOnceOnProviderErrorAfterPriorCompletion(t *testing.T) {
	entries := []mountsync.TreeEntry{
		{Path: "/f/one.txt", Type: "file"},
		{Path: "/f/two.txt", Type: "file"},
	}
	var failing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			http.Error(w, "transient", http.StatusBadGateway)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "/fs/tree"):
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
	defer server.Close()

	localDir := t.TempDir()
	cfg := mountConfig{
		baseURL:          server.URL,
		token:            "test-token",
		workspaceID:      "ws_force_full_recon_after_bootstrapped",
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

	// Run 1: an ordinary --once run (no --full-reconcile) completes the
	// bootstrap and leaves a completed checkpoint on disk -- the "create
	// completed state" step.
	if err := runSinglePollingMount(context.Background(), cfg); err != nil {
		t.Fatalf("initial bootstrap run failed: %v", err)
	}
	statePath := filepath.Join(localDir, ".relay", "state.json")
	if ready, reason := sandboxInitialSyncGuard(statePath); !ready {
		t.Fatalf("expected the first --once run to leave bootstrap complete: %s", reason)
	}

	// Run 2: the same already-bootstrapped mount, but with --full-reconcile
	// explicitly requested this time, and the provider now failing every
	// request. This must NOT reuse run 1's success.
	forceFullReconCfg := cfg
	forceFullReconCfg.forceFullRecon = true
	failing.Store(true)
	err := runSinglePollingMount(context.Background(), forceFullReconCfg)
	if err == nil {
		t.Fatal("expected --once --full-reconcile to fail when the provider request fails, not reuse an unrelated prior completion")
	}
	var httpErr *mountsync.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected a 502 HTTP provider error in the chain, got %v", err)
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
		case strings.Contains(r.URL.Path, "/fs/bulk-read"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotImplemented)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"code":    "bulk_read_unsupported",
				"message": "test server exercises the legacy point-read path",
			})
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
		if !cfg.scopedChild {
			t.Fatal("scoped runner did not preserve child-topology identity")
		}
	}
}

func TestRunScopedPollingMountsRejectsProviderFilterAcrossHeterogeneousRoots(t *testing.T) {
	localRoot := filepath.Join(t.TempDir(), "mirror")
	err := runScopedPollingMountsWithRunner(
		context.Background(),
		mountConfig{localDir: localRoot, stateDir: t.TempDir(), eventProvider: "github"},
		[]string{"/github", "/slack"},
		func(_ context.Context, cfg mountConfig) error {
			t.Fatalf("runner should not start with heterogeneous provider filter: %+v", cfg)
			return nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), "/slack") || !strings.Contains(err.Error(), "omit --provider") {
		t.Fatalf("expected heterogeneous provider-filter refusal, got %v", err)
	}
	if _, statErr := os.Stat(localRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("provider-filter refusal initialized mirror: %v", statErr)
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

// TestRunScopedPollingMountsCancelsSiblingsOnTerminalError pins the
// fail-fast half of the contract: an operator-actionable terminal bootstrap
// error (mountsync.IsBootstrapTerminalError) in one scope still cancels its
// siblings rather than letting them retry a wedged checkpoint forever.
func TestRunScopedPollingMountsCancelsSiblingsOnTerminalError(t *testing.T) {
	wantErr := &mountsync.BootstrapStalledError{Cycles: 3, Limit: 3, Path: "/github"}
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
		t.Fatalf("expected aggregate error to include %v, got %v", wantErr, err)
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

// TestRunScopedPollingMountsCancelsSiblingsOnGenericErrorInDaemonMode pins
// the daemon-mode half of the fix: outside --once (cfg.once unset), ANY
// non-nil error -- including a generic one that is neither
// mountsync.IsBootstrapTerminalError nor *initialBootstrapIncompleteError
// (which only --once's finishInitialBootstrap ever produces) -- still
// cancels every sibling. The --once sibling-cancellation suppression must
// never leak into daemon mode, where a wedged or failed scope should not be
// left running indefinitely alongside a sibling that will exit nonzero
// anyway.
func TestRunScopedPollingMountsCancelsSiblingsOnGenericErrorInDaemonMode(t *testing.T) {
	wantErr := errors.New("generic runtime error unrelated to bootstrap")
	var canceled atomic.Bool
	started := make(chan string, 2)
	releaseFailingMount := make(chan struct{})
	var once sync.Once

	err := runScopedPollingMountsWithRunner(
		context.Background(),
		mountConfig{localDir: t.TempDir(), stateDir: t.TempDir()}, // once unset: daemon mode
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
		t.Fatalf("expected aggregate error to include %v, got %v", wantErr, err)
	}
	if !canceled.Load() {
		t.Fatal("expected sibling mount to observe context cancellation for a generic error in daemon mode")
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

// TestRunScopedPollingMountsCancelsSiblingsOnGenericErrorInOnceMode pins the
// narrowness of the --once suppression: even with cfg.once set, a generic
// error that is NOT the typed *initialBootstrapIncompleteError (e.g. an
// initialization/runtime error, or any other error finishInitialBootstrap
// did not itself produce) still cancels every sibling. Only that one typed
// outcome is exempted -- see
// TestRunScopedPollingMountsLetsHealthySiblingFinishOnNonTerminalError.
func TestRunScopedPollingMountsCancelsSiblingsOnGenericErrorInOnceMode(t *testing.T) {
	wantErr := errors.New("generic initialization error unrelated to bootstrap")
	var canceled atomic.Bool
	started := make(chan string, 2)
	releaseFailingMount := make(chan struct{})
	var once sync.Once

	err := runScopedPollingMountsWithRunner(
		context.Background(),
		mountConfig{localDir: t.TempDir(), stateDir: t.TempDir(), once: true},
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
		t.Fatalf("expected aggregate error to include %v, got %v", wantErr, err)
	}
	if !canceled.Load() {
		t.Fatal("expected sibling mount to observe context cancellation for a generic --once error that is not *initialBootstrapIncompleteError")
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

// TestRunScopedPollingMountsCancelsSiblingsOnMixedIncompleteAndFatalError
// pins the all-branches rule for joined outcomes. A provider failure may be
// wrapped alongside a resumable bootstrap checkpoint, but the aggregate is
// fatal and must still cancel a sibling instead of being downgraded by a
// one-branch errors.As match.
func TestRunScopedPollingMountsCancelsSiblingsOnMixedIncompleteAndFatalError(t *testing.T) {
	resumable := newResumableInitialBootstrapIncompleteError(
		bootstrapResumeState{inProgress: true, synced: 1, total: 10},
		"checkpoint stopped advancing",
		nil,
	)
	fatalProvider := &mountsync.HTTPError{StatusCode: http.StatusBadGateway, Message: "bad gateway"}
	wantErr := errors.Join(resumable, fmt.Errorf("provider request: %w", fatalProvider))
	var canceled atomic.Bool
	started := make(chan string, 2)
	releaseFailingMount := make(chan struct{})
	var once sync.Once

	err := runScopedPollingMountsWithRunner(
		context.Background(),
		mountConfig{localDir: t.TempDir(), stateDir: t.TempDir(), once: true},
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
	if !errors.Is(err, fatalProvider) {
		t.Fatalf("expected aggregate error to include fatal provider error, got %v", err)
	}
	if !errors.Is(err, resumable) {
		t.Fatalf("expected aggregate error to include resumable scope error, got %v", err)
	}
	if !canceled.Load() {
		t.Fatal("expected mixed resumable/fatal --once error to cancel sibling")
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

// TestRunScopedPollingMountsLetsHealthySiblingFinishOnNonTerminalError pins
// the counterpart: in --once mode (cfg.once), a failed or stalled scope
// reporting the explicitly resumable *initialBootstrapIncompleteError outcome
// finishInitialBootstrap returns for a resume-cycle ceiling, a stall bound,
// or a cancelled rootCtx must not cancel a healthy sibling still making
// progress within its own bound. The healthy sibling runs to its own bounded
// completion -- proven here by an explicit delay it must survive uncancelled
// -- and the aggregate result is still a nonzero error once every sibling
// has finished, so a real `--once` operator still sees the failure and
// exits nonzero.
//
// cfg.once must be set here: the sibling-cancellation suppression is scoped
// to --once specifically (see runScopedPollingMountsWithRunner). Its two
// boundaries are pinned by
// TestRunScopedPollingMountsCancelsSiblingsOnGenericErrorInDaemonMode
// (outside --once, any error still cancels) and
// TestRunScopedPollingMountsCancelsSiblingsOnGenericErrorInOnceMode (inside
// --once, only the explicitly resumable outcome is exempted -- a generic or
// fatal error still cancels).
func TestRunScopedPollingMountsLetsHealthySiblingFinishOnNonTerminalError(t *testing.T) {
	nonTerminalErr := newResumableInitialBootstrapIncompleteError(
		bootstrapResumeState{inProgress: true, synced: 1, total: 10},
		"bootstrap checkpoint stopped advancing",
		nil,
	)
	const healthySiblingBoundedWork = 150 * time.Millisecond
	healthyFinished := make(chan struct{})
	var healthyCtxCancelledBeforeFinish atomic.Bool

	done := make(chan error, 1)
	go func() {
		done <- runScopedPollingMountsWithRunner(
			context.Background(),
			mountConfig{localDir: t.TempDir(), stateDir: t.TempDir(), once: true},
			[]string{"/github", "/slack"},
			func(ctx context.Context, cfg mountConfig) error {
				if strings.HasSuffix(cfg.remotePath, "/github") {
					// Simulates a stalled/incomplete scope returning
					// immediately with a non-terminal outcome.
					return nonTerminalErr
				}
				// Healthy sibling: simulates its own bounded --once work.
				// If the unrelated scope's failure cancelled the shared
				// ctx, this select returns early via ctx.Done() instead of
				// running its full bounded duration.
				select {
				case <-time.After(healthySiblingBoundedWork):
				case <-ctx.Done():
				}
				if ctx.Err() != nil {
					healthyCtxCancelledBeforeFinish.Store(true)
				}
				close(healthyFinished)
				return nil
			},
		)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a nonzero aggregate error once every sibling finished")
		}
		if !errors.Is(err, nonTerminalErr) {
			t.Fatalf("expected aggregate error to include the stalled scope's error, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runScopedPollingMountsWithRunner did not return after every sibling's own bounded completion")
	}

	select {
	case <-healthyFinished:
	default:
		t.Fatal("healthy sibling never reached its own bounded completion")
	}
	if healthyCtxCancelledBeforeFinish.Load() {
		t.Fatal("healthy sibling's context was cancelled before its own bounded --once work completed, due to an unrelated non-terminal sibling failure")
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
