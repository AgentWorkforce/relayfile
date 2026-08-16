package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// installBrokerShapedAgentRelayBin reproduces the environment every
// relay-spawned agent runs in: AGENT_RELAY_BIN points at agent-relay-broker,
// a binary that answers `--version` but rejects every agent-relay CLI
// subcommand. It also empties PATH of a real agent-relay, so any surviving
// shell-out fails loudly rather than silently succeeding on the developer's
// own installation.
func installBrokerShapedAgentRelayBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-relay-broker")
	// Version output and failure text copied from the real
	// agent-relay-broker: it satisfies the >= 8.7.0 check and then rejects
	// every agent-relay CLI subcommand, which is exactly why the old error
	// message blamed the CLI version.
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "agent-relay-broker 11.5.4"
  exit 0
fi
echo "error: unrecognized subcommand '$1'" >&2
exit 2
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write broker stub failed: %v", err)
	}
	t.Setenv("AGENT_RELAY_BIN", path)
	// An empty PATH means `agent-relay` cannot be found either, so a shell-out
	// cannot accidentally pass by reaching a real CLI on the test machine.
	t.Setenv("PATH", filepath.Join(dir, "empty"))
	return path
}

// MUST FIRE.
//
// Before this change relayfile resolved its cloud session by execing
// $AGENT_RELAY_BIN — relay's *broker* variable — so a valid, unexpired
// credential file was unreachable and a routine token expiry became an
// outage reported as "agent-relay CLI >= 8.7.0 required".
//
// This test fails on origin/main (11f8d98) with that error and passes here.
func TestCloudCredentialsIgnoreBrokerShapedAgentRelayBin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installBrokerShapedAgentRelayBin(t)
	writeAgentRelayCloudAuthForTest(t, "https://cloud.test", "cld_at_from_canonical_file")

	creds, err := cloudCredentialsFromAgentRelay()
	if err != nil {
		t.Fatalf("cloud credentials must resolve from the canonical file regardless of AGENT_RELAY_BIN: %v", err)
	}
	if creds.AccessToken != "cld_at_from_canonical_file" {
		t.Fatalf("unexpected access token: %q", creds.AccessToken)
	}
	if creds.APIURL != "https://cloud.test" {
		t.Fatalf("unexpected api url: %q", creds.APIURL)
	}
}

// MUST NOT FIRE.
//
// The paired negative: with the same broker-shaped AGENT_RELAY_BIN but no
// credential file and no CLOUD_API_* environment, resolution must still fail.
// Reading a file instead of running a CLI must not turn "not logged in" into
// success, and the error must name the credential file it looked for rather
// than blaming the agent-relay CLI version.
func TestCloudCredentialsStillFailWithoutACanonicalSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearRelayfileEnv(t)
	installBrokerShapedAgentRelayBin(t)

	_, err := cloudCredentialsFromAgentRelay()
	if err == nil {
		t.Fatal("expected an error when no cloud session exists")
	}
	if !errors.Is(err, ErrCloudRefreshExpired) {
		t.Fatalf("missing canonical session must require human action, got: %v", err)
	}
	if !strings.Contains(err.Error(), "cloud-auth.json") {
		t.Fatalf("error must name the credential file it looked for, got: %v", err)
	}
	if !strings.Contains(err.Error(), "agent-relay cloud login") {
		t.Fatalf("error must name the recovery command, got: %v", err)
	}
	if strings.Contains(err.Error(), minAgentRelayCLIVersion) {
		t.Fatalf("a missing login must not be reported as a CLI version problem, got: %v", err)
	}
}

// An incomplete credential file is a distinct failure from a missing one, and
// must not be silently treated as a session.
func TestCloudCredentialsRejectIncompleteCanonicalSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installBrokerShapedAgentRelayBin(t)

	path := mustAgentRelayCloudAuthPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"apiUrl":"https://cloud.test","accessToken":"cld_at_no_refresh"}`), 0o600); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	_, err := cloudCredentialsFromAgentRelay()
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("expected an incomplete-session error, got: %v", err)
	}
	if !errors.Is(err, ErrCloudRefreshExpired) {
		t.Fatalf("incomplete canonical session must require human action, got: %v", err)
	}
}

// The CLOUD_API_* environment is the documented non-interactive escape hatch
// and is read the same way relay's own readStoredAuth reads it.
func TestCloudCredentialsPreferCloudAPIEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installBrokerShapedAgentRelayBin(t)
	writeAgentRelayCloudAuthForTest(t, "https://file.test", "cld_at_from_file")

	t.Setenv("CLOUD_API_URL", "https://env.test")
	t.Setenv("CLOUD_API_ACCESS_TOKEN", "cld_at_from_env")
	t.Setenv("CLOUD_API_REFRESH_TOKEN", "cld_rt_from_env")
	t.Setenv("CLOUD_API_ACCESS_TOKEN_EXPIRES_AT", time.Now().Add(time.Hour).UTC().Format(time.RFC3339))

	creds, err := cloudCredentialsFromAgentRelay()
	if err != nil {
		t.Fatalf("cloudCredentialsFromAgentRelay failed: %v", err)
	}
	if creds.AccessToken != "cld_at_from_env" {
		t.Fatalf("expected the environment session to win, got %q", creds.AccessToken)
	}
}

// Auto-recovery is the point of the change: an access token inside its expiry
// window is rolled through relay's own refresh endpoint, and the rotated pair
// is written back so the next `agent-relay` command sees it. Keeping the
// rotated refresh token to ourselves would invalidate relay's copy.
func TestCloudCredentialsRefreshExpiredSessionAndPersistRotation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installBrokerShapedAgentRelayBin(t)

	var gotRefreshToken string
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/token/refresh" {
			t.Errorf("unexpected refresh path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requests++
		var body struct {
			RefreshToken string `json:"refreshToken"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode refresh body: %v", err)
		}
		gotRefreshToken = body.RefreshToken
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"accessToken":           "cld_at_rotated",
			"refreshToken":          "cld_rt_rotated",
			"accessTokenExpiresAt":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"refreshTokenExpiresAt": time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer server.Close()

	path := writeAgentRelayCloudAuthExpiringForTest(t, server.URL, "cld_at_expired", time.Now().Add(-time.Minute))

	creds, err := cloudCredentialsFromAgentRelay()
	if err != nil {
		t.Fatalf("expired session must auto-recover through refresh: %v", err)
	}
	if requests != 1 {
		t.Fatalf("expected exactly one refresh request, got %d", requests)
	}
	if gotRefreshToken != "cld_rt_test_refresh" {
		t.Fatalf("unexpected refresh token sent: %q", gotRefreshToken)
	}
	if creds.AccessToken != "cld_at_rotated" {
		t.Fatalf("expected the rotated access token, got %q", creds.AccessToken)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back credential file: %v", err)
	}
	var persisted agentRelayStoredAuth
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("parse credential file: %v", err)
	}
	if persisted.AccessToken != "cld_at_rotated" || persisted.RefreshToken != "cld_rt_rotated" {
		t.Fatalf("rotated tokens were not written back: accessToken=%q refreshToken=%q",
			persisted.AccessToken, persisted.RefreshToken)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("stat credential file: %v", err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credential file must stay 0600, got %o", perm)
	}
}

// A refused refresh is a real expiry, not a CLI problem, and must say so.
func TestCloudCredentialsReportExpiredLoginWhenRefreshRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installBrokerShapedAgentRelayBin(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	writeAgentRelayCloudAuthExpiringForTest(t, server.URL, "cld_at_expired", time.Now().Add(-time.Minute))

	_, err := cloudCredentialsFromAgentRelay()
	if err == nil || !strings.Contains(err.Error(), "agent-relay cloud login") {
		t.Fatalf("expected an expired-login error naming the recovery command, got: %v", err)
	}
	if !errors.Is(err, ErrCloudRefreshExpired) {
		t.Fatalf("rejected Cloud refresh must preserve the needs-human sentinel, got: %v", err)
	}
	reason := degradedStallReasonFor(err)
	if !strings.Contains(reason, "requires human action") || !strings.Contains(reason, "agent-relay cloud login") {
		t.Fatalf("expired Cloud session must name the required sign-in action, got: %s", reason)
	}
	if strings.Contains(reason, "relayfile will retry") {
		t.Fatalf("expired Cloud session must not claim automatic recovery, got: %s", reason)
	}
	if strings.Contains(err.Error(), minAgentRelayCLIVersion) {
		t.Fatalf("an expired login must not be reported as a CLI version problem, got: %v", err)
	}
	// A session supplied through CLOUD_API_* cannot run an interactive login,
	// and this branch cannot tell which source `auth` came from. Naming only
	// the interactive command misdirects non-interactive callers — the exact
	// class of defect this PR exists to remove. The missing-session branch
	// already names both paths; this one must too.
	if !strings.Contains(err.Error(), "CLOUD_API_*") {
		t.Fatalf("a rejected refresh must also name the non-interactive CLOUD_API_* alternative, got: %v", err)
	}
}

func TestCloudCredentialsTreatTransientRefreshFailureAsRetryable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installBrokerShapedAgentRelayBin(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	writeAgentRelayCloudAuthExpiringForTest(t, server.URL, "cld_at_expired", time.Now().Add(-time.Minute))

	_, err := cloudCredentialsFromAgentRelay()
	if err == nil {
		t.Fatal("expected transient Cloud refresh failure")
	}
	if errors.Is(err, ErrCloudRefreshExpired) {
		t.Fatalf("HTTP 503 must not be classified as requiring a new sign-in: %v", err)
	}
	reason := degradedStallReasonFor(err)
	if !strings.Contains(reason, "is retryable") || strings.Contains(reason, "requires human action") {
		t.Fatalf("HTTP 503 must remain retryable, got: %s", reason)
	}
}

// MUST FIRE.
//
// The name collision itself: AGENT_RELAY_BIN belongs to relay's broker, and
// relayfile must not read it for any purpose. Fails on origin/main, where
// agentRelayBinary() returned the broker path.
func TestAgentRelayBinaryNeverResolvesFromBrokerEnvVar(t *testing.T) {
	clearRelayfileEnv(t)
	t.Setenv("AGENT_RELAY_BIN", "/usr/local/bin/agent-relay-broker")

	bin, origin := agentRelayBinary()
	if bin != "agent-relay" {
		t.Fatalf("AGENT_RELAY_BIN must not select relayfile's CLI, got %q", bin)
	}
	if origin != "PATH" {
		t.Fatalf("unexpected origin: %q", origin)
	}
}

// MUST NOT FIRE: relayfile's own override still works, so operators keep a
// supported way to point at a non-PATH CLI build.
func TestAgentRelayBinaryHonoursRelayfileOverride(t *testing.T) {
	clearRelayfileEnv(t)
	t.Setenv("AGENT_RELAY_BIN", "/usr/local/bin/agent-relay-broker")
	t.Setenv(agentRelayCLIOverrideEnv, "/opt/agent-relay/bin/agent-relay")

	bin, origin := agentRelayBinary()
	if bin != "/opt/agent-relay/bin/agent-relay" {
		t.Fatalf("unexpected binary: %q", bin)
	}
	if origin != agentRelayCLIOverrideEnv {
		t.Fatalf("unexpected origin: %q", origin)
	}
}

// The probe error must name the argv it ran, the binary it ran it with, and
// how that binary was chosen — the information the old "agent-relay CLI >=
// 8.7.0 required" message withheld while the real cause was a wrong binary.
func TestAgentRelayCLIProbeErrorNamesWhatWasProbed(t *testing.T) {
	clearRelayfileEnv(t)
	broker := installBrokerShapedAgentRelayBin(t)
	t.Setenv(agentRelayCLIOverrideEnv, broker)

	err := ensureAgentRelayCLICompatible()
	if err == nil {
		t.Fatal("expected the broker stub to fail the CLI probe")
	}
	for _, want := range []string{
		"agent-relay workspace active --help",
		broker,
		agentRelayCLIOverrideEnv,
		"unrecognized subcommand",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("probe error must mention %q, got: %v", want, err)
		}
	}
}

// The CLI probe must no longer require a `cloud` subcommand: relayfile stopped
// asking the CLI for its cloud session, so a CLI without `cloud` is fine.
func TestAgentRelayCLIProbeDoesNotRequireCloudSubcommand(t *testing.T) {
	clearRelayfileEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-relay")
	script := `#!/bin/sh
if [ "$*" = "--version" ]; then
  echo "8.7.0"
  exit 0
fi
if [ "$*" = "workspace active --help" ] || [ "$*" = "workspace switch --help" ]; then
  exit 0
fi
echo "error: unrecognized subcommand '$1'" >&2
exit 2
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub failed: %v", err)
	}
	t.Setenv(agentRelayCLIOverrideEnv, path)

	if err := ensureAgentRelayCLICompatible(); err != nil {
		t.Fatalf("a CLI without `cloud` must still pass the probe: %v", err)
	}
}

func TestEnsureAgentRelayCloudSessionReportsItsSource(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installBrokerShapedAgentRelayBin(t)
	writeAgentRelayCloudAuthForTest(t, "https://cloud.test", "cld_at_file")

	_, source, err := ensureAgentRelayCloudSession(context.Background())
	if err != nil {
		t.Fatalf("ensureAgentRelayCloudSession failed: %v", err)
	}
	if source != agentRelayCloudSessionFromFile {
		t.Fatalf("unexpected source: %q", source)
	}
}

// MUST FIRE — the lost-update defect three reviewers converged on.
//
// The double-check after acquiring the lock exists because another process may
// have refreshed while we waited. Before the fix, the re-read value was used
// only to return early; the refresh itself still presented the PRE-LOCK copy's
// refresh token. Since Cloud rotates the refresh token on every refresh, that
// token is already dead by then, so the second refresher would fail — and if it
// somehow succeeded, it would overwrite the first refresher's rotation.
//
// This drives the real ordering: the fixture makes the file rotate underneath
// the caller while it waits for the lock, and the refresh endpoint rejects any
// refresh token that is not the newest one on disk — exactly as Cloud does.
func TestCloudSessionRefreshAdoptsARotationThatLandedWhileWaiting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installBrokerShapedAgentRelayBin(t)

	var mu sync.Mutex
	// The only refresh token Cloud still accepts. Rotating invalidates the old.
	liveRefreshToken := "cld_rt_test_refresh"
	var rejected []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refreshToken"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode refresh body: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if body.RefreshToken != liveRefreshToken {
			// Presenting a rotated-away refresh token is exactly what Cloud
			// refuses, and what the pre-lock copy would have presented.
			rejected = append(rejected, body.RefreshToken)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		liveRefreshToken = "cld_rt_second_rotation"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"accessToken":           "cld_at_second_rotation",
			"refreshToken":          liveRefreshToken,
			"accessTokenExpiresAt":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"refreshTokenExpiresAt": time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer server.Close()

	// The expired session this process will read.
	path := writeAgentRelayCloudAuthExpiringForTest(t, server.URL, "cld_at_expired", time.Now().Add(-time.Minute))

	// Ordering is the whole test, so it is spelled out rather than slept
	// through loosely:
	//
	//  1. take the lock, standing in for the other process that is refreshing;
	//  2. start the caller — it reads the STALE session and then blocks on the
	//     lock, which is the only window in which a lost update can occur;
	//  3. rotate the file, so the caller's pre-lock copy is now invalid;
	//  4. release, so the caller re-reads and must choose which copy to use.
	//
	// Rotating before step 2 is what made an earlier version of this test pass
	// against the unfixed code: the caller's first read already saw the rotated
	// file, so it never held a stale copy at all.
	release, err := acquireAgentRelayAuthLock(context.Background())
	if err != nil {
		t.Fatalf("seed lock: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, err := ensureAgentRelayCloudSession(context.Background())
		done <- err
	}()

	// Let the caller complete its pre-lock read and settle into the lock wait.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	liveRefreshToken = "cld_rt_first_rotation"
	mu.Unlock()
	if err := os.WriteFile(path, mustJSON(t, agentRelayStoredAuth{
		APIURL:       server.URL,
		AccessToken:  "cld_at_first_rotation",
		RefreshToken: "cld_rt_first_rotation",
		// Still inside the refresh window, so the caller must refresh rather
		// than simply return this record — which is what forces it to choose
		// between the stale copy and the re-read one.
		AccessTokenExpiresAt:  time.Now().Add(-time.Second).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339),
	}), 0o600); err != nil {
		t.Fatalf("rotate file under the lock: %v", err)
	}

	release()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("refresh must adopt the rotated session on disk, not the pre-lock copy: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ensureAgentRelayCloudSession did not return")
	}

	mu.Lock()
	gotRejected := append([]string(nil), rejected...)
	mu.Unlock()
	if len(gotRejected) != 0 {
		t.Fatalf("a stale refresh token was presented to Cloud: %q", gotRejected)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back credential file: %v", err)
	}
	var persisted agentRelayStoredAuth
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("parse credential file: %v", err)
	}
	if persisted.AccessToken != "cld_at_second_rotation" || persisted.RefreshToken != "cld_rt_second_rotation" {
		t.Fatalf("the adopted rotation was not persisted: accessToken=%q refreshToken=%q",
			persisted.AccessToken, persisted.RefreshToken)
	}
}

// MUST NOT FIRE: adopting the re-read session must not defeat the early return.
// When the session on disk is already fresh, no refresh may be issued at all.
func TestCloudSessionSkipsRefreshWhenTheLockWaitProducedAFreshSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installBrokerShapedAgentRelayBin(t)

	refreshes := 0
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		refreshes++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	path := writeAgentRelayCloudAuthExpiringForTest(t, server.URL, "cld_at_expired", time.Now().Add(-time.Minute))

	release, err := acquireAgentRelayAuthLock(context.Background())
	if err != nil {
		t.Fatalf("seed lock: %v", err)
	}
	if err := os.WriteFile(path, mustJSON(t, agentRelayStoredAuth{
		APIURL:                server.URL,
		AccessToken:           "cld_at_already_fresh",
		RefreshToken:          "cld_rt_already_fresh",
		AccessTokenExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339),
	}), 0o600); err != nil {
		t.Fatalf("rotate file under the lock: %v", err)
	}

	type result struct {
		auth agentRelayStoredAuth
		err  error
	}
	done := make(chan result, 1)
	go func() {
		auth, _, err := ensureAgentRelayCloudSession(context.Background())
		done <- result{auth, err}
	}()
	time.Sleep(150 * time.Millisecond)
	release()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("a fresh session on disk must be adopted without refreshing: %v", got.err)
		}
		if got.auth.AccessToken != "cld_at_already_fresh" {
			t.Fatalf("unexpected access token: %q", got.auth.AccessToken)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ensureAgentRelayCloudSession did not return")
	}

	mu.Lock()
	defer mu.Unlock()
	if refreshes != 0 {
		t.Fatalf("expected no refresh request, got %d", refreshes)
	}
}

// The stale-lock window must stay strictly longer than the refresh timeout, or
// a live holder waiting on its HTTP call looks dead and has its lock stolen.
// relay keeps a 3x margin (10s refresh vs 30s stale); this pins ours to the
// same relationship so the two implementations cannot steal from each other.
func TestAuthLockStaleWindowExceedsTheRefreshTimeout(t *testing.T) {
	if agentRelayAuthLockStaleAfter <= agentRelayCloudRefreshTimeout {
		t.Fatalf("stale window %v must exceed the refresh timeout %v",
			agentRelayAuthLockStaleAfter, agentRelayCloudRefreshTimeout)
	}
}

// CI commonly exports an access token and nothing else. That shape is a valid
// session per packages/agents/src/connect.ts, and must not be rejected or fall
// through to an unrelated credential file.
func TestCloudCredentialsAcceptAccessTokenOnlyEnvironment(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installBrokerShapedAgentRelayBin(t)
	// A file that must NOT be consulted: the environment wins.
	writeAgentRelayCloudAuthForTest(t, "https://file.test", "cld_at_from_file")
	t.Setenv("CLOUD_API_ACCESS_TOKEN", "cld_at_ci_only")

	creds, err := cloudCredentialsFromAgentRelay()
	if err != nil {
		t.Fatalf("an access-token-only environment must be a usable session: %v", err)
	}
	if creds.AccessToken != "cld_at_ci_only" {
		t.Fatalf("unexpected access token: %q", creds.AccessToken)
	}
	if creds.APIURL != defaultCloudAPIURL {
		t.Fatalf("expected the default cloud API URL, got %q", creds.APIURL)
	}
}

// MUST NOT FIRE: with no refresh token there is nothing to roll, so relayfile
// must never attempt a refresh — doing so would fail with an empty credential
// and turn a working CI session into an error.
func TestAccessTokenOnlyEnvironmentNeverAttemptsARefresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installBrokerShapedAgentRelayBin(t)

	refreshes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshes++
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	t.Setenv("CLOUD_API_URL", server.URL)
	t.Setenv("CLOUD_API_ACCESS_TOKEN", "cld_at_ci_only")
	// Already past expiry: with a refresh token this would refresh; without
	// one it must be used as-is.
	t.Setenv("CLOUD_API_ACCESS_TOKEN_EXPIRES_AT", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339))

	creds, err := cloudCredentialsFromAgentRelay()
	if err != nil {
		t.Fatalf("access-token-only session must be used as-is: %v", err)
	}
	if creds.AccessToken != "cld_at_ci_only" {
		t.Fatalf("unexpected access token: %q", creds.AccessToken)
	}
	if refreshes != 0 {
		t.Fatalf("expected no refresh attempt without a refresh token, got %d", refreshes)
	}
}

// A rotated refresh token must not inherit the previous token's expiry. Doing
// so re-arms the 24-hour refresh-token window immediately, so every subsequent
// command rotates again.
func TestRefreshDoesNotInheritAStaleRefreshTokenExpiry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installBrokerShapedAgentRelayBin(t)

	refreshes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		refreshes++
		w.Header().Set("Content-Type", "application/json")
		// Server omits the optional refreshTokenExpiresAt.
		_ = json.NewEncoder(w).Encode(map[string]string{
			"accessToken":          "cld_at_rotated",
			"refreshToken":         "cld_rt_rotated",
			"accessTokenExpiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer server.Close()

	path := mustAgentRelayCloudAuthPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	// Refresh token within its 24-hour window: this is what triggers the
	// refresh, and what would be inherited onto the rotated token.
	if err := os.WriteFile(path, mustJSON(t, agentRelayStoredAuth{
		APIURL:                server.URL,
		AccessToken:           "cld_at_ok",
		RefreshToken:          "cld_rt_near_expiry",
		AccessTokenExpiresAt:  time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}), 0o600); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	if _, err := cloudCredentialsFromAgentRelay(); err != nil {
		t.Fatalf("first resolve failed: %v", err)
	}
	if refreshes != 1 {
		t.Fatalf("expected one refresh, got %d", refreshes)
	}
	// The rotated session must now be stable: a second resolve must not rotate
	// again just because the old token was near expiry.
	if _, err := cloudCredentialsFromAgentRelay(); err != nil {
		t.Fatalf("second resolve failed: %v", err)
	}
	if refreshes != 1 {
		t.Fatalf("rotated session re-refreshed: expected 1 refresh, got %d", refreshes)
	}
}

// A relative credential path would read from, and write rotated tokens into,
// the process's working directory — usually a repository — and be invisible to
// agent-relay. Fail instead.
func TestCloudAuthPathFailsRatherThanFallingBackToARelativePath(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	path, err := agentRelayCloudAuthPath()
	if err == nil {
		if filepath.IsAbs(path) {
			t.Skipf("this platform resolved a home directory without HOME (%q)", path)
		}
		t.Fatalf("expected an error, got relative path %q", path)
	}
	if !strings.Contains(err.Error(), "Agent Relay cloud session") {
		t.Fatalf("error should name what could not be located, got: %v", err)
	}
}

func mustJSON(t *testing.T, auth agentRelayStoredAuth) []byte {
	t.Helper()
	data, err := json.Marshal(auth)
	if err != nil {
		t.Fatalf("marshal auth: %v", err)
	}
	return append(data, '\n')
}
