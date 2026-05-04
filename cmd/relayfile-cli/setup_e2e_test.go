package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// signedExpJWT crafts an unsigned JWT with the given exp (relative to now)
// and iat=now. The CLI's relayfileTokenNeedsRefresh predicate only inspects
// claims, so a `none`-alg token is sufficient for these tests.
func signedExpJWT(t *testing.T, expIn time.Duration) string {
	t.Helper()
	now := time.Now().UTC().Unix()
	claims := map[string]any{
		"iat": now,
		"exp": time.Now().UTC().Add(expIn).Unix(),
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal jwt claims failed: %v", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + encoded + ".sig"
}

// TestA8RelayfileTokenNeedsRefreshDetectsNearExpiry covers the predicate
// the mount loop uses (in withAuthRefresh) to decide when to call
// joinWorkspaceViaCloud — the trigger half of contract acceptance test
// A8 ("Token refresh: VFS token expires mid-mount").
func TestA8RelayfileTokenNeedsRefreshDetectsNearExpiry(t *testing.T) {
	cases := []struct {
		name string
		exp  time.Duration
		want bool
	}{
		{"already expired", -10 * time.Second, true},
		{"under 5 minute leeway", 30 * time.Second, true},
		{"under lifetime/10 leeway (1h token)", 4 * time.Minute, true},
		{"safely in the future", time.Hour, false},
		{"far in the future", 24 * time.Hour, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token := signedExpJWT(t, tc.exp)
			if got := relayfileTokenNeedsRefresh(token); got != tc.want {
				t.Fatalf("relayfileTokenNeedsRefresh(%s)=%v, want %v", tc.name, got, tc.want)
			}
		})
	}

	if got := relayfileTokenNeedsRefresh(""); got != false {
		t.Fatalf("empty token should not trigger refresh, got %v", got)
	}
	if got := relayfileTokenNeedsRefresh("not.a.jwt"); got != false {
		t.Fatalf("unparseable token should not trigger refresh, got %v", got)
	}
}

// TestA8JoinWorkspaceMintsFreshRelayfileToken covers the action half of A8:
// when the cloud access token is valid and we call joinWorkspaceViaCloud,
// the cloud responds with a brand-new relayfile token + URL without the
// CLI dropping the websocket. We verify the call shape (token bearer, body)
// and that the returned credentials are well-formed.
func TestA8JoinWorkspaceMintsFreshRelayfileToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var joinCalls int32
	var seenBody cloudWorkspaceJoinRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/workspaces/ws_demo/join" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
			t.Fatalf("unexpected join Authorization: %q", got)
		}
		atomic.AddInt32(&joinCalls, 1)
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode join body failed: %v", err)
		}
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","token":"rf_new_token","relayfileUrl":"https://relayfile.test","wsUrl":"wss://relayfile.test/ws"}`))
	}))
	defer server.Close()

	creds := cloudCredentials{
		APIURL:               server.URL,
		AccessToken:          "cld_access",
		AccessTokenExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	joined, err := joinWorkspaceViaCloud(creds, "ws_demo", "relayfile-cli", append([]string(nil), defaultJoinScopes...))
	if err != nil {
		t.Fatalf("joinWorkspaceViaCloud failed: %v", err)
	}
	if atomic.LoadInt32(&joinCalls) != 1 {
		t.Fatalf("expected exactly 1 /join call, got %d", joinCalls)
	}
	if joined.Token != "rf_new_token" {
		t.Fatalf("expected fresh relayfile token, got %q", joined.Token)
	}
	if joined.RelayfileURL == "" || joined.WSURL == "" {
		t.Fatalf("expected join response to include relayfileUrl and wsUrl, got %+v", joined)
	}
	if seenBody.AgentName != "relayfile-cli" {
		t.Fatalf("expected agent name in join body, got %+v", seenBody)
	}
}

// TestA1SetupHappyPathBannerAfterSetup verifies productized cloud-mount
// contract A1's mount-cycle requirement: after `relayfile setup` runs the
// happy path with --cloud-token (no browser) and exits, the synced-mirror
// banner is the user-visible signal the mount loop printed before exiting.
// We assert the formatter directly so the test is independent of stdlib log
// flag plumbing.
func TestA1SyncedMirrorBannerNamingMatchesContract(t *testing.T) {
	got := mountStartBanner("/tmp/relay", 30*time.Second, 0.2)
	if !strings.HasPrefix(got, "Synced mirror started at /tmp/relay.") {
		t.Fatalf("expected banner to lead with 'Synced mirror started at <dir>.', got: %q", got)
	}
	if !strings.Contains(got, "Sync interval 30s") {
		t.Fatalf("expected banner to mention the 30s interval, got: %q", got)
	}
	if !strings.Contains(got, "±20%") {
		t.Fatalf("expected banner to advertise the jitter, got: %q", got)
	}
}

// TestA3EnsureCloudIntegrationSkipsConnectWhenAlreadyReady exercises the
// re-run half of contract A3: when a previously-saved connection id is
// already `ready`, ensureCloudIntegration MUST short-circuit instead of
// creating a fresh connect-session.
func TestA3EnsureCloudIntegrationSkipsConnectWhenAlreadyReady(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := saveIntegrationConnection(localDir, integrationConnectionState{
		Provider:     "notion",
		ConnectionID: "conn_existing",
		ConnectedAt:  time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("saveIntegrationConnection failed: %v", err)
	}

	var statusCalls, connectCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_demo/integrations/notion/status":
			atomic.AddInt32(&statusCalls, 1)
			if got := r.URL.Query().Get("connectionId"); got != "conn_existing" {
				t.Fatalf("expected status to use saved connection id, got %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/api/v1/workspaces/ws_demo/integrations/connect-session":
			atomic.AddInt32(&connectCalls, 1)
			t.Fatalf("connect-session should be skipped on re-run with ready integration")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := ensureCloudIntegration(server.URL, "ws_demo", "rf_token", "notion", localDir, 5*time.Second, false, &stdout); err != nil {
		t.Fatalf("ensureCloudIntegration failed: %v", err)
	}
	if atomic.LoadInt32(&statusCalls) != 1 {
		t.Fatalf("expected exactly one status check, got %d", statusCalls)
	}
	if atomic.LoadInt32(&connectCalls) != 0 {
		t.Fatalf("expected zero connect-session calls, got %d", connectCalls)
	}
	if !strings.Contains(stdout.String(), "notion already connected") {
		t.Fatalf("expected 'already connected' notice on re-run, got: %q", stdout.String())
	}
}

// TestProviderRootDirHandlesAllSlackVariants pins the mapping between
// catalog provider ids and the directory each provider occupies inside
// the local mirror. Drift here breaks status probes, integration disconnect
// cleanup, and the connect "files will appear under <localDir>/<root>"
// banner — so each Slack variant in fallbackIntegrationCatalog must be
// represented.
func TestProviderRootDirHandlesAllSlackVariants(t *testing.T) {
	cases := []struct {
		provider string
		want     string
	}{
		{"github", "github"},
		{"notion", "notion"},
		{"linear", "linear"},
		{"slack", "slack"},
		{"slack-sage", "slack"},
		{"slack-my-senior-dev", "slack-msd"},
		{"slack-nightcto", "slack-nightcto"},
	}
	for _, tc := range cases {
		if got := providerRootDir(tc.provider); got != tc.want {
			t.Fatalf("providerRootDir(%q)=%q, want %q", tc.provider, got, tc.want)
		}
	}

	// Cross-check against fallbackIntegrationCatalog so future catalog
	// additions cannot quietly leave providerRootDir behind. Every entry
	// in the catalog must round-trip: vfsRoot stripped of the leading
	// slash MUST equal providerRootDir of the same id.
	for _, entry := range fallbackIntegrationCatalog() {
		want := strings.TrimPrefix(entry.VFSRoot, "/")
		if got := providerRootDir(entry.ID); got != want {
			t.Fatalf("providerRootDir(%q)=%q, but catalog vfsRoot=%q", entry.ID, got, entry.VFSRoot)
		}
	}
}

// TestA13MountHelpListsSyncedMirrorLimitations exercises contract A13: the
// `relayfile mount --help` output must surface the §3.6 list of synced-
// mirror limitations so the user does not assume kernel filesystem
// semantics.
func TestA13MountHelpListsSyncedMirrorLimitations(t *testing.T) {
	var buf bytes.Buffer
	printMountHelp(&buf)
	got := buf.String()
	for _, fragment := range []string{
		"Synced-mirror limitations",
		"File handles are not stable",
		"mtime reflects the local write time",
		"Directory listings can briefly omit",
		"inotify/fsevents",
		"--mode poll|fuse",
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("expected mount --help to contain %q, got:\n%s", fragment, got)
		}
	}
}

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
