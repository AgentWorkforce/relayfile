package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

	path := agentRelayCloudAuthPath()
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
		t.Fatalf("rotated tokens were not written back: %+v", persisted.AccessTokenExpiresAt)
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
	if strings.Contains(err.Error(), minAgentRelayCLIVersion) {
		t.Fatalf("an expired login must not be reported as a CLI version problem, got: %v", err)
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
