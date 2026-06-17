package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/delegatedauth"
)

func TestWorkspaceCreateStoresCatalogEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveCredentials(credentials{
		Server: defaultServerURL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "create", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace create failed: %v", err)
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		t.Fatalf("loadWorkspaceCatalog failed: %v", err)
	}
	if len(catalog.Workspaces) != 1 {
		t.Fatalf("expected 1 workspace, got %d", len(catalog.Workspaces))
	}
	if catalog.Workspaces[0].Name != "demo" {
		t.Fatalf("expected workspace demo, got %q", catalog.Workspaces[0].Name)
	}
	if catalog.Workspaces[0].ID != "demo" {
		t.Fatalf("expected workspace id demo, got %q", catalog.Workspaces[0].ID)
	}
}

func TestResolveServerDefaultsToHostedRelayfile(t *testing.T) {
	clearRelayfileEnv(t)

	if got := resolveServer("", credentials{}); got != "https://api.relayfile.dev" {
		t.Fatalf("expected hosted Relayfile default server, got %q", got)
	}
}

func TestEnforcePollIntervalFloor(t *testing.T) {
	if got := enforcePollIntervalFloor(time.Second); got != minMountPollInterval {
		t.Fatalf("expected interval floor %s, got %s", minMountPollInterval, got)
	}
	if got := enforcePollIntervalFloor(defaultMountInterval); got != defaultMountInterval {
		t.Fatalf("expected default interval passthrough, got %s", got)
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

func TestObserverPrintsFragmentLaunchURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	token := testJWTWithWorkspace("ws_observer")
	t.Setenv("RELAYFILE_TOKEN", token)
	t.Setenv("RELAYFILE_SERVER", "https://api.example.test")

	var stdout bytes.Buffer
	if err := run([]string{"observer", "--no-open", "--url", "https://files.example.test/app"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run observer failed: %v", err)
	}

	launchURL := strings.TrimSpace(stdout.String())
	parsed, err := url.Parse(launchURL)
	if err != nil {
		t.Fatalf("parse observer url failed: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "files.example.test" || parsed.Path != "/app" {
		t.Fatalf("unexpected observer url: %s", parsed.String())
	}
	fragment := parseBrowserFragment(t, launchURL)
	if fragment.Get("baseUrl") != "https://api.example.test" {
		t.Fatalf("expected baseUrl fragment, got %q", fragment.Get("baseUrl"))
	}
	if fragment.Get("workspaceId") != "ws_observer" {
		t.Fatalf("expected workspaceId fragment, got %q", fragment.Get("workspaceId"))
	}
	if fragment.Get("token") != token {
		t.Fatalf("expected token fragment to match")
	}
}

func TestBuildObserverURLDefaultsToHostedRouterPath(t *testing.T) {
	got, err := buildObserverURL("", "https://api.example.test", "test-token", "ws_observer")
	if err != nil {
		t.Fatalf("build observer url failed: %v", err)
	}

	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse observer url failed: %v", err)
	}
	if parsed.Scheme != "https" || parsed.Host != "agentrelay.com" || parsed.Path != "/observer/file" {
		t.Fatalf("unexpected default observer url: %s", parsed.String())
	}

	fragment := parseBrowserFragment(t, got)
	if fragment.Get("baseUrl") != "https://api.example.test" {
		t.Fatalf("expected browser-decoded baseUrl fragment, got %q", fragment.Get("baseUrl"))
	}
}

func parseBrowserFragment(t *testing.T, rawURL string) url.Values {
	t.Helper()

	_, rawFragment, ok := strings.Cut(rawURL, "#")
	if !ok {
		t.Fatalf("expected observer url to include a fragment: %s", rawURL)
	}
	if strings.Contains(rawFragment, "%25") {
		t.Fatalf("observer fragment appears double-encoded: %s", rawFragment)
	}
	fragment, err := url.ParseQuery(rawFragment)
	if err != nil {
		t.Fatalf("parse observer fragment failed: %v", err)
	}
	return fragment
}

func TestHelpFlagPrintsUsageForCommandsAndSubcommands(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "root short", args: []string{"-h"}, want: "relayfile is the RelayFile CLI."},
		{name: "root long", args: []string{"--help"}, want: "relayfile is the RelayFile CLI."},
		{name: "setup", args: []string{"setup", "-h"}, want: "Usage: relayfile setup"},
		{name: "login", args: []string{"login", "-h"}, want: "Usage: relayfile login"},
		{name: "workspace group", args: []string{"workspace", "-h"}, want: "relayfile workspace create NAME"},
		{name: "workspace create", args: []string{"workspace", "create", "-h"}, want: "Usage: relayfile workspace create NAME"},
		{name: "workspace join", args: []string{"workspace", "join", "-h"}, want: "Usage: relayfile workspace join WORKSPACE_ID"},
		{name: "workspace use", args: []string{"workspace", "use", "-h"}, want: "Usage: relayfile workspace use NAME"},
		{name: "workspace list", args: []string{"workspace", "list", "-h"}, want: "Usage: relayfile workspace list"},
		{name: "workspace current", args: []string{"workspace", "current", "-h"}, want: "Usage: relayfile workspace current"},
		{name: "workspace delete", args: []string{"workspace", "delete", "-h"}, want: "Usage: relayfile workspace delete NAME"},
		{name: "integration group", args: []string{"integration", "-h"}, want: "relayfile integration search QUERY"},
		{name: "integration connect", args: []string{"integration", "connect", "-h"}, want: "Usage: relayfile integration connect PROVIDER"},
		{name: "integration available", args: []string{"integration", "available", "-h"}, want: "Usage: relayfile integration available"},
		{name: "integration catalog alias", args: []string{"integration", "catalog", "-h"}, want: "Usage: relayfile integration available"},
		{name: "integration providers alias", args: []string{"integration", "providers", "-h"}, want: "Usage: relayfile integration available"},
		{name: "integration search", args: []string{"integration", "search", "-h"}, want: "Usage: relayfile integration search QUERY"},
		{name: "integration search after query", args: []string{"integration", "search", "docker", "-h"}, want: "Usage: relayfile integration search QUERY"},
		{name: "integration list", args: []string{"integration", "list", "-h"}, want: "Usage: relayfile integration list"},
		{name: "integration disconnect", args: []string{"integration", "disconnect", "-h"}, want: "Usage: relayfile integration disconnect PROVIDER"},
		{name: "integration adopt", args: []string{"integration", "adopt", "-h"}, want: "Usage: relayfile integration adopt PROVIDER"},
		{name: "integration set metadata", args: []string{"integration", "set-metadata", "-h"}, want: "Usage: relayfile integration set-metadata PROVIDER"},
		{name: "ops group", args: []string{"ops", "-h"}, want: "relayfile ops replay OPID"},
		{name: "ops list", args: []string{"ops", "list", "-h"}, want: "Usage: relayfile ops list"},
		{name: "ops replay", args: []string{"ops", "replay", "-h"}, want: "Usage: relayfile ops replay OPID"},
		{name: "writeback group", args: []string{"writeback", "-h"}, want: "relayfile writeback retry --opId OP"},
		{name: "writeback list", args: []string{"writeback", "list", "-h"}, want: writebackListUsage},
		{name: "writeback status", args: []string{"writeback", "status", "-h"}, want: "Usage: relayfile writeback status"},
		{name: "writeback update", args: []string{"writeback", "update", "-h"}, want: "Usage: relayfile writeback update"},
		{name: "writeback delete", args: []string{"writeback", "delete", "-h"}, want: "Usage: relayfile writeback delete"},
		{name: "writeback retry", args: []string{"writeback", "retry", "-h"}, want: "Usage: relayfile writeback retry --opId OP"},
		{name: "digest group", args: []string{"digest", "-h"}, want: "today|yesterday|YYYY-MM-DD|this-week|last-week"},
		{name: "digest rebuild", args: []string{"digest", "rebuild", "-h"}, want: digestRebuildUsage},
		{name: "pull", args: []string{"pull", "-h"}, want: "Usage: relayfile pull"},
		{name: "mount", args: []string{"mount", "-h"}, want: "Usage: relayfile mount"},
		{name: "start alias", args: []string{"start", "-h"}, want: "Usage: relayfile mount"},
		{name: "restart", args: []string{"restart", "-h"}, want: "Usage: relayfile restart"},
		{name: "tree", args: []string{"tree", "-h"}, want: "Usage: relayfile tree"},
		{name: "ls alias", args: []string{"ls", "-h"}, want: "Usage: relayfile tree"},
		{name: "read", args: []string{"read", "-h"}, want: "Usage: relayfile read"},
		{name: "cat alias", args: []string{"cat", "-h"}, want: "Usage: relayfile read"},
		{name: "seed", args: []string{"seed", "-h"}, want: "Usage: relayfile seed"},
		{name: "export", args: []string{"export", "-h"}, want: "Usage: relayfile export"},
		{name: "status", args: []string{"status", "-h"}, want: "Usage: relayfile status"},
		{name: "stop", args: []string{"stop", "-h"}, want: "Usage: relayfile stop"},
		{name: "logs", args: []string{"logs", "-h"}, want: "Usage: relayfile logs"},
		{name: "observer", args: []string{"observer", "-h"}, want: "Usage: relayfile observer"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			clearRelayfileEnv(t)

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if err := run(tc.args, strings.NewReader(""), &stdout, &stderr); err != nil {
				t.Fatalf("run(%v) returned error: %v\nstdout:\n%s\nstderr:\n%s", tc.args, err, stdout.String(), stderr.String())
			}
			if got := stdout.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("expected help output to contain %q, got:\n%s", tc.want, got)
			}
			if got := stderr.String(); got != "" {
				t.Fatalf("expected no stderr for help, got %q", got)
			}
		})
	}
}

func TestWorkspaceRequiresSubcommandMentionsJoin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var stdout bytes.Buffer
	err := run([]string{"workspace"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatal("expected workspace without subcommand to fail")
	}
	if got := err.Error(); !strings.Contains(got, "create, join, use, list, current, status, or delete") {
		t.Fatalf("workspace subcommand error = %q", got)
	}
}

func TestWorkspaceUseSetsDefaultWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	logPath := filepath.Join(t.TempDir(), "agent-relay.log")
	t.Setenv("AGENT_RELAY_LOG", logPath)
	installFakeAgentRelay(t, `
printf '%s\n' "$*" >> "$AGENT_RELAY_LOG"
if [ "$*" = "workspace switch ws_cloud" ]; then
  echo "agent-relay workspace switch ok"
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`)

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "use", "ws_cloud"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace use failed: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake agent-relay log failed: %v", err)
	}
	if strings.TrimSpace(string(logBytes)) != "workspace switch ws_cloud" {
		t.Fatalf("expected agent-relay workspace switch call, got %q", string(logBytes))
	}
	if got := stdout.String(); !strings.Contains(got, "Relayfile uses the active agent-relay workspace") {
		t.Fatalf("unexpected workspace use output: %q", got)
	}
}

func TestWorkspaceJoinStoresDelegatedRelayfileCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var seenBootstrap bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/ws_cloud/relayfile/delegated-token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		seenBootstrap = true
		if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
			t.Fatalf("unexpected bootstrap Authorization: %q", got)
		}
		var body cloudRelayfileDelegatedTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode bootstrap body: %v", err)
		}
		if got, want := strings.Join(body.Scopes, ","), "relayfile:fs:read:*"; got != want {
			t.Fatalf("bootstrap scopes = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		writeDelegatedBundleResponse(t, w, "https://relayfile.test", "ws_cloud", "rf_read", "refresh_read")
	}))
	defer server.Close()
	installFakeAgentRelaySession(t, server.URL, "cld_access", "cloud-prod", "ws_cloud", "ws_cloud")

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "join", "ws_cloud", "--name", "cloud-prod", "--cloud-api-url", server.URL}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace join failed: %v", err)
	}
	if !seenBootstrap {
		t.Fatalf("expected delegated-token bootstrap call")
	}
	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected workspace join not to persist relayfile token credentials, got err=%v", err)
	}
	delegated, err := delegatedauth.Load(delegatedCredentialsPathForRequest("ws_cloud", defaultInspectScopes))
	if err != nil {
		t.Fatalf("expected delegated credentials to be stored: %v", err)
	}
	if delegated.BearerToken() != "rf_read" || delegated.RotationToken() != "refresh_read" {
		t.Fatalf("unexpected delegated credentials: %#v", delegated)
	}
	record, ok := workspaceRecordByID("ws_cloud")
	if !ok {
		t.Fatalf("expected workspace catalog record")
	}
	if record.Name != "cloud-prod" || strings.Join(record.Scopes, ",") != "relayfile:fs:read:*" {
		t.Fatalf("unexpected workspace record: %#v", record)
	}
	if got := stdout.String(); !strings.Contains(got, "Joined workspace cloud-prod") {
		t.Fatalf("unexpected stdout: %q", got)
	}
}

func TestTreeJoinsCloudWorkspaceWithoutServerCredentials(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:             "cloud-prod",
		ID:               "ws_cloud",
		RelayWorkspaceID: "rw_cloud",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		AgentName:        "relayfile-cli",
		Scopes:           append([]string(nil), defaultJoinScopes...),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workspaces/rw_cloud/fs/tree":
			if got := r.Header.Get("Authorization"); got != "Bearer rf_delegated" {
				t.Fatalf("unexpected tree Authorization: %q", got)
			}
			if got := r.URL.Query().Get("path"); got != "/google-mail" {
				t.Fatalf("unexpected tree path: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"path":"/google-mail","entries":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayfileWorkspaceID:  "rw_cloud",
		AccessToken:           "rf_delegated",
		RefreshToken:          "refresh_delegated",
		AccessTokenExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "ws_cloud", "/google-mail"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "Tree /google-mail") {
		t.Fatalf("unexpected stdout: %q", got)
	}
}

func TestTreeRefreshesDelegatedWorkspaceTokenAndRetriesCanonicalWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	oldToken := testJWTWithWorkspaceAgentAndExpiry("ws_cloud", "old", time.Now().Add(-time.Minute))
	newToken := testJWTWithWorkspaceAndAgent("rw_cloud", "new")
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:             "cloud-prod",
		ID:               "ws_cloud",
		RelayWorkspaceID: "rw_cloud",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		AgentName:        "relayfile-cli",
		Scopes:           append([]string(nil), defaultInspectScopes...),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if _, err := setDefaultWorkspace("cloud-prod"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}

	var refreshCount int
	var treeCount int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			refreshCount++
			var body struct {
				RefreshToken string `json:"refreshToken"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode refresh body: %v", err)
			}
			if body.RefreshToken != "refresh_old" {
				t.Fatalf("unexpected refresh token %q", body.RefreshToken)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"accessToken":           newToken,
				"refreshToken":          "refresh_new",
				"accessTokenExpiresAt":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				"refreshTokenExpiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			})
		case "/v1/workspaces/rw_cloud/fs/tree":
			treeCount++
			if got := r.Header.Get("Authorization"); got != "Bearer "+newToken {
				t.Fatalf("unexpected refreshed tree Authorization: %q", got)
			}
			if got := r.URL.Query().Get("path"); got != "/google-mail" {
				t.Fatalf("unexpected tree path: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"path":"/google-mail","entries":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayfileWorkspaceID:  "rw_cloud",
		AccessToken:           oldToken,
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "cloud-prod", "/google-mail"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}
	if refreshCount != 1 || treeCount != 1 {
		t.Fatalf("refreshCount/treeCount = %d/%d, want 1/1", refreshCount, treeCount)
	}
	record, ok := workspaceRecordByName("cloud-prod")
	if !ok {
		t.Fatalf("expected refreshed workspace catalog record")
	}
	if record.ID != "ws_cloud" {
		t.Fatalf("workspace record ID = %q, want ws_cloud", record.ID)
	}
	if record.RelayWorkspaceID != "rw_cloud" {
		t.Fatalf("relay workspace ID = %q, want rw_cloud", record.RelayWorkspaceID)
	}
	if record.Server != server.URL {
		t.Fatalf("workspace record Server = %q, want %q", record.Server, server.URL)
	}
	if record.CloudAPIURL != "" {
		t.Fatalf("workspace record CloudAPIURL = %q, want empty", record.CloudAPIURL)
	}
	if record.LastUsedAt == "" {
		t.Fatal("workspace record LastUsedAt was not refreshed")
	}
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		t.Fatalf("loadWorkspaceCatalog failed: %v", err)
	}
	if catalog.Default != "cloud-prod" {
		t.Fatalf("default workspace = %q, want cloud-prod", catalog.Default)
	}
	if got := stdout.String(); !strings.Contains(got, "Tree /google-mail") {
		t.Fatalf("unexpected stdout: %q", got)
	}
}

func TestReadRefreshesDelegatedWorkspaceTokenAfterUnauthorized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	oldToken := testJWTWithWorkspaceAgentAndExpiry("rw_cloud", "old", time.Now().Add(-time.Minute))
	newToken := testJWTWithWorkspaceAndAgent("rw_cloud", "new")
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:             "cloud-prod",
		ID:               "ws_cloud",
		RelayWorkspaceID: "rw_cloud",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		AgentName:        "relayfile-cli",
		Scopes:           append([]string(nil), defaultInspectScopes...),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:      "other-prod",
		ID:        "ws_other",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails(other) failed: %v", err)
	}
	if _, err := setDefaultWorkspace("other-prod"); err != nil {
		t.Fatalf("setDefaultWorkspace(other) failed: %v", err)
	}

	var readCount int
	var refreshCount int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			refreshCount++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"accessToken":           newToken,
				"refreshToken":          "refresh_new",
				"accessTokenExpiresAt":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				"refreshTokenExpiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			})
		case "/v1/workspaces/rw_cloud/fs/file":
			readCount++
			if got := r.Header.Get("Authorization"); got != "Bearer "+newToken {
				t.Fatalf("unexpected refreshed read Authorization: %q", got)
			}
			if got := r.URL.Query().Get("path"); got != "/google-mail/msg.json" {
				t.Fatalf("unexpected read path: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"path":"/google-mail/msg.json","revision":"1","contentType":"application/json","content":"ok"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayfileWorkspaceID:  "rw_cloud",
		AccessToken:           oldToken,
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	})

	var stdout bytes.Buffer
	if err := run([]string{"read", "cloud-prod", "/google-mail/msg.json"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run read failed: %v", err)
	}
	if refreshCount != 1 || readCount != 1 {
		t.Fatalf("refreshCount/readCount = %d/%d, want 1/1", refreshCount, readCount)
	}
	record, ok := workspaceRecordByName("cloud-prod")
	if !ok {
		t.Fatalf("expected refreshed workspace catalog record")
	}
	if record.ID != "ws_cloud" {
		t.Fatalf("workspace record ID = %q, want ws_cloud", record.ID)
	}
	if record.RelayWorkspaceID != "rw_cloud" {
		t.Fatalf("relay workspace ID = %q, want rw_cloud", record.RelayWorkspaceID)
	}
	if record.Server != server.URL {
		t.Fatalf("workspace record Server = %q, want %q", record.Server, server.URL)
	}
	if record.CloudAPIURL != "" {
		t.Fatalf("workspace record CloudAPIURL = %q, want empty", record.CloudAPIURL)
	}
	if record.LastUsedAt == "" {
		t.Fatal("workspace record LastUsedAt was not refreshed")
	}
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		t.Fatalf("loadWorkspaceCatalog failed: %v", err)
	}
	if catalog.Default != "other-prod" {
		t.Fatalf("default workspace = %q, want other-prod", catalog.Default)
	}
	if got := stdout.String(); got != "ok" {
		t.Fatalf("unexpected stdout: %q", got)
	}
}

func TestWorkspaceDeleteRemovesCatalogEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if _, err := upsertWorkspace("demo"); err != nil {
		t.Fatalf("upsertWorkspace failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "delete", "demo", "--yes"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace delete failed: %v", err)
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		t.Fatalf("loadWorkspaceCatalog failed: %v", err)
	}
	if len(catalog.Workspaces) != 0 {
		t.Fatalf("expected workspace catalog to be empty, got %d entries", len(catalog.Workspaces))
	}
}

func TestWorkspaceListIncludesEnvWorkspaceWhenRemoteListUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_WORKSPACE", "ws_env")
	if _, err := upsertWorkspace(".relay/vfs"); err != nil {
		t.Fatalf("upsertWorkspace .relay/vfs failed: %v", err)
	}
	if _, err := upsertWorkspace("relay-test"); err != nil {
		t.Fatalf("upsertWorkspace relay-test failed: %v", err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "list"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace list failed: %v", err)
	}

	if got := strings.TrimSpace(stdout.String()); got != "* ws_env\n  .relay/vfs\n  relay-test" {
		t.Fatalf("unexpected workspace list output: %q", got)
	}
}

func TestWorkspaceListIncludesTokenWorkspaceWhenRemoteListUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  testJWTWithWorkspace("ws_token"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "list"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace list failed: %v", err)
	}

	if got := strings.TrimSpace(stdout.String()); got != "* ws_token" {
		t.Fatalf("unexpected workspace list output: %q", got)
	}
}

func TestWorkspaceListFallsBackToAdminSync(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/admin/workspaces":
			http.NotFound(w, r)
		case "/v1/admin/sync":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"workspaceIds":["ws_bravo","ws_alpha"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "list"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace list failed: %v", err)
	}

	if got := strings.TrimSpace(stdout.String()); got != "ws_alpha\nws_bravo" {
		t.Fatalf("unexpected workspace list output: %q", got)
	}
}

func TestTreeUsesEnvWorkspaceWhenWorkspaceArgOmitted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_WORKSPACE", "ws_env")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_env/fs/tree" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/github" {
			t.Fatalf("expected path /github, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/github","entries":[],"nextCursor":null}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_env",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "/github"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}

	if got := stdout.String(); !strings.Contains(got, "Tree /github") {
		t.Fatalf("expected tree header, got %q", got)
	}
}

func TestTreeTreatsPathLikeSingleArgAsRemotePath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_WORKSPACE", "ws_env")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_env/fs/tree" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/linear/projects" {
			t.Fatalf("expected path /linear/projects, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/linear/projects","entries":[],"nextCursor":null}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_env",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "linear/projects"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "Tree /linear/projects") {
		t.Fatalf("expected tree header, got %q", got)
	}
}

func TestTreePreservesUncatalogedSlashWorkspaceWhenRootIsUnknown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != "/v1/workspaces/team%2Fproject/fs/tree" {
			t.Fatalf("unexpected escaped path: %s", got)
		}
		if got := r.URL.Query().Get("path"); got != "/" {
			t.Fatalf("expected root path, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/","entries":[],"nextCursor":null}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "team/project",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "team/project"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "Tree /") {
		t.Fatalf("expected tree header, got %q", got)
	}
}

func TestTreeReadsRemoteWorkspacePath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_cloud/fs/tree" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/github" {
			t.Fatalf("expected path /github, got %q", got)
		}
		if got := r.URL.Query().Get("depth"); got != "5" {
			t.Fatalf("expected depth 5, got %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("unexpected Authorization header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/github","entries":[{"path":"/github/repos","type":"dir","revision":"rev_dir"},{"path":"/github/README.md","type":"file","revision":"rev_file","provider":"github","size":12}],"nextCursor":null}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_cloud",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "ws_cloud", "/github", "--depth", "5"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}

	got := stdout.String()
	if !strings.Contains(got, "Tree /github") {
		t.Fatalf("expected tree header, got %q", got)
	}
	if !strings.Contains(got, "dir  /github/repos") {
		t.Fatalf("expected dir entry, got %q", got)
	}
	if !strings.Contains(got, "file /github/README.md  provider=github  size=12  rev=rev_file") {
		t.Fatalf("expected file metadata entry, got %q", got)
	}
}

func TestTreeCanPrintPrettyJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/","entries":[],"nextCursor":null}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_cloud",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"tree", "ws_cloud", "--json"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree --json failed: %v", err)
	}

	if got := stdout.String(); !strings.Contains(got, "\n  \"path\": \"/\"") {
		t.Fatalf("expected pretty JSON, got %q", got)
	}
}

func TestReadPrintsRemoteFileContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_cloud/fs/file" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/github/README.md" {
			t.Fatalf("expected file path, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/github/README.md","revision":"rev_1","contentType":"text/markdown","content":"# readme\n"}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_cloud",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"read", "ws_cloud", "/github/README.md"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run read failed: %v", err)
	}

	if got := stdout.String(); got != "# readme\n" {
		t.Fatalf("unexpected file content: %q", got)
	}
}

func TestReadUsesTokenWorkspaceWhenWorkspaceArgOmitted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	token := testJWTWithWorkspace("ws_token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_token/fs/file" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("path"); got != "/docs/readme.md" {
			t.Fatalf("expected file path, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/docs/readme.md","revision":"rev_1","contentType":"text/markdown","content":"ok\n"}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_token",
		AccessToken:          token,
	})

	var stdout bytes.Buffer
	if err := run([]string{"read", "/docs/readme.md"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run read failed: %v", err)
	}

	if got := stdout.String(); got != "ok\n" {
		t.Fatalf("unexpected file content: %q", got)
	}
}

func TestReadDecodesBase64Content(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	encoded := base64.StdEncoding.EncodeToString([]byte{0x00, 0x7f, 0xff, 0x10})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"path":"/external/blob.bin","revision":"rev_1","contentType":"application/octet-stream","content":"` + encoded + `","encoding":"base64"}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_cloud",
		AccessToken:          "token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"cat", "ws_cloud", "/external/blob.bin"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run cat failed: %v", err)
	}

	if got := stdout.Bytes(); !bytes.Equal(got, []byte{0x00, 0x7f, 0xff, 0x10}) {
		t.Fatalf("unexpected decoded content: %#v", got)
	}
}

func TestCollectSeedFilesReportsProgress(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatalf("write a.txt failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("bravo"), 0o644); err != nil {
		t.Fatalf("write b.txt failed: %v", err)
	}

	var stdout bytes.Buffer
	files, err := collectSeedFiles(root, &stdout)
	if err != nil {
		t.Fatalf("collectSeedFiles failed: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if !strings.Contains(stdout.String(), "Seeding 2/2 files...") {
		t.Fatalf("expected progress output, got %q", stdout.String())
	}
}

func TestBuildCloudURLKeepsBasePath(t *testing.T) {
	got, err := buildCloudURL("https://agentrelay.test/cloud", "api/v1/cli/login")
	if err != nil {
		t.Fatalf("buildCloudURL failed: %v", err)
	}
	if got.String() != "https://agentrelay.test/cloud/api/v1/cli/login" {
		t.Fatalf("unexpected cloud URL: %s", got.String())
	}
}

func TestSetupCreatesWorkspaceConnectsIntegrationAndSkipsMount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Cleanup(func() { _ = os.RemoveAll("vfs") })

	seen := map[string]bool{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces":
			seen["create"] = true
			if r.Method != http.MethodPost {
				t.Fatalf("expected create POST, got %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cld_test" {
				t.Fatalf("unexpected create Authorization: %q", got)
			}
			var body cloudWorkspaceCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body failed: %v", err)
			}
			if body.Name != "demo" {
				t.Fatalf("expected workspace name demo, got %q", body.Name)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","relayfileUrl":"https://relayfile.test","createdAt":"2026-05-01T00:00:00Z","name":"demo"}`))
		case "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			seen["delegated"] = true
			if r.Method != http.MethodPost {
				t.Fatalf("expected delegated-token POST, got %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cld_test" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			writeDelegatedBundleResponse(t, w, "https://relayfile.test", "ws_123", "rf_join", "refresh_join")
		case "/api/v1/workspaces/ws_123/integrations/connect-session":
			seen["connect"] = true
			if r.Method != http.MethodPost {
				t.Fatalf("expected connect POST, got %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cld_test" {
				t.Fatalf("unexpected connect Authorization: %q", got)
			}
			var body cloudConnectSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode connect body failed: %v", err)
			}
			if len(body.AllowedIntegrations) != 1 || body.AllowedIntegrations[0] != "github" {
				t.Fatalf("unexpected allowed integrations: %#v", body.AllowedIntegrations)
			}
			_, _ = w.Write([]byte(`{"token":"session_token","expiresAt":"2026-05-01T01:00:00Z","connectLink":"https://connect.test/github","connectionId":"conn_123"}`))
		case "/api/v1/workspaces/ws_123/integrations/github/status":
			seen["status"] = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_test" {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			if got := r.URL.Query().Get("connectionId"); got != "conn_123" {
				t.Fatalf("unexpected connectionId: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{
		"setup",
		"--cloud-api-url", server.URL,
		"--cloud-token", "cld_test",
		"--workspace", "demo",
		"--provider", "github",
		"--local-dir", "./vfs",
		"--no-open",
		"--skip-mount",
	}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("run setup failed: %v\noutput:\n%s", err, stdout.String())
	}
	for _, key := range []string{"create", "delegated", "connect", "status"} {
		if !seen[key] {
			t.Fatalf("expected %s request", key)
		}
	}

	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected setup not to persist relayfile token credentials, got err=%v", err)
	}
	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		t.Fatalf("loadWorkspaceCatalog failed: %v", err)
	}
	if catalog.Default != "demo" || len(catalog.Workspaces) != 1 || catalog.Workspaces[0].ID != "ws_123" {
		t.Fatalf("unexpected workspace catalog: %#v", catalog)
	}
	gotOutput := stdout.String()
	if !strings.Contains(gotOutput, "Connect github: https://connect.test/github") {
		t.Fatalf("expected printed connect link, got %q", gotOutput)
	}
	if !strings.Contains(gotOutput, "relayfile mount ws_123 ./vfs") {
		t.Fatalf("expected mount command guidance, got %q", gotOutput)
	}
}

func TestSetupJiraRunsSitePickerAfterFreshConnect(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Cleanup(func() { _ = os.RemoveAll("vfs") })

	seen := map[string]bool{}
	var picked map[string]any
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces":
			seen["create"] = true
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","relayfileUrl":"https://relayfile.test","createdAt":"2026-05-01T00:00:00Z","name":"demo"}`))
		case "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			seen["delegated"] = true
			writeDelegatedBundleResponse(t, w, "https://relayfile.test", "ws_123", "rf_join", "refresh_join")
		case "/api/v1/workspaces/ws_123/integrations/connect-session":
			seen["connect"] = true
			_, _ = w.Write([]byte(`{"connectionId":"conn_jira","connectLink":"","backend":"nango"}`))
		case "/api/v1/workspaces/ws_123/integrations/jira/status":
			seen["status"] = true
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"jira","backend":"nango"}`))
		case "/api/v1/workspaces/ws_123/integrations/jira/accessible-resources":
			seen["accessible-resources"] = true
			_, _ = w.Write([]byte(`{"ok":true,"resources":[{"id":"cloud-1","url":"https://foo.atlassian.net","name":"Foo"}]}`))
		case "/api/v1/workspaces/ws_123/integrations/jira/metadata":
			seen["metadata"] = true
			if r.Method != http.MethodPut {
				t.Fatalf("expected metadata PUT, got %s", r.Method)
			}
			var body map[string]map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode metadata body failed: %v", err)
			}
			picked = body["metadata"]
			_, _ = w.Write([]byte(`{"ok":true,"metadata":` + jsonMarshalOrPanic(picked) + `}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{
		"setup",
		"--cloud-api-url", server.URL,
		"--cloud-token", "cld_test",
		"--workspace", "demo",
		"--provider", "jira",
		"--local-dir", "./vfs",
		"--no-open",
		"--skip-mount",
	}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("run setup failed: %v\noutput:\n%s", err, stdout.String())
	}
	for _, key := range []string{"create", "delegated", "connect", "status", "accessible-resources", "metadata"} {
		if !seen[key] {
			t.Fatalf("expected %s request", key)
		}
	}
	if picked["cloudId"] != "cloud-1" || picked["baseUrl"] != "https://foo.atlassian.net" {
		t.Fatalf("unexpected site metadata: %#v", picked)
	}
}

func TestIntegrationConnectRefreshesCloudAccessTokenAndReusesWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_123",
		LocalDir:   localDir,
		AgentName:  "relayfile-cli",
		Scopes:     append([]string(nil), defaultJoinScopes...),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	seen := map[string]bool{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			seen["delegated"] = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_123", "rf_join", "refresh_join")
		case "/api/v1/workspaces/ws_123/integrations/connect-session":
			seen["connect"] = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected connect Authorization: %q", got)
			}
			var body cloudConnectSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode connect body failed: %v", err)
			}
			if len(body.AllowedIntegrations) != 1 || body.AllowedIntegrations[0] != "notion" {
				t.Fatalf("unexpected allowed integrations: %#v", body.AllowedIntegrations)
			}
			if body.RequestedBackend != "composio" {
				t.Fatalf("expected requested backend composio, got %q", body.RequestedBackend)
			}
			_, _ = w.Write([]byte(`{"connectLink":"https://connect.test/notion","connectionId":"conn_789","backend":"composio"}`))
		case "/api/v1/workspaces/ws_123/integrations/notion/status":
			seen["status"] = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			if got := r.URL.Query().Get("connectionId"); got != "conn_789" {
				t.Fatalf("unexpected connectionId: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/v1/workspaces/ws_123/sync/status":
			seen["sync"] = true
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
				t.Fatalf("unexpected sync Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"notion","status":"ready","lagSeconds":4,"watermarkTs":"2026-05-02T18:00:00Z"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	installFakeAgentRelaySession(t, server.URL, "cld_new", "demo", "ws_123", "ws_123")

	var stdout bytes.Buffer
	err := run([]string{
		"integration", "connect", "notion",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--backend", "composio",
		"--no-open",
		"--timeout", "1s",
	}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("run integration connect failed: %v\noutput:\n%s", err, stdout.String())
	}
	for _, key := range []string{"delegated", "connect", "status", "sync"} {
		if !seen[key] {
			t.Fatalf("expected %s request", key)
		}
	}
	got := stdout.String()
	if !strings.Contains(got, "notion connected") {
		t.Fatalf("unexpected integration connect output: %q", got)
	}
	if !strings.Contains(got, "keep this command running while the mount is active") {
		t.Fatalf("expected connected-but-still-working guidance, got %q", got)
	}
	if !strings.Contains(got, "Waiting for notion initial sync. Leave this command running") {
		t.Fatalf("expected initial sync wait guidance, got %q", got)
	}
	state := loadSavedConnection(localDir, "notion")
	if state.ConnectionID != "conn_789" || state.Backend != "composio" {
		t.Fatalf("unexpected saved integration connection: %#v", state)
	}
}

func TestConnectCloudIntegrationAcceptsWorkspaceScopedGitHubStatus(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	var seenConnectionScopedStatus atomic.Bool
	var seenWorkspaceStatus atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_123/integrations/connect-session":
			if r.Method != http.MethodPost {
				t.Fatalf("expected connect POST, got %s", r.Method)
			}
			_, _ = w.Write([]byte(`{"connectLink":"https://connect.test/github","connectionId":"conn_session","backend":"nango"}`))
		case "/api/v1/workspaces/ws_123/integrations/github/status":
			switch r.URL.Query().Get("connectionId") {
			case "conn_session":
				seenConnectionScopedStatus.Store(true)
				_, _ = w.Write([]byte(`{"ready":false,"state":"not_connected","connectionId":"conn_session"}`))
			case "":
				seenWorkspaceStatus.Store(true)
				_, _ = w.Write([]byte(`{"ready":false,"state":"oauth_connected","provider":"github","connectionId":"conn_actual"}`))
			default:
				t.Fatalf("unexpected connectionId query: %q", r.URL.Query().Get("connectionId"))
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := connectCloudIntegration(server.URL, "ws_123", "rf_join", "github", "nango", localDir, time.Second, false, &stdout); err != nil {
		t.Fatalf("connectCloudIntegration failed: %v\noutput:\n%s", err, stdout.String())
	}
	if !seenConnectionScopedStatus.Load() {
		t.Fatalf("expected connection-scoped status probe before workspace fallback")
	}
	if !seenWorkspaceStatus.Load() {
		t.Fatalf("expected workspace-scoped status fallback")
	}
	if got := stdout.String(); !strings.Contains(got, "github connected") {
		t.Fatalf("expected connected output, got %q", got)
	}
	state := loadSavedConnection(localDir, "github")
	if state.ConnectionID != "conn_actual" || state.Backend != "nango" {
		t.Fatalf("expected resolved github connection to be saved, got %#v", state)
	}
}

func TestStatusIncludesLocalMirrorAndDaemonCounts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if err := saveCredentials(credentials{
		Server: defaultServerURL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relayfile-mount-state.json"), []byte(`{"files":{"a":{"dirty":true},"b":{"dirty":false}}}`), 0o644); err != nil {
		t.Fatalf("write mount state failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "permissions-denied.log"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatalf("write denied log failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "conflicts", "note.local"), []byte("conflict"), 0o644); err != nil {
		t.Fatalf("write conflict file failed: %v", err)
	}
	if err := writeDaemonPIDState(mountPIDFile(localDir), daemonPIDState{PID: 4242, WorkspaceID: "ws_demo", LocalDir: localDir}); err != nil {
		t.Fatalf("writeDaemonPIDState failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/workspaces/ws_demo/sync/status" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer delegated_token" {
			t.Fatalf("expected delegated Authorization, got %q", got)
		}
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"ready","lagSeconds":4,"watermarkTs":"2026-05-02T18:00:00Z"}]}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected stale relayfile credentials removed, got err=%v", err)
	}
	got := stdout.String()
	for _, fragment := range []string{
		"workspace ws_demo (demo)",
		"daemon: running (pid 4242)",
		"pending writebacks: 1",
		"conflicts: 1",
		"denied: 2",
	} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("expected %q in status output, got %q", fragment, got)
		}
	}
}

func TestStatusRendersUnavailableAgentRelaySessionAuthLine(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[]}`))
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "token",
	})
	installFakeAgentRelay(t, `
echo "session expired" >&2
exit 1
`)

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "auth: agent-relay session unavailable - run 'agent-relay cloud login'") {
		t.Fatalf("expected agent-relay unavailable auth line, got %q", got)
	}
}

func TestStatusWarnsWhenDaemonPredatesLastLogin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	startedAt := time.Now().UTC().Add(-time.Hour)
	if err := writeDaemonPIDState(mountPIDFile(localDir), daemonPIDState{
		PID:         4242,
		WorkspaceID: "ws_demo",
		LocalDir:    localDir,
		StartedAt:   startedAt.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("writeDaemonPIDState failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  startedAt.Format(time.RFC3339),
		LastUsedAt: startedAt.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[]}`))
	}))
	defer server.Close()
	if err := saveCredentials(credentials{Server: "https://legacy.relayfile.invalid", Token: "legacy_token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}
	oldLoginTime := startedAt.Add(-30 * time.Minute)
	if err := os.Chtimes(credentialsPath(), oldLoginTime, oldLoginTime); err != nil {
		t.Fatalf("chtimes credentials failed: %v", err)
	}
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})
	agentRelayAuthPath := agentRelayCloudAuthPath()
	if err := os.MkdirAll(filepath.Dir(agentRelayAuthPath), 0o700); err != nil {
		t.Fatalf("mkdir agent-relay auth dir failed: %v", err)
	}
	if err := os.WriteFile(agentRelayAuthPath, []byte(`{"accessToken":"new"}`), 0o600); err != nil {
		t.Fatalf("write agent-relay auth failed: %v", err)
	}
	loginTime := startedAt.Add(30 * time.Minute)
	if err := os.Chtimes(agentRelayAuthPath, loginTime, loginTime); err != nil {
		t.Fatalf("chtimes agent-relay auth failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "auth: daemon predates last login - restart the daemon") {
		t.Fatalf("expected daemon stale auth line, got %q", got)
	}
}

func TestStatusUnauthorizedReturnsLoginHint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:      "demo",
		ID:        "ws_demo",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Token has expired"}`))
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "expired",
	})

	var stdout bytes.Buffer
	err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout)
	if !errors.Is(err, ErrCloudRefreshExpired) {
		t.Fatalf("expected ErrCloudRefreshExpired, got %v", err)
	}
}

func TestStatusSurfacesOrphanDaemonFromProcessScan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/workspaces/ws_demo/sync/status" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[]}`))
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	oldList := listProcessCommands
	listProcessCommands = func() ([]processCommandSnapshot, error) {
		return []processCommandSnapshot{{
			PID:     7777,
			Command: "relayfile mount ws_demo " + localDir + " --daemonized",
		}}, nil
	}
	t.Cleanup(func() { listProcessCommands = oldList })

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "daemon: orphan (pid 7777)") {
		t.Fatalf("expected orphan daemon status, got %q", got)
	}
}

func TestMountDaemonCommandMatchesStartAliasAndLocalDirBoundaries(t *testing.T) {
	localDir := filepath.Join(t.TempDir(), "ws")
	if !mountDaemonCommandMatches("relayfile start demo "+localDir+" --daemonized", localDir, "ws_demo", "demo") {
		t.Fatalf("expected relayfile start alias to match daemon command")
	}
	if mountDaemonCommandMatches("relayfile mount other "+localDir+"-old --daemonized", localDir, "ws_demo", "demo") {
		t.Fatalf("expected sibling path with shared prefix not to match")
	}
	if !mountDaemonCommandMatches("relayfile mount other "+filepath.Join(localDir, "nested")+" --daemonized", localDir, "ws_demo", "demo") {
		t.Fatalf("expected localDir subpath token to match")
	}
}

func TestRestartRequiresRecordedLocalMirror(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			ID:         "ws_demo",
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}

	var stdout bytes.Buffer
	err := runRestart([]string{"demo"}, &stdout)
	if err == nil {
		t.Fatalf("expected restart to fail without a recorded local mirror")
	}
	if !strings.Contains(err.Error(), "no recorded local mirror directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMountRequiresLocalDirWhenWorkspaceHasNoRecordedMirror(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			ID:         "ws_demo",
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}
	if err := saveCredentials(credentials{
		Server: defaultServerURL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	err := run([]string{"mount", "demo", "--token", testJWTWithWorkspace("ws_demo"), "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected mount without LOCAL_DIR to fail when no localDir is recorded")
	}
	if !strings.Contains(err.Error(), "no recorded local mirror directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMountUsesRecordedLocalDirWhenOmitted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			ID:         "ws_demo",
			LocalDir:   localDir,
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/ws_demo/fs/export":
			_, _ = w.Write([]byte(`[{"path":"/notion/Docs/A.md","revision":"rev_1","contentType":"text/markdown","content":"# A"}]`))
		case "/v1/workspaces/ws_demo/fs/events":
			_, _ = w.Write([]byte(`{"events":[{"eventId":"evt_1","type":"file.created","path":"/notion/Docs/A.md","revision":"rev_1"}]}`))
		case "/v1/workspaces/ws_demo/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	if err := run([]string{"mount", "demo", "--server", server.URL, "--token", testJWTWithWorkspace("ws_demo"), "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run mount failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(localDir, "notion", "Docs", "A.md"))
	if err != nil {
		t.Fatalf("expected file under recorded localDir: %v", err)
	}
	if string(data) != "# A" {
		t.Fatalf("unexpected mirrored content: %q", data)
	}
}

func TestMountWithoutTokenRejectsWorkspaceOutsideAgentRelayActive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "stale",
		Workspaces: []workspaceRecord{
			{
				Name:       "active",
				ID:         "rw_active",
				LocalDir:   localDir,
				CreatedAt:  now,
				LastUsedAt: now,
			},
			{
				Name:       "stale",
				ID:         "rw_stale",
				LocalDir:   localDir,
				CreatedAt:  now,
				LastUsedAt: now,
			},
		},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}

	joinCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		joinCalls++
		t.Fatalf("unexpected path: %s", r.URL.Path)
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayfileWorkspaceID:  "rw_active",
		AccessToken:           testJWTWithWorkspace("rw_active"),
		RefreshToken:          "refresh_active",
		AccessTokenExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	})

	err := run([]string{"mount", "stale", localDir, "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected mount to reject stale workspace")
	}
	if !strings.Contains(err.Error(), "uses delegated relayfile workspace rw_active") {
		t.Fatalf("unexpected error: %v", err)
	}
	if joinCalls != 0 {
		t.Fatalf("expected no runtime token mint for mismatched workspace, got %d calls", joinCalls)
	}
}

func TestMountUsesLegacyRecordedLocalDirWhenOmitted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			LocalDir:   localDir,
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/demo/fs/export":
			_, _ = w.Write([]byte(`[{"path":"/notion/Docs/Legacy.md","revision":"rev_1","contentType":"text/markdown","content":"# Legacy"}]`))
		case "/v1/workspaces/demo/fs/events":
			_, _ = w.Write([]byte(`{"events":[{"eventId":"evt_1","type":"file.created","path":"/notion/Docs/Legacy.md","revision":"rev_1"}]}`))
		case "/v1/workspaces/demo/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"demo","providers":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  testJWTWithWorkspace("demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	if err := run([]string{"mount", "demo", "--server", server.URL, "--token", testJWTWithWorkspace("demo"), "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run mount failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(localDir, "notion", "Docs", "Legacy.md"))
	if err != nil {
		t.Fatalf("expected file under legacy recorded localDir: %v", err)
	}
	if string(data) != "# Legacy" {
		t.Fatalf("unexpected mirrored content: %q", data)
	}
}

func TestShouldRegisterMountPID(t *testing.T) {
	tests := []struct {
		name       string
		daemonized bool
		once       bool
		want       bool
	}{
		{name: "background child", daemonized: true, once: false, want: true},
		{name: "foreground loop", daemonized: false, once: false, want: true},
		{name: "one shot", daemonized: false, once: true, want: false},
		{name: "daemonized one shot still visible", daemonized: true, once: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRegisterMountPID(tt.daemonized, tt.once); got != tt.want {
				t.Fatalf("shouldRegisterMountPID(%v, %v) = %v, want %v", tt.daemonized, tt.once, got, tt.want)
			}
		})
	}
}

func TestMountRefusesExplicitLocalDirThatRehomesRecordedMirror(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	otherDir := filepath.Join(t.TempDir(), "other-mirror")
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			ID:         "ws_demo",
			LocalDir:   localDir,
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}
	if err := saveCredentials(credentials{
		Server: defaultServerURL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	err := run([]string{"mount", "demo", otherDir, "--token", testJWTWithWorkspace("ws_demo"), "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected mount to refuse re-homing a recorded mirror")
	}
	if !strings.Contains(err.Error(), "refusing to silently re-home") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(otherDir, ".relay")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused re-home should not initialize other mirror dir; stat err=%v", statErr)
	}
}

func TestMountRehomeRefusesRunningRecordedDaemon(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	otherDir := filepath.Join(t.TempDir(), "other-mirror")
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			ID:         "ws_demo",
			LocalDir:   localDir,
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := os.WriteFile(mountPIDFile(localDir), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatalf("write legacy pid failed: %v", err)
	}
	if err := saveCredentials(credentials{
		Server: defaultServerURL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	err := run([]string{"mount", "demo", otherDir, "--token", testJWTWithWorkspace("ws_demo"), "--rehome", "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected rehome to refuse while old mirror has a running daemon")
	}
	if !strings.Contains(err.Error(), "has a running mount") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(otherDir, ".relay")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused rehome should not initialize other mirror dir; stat err=%v", statErr)
	}
}

func TestMountRehomeRefusesUnverifiedRecordedDaemon(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	otherDir := filepath.Join(t.TempDir(), "other-mirror")
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			ID:         "ws_demo",
			LocalDir:   localDir,
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeDaemonPIDState(mountPIDFile(localDir), daemonPIDState{
		PID:         os.Getpid(),
		WorkspaceID: "ws_other",
		LocalDir:    localDir,
	}); err != nil {
		t.Fatalf("write daemon pid state failed: %v", err)
	}
	if err := saveCredentials(credentials{
		Server: defaultServerURL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	err := run([]string{"mount", "demo", otherDir, "--token", testJWTWithWorkspace("ws_demo"), "--rehome", "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected rehome to refuse unverified mount state")
	}
	if !strings.Contains(err.Error(), "unverified mount state") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(mountPIDFile(localDir)); statErr != nil {
		t.Fatalf("unverified pid file should remain in place: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(otherDir, ".relay")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused rehome should not initialize other mirror dir; stat err=%v", statErr)
	}
}

func TestMountRehomeAllowsExplicitMoveAndPersistsLocalDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	otherDir := t.TempDir()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:       "demo",
			ID:         "ws_demo",
			LocalDir:   localDir,
			CreatedAt:  now,
			LastUsedAt: now,
		}},
	}); err != nil {
		t.Fatalf("saveWorkspaceCatalog failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/ws_demo/fs/export":
			_, _ = w.Write([]byte(`[{"path":"/notion/Docs/Rehomed.md","revision":"rev_1","contentType":"text/markdown","content":"# Rehomed"}]`))
		case "/v1/workspaces/ws_demo/fs/events":
			_, _ = w.Write([]byte(`{"events":[{"eventId":"evt_1","type":"file.created","path":"/notion/Docs/Rehomed.md","revision":"rev_1"}]}`))
		case "/v1/workspaces/ws_demo/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  testJWTWithWorkspace("ws_demo"),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	if err := run([]string{"mount", "demo", otherDir, "--server", server.URL, "--token", testJWTWithWorkspace("ws_demo"), "--rehome", "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run mount --rehome failed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(otherDir, "notion", "Docs", "Rehomed.md"))
	if err != nil {
		t.Fatalf("expected file under rehomed localDir: %v", err)
	}
	if string(data) != "# Rehomed" {
		t.Fatalf("unexpected mirrored content: %q", data)
	}
	record, ok := workspaceRecordByID("ws_demo")
	if !ok {
		t.Fatalf("expected workspace record")
	}
	wantLocalDir, _ := filepath.Abs(otherDir)
	if record.LocalDir != wantLocalDir {
		t.Fatalf("LocalDir = %q, want %q", record.LocalDir, wantLocalDir)
	}
}

func TestLogsPrintsTailForWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := os.WriteFile(mountLogFile(localDir), []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatalf("write mount log failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"logs", "demo", "--lines", "2"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run logs failed: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "three\nfour" {
		t.Fatalf("unexpected logs output: %q", got)
	}
}

func TestMountOnceRefreshesDelegatedWorkspaceTokenBeforeSync(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	oldToken := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "old", time.Now().Add(-time.Minute))
	newToken := testJWTWithWorkspaceAndAgent("ws_refresh", "new")
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_refresh",
		LocalDir:   localDir,
		AgentName:  "relayfile-cli",
		Scopes:     append([]string(nil), defaultJoinScopes...),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	eventCalls := 0
	exportCalls := 0
	refreshCalls := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			refreshCalls++
			var body struct {
				RefreshToken string `json:"refreshToken"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode refresh body: %v", err)
			}
			if body.RefreshToken != "refresh_old" {
				t.Fatalf("unexpected refresh token %q", body.RefreshToken)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{
				"accessToken":           newToken,
				"refreshToken":          "refresh_new",
				"accessTokenExpiresAt":  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				"refreshTokenExpiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			})
		case "/v1/workspaces/ws_refresh/fs/events":
			eventCalls++
			gotAuth := r.Header.Get("Authorization")
			if gotAuth != "Bearer "+newToken {
				t.Fatalf("unexpected events Authorization: %q", gotAuth)
			}
			_, _ = w.Write([]byte(`{"events":[]}`))
		case "/v1/workspaces/ws_refresh/fs/export":
			exportCalls++
			gotAuth := r.Header.Get("Authorization")
			if gotAuth != "Bearer "+newToken {
				t.Fatalf("unexpected refreshed export Authorization: %q", gotAuth)
			}
			_, _ = w.Write([]byte(`[]`))
		case "/v1/workspaces/ws_refresh/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_refresh","providers":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayfileWorkspaceID:  "ws_refresh",
		AccessToken:           oldToken,
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	})

	if err := run([]string{
		"mount", "ws_refresh", localDir,
		"--server", server.URL,
		"--once",
		"--websocket=false",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run mount failed: %v", err)
	}
	if refreshCalls != 1 || exportCalls != 1 {
		t.Fatalf("expected 1 refresh and 1 export call, got refresh=%d events=%d export=%d", refreshCalls, eventCalls, exportCalls)
	}
	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected refreshed relayfile token not to be persisted, got err=%v", err)
	}
}

func TestMountSkipsDataPlaneWhenDelegatedRefreshRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	oldToken := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "old", time.Now().Add(-time.Minute))
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_refresh",
		LocalDir:   localDir,
		AgentName:  "relayfile-cli",
		Scopes:     append([]string(nil), defaultJoinScopes...),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	refreshCalls := 0
	dataPlaneCalls := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			refreshCalls++
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"delegation_expired","message":"delegation expired"}`))
		case "/v1/workspaces/ws_refresh/fs/events", "/v1/workspaces/ws_refresh/fs/export", "/v1/workspaces/ws_refresh/sync/status":
			dataPlaneCalls++
			t.Fatalf("data-plane call should be skipped after rejected refresh: %s", r.URL.Path)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	credsPath := writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayfileWorkspaceID:  "ws_refresh",
		AccessToken:           oldToken,
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	})
	before, err := os.ReadFile(credsPath)
	if err != nil {
		t.Fatalf("read delegated credentials before mount failed: %v", err)
	}

	err = run([]string{
		"mount", "ws_refresh", localDir,
		"--server", server.URL,
		"--once",
		"--websocket=false",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatalf("expected mount to fail fast after rejected delegated refresh")
	}
	if !errors.Is(err, ErrDelegatedRelayfileCredentialsExpired) {
		t.Fatalf("expected delegated credential expiry error, got %v", err)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1", refreshCalls)
	}
	if dataPlaneCalls != 0 {
		t.Fatalf("dataPlaneCalls = %d, want 0", dataPlaneCalls)
	}
	after, err := os.ReadFile(credsPath)
	if err != nil {
		t.Fatalf("read delegated credentials after mount failed: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("delegated credentials changed after rejected refresh\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func clearRelayfileEnv(t *testing.T) {
	t.Helper()
	t.Setenv("RELAYFILE_SERVER", "")
	t.Setenv("RELAYFILE_BASE_URL", "")
	t.Setenv("RELAYFILE_TOKEN", "")
	t.Setenv("RELAYFILE_WORKSPACE", "")
	t.Setenv("RELAYFILE_CLOUD_API_URL", "")
	t.Setenv("RELAYFILE_CLOUD_TOKEN", "")
	t.Setenv("RELAYFILE_MOUNT_CREDS_FILE", "")
	t.Setenv("RELAYFILE_DELEGATED_CREDENTIALS_FILE", "")
	t.Setenv("AGENT_RELAY_BIN", "")
}

func installFakeAgentRelay(t *testing.T, scriptBody string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-relay")
	script := `#!/bin/sh
set -eu
if [ "$*" = "--version" ]; then
  echo "8.7.0"
  exit 0
fi
if [ "$*" = "cloud session --help" ]; then
  echo "Usage: agent-relay cloud session [options]"
  echo "  --json"
  exit 0
fi
if [ "$*" = "workspace active --help" ]; then
  echo "Usage: agent-relay workspace active [options]"
  echo "  --json"
  exit 0
fi
if [ "$*" = "workspace switch --help" ]; then
  echo "Usage: agent-relay workspace switch [options] <name>"
  exit 0
fi
` + scriptBody + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake agent-relay failed: %v", err)
	}
	t.Setenv("AGENT_RELAY_BIN", path)
	return path
}

func installFakeAgentRelaySession(t *testing.T, apiURL, accessToken, name, cloudWorkspaceID, relayfileWorkspaceID string) {
	t.Helper()
	body := fmt.Sprintf(`
if [ "$*" = "cloud session --json" ]; then
  echo '{"apiUrl":%q,"accessToken":%q}'
  exit 0
fi
if [ "$*" = "workspace active --json" ]; then
  echo '{"name":%q,"cloudWorkspaceId":%q,"relayfileWorkspaceId":%q}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`, apiURL, accessToken, name, cloudWorkspaceID, relayfileWorkspaceID)
	installFakeAgentRelay(t, body)
}

func testJWTWithWorkspace(workspaceID string) string {
	return testJWTWithWorkspaceAndAgent(workspaceID, "test")
}

func testJWTWithWorkspaceAndAgent(workspaceID, agentName string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"workspace_id":"` + workspaceID + `","agent_name":"` + agentName + `","aud":"relayfile"}`))
	return header + "." + payload + ".sig"
}

func testJWTWithWorkspaceAndScopes(workspaceID, agentName string, scopes []string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{
		"workspace_id": workspaceID,
		"agent_name":   agentName,
		"aud":          "relayfile",
		"scopes":       scopes,
	})
	if err != nil {
		panic(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func testJWTWithWorkspaceAgentAndExpiry(workspaceID, agentName string, exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(map[string]any{
		"workspace_id": workspaceID,
		"agent_name":   agentName,
		"aud":          "relayfile",
		"iat":          exp.Add(-time.Hour).Unix(),
		"exp":          exp.Unix(),
	})
	if err != nil {
		panic(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func writeDelegatedBundleResponse(t *testing.T, w http.ResponseWriter, relayfileURL, workspaceID, accessToken, refreshToken string) {
	t.Helper()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"relayfileUrl":                   relayfileURL,
		"relayauthUrl":                   relayfileURL,
		"relayfileWorkspaceId":           workspaceID,
		"relayfileToken":                 accessToken,
		"relayfileTokenExpiresAt":        time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"relayfileRefreshToken":          refreshToken,
		"relayfileRefreshTokenExpiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		"relayfileScopes":                []string{"relayfile:fs:read:*", "relayfile:fs:write:*"},
		"delegationNotAfter":             time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		"relayfileMountPaths":            []string{"/"},
	})
}

func writeDelegatedCredentialsForTest(t *testing.T, bundle delegatedauth.Bundle) string {
	t.Helper()
	if bundle.RelayfileURL == "" && bundle.BaseURL == "" && bundle.Server == "" {
		bundle.RelayfileURL = defaultServerURL
	}
	if bundle.RelayfileWorkspaceID == "" && bundle.WorkspaceID == "" {
		bundle.RelayfileWorkspaceID = "ws_test"
	}
	if len(bundle.Scopes) == 0 && len(bundle.RelayfileScopes) == 0 {
		bundle.Scopes = append([]string(nil), defaultJoinScopes...)
	}
	if bundle.AccessToken == "" && bundle.Token == "" && bundle.RelayfileToken == "" {
		bundle.AccessToken = testJWTWithWorkspaceAndScopes(bundle.Workspace(), "test", bundle.Scopes)
	}
	if bundle.RefreshToken == "" && bundle.RelayfileRefreshToken == "" {
		bundle.RefreshToken = "refresh_test"
	}
	if bundle.RelayauthURL == "" && bundle.RefreshURL == "" {
		bundle.RelayauthURL = bundle.ServerURL()
	}
	path := delegatedCredentialsPath()
	if err := delegatedauth.SaveAtomic(path, bundle); err != nil {
		t.Fatalf("save delegated credentials failed: %v", err)
	}
	return path
}

func TestWritebackPushPostsBulkAndWritesAckedReceipt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	const remotePath = "/linear/issues/factory-create-ar-272-test.json"
	const opID = "op_ar_272"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         workspaceID,
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	localPath := filepath.Join(localDir, "factory-create-ar-272-test.json")
	content := []byte(`{"title":"AR-272 synthetic test"}` + "\n")
	if err := os.WriteFile(localPath, content, 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	if err := saveLegacyCloudCredentials(cloudCredentials{
		APIURL:      "https://legacy-cloud-credentials.invalid",
		AccessToken: "legacy_token_must_not_be_used",
	}); err != nil {
		t.Fatalf("save legacy cloud credentials failed: %v", err)
	}

	var sawDelegated atomic.Bool
	var sawBulk atomic.Bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/workspaces/"+workspaceID+"/relayfile/delegated-token":
			sawDelegated.Store(true)
			if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			var body cloudRelayfileDelegatedTokenRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode delegated-token body failed: %v", err)
			}
			if body.AgentName != "relayfile-cli" {
				t.Fatalf("agentName = %q, want relayfile-cli", body.AgentName)
			}
			if got, want := strings.Join(body.Scopes, ","), "fs:write:/linear/**,ops:read"; got != want {
				t.Fatalf("delegated-token scopes = %q, want %q", got, want)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"relayfileUrl":                   server.URL,
				"relayauthUrl":                   server.URL,
				"relayfileWorkspaceId":           workspaceID,
				"relayfileToken":                 "rf_write",
				"relayfileTokenExpiresAt":        time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				"relayfileRefreshToken":          "refresh_write",
				"relayfileRefreshTokenExpiresAt": time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
				"relayfileScopes":                []string{"relayfile:fs:write:/linear/**"},
				"delegationNotAfter":             time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
				"relayfileMountPaths":            []string{"/linear/**"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/"+workspaceID+"/fs/bulk":
			sawBulk.Store(true)
			if got := r.Header.Get("Authorization"); got != "Bearer rf_write" {
				t.Fatalf("unexpected bulk Authorization: %q", got)
			}
			var req bulkWriteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode bulk request failed: %v", err)
			}
			if len(req.Files) != 1 {
				t.Fatalf("bulk files = %d, want 1", len(req.Files))
			}
			file := req.Files[0]
			if file.Path != remotePath {
				t.Fatalf("bulk path = %q, want %q", file.Path, remotePath)
			}
			if file.Content != string(content) {
				t.Fatalf("bulk content = %q, want %q", file.Content, string(content))
			}
			if file.ContentIdentity == nil {
				t.Fatal("expected content identity for factory-create writeback")
			}
			if file.ContentIdentity.Kind != "mount-writeback-create-draft" {
				t.Fatalf("content identity kind = %q", file.ContentIdentity.Kind)
			}
			if !strings.HasPrefix(file.ContentIdentity.Key, workspaceID+":"+remotePath+":") {
				t.Fatalf("content identity key = %q", file.ContentIdentity.Key)
			}
			_, _ = io.WriteString(w, `{"written":1,"errorCount":0,"correlationId":"corr_ar_272","results":[{"path":"`+remotePath+`","revision":"rev_1","opId":"`+opID+`","writeback":{"provider":"linear","state":"succeeded"}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspaces/"+workspaceID+"/ops/"+opID:
			if got := r.Header.Get("Authorization"); got != "Bearer rf_write" {
				t.Fatalf("unexpected op Authorization: %q", got)
			}
			_, _ = io.WriteString(w, `{"opId":"`+opID+`","path":"`+remotePath+`","status":"succeeded","revision":"rev_1"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	installFakeAgentRelaySession(t, server.URL, "cld_access", "demo", workspaceID, workspaceID)

	var stdout bytes.Buffer
	if err := run([]string{"writeback", "push", localPath, "--workspace", "demo", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("writeback push failed: %v\n%s", err, stdout.String())
	}
	if !sawDelegated.Load() {
		t.Fatal("expected delegated-token request via agent-relay cloud session")
	}
	if !sawBulk.Load() {
		t.Fatal("expected bulk write request")
	}
	if got := stdout.String(); !strings.Contains(got, "Pushed ") || !strings.Contains(got, opID) {
		t.Fatalf("unexpected stdout: %s", got)
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "pending")); got != 0 {
		t.Fatalf("pending receipt count = %d, want 0", got)
	}
	ackedDir := filepath.Join(localDir, ".relay", "outbox", "acked")
	entries, err := os.ReadDir(ackedDir)
	if err != nil {
		t.Fatalf("read acked dir failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("acked receipt count = %d, want 1", len(entries))
	}
	payload, err := os.ReadFile(filepath.Join(ackedDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read acked receipt failed: %v", err)
	}
	var receipt writebackPushReceipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		t.Fatalf("decode acked receipt failed: %v", err)
	}
	if receipt.Status != "acked" || receipt.OpID != opID || receipt.RemotePath != remotePath || receipt.Revision != "rev_1" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	// Terminal receipts must not retain the user's file body on disk.
	if receipt.Content != "" || receipt.Encoding != "" {
		t.Fatalf("acked receipt must redact content/encoding, got content=%q encoding=%q", receipt.Content, receipt.Encoding)
	}
	// The acked JSON printed to stdout must likewise omit the body.
	if strings.Contains(stdout.String(), "AR-272 synthetic test") {
		t.Fatalf("stdout leaked file content: %s", stdout.String())
	}
}

func TestWritebackPushCanonicalPathPostsBulkWithoutContentIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	const remotePath = "/linear/issues/AR-272__00000000-0000-0000-0000-000000000272.json"
	const opID = "op_update_272"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}

	localPath := filepath.Join(localDir, "AR-272__00000000-0000-0000-0000-000000000272.json")
	content := []byte(`{"title":"updated title","stateId":"state_1"}` + "\n")
	if err := os.WriteFile(localPath, content, 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	var sawBulk atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/"+workspaceID+"/fs/bulk":
			sawBulk.Store(true)
			if got := r.Header.Get("Authorization"); got != "Bearer rf_write" {
				t.Fatalf("unexpected bulk Authorization: %q", got)
			}
			var req bulkWriteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode bulk request failed: %v", err)
			}
			if len(req.Files) != 1 {
				t.Fatalf("bulk files = %d, want 1", len(req.Files))
			}
			file := req.Files[0]
			if file.Path != remotePath {
				t.Fatalf("bulk path = %q, want %q", file.Path, remotePath)
			}
			if file.Content != string(content) {
				t.Fatalf("bulk content = %q, want %q", file.Content, string(content))
			}
			if file.ContentIdentity != nil {
				t.Fatalf("canonical update must not carry draft content identity: %+v", file.ContentIdentity)
			}
			_, _ = io.WriteString(w, `{"written":1,"errorCount":0,"correlationId":"corr_update_272","results":[{"path":"`+remotePath+`","revision":"rev_2","opId":"`+opID+`","writeback":{"provider":"linear","state":"pending"}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspaces/"+workspaceID+"/ops/"+opID:
			_, _ = io.WriteString(w, `{"opId":"`+opID+`","path":"`+remotePath+`","status":"succeeded","revision":"rev_2"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{"writeback", "push", localPath, "--server", server.URL, "--token", "rf_write", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("writeback push canonical failed: %v\n%s", err, stdout.String())
	}
	if !sawBulk.Load() {
		t.Fatal("expected bulk write request")
	}
	if got := stdout.String(); !strings.Contains(got, "Pushed ") || !strings.Contains(got, opID) {
		t.Fatalf("unexpected stdout: %s", got)
	}
}

func TestWritebackUpdateCanonicalPathRequiresOperationAndSendsIntent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	const remotePath = "/linear/issues/AR-292__00000000-0000-0000-0000-000000000292.json"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}

	localPath := filepath.Join(localDir, "AR-292__00000000-0000-0000-0000-000000000292.json")
	content := []byte(`{"labels":[{"name":"harness"}]}` + "\n")
	if err := os.WriteFile(localPath, content, 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	var sawBulk atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/"+workspaceID+"/fs/bulk":
			sawBulk.Store(true)
			var req bulkWriteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode bulk request failed: %v", err)
			}
			if len(req.Files) != 1 {
				t.Fatalf("bulk files = %d, want 1", len(req.Files))
			}
			file := req.Files[0]
			if file.Path != remotePath {
				t.Fatalf("bulk path = %q, want %q", file.Path, remotePath)
			}
			if file.Content != string(content) {
				t.Fatalf("bulk content = %q, want %q", file.Content, string(content))
			}
			if file.ContentIdentity != nil {
				t.Fatalf("canonical update must not carry draft content identity: %+v", file.ContentIdentity)
			}
			if file.WritebackIntent != "update" {
				t.Fatalf("writebackIntent = %q, want update", file.WritebackIntent)
			}
			_, _ = io.WriteString(w, `{"written":1,"errorCount":0,"correlationId":"corr_update_292","results":[{"path":"`+remotePath+`","revision":"rev_292","writeback":{"provider":"linear","state":"succeeded"}}]}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{"writeback", "update", localPath, "--server", server.URL, "--token", "rf_write", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatal("expected missing operation id error")
	}
	if got := err.Error(); !strings.Contains(got, "provider writeback was not dispatched") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sawBulk.Load() {
		t.Fatal("expected bulk write request")
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "failed")); got != 1 {
		t.Fatalf("failed receipt count = %d, want 1", got)
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "acked")); got != 0 {
		t.Fatalf("acked receipt count = %d, want 0", got)
	}
}

func TestWritebackUpdateRejectsDraftPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}
	localPath := filepath.Join(localDir, "factory-create-ar-272-test.json")
	if err := os.WriteFile(localPath, []byte(`{"title":"draft"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	var stdout bytes.Buffer
	err := run([]string{"writeback", "update", localPath, "--token", "rf_write"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatal("expected draft path rejection")
	}
	if got := err.Error(); !strings.Contains(got, "requires a canonical provider record path") {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, state := range []string{"pending", "acked", "failed"} {
		if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", state)); got != 0 {
			t.Fatalf("%s receipt count = %d, want 0", state, got)
		}
	}
}

func TestWritebackDeleteCallsFilesystemDeleteAndPollsOperation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	const remotePath = "/linear/issues/AR-272__00000000-0000-0000-0000-000000000272.json"
	const opID = "op_delete_272"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}
	localPath := filepath.Join(localDir, "AR-272__00000000-0000-0000-0000-000000000272.json")
	if err := os.WriteFile(localPath, []byte(`{"title":"delete me"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	var sawDelete atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/workspaces/"+workspaceID+"/fs/file":
			sawDelete.Store(true)
			if got := r.Header.Get("If-Match"); got != "*" {
				t.Fatalf("If-Match = %q, want *", got)
			}
			if got := r.URL.Query().Get("path"); got != remotePath {
				t.Fatalf("delete path = %q, want %q", got, remotePath)
			}
			_, _ = io.WriteString(w, `{"opId":"`+opID+`","status":"queued","targetRevision":"rev_3","writeback":{"provider":"linear","state":"pending"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspaces/"+workspaceID+"/ops/"+opID:
			_, _ = io.WriteString(w, `{"opId":"`+opID+`","path":"`+remotePath+`","status":"succeeded","revision":"rev_3"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := run([]string{"writeback", "delete", localPath, "--server", server.URL, "--token", "rf_write", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("writeback delete failed: %v\n%s", err, stdout.String())
	}
	if !sawDelete.Load() {
		t.Fatal("expected delete request")
	}
	if got := stdout.String(); !strings.Contains(got, "Deleted "+remotePath) || !strings.Contains(got, opID) {
		t.Fatalf("unexpected stdout: %s", got)
	}
}

func TestWritebackDeleteRequiresOperationID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	const remotePath = "/linear/issues/AR-292__00000000-0000-0000-0000-000000000292.json"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}
	localPath := filepath.Join(localDir, "AR-292__00000000-0000-0000-0000-000000000292.json")
	if err := os.WriteFile(localPath, []byte(`{"title":"delete me"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	var sawDelete atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/workspaces/"+workspaceID+"/fs/file":
			sawDelete.Store(true)
			_, _ = io.WriteString(w, `{"status":"queued","targetRevision":"rev_delete_missing_op","writeback":{"provider":"linear","state":"pending"}}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{"writeback", "delete", localPath, "--server", server.URL, "--token", "rf_write", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatal("expected missing operation id error")
	}
	if got := err.Error(); !strings.Contains(got, "writeback delete did not return an operation id") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sawDelete.Load() {
		t.Fatal("expected delete request")
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "failed")); got != 1 {
		t.Fatalf("failed receipt count = %d, want 1", got)
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "acked")); got != 0 {
		t.Fatalf("acked receipt count = %d, want 0", got)
	}
}

func TestWritebackUpdateFailsWhenOperationFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	const workspaceID = "ws_demo"
	const remotePath = "/linear/issues/AR-272__00000000-0000-0000-0000-000000000272.json"
	const opID = "op_failed_272"
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: workspaceID,
		RemoteRoot:  "/linear/issues",
		Mode:        defaultMountMode,
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}
	localPath := filepath.Join(localDir, "AR-272__00000000-0000-0000-0000-000000000272.json")
	if err := os.WriteFile(localPath, []byte(`{"stateId":"missing"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write local payload failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/workspaces/"+workspaceID+"/fs/bulk":
			_, _ = io.WriteString(w, `{"written":1,"errorCount":0,"correlationId":"corr_failed_272","results":[{"path":"`+remotePath+`","revision":"rev_4","opId":"`+opID+`","writeback":{"provider":"linear","state":"pending"}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/workspaces/"+workspaceID+"/ops/"+opID:
			_, _ = io.WriteString(w, `{"opId":"`+opID+`","path":"`+remotePath+`","status":"failed","revision":"rev_4","lastError":"ADAPTER_ERROR: not found"}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{"writeback", "update", localPath, "--server", server.URL, "--token", "rf_write", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatal("expected failed operation error")
	}
	if got := err.Error(); !strings.Contains(got, "writeback operation "+opID+" failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "failed")); got != 1 {
		t.Fatalf("failed receipt count = %d, want 1", got)
	}
	if got := countJSONFiles(filepath.Join(localDir, ".relay", "outbox", "acked")); got != 0 {
		t.Fatalf("acked receipt count = %d, want 0", got)
	}
}

func TestWorkspaceStatusReportsLocalMountHealth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID:               "ws_demo",
		RemoteRoot:                "/linear/issues",
		Mode:                      defaultMountMode,
		Status:                    "syncing",
		LastSuccessfulReconcileAt: "2026-06-11T15:14:48Z",
		LastReconcileAt:           "2026-06-15T10:00:00Z",
		LastError:                 &statusError{Message: "changed event not readable yet"},
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}
	mountState := map[string]any{
		"incrementalReadNotReadySince": map[string]string{
			"/linear/issues/by-state/ready-for-agent/AR-1.json": "2026-06-15T09:00:00Z",
			"/linear/issues/by-state/ready-for-agent/AR-2.json": "2026-06-15T09:01:00Z",
		},
		"incrementalBacklogDraining": true,
	}
	mountStatePayload, err := json.Marshal(mountState)
	if err != nil {
		t.Fatalf("marshal mount state failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relayfile-mount-state.json"), mountStatePayload, 0o644); err != nil {
		t.Fatalf("write mount state failed: %v", err)
	}
	for _, path := range []string{
		filepath.Join(localDir, ".relay", "outbox", "pending", "mountcmd_pending.json"),
		filepath.Join(localDir, ".relay", "outbox", "failed", "mountcmd_failed.json"),
		filepath.Join(localDir, ".relay", "outbox", "acked", "mountcmd_acked.json"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s failed: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", path, err)
		}
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "status", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("workspace status failed: %v\n%s", err, stdout.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"workspace: ws_demo (demo)",
		"status: syncing",
		"last successful reconcile: 2026-06-11T15:14:48Z",
		"stuck events: 2",
		"outbox: pending=1 failed=1 acked=1",
		"last error: changed event not readable yet",
		"incremental backlog: draining",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("workspace status output missing %q:\n%s", want, output)
		}
	}
}

func TestStatusSurfacesDegradedStallReason(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	persisted := syncStateFile{
		WorkspaceID: "ws_demo",
		Mode:        defaultMountMode,
		StallReason: "cloud session expired — run 'agent-relay login' to refresh",
	}
	if err := writeMirrorStateFile(localDir, persisted); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"ready","lagSeconds":4,"watermarkTs":"2026-05-02T18:00:00Z"}]}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "stall: cloud session expired") {
		t.Fatalf("expected degraded stall reason in status output, got: %q", got)
	}
	if !strings.Contains(got, "agent-relay login") {
		t.Fatalf("expected recovery hint in status output, got: %q", got)
	}
}

func TestEnsureMirrorLayoutDoesNotRewriteUnchangedSkill(t *testing.T) {
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	skillPath := filepath.Join(localDir, ".skills", "activity-summary.md")
	before, err := os.Stat(skillPath)
	if err != nil {
		t.Fatalf("stat skill before second layout failed: %v", err)
	}
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("second ensureMirrorLayout failed: %v", err)
	}
	after, err := os.Stat(skillPath)
	if err != nil {
		t.Fatalf("stat skill after second layout failed: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatalf("expected unchanged skill file not to be atomically replaced")
	}
}

func TestBuildSyncStateSnapshotPreservesRemoteRoot(t *testing.T) {
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, ".relay"), 0o755); err != nil {
		t.Fatalf("mkdir .relay failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "state.json"), []byte(`{"remoteRoot":"/notion"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write state failed: %v", err)
	}

	snapshot := buildSyncStateSnapshot(syncStatusResponse{}, "ws_demo", defaultMountMode, defaultMountInterval, localDir, 0, "")
	if snapshot.RemoteRoot != "/notion" {
		t.Fatalf("expected remoteRoot to be preserved, got %q", snapshot.RemoteRoot)
	}
}

func TestReadGuardCountersAcceptsMirrorStateGuardsShape(t *testing.T) {
	localDir := t.TempDir()
	if err := writeMirrorStateFile(localDir, syncStateFile{
		WorkspaceID: "ws_demo",
		Mode:        defaultMountMode,
		Guards: &syncStateGuards{
			SkippedOversizeWriteback: 3,
			SnapshotDeleteBlocked:    2,
			PathCollisionQuarantined: 5,
			LastAppliedRevision:      "rev_9",
			Circuit: &syncStateGuardCirc{
				Open:       true,
				OpenEvents: 1,
				Failures:   4,
			},
		},
	}); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}

	got := readGuardCounters(localDir)
	if got == nil {
		t.Fatalf("expected guard counters")
	}
	if got.SkippedOversizeWriteback != 3 || got.SnapshotDeleteBlocked != 2 || got.PathCollisionQuarantined != 5 || got.LastAppliedRevision != "rev_9" {
		t.Fatalf("unexpected guard counters: %#v", got)
	}
	if got.Circuit == nil || !got.Circuit.Open || got.Circuit.OpenEvents != 1 || got.Circuit.Failures != 4 {
		t.Fatalf("unexpected circuit counters: %#v", got.Circuit)
	}
}

func TestReadGuardCountersAcceptsMountsyncCountersShape(t *testing.T) {
	localDir := t.TempDir()
	stateDir := filepath.Join(localDir, ".relay")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir .relay failed: %v", err)
	}
	payload := []byte(`{"counters":{"pathCollisionQuarantined":7}}` + "\n")
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), payload, 0o644); err != nil {
		t.Fatalf("write state failed: %v", err)
	}

	got := readGuardCounters(localDir)
	if got == nil {
		t.Fatalf("expected guard counters")
	}
	if got.PathCollisionQuarantined != 7 {
		t.Fatalf("expected path collision counter, got %#v", got)
	}
}

func TestStatusRendersWebhookUnhealthyWarning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/workspaces/ws_demo/sync/status" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"lagging","lagSeconds":80,"watermarkTs":"2026-05-02T18:00:00Z","webhookHealthy":false}]}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "notion webhook unhealthy") {
		t.Fatalf("expected webhook-unhealthy warning row, got: %q", got)
	}
	if !strings.Contains(got, "lag 1m20s") {
		t.Fatalf("expected lag in warning row, got: %q", got)
	}
}

func TestStatusExplainsLaggingProviderWithoutWatermark(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	var ingressCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/ws_demo/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"github","status":"lagging","cursor":"","watermarkTs":" ","lagSeconds":0}]}`))
		case "/v1/workspaces/ws_demo/sync/ingress":
			ingressCalls.Add(1)
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","queueDepth":0,"pendingTotal":0,"deadLetterTotal":0,"acceptedTotal":55,"ingressByProvider":{"linear":{"acceptedTotal":55,"pendingTotal":0}}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "github       lagging  lag 0s") {
		t.Fatalf("expected lagging provider row, got: %q", got)
	}
	if !strings.Contains(got, "reason: no sync cursor or watermark; no provider-specific ingress events recorded") {
		t.Fatalf("expected lag reason in status output, got: %q", got)
	}
	if ingressCalls.Load() != 1 {
		t.Fatalf("expected status command to request /sync/ingress once, got %d", ingressCalls.Load())
	}
}

func TestSyncProviderLagReasonUsesAliasesAndObservedCounters(t *testing.T) {
	blank := ""
	provider := syncProviderStatus{
		Provider:    "slack-sage",
		Status:      "lagging",
		Cursor:      &blank,
		WatermarkTs: &blank,
	}
	ingress := &syncIngressStatusResponse{
		IngressByProvider: map[string]syncIngressProviderStatus{
			"slack": {DedupedTotal: 2, CoalescedTotal: 1},
		},
	}

	got := syncProviderLagReason(provider, ingress)
	if !strings.Contains(got, "3 ingress event(s) observed") {
		t.Fatalf("expected alias ingress observation reason, got %q", got)
	}
}

func TestStatusOmitsWebhookWarningWhenHealthy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"ready","lagSeconds":4,"watermarkTs":"2026-05-02T18:00:00Z","webhookHealthy":true}]}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	if got := stdout.String(); strings.Contains(got, "webhook unhealthy") {
		t.Fatalf("did not expect webhook-unhealthy warning, got: %q", got)
	}
}

func TestOpsListReadsLocalDeadLetterMirror(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	dlDir := filepath.Join(localDir, ".relay", "dead-letter")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatalf("mkdir dead-letter failed: %v", err)
	}
	record := `{"opId":"op_abc","path":"/notion/page.json","code":"validation_error","message":"body must include event","attempts":3,"lastAttemptedAt":"2026-05-02T18:05:00Z","replayUrl":"https://cloud.test/api/v1/workspaces/ws_demo/ops/op_abc/replay"}`
	if err := os.WriteFile(filepath.Join(dlDir, "op_abc.json"), []byte(record), 0o644); err != nil {
		t.Fatalf("write dead-letter record failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"ops", "list", "--workspace", "demo", "--no-refresh"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run ops list failed: %v", err)
	}
	got := stdout.String()
	for _, fragment := range []string{"op_abc", "/notion/page.json", "validation_error", "2026-05-02T18:05:00Z"} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("expected %q in ops list output, got %q", fragment, got)
		}
	}
}

func TestOpsListReportsEmptyWhenNoDeadLetterDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"ops", "list", "--workspace", "demo", "--no-refresh"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run ops list failed: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "No dead-lettered ops" {
		t.Fatalf("expected empty marker, got: %q", got)
	}
}

func TestOpsListRefreshesMirrorFromServer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	dlDir := filepath.Join(localDir, ".relay", "dead-letter")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatalf("mkdir dead-letter failed: %v", err)
	}
	// Pre-existing record that the server no longer reports — must be pruned.
	if err := os.WriteFile(filepath.Join(dlDir, "op_old.json"), []byte(`{"opId":"op_old"}`), 0o644); err != nil {
		t.Fatalf("write stale record failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/workspaces/ws_demo/ops" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("status"); got != "dead_lettered" {
			t.Fatalf("unexpected status filter: %q", got)
		}
		_, _ = w.Write([]byte(`{"items":[{"opId":"op_new","path":"/notion/page.json","status":"dead_lettered","attemptCount":3,"lastError":"schema validation failed: body must include event","createdAt":"2026-05-02T17:00:00Z","updatedAt":"2026-05-02T18:05:00Z"}]}`))
	}))
	defer server.Close()

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"ops", "list", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run ops list failed: %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "op_new") {
		t.Fatalf("expected refreshed record op_new in output: %q", got)
	}
	if strings.Contains(got, "op_old") {
		t.Fatalf("expected stale record op_old to be pruned, got: %q", got)
	}
	// Verify the new record landed on disk with the expected shape.
	payload, err := os.ReadFile(filepath.Join(dlDir, "op_new.json"))
	if err != nil {
		t.Fatalf("read refreshed record failed: %v", err)
	}
	var record deadLetterRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		t.Fatalf("unmarshal refreshed record failed: %v", err)
	}
	if record.Code != "validation_error" {
		t.Fatalf("expected validation_error code, got %q", record.Code)
	}
	if !strings.HasSuffix(record.ReplayURL, "/v1/workspaces/ws_demo/ops/op_new/replay") {
		t.Fatalf("unexpected replay URL: %q", record.ReplayURL)
	}
	if _, err := os.Stat(filepath.Join(dlDir, "op_old.json")); !os.IsNotExist(err) {
		t.Fatalf("expected stale record removed, got err=%v", err)
	}
}

func TestOpsListRefreshRejectsUnsafeOpIDAndKeepsSidecar(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	dlDir := filepath.Join(localDir, ".relay", "dead-letter")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatalf("mkdir dead-letter failed: %v", err)
	}
	// Diagnostic sidecar for a still-live op — must survive a refresh.
	if err := os.WriteFile(filepath.Join(dlDir, "op_keep.error.json"), []byte(`{"providerResponse":"boom"}`), 0o644); err != nil {
		t.Fatalf("write sidecar failed: %v", err)
	}
	// Stale payload the server no longer reports — must be pruned along
	// with any matching sidecar.
	if err := os.WriteFile(filepath.Join(dlDir, "op_old.json"), []byte(`{"opId":"op_old"}`), 0o644); err != nil {
		t.Fatalf("write stale record failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dlDir, "op_old.error.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write stale sidecar failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[` +
			`{"opId":"op_keep","path":"/n/p.json","status":"dead_lettered","lastError":"schema validation failed"},` +
			`{"opId":"../../pwn","path":"/n/x.json","status":"dead_lettered","lastError":"oops"}` +
			`]}`))
	}))
	defer server.Close()

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"ops", "list", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run ops list failed: %v", err)
	}

	// The traversal op id must not escape the dead-letter directory.
	traversal := filepath.Join(localDir, "pwn.json")
	if _, err := os.Stat(traversal); !os.IsNotExist(err) {
		t.Fatalf("unsafe opId escaped dead-letter dir: %s exists (err=%v)", traversal, err)
	}
	if _, err := os.Stat(filepath.Join(dlDir, "op_keep.json")); err != nil {
		t.Fatalf("expected op_keep payload written: %v", err)
	}
	// Sidecar for a still-live op must be preserved, not pruned as if it
	// were a standalone payload named "op_keep.error".
	if _, err := os.Stat(filepath.Join(dlDir, "op_keep.error.json")); err != nil {
		t.Fatalf("expected op_keep.error.json preserved: %v", err)
	}
	// Stale payload and its sidecar must both be removed.
	if _, err := os.Stat(filepath.Join(dlDir, "op_old.json")); !os.IsNotExist(err) {
		t.Fatalf("expected stale op_old.json pruned, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dlDir, "op_old.error.json")); !os.IsNotExist(err) {
		t.Fatalf("expected stale op_old.error.json pruned, err=%v", err)
	}
}

func TestPullTriggersSyncRefreshForConnectedProviders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	refreshes := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/workspaces/ws_demo/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"ready","lagSeconds":4},{"provider":"github","status":"ready","lagSeconds":1}]}`))
		case "/v1/workspaces/ws_demo/sync/refresh":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			var body struct {
				Provider string `json:"provider"`
				Reason   string `json:"reason"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode refresh body failed: %v", err)
			}
			if body.Reason != "manual" {
				t.Fatalf("unexpected refresh reason: %q", body.Reason)
			}
			refreshes[body.Provider] = true
			_, _ = w.Write([]byte(`{"status":"queued","id":"sync_job_1"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"pull", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run pull failed: %v\noutput:\n%s", err, stdout.String())
	}
	for _, provider := range []string{"notion", "github"} {
		if !refreshes[provider] {
			t.Fatalf("expected refresh for %s", provider)
		}
	}
	if got := stdout.String(); !strings.Contains(got, "notion refresh queued") || !strings.Contains(got, "github refresh queued") {
		t.Fatalf("unexpected pull output: %q", got)
	}
}

func TestPullScopedToSingleProviderSkipsStatusCall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/workspaces/ws_demo/sync/refresh" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"queued","id":"sync_job_2"}`))
	}))
	defer server.Close()

	writeDelegatedCredentialsForTest(t, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_demo",
		AccessToken:          "delegated_token",
	})

	var stdout bytes.Buffer
	if err := run([]string{"pull", "--workspace", "demo", "--provider", "notion", "--reason", "investigation"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run pull failed: %v\noutput:\n%s", err, stdout.String())
	}
	if got := stdout.String(); !strings.Contains(got, "notion refresh queued") {
		t.Fatalf("unexpected pull output: %q", got)
	}
}

func TestIntegrationCatalogCacheServesWithinTTL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers":[{"id":"notion","displayName":"Notion","vfsRoot":"/notion"}],"version":"v1"}`))
	}))
	defer server.Close()

	first, err := loadIntegrationCatalog(server.URL, "tok")
	if err != nil {
		t.Fatalf("loadIntegrationCatalog first call failed: %v", err)
	}
	second, err := loadIntegrationCatalog(server.URL, "tok")
	if err != nil {
		t.Fatalf("loadIntegrationCatalog second call failed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 catalog fetch, got %d", calls)
	}
	if len(first) != 1 || first[0].ID != "notion" || len(second) != 1 || second[0].ID != "notion" {
		t.Fatalf("unexpected catalog contents: %+v / %+v", first, second)
	}
}

func TestIntegrationAvailableSearchesDynamicCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("dynamic") != "true" {
			t.Fatalf("expected dynamic catalog query, got %s", r.URL.RawQuery)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("available catalog should not require auth, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers":[{"id":"salesforce","displayName":"Salesforce","backend":"nango","backends":["nango"],"vfsRoot":"/salesforce"},{"id":"docker_hub","displayName":"Docker Hub","backend":"composio","backends":["composio"],"vfsRoot":"/docker_hub"}],"version":"v_dynamic"}`))
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := run([]string{"integration", "search", "--cloud-api-url", server.URL, "--backend", "composio", "docker"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run integration available failed: %v\noutput:\n%s", err, stdout.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "docker_hub") || !strings.Contains(got, "Docker Hub") {
		t.Fatalf("expected docker_hub in output, got %q", got)
	}
	if strings.Contains(got, "salesforce") {
		t.Fatalf("expected backend/search filter to exclude salesforce, got %q", got)
	}
}

func TestIntegrationAvailableFallsBackWhenDynamicCatalogUnavailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("dynamic") != "true" {
			t.Fatalf("expected dynamic catalog query, got %s", r.URL.RawQuery)
		}
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := run([]string{"integration", "available", "--cloud-api-url", server.URL, "--backend", "composio", "--search", "github"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run integration available failed: %v\noutput:\n%s", err, stdout.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "github") || !strings.Contains(got, "GitHub") {
		t.Fatalf("expected fallback github entry in output, got %q", got)
	}
}

func TestIntegrationAvailableUsesDynamicCatalogCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers":[{"id":"docker_hub","displayName":"Docker Hub","backend":"composio","backends":["composio"],"vfsRoot":"/docker_hub"}],"version":"v_dynamic"}`))
	}))
	defer server.Close()

	for i := 0; i < 2; i++ {
		var stdout bytes.Buffer
		if err := run([]string{"integration", "search", "docker", "--cloud-api-url", server.URL}, strings.NewReader(""), &stdout, &stdout); err != nil {
			t.Fatalf("run integration search failed: %v\noutput:\n%s", err, stdout.String())
		}
	}
	if calls != 1 {
		t.Fatalf("expected dynamic catalog cache to serve second search, got %d calls", calls)
	}
}

func TestIntegrationCatalogCachesDynamicAndRegularSeparately(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	regularCalls := 0
	dynamicCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("dynamic") == "true" {
			dynamicCalls++
			_, _ = w.Write([]byte(`{"providers":[{"id":"docker_hub","displayName":"Docker Hub","backend":"composio","backends":["composio"],"vfsRoot":"/docker_hub"}],"version":"v_dynamic"}`))
			return
		}
		regularCalls++
		_, _ = w.Write([]byte(`{"providers":[{"id":"notion","displayName":"Notion","vfsRoot":"/notion"}],"version":"v_regular"}`))
	}))
	defer server.Close()

	if _, err := loadIntegrationCatalog(server.URL, "tok"); err != nil {
		t.Fatalf("loadIntegrationCatalog first call failed: %v", err)
	}
	var stdout bytes.Buffer
	if err := run([]string{"integration", "search", "docker", "--cloud-api-url", server.URL}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run integration search failed: %v\noutput:\n%s", err, stdout.String())
	}
	if _, err := loadIntegrationCatalog(server.URL, "tok"); err != nil {
		t.Fatalf("loadIntegrationCatalog second call failed: %v", err)
	}
	if regularCalls != 1 {
		t.Fatalf("expected regular catalog cache to survive dynamic fetch, got %d regular calls", regularCalls)
	}
	if dynamicCalls != 1 {
		t.Fatalf("expected one dynamic catalog fetch, got %d", dynamicCalls)
	}
}

func TestIntegrationCatalogCacheRefetchesAfterInvalidate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers":[{"id":"notion","displayName":"Notion","vfsRoot":"/notion"}],"version":"v1"}`))
	}))
	defer server.Close()

	if _, err := loadIntegrationCatalog(server.URL, "tok"); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	invalidateIntegrationCatalogCache()
	if _, err := loadIntegrationCatalog(server.URL, "tok"); err != nil {
		t.Fatalf("post-invalidate call failed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 catalog fetches after invalidate, got %d", calls)
	}
}

func TestOpsReplayPostsToCloudAndRemovesLocalRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	dlDir := filepath.Join(localDir, ".relay", "dead-letter")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatalf("mkdir dead-letter failed: %v", err)
	}
	dlPath := filepath.Join(dlDir, "op_abc.json")
	if err := os.WriteFile(dlPath, []byte(`{"opId":"op_abc","attempts":3}`), 0o644); err != nil {
		t.Fatalf("write dead-letter record failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		AgentName:  "relayfile-cli",
		Scopes:     append([]string(nil), defaultJoinScopes...),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_demo/ops/op_abc/replay":
			seen["replay"] = true
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected replay method: %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cld_token" {
				t.Fatalf("unexpected replay Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"status":"queued","id":"op_abc"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	installFakeAgentRelaySession(t, server.URL, "cld_token", "demo", "ws_demo", "ws_demo")

	var stdout bytes.Buffer
	if err := run([]string{
		"ops", "replay", "op_abc",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run ops replay failed: %v\noutput:\n%s", err, stdout.String())
	}
	for _, key := range []string{"replay"} {
		if !seen[key] {
			t.Fatalf("expected %s request", key)
		}
	}
	if got := stdout.String(); !strings.Contains(got, "Replay queued for op op_abc") {
		t.Fatalf("unexpected replay output: %q", got)
	}
	if _, err := os.Stat(dlPath); !os.IsNotExist(err) {
		t.Fatalf("expected dead-letter record removed, got err=%v", err)
	}
}

// TestLoginWithExplicitTokenPersistsServerCreds covers the legacy API-key
// path: when --token is supplied, runLogin validates it against /health and
// writes ~/.relayfile/credentials.json (no cloud creds touched).
func TestLoginWithExplicitTokenPersistsServerCreds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveLegacyCloudCredentials(cloudCredentials{
		APIURL:      "https://cloud.relayfile.test",
		AccessToken: "stale_cloud_token",
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}

	var healthCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer rf_test" {
			t.Fatalf("unexpected Authorization: %q", got)
		}
		healthCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	if err := run([]string{"login", "--server", server.URL, "--token", "rf_test"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run login failed: %v\noutput:\n%s", err, stdout.String())
	}
	if healthCalls != 1 {
		t.Fatalf("expected 1 /health call, got %d", healthCalls)
	}
	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials failed: %v", err)
	}
	if creds.Token != "rf_test" {
		t.Fatalf("expected stored token rf_test, got %q", creds.Token)
	}
	if _, err := os.Stat(cloudCredentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected stale cloud credentials removed, got err=%v", err)
	}
}

// TestLoginDelegatesToAgentRelay covers the unified auth behavior: relayfile
// login no longer writes its own cloud credential store; it delegates to the
// canonical agent-relay login command.
func TestLoginDelegatesToAgentRelay(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveLegacyCloudCredentials(cloudCredentials{
		APIURL:      "https://cloud.relayfile.test",
		AccessToken: "stale_cloud_token",
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "agent-relay.log")
	t.Setenv("AGENT_RELAY_LOG", logPath)
	var seenDelegated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/ws_123/relayfile/delegated-token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		seenDelegated = true
		if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
			t.Fatalf("unexpected delegated-token Authorization: %q", got)
		}
		var body cloudRelayfileDelegatedTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode delegated-token body: %v", err)
		}
		if body.AgentName != "relayfile-cli" {
			t.Fatalf("unexpected agentName: %q", body.AgentName)
		}
		if got, want := strings.Join(body.Scopes, ","), "fs:read,fs:write"; got != want {
			t.Fatalf("delegated-token scopes = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		writeDelegatedBundleResponse(t, w, "https://relayfile.test", "ws_123", "rf_login", "refresh_login")
	}))
	defer server.Close()
	installFakeAgentRelay(t, `
printf '%s\n' "$*" >> "$AGENT_RELAY_LOG"
if [ "$*" = "cloud login --no-open" ]; then
  echo "agent-relay login ok"
  exit 0
fi
if [ "$*" = "cloud session --json" ]; then
  echo '{"apiUrl":"`+server.URL+`","accessToken":"cld_new"}'
  exit 0
fi
if [ "$*" = "workspace active --json" ]; then
  echo '{"name":"demo","cloudWorkspaceId":"ws_123","relayfileWorkspaceId":"ws_123"}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`)

	var stdout bytes.Buffer
	if err := run([]string{
		"login",
		"--no-open",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run login failed: %v\noutput:\n%s", err, stdout.String())
	}

	got := stdout.String()
	if !strings.Contains(got, "agent-relay login ok") {
		t.Fatalf("expected delegated agent-relay output, got %q", got)
	}
	if !strings.Contains(got, "Relayfile now uses the active agent-relay cloud session and workspace demo.") {
		t.Fatalf("expected relayfile canonical-session note, got %q", got)
	}
	if !seenDelegated {
		t.Fatalf("expected delegated-token bootstrap call")
	}
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake agent-relay log failed: %v", err)
	}
	gotLog := strings.TrimSpace(string(logBytes))
	for _, want := range []string{"cloud login --no-open", "cloud session --json", "workspace active --json"} {
		if !strings.Contains(gotLog, want) {
			t.Fatalf("expected agent-relay %s call, got %q", want, string(logBytes))
		}
	}
	if _, err := os.Stat(cloudCredentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected stale relayfile cloud credentials removed, got err=%v", err)
	}
	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected stale relayfile credentials removed, got err=%v", err)
	}
	delegated, err := delegatedauth.Load(delegatedCredentialsPathForRequest("ws_123", defaultJoinScopes))
	if err != nil {
		t.Fatalf("expected delegated credentials to be stored: %v", err)
	}
	if delegated.BearerToken() != "rf_login" || delegated.RotationToken() != "refresh_login" {
		t.Fatalf("unexpected delegated credentials: %#v", delegated)
	}
}

func TestPrepareWorkspaceCommandClientBootstrapsDelegatedCredentialsDespiteStaleLegacyToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveCredentials(credentials{
		Server:    "https://stale-relayfile.test",
		Token:     "stale_legacy_token",
		UpdatedAt: time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var seenDelegated bool
	var seenTree bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_cloud/relayfile/delegated-token":
			seenDelegated = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			var body cloudRelayfileDelegatedTokenRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode delegated-token body: %v", err)
			}
			if got, want := strings.Join(body.Scopes, ","), "relayfile:fs:read:*"; got != want {
				t.Fatalf("delegated-token scopes = %q, want %q", got, want)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_cloud", "rf_read", "refresh_read")
		case "/v1/workspaces/ws_cloud/fs/tree":
			seenTree = true
			if got := r.Header.Get("Authorization"); got != "Bearer rf_read" {
				t.Fatalf("unexpected data-plane Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"path":"/github","entries":[],"nextCursor":null}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	installFakeAgentRelaySession(t, server.URL, "cld_access", "cloud-prod", "ws_cloud", "ws_cloud")

	var stdout bytes.Buffer
	if err := run([]string{"tree", "cloud-prod", "/github"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v\noutput:\n%s", err, stdout.String())
	}
	if !seenDelegated {
		t.Fatalf("expected delegated-token bootstrap call")
	}
	if !seenTree {
		t.Fatalf("expected tree data-plane call")
	}
	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected legacy relayfile credentials removed, got err=%v", err)
	}
	delegated, err := delegatedauth.Load(delegatedCredentialsPathForRequest("cloud-prod", defaultInspectScopes))
	if err != nil {
		t.Fatalf("expected delegated credentials to be stored: %v", err)
	}
	if delegated.BearerToken() != "rf_read" || delegated.RotationToken() != "refresh_read" {
		t.Fatalf("unexpected delegated credentials: %#v", delegated)
	}
}

func TestWorkspaceCommandClientRefreshesExpiredDelegatedAccessToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var sawRefresh bool
	var sawStatus bool
	expired := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "relayfile-cli", time.Now().Add(-time.Minute))
	renewedAccessToken := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "relayfile-cli", time.Now().Add(time.Hour))
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			sawRefresh = true
			var body struct {
				RefreshToken string `json:"refreshToken"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode refresh body: %v", err)
			}
			if body.RefreshToken != "refresh_old" {
				t.Fatalf("refresh token = %q, want refresh_old", body.RefreshToken)
			}
			_ = json.NewEncoder(w).Encode(delegatedauth.TokenPair{
				AccessToken:           renewedAccessToken,
				RefreshToken:          "refresh_new",
				AccessTokenExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
				DelegationNotAfter:    time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			})
		case "/v1/workspaces/ws_refresh/sync/status":
			sawStatus = true
			if got, want := r.Header.Get("Authorization"), "Bearer "+renewedAccessToken; got != want {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_refresh","providers":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	credsPath := delegatedCredentialsPathForRequest("ws_refresh", defaultInspectScopes)
	if err := delegatedauth.SaveAtomic(credsPath, delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayauthURL:          server.URL,
		RelayfileWorkspaceID:  "ws_refresh",
		AccessToken:           expired,
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save delegated credentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"status", "ws_refresh"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v\noutput:\n%s", err, stdout.String())
	}
	if !sawRefresh {
		t.Fatalf("expected delegated refresh call")
	}
	if !sawStatus {
		t.Fatalf("expected status data-plane call")
	}
	renewed, err := delegatedauth.Load(credsPath)
	if err != nil {
		t.Fatalf("load renewed delegated credentials failed: %v", err)
	}
	if renewed.RotationToken() != "refresh_new" {
		t.Fatalf("expected rotated refresh token, got %#v", renewed)
	}
}

func TestLoadDelegatedCredentialsForRequestIgnoresInsufficientLegacyBundle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if err := delegatedauth.SaveAtomic(delegatedCredentialsPath(), delegatedauth.Bundle{
		RelayfileURL:         "https://relayfile.test",
		RelayfileWorkspaceID: "ws_cloud",
		AccessToken:          testJWTWithWorkspaceAndScopes("ws_cloud", "relayfile-cli", []string{"relayfile:fs:read:*"}),
		RefreshToken:         "refresh_read",
		RelayfileScopes:      append([]string(nil), defaultInspectScopes...),
	}); err != nil {
		t.Fatalf("save legacy delegated credentials failed: %v", err)
	}

	var sawBootstrap bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_cloud/relayfile/delegated-token":
			sawBootstrap = true
			var body cloudRelayfileDelegatedTokenRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode delegated-token body: %v", err)
			}
			if got, want := strings.Join(body.Scopes, ","), strings.Join(defaultJoinScopes, ","); got != want {
				t.Fatalf("delegated-token scopes = %q, want %q", got, want)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_cloud", "rf_join", "refresh_join")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	installFakeAgentRelaySession(t, server.URL, "cld_access", "cloud-prod", "ws_cloud", "ws_cloud")

	bundle, path, err := loadOrBootstrapDelegatedCredentials("cloud-prod", defaultJoinScopes)
	if err != nil {
		t.Fatalf("loadOrBootstrapDelegatedCredentials failed: %v", err)
	}
	if !sawBootstrap {
		t.Fatalf("expected insufficient legacy delegated bundle to be ignored")
	}
	if path == delegatedCredentialsPath() {
		t.Fatalf("expected scoped delegated credential path, got legacy path")
	}
	if bundle.BearerToken() != "rf_join" {
		t.Fatalf("expected bootstrapped bundle, got %#v", bundle)
	}
}

func TestRefreshDelegatedCredentialsFallsBackToCloudRemint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	expired := testJWTWithWorkspaceAgentAndExpiry("ws_relay", "relayfile-cli", time.Now().Add(-time.Minute))
	reminted := testJWTWithWorkspaceAgentAndExpiry("ws_relay", "relayfile-cli", time.Now().Add(time.Hour))
	var sawRemint bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_cloud/relayfile/delegated-token":
			sawRemint = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
				t.Fatalf("unexpected remint Authorization: %q", got)
			}
			var body cloudRelayfileDelegatedTokenRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode remint body: %v", err)
			}
			if got, want := strings.Join(body.Scopes, ","), "relayfile:fs:read:*"; got != want {
				t.Fatalf("remint scopes = %q, want %q", got, want)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_relay", reminted, "refresh_new")
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	installFakeAgentRelaySession(t, server.URL, "cld_access", "demo", "ws_cloud", "ws_relay")

	path := delegatedCredentialsPathForRequest("ws_cloud", defaultInspectScopes)
	if err := delegatedauth.SaveAtomic(path, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_relay",
		WorkspaceID:          "ws_cloud",
		AccessToken:          expired,
		RefreshToken:         "refresh_old",
		AgentName:            "relayfile-cli",
		Scopes:               append([]string(nil), defaultInspectScopes...),
	}); err != nil {
		t.Fatalf("save delegated credentials failed: %v", err)
	}

	renewed, err := refreshDelegatedCredentials(path, delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayfileWorkspaceID: "ws_relay",
		WorkspaceID:          "ws_cloud",
		AccessToken:          expired,
		RefreshToken:         "refresh_old",
		AgentName:            "relayfile-cli",
		Scopes:               append([]string(nil), defaultInspectScopes...),
	}, false)
	if err != nil {
		t.Fatalf("refreshDelegatedCredentials failed: %v", err)
	}
	if !sawRemint {
		t.Fatalf("expected cloud delegated-token re-mint")
	}
	if renewed.BearerToken() != reminted || renewed.RotationToken() != "refresh_new" {
		t.Fatalf("unexpected renewed bundle: %#v", renewed)
	}
	persisted, err := delegatedauth.Load(path)
	if err != nil {
		t.Fatalf("load persisted remint failed: %v", err)
	}
	if persisted.BearerToken() != reminted {
		t.Fatalf("expected reminted token persisted, got %#v", persisted)
	}
}

func TestRefreshDelegatedCredentialsSurfacesRemintFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	installFakeAgentRelay(t, `
if [ "$*" = "cloud session --json" ]; then
  echo "no active cloud session" >&2
  exit 2
fi
echo "unexpected args: $*" >&2
exit 2
`)

	expired := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "relayfile-cli", time.Now().Add(-time.Minute))
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"delegation_expired"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	path := delegatedCredentialsPathForRequest("ws_refresh", defaultInspectScopes)
	bundle := delegatedauth.Bundle{
		RelayfileURL:         server.URL,
		RelayauthURL:         server.URL,
		RelayfileWorkspaceID: "ws_refresh",
		AccessToken:          expired,
		RefreshToken:         "refresh_old",
		Scopes:               append([]string(nil), defaultInspectScopes...),
	}
	if err := delegatedauth.SaveAtomic(path, bundle); err != nil {
		t.Fatalf("save delegated credentials failed: %v", err)
	}

	_, err := refreshDelegatedCredentials(path, bundle, false)
	if err == nil {
		t.Fatal("expected refresh failure")
	}
	if !errors.Is(err, ErrDelegatedRelayfileCredentialsExpired) {
		t.Fatalf("expected delegated expiry sentinel, got %v", err)
	}
	if !strings.Contains(err.Error(), "no active cloud session") {
		t.Fatalf("expected remint failure detail, got %v", err)
	}
}

func TestWorkspaceCommandRefreshPreservesCatalogDefaultScopes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_refresh",
		AgentName:  "relayfile-cli",
		Scopes:     append([]string(nil), defaultJoinScopes...),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	expired := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "relayfile-cli", time.Now().Add(-time.Minute))
	renewedAccessToken := testJWTWithWorkspaceAgentAndExpiry("ws_refresh", "relayfile-cli", time.Now().Add(time.Hour))
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/tokens/refresh":
			_ = json.NewEncoder(w).Encode(delegatedauth.TokenPair{
				AccessToken:           renewedAccessToken,
				RefreshToken:          "refresh_new",
				AccessTokenExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
				RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			})
		case "/v1/workspaces/ws_refresh/sync/status":
			if got, want := r.Header.Get("Authorization"), "Bearer "+renewedAccessToken; got != want {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_refresh","providers":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err := delegatedauth.SaveAtomic(delegatedCredentialsPathForRequest("ws_refresh", defaultInspectScopes), delegatedauth.Bundle{
		RelayfileURL:          server.URL,
		RelayauthURL:          server.URL,
		RelayfileWorkspaceID:  "ws_refresh",
		AccessToken:           expired,
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		Scopes:                append([]string(nil), defaultInspectScopes...),
	}); err != nil {
		t.Fatalf("save delegated credentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"status", "ws_refresh"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v\noutput:\n%s", err, stdout.String())
	}
	record, ok := workspaceRecordByID("ws_refresh")
	if !ok {
		t.Fatalf("expected workspace record")
	}
	if got, want := strings.Join(record.Scopes, ","), strings.Join(defaultJoinScopes, ","); got != want {
		t.Fatalf("workspace scopes were clobbered: got %q want %q", got, want)
	}
}

func TestDelegatedRelayfileTokenViaCloudDefaultsToCoarseScopes(t *testing.T) {
	var sawRequest bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/ws_contract/relayfile/delegated-token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		sawRequest = true
		if got := r.Header.Get("Authorization"); got != "Bearer cld_contract" {
			t.Fatalf("unexpected Authorization: %q", got)
		}
		var body cloudRelayfileDelegatedTokenRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode delegated-token body: %v", err)
		}
		if body.AgentName != "relayfile-cli" {
			t.Fatalf("agentName = %q, want relayfile-cli", body.AgentName)
		}
		if got, want := strings.Join(body.Scopes, ","), "fs:read,fs:write"; got != want {
			t.Fatalf("scopes = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		writeDelegatedBundleResponse(t, w, server.URL, "ws_contract", "rf_contract", "refresh_contract")
	}))
	defer server.Close()

	bundle, err := delegatedRelayfileTokenViaCloud(cloudCredentials{
		APIURL:      server.URL,
		AccessToken: "cld_contract",
	}, "ws_contract", "", nil)
	if err != nil {
		t.Fatalf("delegatedRelayfileTokenViaCloud failed: %v", err)
	}
	if !sawRequest {
		t.Fatalf("expected delegated-token request")
	}
	if bundle.BearerToken() != "rf_contract" {
		t.Fatalf("unexpected bundle: %#v", bundle)
	}
}

func TestEnsureCloudCredentialsUsesAgentRelaySession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveLegacyCloudCredentials(cloudCredentials{
		APIURL:      "https://stale-cloud.test",
		AccessToken: "stale_cloud_token",
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}
	installFakeAgentRelay(t, `
if [ "$*" = "cloud session --json" ]; then
  echo '{"apiUrl":"https://relay-cloud.test","accessToken":"agent_cloud_token"}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`)

	creds, err := ensureCloudCredentials("", "", 0, false, io.Discard)
	if err != nil {
		t.Fatalf("ensureCloudCredentials failed: %v", err)
	}
	if creds.APIURL != "https://relay-cloud.test" {
		t.Fatalf("expected agent-relay APIURL, got %q", creds.APIURL)
	}
	if creds.AccessToken != "agent_cloud_token" {
		t.Fatalf("expected agent-relay access token, got %q", creds.AccessToken)
	}
	stale, err := loadLegacyCloudCredentials()
	if err != nil {
		t.Fatalf("loadCloudCredentials failed: %v", err)
	}
	if stale.AccessToken != "stale_cloud_token" {
		t.Fatalf("expected relayfile cloud credential file to be ignored and left untouched, got %q", stale.AccessToken)
	}
}

func TestEnsureCloudCredentialsRejectsStaleAgentRelayBeforeSessionCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	marker := filepath.Join(t.TempDir(), "session-called")
	path := filepath.Join(t.TempDir(), "agent-relay")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
if [ "$*" = "--version" ]; then
  echo "8.3.7"
  exit 0
fi
if [ "$*" = "cloud session --json" ]; then
  touch %q
  echo '{"apiUrl":"https://relay-cloud.test","accessToken":"agent_cloud_token"}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`, marker)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake stale agent-relay failed: %v", err)
	}
	t.Setenv("AGENT_RELAY_BIN", path)

	_, err := ensureCloudCredentials("", "", 0, false, io.Discard)
	if err == nil {
		t.Fatal("expected stale agent-relay CLI to be rejected")
	}
	got := err.Error()
	for _, want := range []string{"agent-relay CLI >= 8.7.0 required", "8.3.7", "npm install -g agent-relay@8.7.0", "sandbox image"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected error to contain %q, got %q", want, got)
		}
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("expected cloud session command to be skipped, stat err=%v", statErr)
	}
}

// TestLoginAPIKeyFlagPromptsForToken covers the opt-in legacy interactive
// flow: --api-key forces runLogin to prompt for an API key on stdin instead
// of running the cloud browser flow.
func TestLoginAPIKeyFlagPromptsForToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer prompted_key" {
			t.Fatalf("unexpected Authorization: %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	stdin := strings.NewReader("prompted_key\n")
	var stdout bytes.Buffer
	if err := run([]string{"login", "--api-key", "--server", server.URL}, stdin, &stdout, &stdout); err != nil {
		t.Fatalf("run login --api-key failed: %v\noutput:\n%s", err, stdout.String())
	}
	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials failed: %v", err)
	}
	if creds.Token != "prompted_key" {
		t.Fatalf("expected stored token prompted_key, got %q", creds.Token)
	}
}

// TestWorkspaceCurrentPrintsActiveWorkspace covers the new
// `relayfile workspace current` subcommand: it returns the workspace
// resolveWorkspaceRecord("") would pick, and `--verbose` annotates it
// with the resolution source.
func TestWorkspaceCurrentPrintsActiveWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if _, err := upsertWorkspace("alpha"); err != nil {
		t.Fatalf("upsertWorkspace alpha failed: %v", err)
	}
	if _, err := setDefaultWorkspace("alpha"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "current"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace current failed: %v\noutput:\n%s", err, stdout.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "alpha" {
		t.Fatalf("unexpected workspace current output: %q", got)
	}

	stdout.Reset()
	if err := run([]string{"workspace", "current", "--verbose"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace current --verbose failed: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); !strings.Contains(got, "source: default") {
		t.Fatalf("expected verbose output to include source, got %q", got)
	}
}

func TestWorkspaceCurrentPrefersAgentRelayActiveWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if _, err := upsertWorkspace("stale-local-default"); err != nil {
		t.Fatalf("upsertWorkspace failed: %v", err)
	}
	if _, err := setDefaultWorkspace("stale-local-default"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}
	installFakeAgentRelay(t, `
if [ "$*" = "workspace active --json" ]; then
  echo '{"name":"canonical","cloudWorkspaceId":"rw_cloud","relayfileWorkspaceId":"rw_relayfile","relaycastWorkspaceId":"rw_relaycast","relayauthWorkspaceId":"rw_relayauth"}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`)

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "current", "--verbose"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace current failed: %v\noutput:\n%s", err, stdout.String())
	}
	got := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(got, "canonical") {
		t.Fatalf("expected canonical active workspace, got %q", got)
	}
	if !strings.Contains(got, "source: agent-relay") {
		t.Fatalf("expected agent-relay source, got %q", got)
	}
}

func TestResolveWorkspaceUsesAgentRelayRelayfileWorkspaceID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if _, err := upsertWorkspace("stale-local-default"); err != nil {
		t.Fatalf("upsertWorkspace failed: %v", err)
	}
	if _, err := setDefaultWorkspace("stale-local-default"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}
	installFakeAgentRelay(t, `
if [ "$*" = "workspace active --json" ]; then
  echo '{"name":"canonical","cloudWorkspaceId":"rw_cloud","relayfileWorkspaceId":"rw_relayfile"}'
  exit 0
fi
echo "unexpected args: $*" >&2
exit 2
`)

	got, err := resolveWorkspaceIDWithToken("", "")
	if err != nil {
		t.Fatalf("resolveWorkspaceIDWithToken failed: %v", err)
	}
	if got != "rw_relayfile" {
		t.Fatalf("expected relayfile workspace id from agent-relay descriptor, got %q", got)
	}
}

// TestWorkspaceCurrentReportsEnvOverride confirms env-var override beats
// the catalog default, matching resolveWorkspaceRecord's precedence.
func TestWorkspaceCurrentReportsEnvOverride(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if _, err := upsertWorkspace("alpha"); err != nil {
		t.Fatalf("upsertWorkspace alpha failed: %v", err)
	}
	if _, err := setDefaultWorkspace("alpha"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}
	t.Setenv("RELAYFILE_WORKSPACE", "from_env")

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "current", "--verbose"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace current failed: %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(got, "from_env") {
		t.Fatalf("expected 'from_env' to win, got %q", got)
	}
	if !strings.Contains(got, "RELAYFILE_WORKSPACE") {
		t.Fatalf("expected source to mention RELAYFILE_WORKSPACE, got %q", got)
	}
}

// TestWorkspaceCurrentErrorsWhenNoneActive covers the error path: with
// nothing in env/token/catalog, runWorkspaceCurrent returns an error
// rather than printing an empty line.
func TestWorkspaceCurrentErrorsWhenNoneActive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var stdout bytes.Buffer
	err := run([]string{"workspace", "current"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error when no active workspace, got output %q", stdout.String())
	}
	if !strings.Contains(err.Error(), "no active workspace") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestNormalizeProviderIDCanonicalizesSlackAlias asserts that both the
// canonical provider id ("slack") and the demoted alias ("slack-sage")
// normalize to the canonical id. Before this fix the function returned
// "slack-sage" for both inputs, poisoning every connect-session request
// with the demoted alias even after the cloud registry migrated to
// "slack" with "slack-sage" retained only as a backwards-compat alias.
func TestNormalizeProviderIDCanonicalizesSlackAlias(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"slack", "slack"},
		{"slack-sage", "slack"},
		{"Slack", "slack"},
		{"  slack-sage  ", "slack"},
		{"github", "github"},
		{"linear", "linear"},
		{"notion", "notion"},
		{"slack-my-senior-dev", "slack-my-senior-dev"},
	}
	for _, tc := range cases {
		if got := normalizeProviderID(tc.input); got != tc.want {
			t.Fatalf("normalizeProviderID(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestFallbackIntegrationCatalogUsesCanonicalSlackId guards the offline
// fallback catalog so it advertises "slack" rather than "slack-sage"
// when the cloud catalog endpoint is unreachable.
func TestFallbackIntegrationCatalogUsesCanonicalSlackId(t *testing.T) {
	entries := fallbackIntegrationCatalog()
	var slack *integrationCatalogEntry
	for i := range entries {
		if entries[i].ID == "slack-sage" {
			t.Fatalf("fallback catalog still advertises legacy id slack-sage: %#v", entries[i])
		}
		if entries[i].ID == "slack" {
			slack = &entries[i]
		}
	}
	if slack == nil {
		t.Fatalf("fallback catalog missing canonical slack entry: %#v", entries)
	}
	if slack.VFSRoot != "/slack" {
		t.Fatalf("expected slack VFSRoot /slack, got %q", slack.VFSRoot)
	}
}

// TestWorkspaceListNamesOnlyRestoresBareOutput verifies the --names-only
// escape hatch for scripts that parsed the legacy unmarked output.
func TestWorkspaceListNamesOnlyRestoresBareOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_WORKSPACE", "ws_env")
	if _, err := upsertWorkspace("alpha"); err != nil {
		t.Fatalf("upsertWorkspace alpha failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()
	if err := saveCredentials(credentials{Server: server.URL, Token: "token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "list", "--names-only"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace list --names-only failed: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "ws_env\nalpha" {
		t.Fatalf("unexpected --names-only output: %q", got)
	}
}

// adoptTestServer wires the cloud endpoint the adopt verb exercises:
// POST .../adopt.
// Each test injects its own adopt handler so it can simulate the four
// distinct outcomes — success (fresh insert), success (replacement),
// 409 refusal, 404 missing-connection — without rebuilding the rest of
// the bootstrap chain.
func newAdoptTestServer(t *testing.T, adoptHandler func(http.ResponseWriter, *http.Request)) (*httptest.Server, map[string]int) {
	t.Helper()
	seen := map[string]int{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/github/adopt" && r.Method == http.MethodPost:
			seen["adopt"]++
			adoptHandler(w, r)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	return server, seen
}

func setupAdoptWorkspace(t *testing.T) (workspaceRecord, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	localDir := t.TempDir()
	record, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_123",
		LocalDir:   localDir,
		AgentName:  "relayfile-cli",
		Scopes:     append([]string(nil), defaultJoinScopes...),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	return record, localDir
}

func pointAdoptCloudCredentials(t *testing.T, apiURL string) {
	t.Helper()
	installFakeAgentRelaySession(t, apiURL, "cld_new", "demo", "ws_123", "ws_123")
}

func TestIntegrationAdoptForwardsConnectionIdAndPersistsLocalState(t *testing.T) {
	// Happy path: operator runs `relayfile integration adopt github
	// --connection-id conn_adopt` against a workspace whose row Cloud has
	// not yet seen. Cloud returns ok with no replacedConnectionId; the CLI
	// must (1) POST the request body in the expected shape, (2) persist
	// the new local connection state so subsequent mount / status probes
	// pick it up, and (3) print a single-line confirmation that does NOT
	// mention a replaced connection.
	record, localDir := setupAdoptWorkspace(t)
	server, seen := newAdoptTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode adopt body failed: %v", err)
		}
		if body["connectionId"] != "conn_adopt_new" {
			t.Fatalf("unexpected connectionId: %q", body["connectionId"])
		}
		if body["providerConfigKey"] != "github-relay" {
			t.Fatalf("unexpected providerConfigKey: %q", body["providerConfigKey"])
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
			t.Fatalf("unexpected Authorization: %q", got)
		}
		_, _ = w.Write([]byte(`{"ok":true,"connectionId":"conn_adopt_new"}`))
	})
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	err := run([]string{
		"integration", "adopt", "github",
		"--connection-id", "conn_adopt_new",
		"--provider-config-key", "github-relay",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("run integration adopt failed: %v\noutput:\n%s", err, stdout.String())
	}
	if seen["adopt"] != 1 {
		t.Fatalf("expected 1 adopt call, got %d", seen["adopt"])
	}
	got := stdout.String()
	if !strings.Contains(got, "github adopted into workspace demo") {
		t.Fatalf("unexpected output (missing confirmation): %q", got)
	}
	if strings.Contains(got, "replaced previous connection") {
		t.Fatalf("did not expect replacement note on fresh insert: %q", got)
	}
	state := loadSavedConnection(localDir, "github")
	if state.ConnectionID != "conn_adopt_new" || state.Backend != "nango" {
		t.Fatalf("unexpected saved integration connection: %#v", state)
	}
	_ = record
}

func TestIntegrationAdoptReportsReplacedConnection(t *testing.T) {
	// Stale-row migration path: Cloud detected the existing row pointed at
	// a connection Nango reports as gone, swapped it atomically, and
	// returned replacedConnectionId. The CLI must surface that id to the
	// operator so they know an old binding was overwritten — silent
	// replacement here would hide a meaningful state change.
	_, localDir := setupAdoptWorkspace(t)
	server, _ := newAdoptTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"connectionId":"conn_new","replacedConnectionId":"conn_old_dead"}`))
	})
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "adopt", "github",
		"--connection-id", "conn_new",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run integration adopt failed: %v\noutput:\n%s", err, stdout.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "replaced previous connection conn_old_dead") {
		t.Fatalf("expected replacement note in output, got: %q", got)
	}
	state := loadSavedConnection(localDir, "github")
	if state.ConnectionID != "conn_new" {
		t.Fatalf("expected local state to bind conn_new, got %#v", state)
	}
}

func TestIntegrationAdoptReturnsErrorOnConflictAndPreservesLocalState(t *testing.T) {
	// Refusal path: Cloud reports a 409 (e.g. another connection is still
	// live for this workspace + provider). The CLI must return a non-nil
	// error so the shell exit code is non-zero, AND must NOT overwrite the
	// local saved connection — the operator should see no local drift from
	// a failed adopt.
	_, localDir := setupAdoptWorkspace(t)
	// Prime existing local state so we can prove it survives.
	if err := saveIntegrationConnection(localDir, integrationConnectionState{
		Provider:     "github",
		ConnectionID: "conn_existing_local",
		Backend:      "nango",
	}); err != nil {
		t.Fatalf("seed saveIntegrationConnection failed: %v", err)
	}
	server, _ := newAdoptTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"ok":false,"code":"existing_connection_live_or_unknown","error":"Workspace ws_123 already has a live github connection (conn_live). Disconnect it first."}`))
	})
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	err := run([]string{
		"integration", "adopt", "github",
		"--connection-id", "conn_intruder",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error on 409, got nil; stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "Disconnect it first") {
		t.Fatalf("expected error to surface Cloud message, got: %v", err)
	}
	var httpErr *apiError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		t.Fatalf("expected apiError with 409 status, got %#v", err)
	}
	state := loadSavedConnection(localDir, "github")
	if state.ConnectionID != "conn_existing_local" {
		t.Fatalf("expected local state to be preserved, got %#v", state)
	}
}

func TestIntegrationAdoptRequiresConnectionID(t *testing.T) {
	// Flag validation: omitting --connection-id must fail before the CLI
	// even hits the network. Otherwise the operator could silently POST
	// an empty body and get a confusing 400 from Cloud.
	_, _ = setupAdoptWorkspace(t)
	var stdout bytes.Buffer
	err := run([]string{
		"integration", "adopt", "github",
		"--workspace", "demo",
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error when --connection-id is missing; stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "connection-id") {
		t.Fatalf("expected error to mention --connection-id, got: %v", err)
	}
}

func TestIntegrationAdoptRejectsUnsafeProvider(t *testing.T) {
	var stdout bytes.Buffer
	err := run([]string{
		"integration", "adopt", "../github",
		"--connection-id", "conn_adopt",
		"--workspace", "demo",
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error for unsafe provider; stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "invalid provider") {
		t.Fatalf("expected invalid provider error, got: %v", err)
	}
}

func TestIntegrationAdoptClearsDisconnectMarker(t *testing.T) {
	// After a `disconnect` the CLI writes a marker file under
	// .relay/disconnected/{provider}.json. Adopting the provider again
	// must clear that marker so the status probe stops reporting the
	// workspace as disconnected — otherwise the operator gets a stale
	// "disconnected" reading for a workspace they just adopted into.
	_, localDir := setupAdoptWorkspace(t)
	if err := markProviderDisconnected(localDir, "github"); err != nil {
		t.Fatalf("markProviderDisconnected failed: %v", err)
	}
	markerPath := filepath.Join(localDir, ".relay", "disconnected", "github.json")
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("expected disconnect marker to exist, got: %v", err)
	}
	server, _ := newAdoptTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"connectionId":"conn_adopt"}`))
	})
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "adopt", "github",
		"--connection-id", "conn_adopt",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run integration adopt failed: %v", err)
	}
	if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("expected disconnect marker to be removed, got: %v", err)
	}
}

// ----------------------------------------------------------------------
// Integration set-metadata + post-OAuth Atlassian site picker
// ----------------------------------------------------------------------

// newSetMetadataTestServer mirrors newAdoptTestServer but stubs the
// metadata PUT endpoint so set-metadata happy-path tests can record
// the request body and return a canned response.
func newSetMetadataTestServer(t *testing.T, metadataHandler func(http.ResponseWriter, *http.Request)) (*httptest.Server, map[string]int) {
	t.Helper()
	seen := map[string]int{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/metadata") && r.Method == http.MethodPut:
			seen["metadata"]++
			metadataHandler(w, r)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	return server, seen
}

func TestIntegrationSetMetadataHappyPath(t *testing.T) {
	// Operator runs `relayfile integration set-metadata jira cloudId=... baseUrl=...`.
	// The CLI must parse KEY=VALUE args, PUT them under
	// /api/v1/workspaces/.../metadata wrapped in { metadata: { ... } },
	// and exit 0 on cloud's 200.
	_, _ = setupAdoptWorkspace(t)
	server, seen := newSetMetadataTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode metadata body failed: %v", err)
		}
		md := body["metadata"]
		if md["cloudId"] != "cloud-1" || md["baseUrl"] != "https://foo.atlassian.net" {
			t.Fatalf("unexpected metadata body: %#v", md)
		}
		if r.URL.Path != "/api/v1/workspaces/ws_123/integrations/jira/metadata" {
			t.Fatalf("unexpected metadata path: %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
			t.Fatalf("unexpected metadata Authorization: %q", got)
		}
		_, _ = w.Write([]byte(`{"ok":true,"metadata":{"cloudId":"cloud-1","baseUrl":"https://foo.atlassian.net"}}`))
	})
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "set-metadata", "jira",
		"cloudId=cloud-1",
		"baseUrl=https://foo.atlassian.net",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("set-metadata failed: %v\noutput:\n%s", err, stdout.String())
	}
	if seen["metadata"] != 1 {
		t.Fatalf("expected 1 metadata PUT, got %d", seen["metadata"])
	}
	if !strings.Contains(stdout.String(), "jira metadata updated") {
		t.Fatalf("expected confirmation in output: %q", stdout.String())
	}
}

func TestIntegrationSetMetadataRejectsMissingArgs(t *testing.T) {
	// Missing KEY=VALUE args must fail before we hit the network so the
	// operator sees usage text instead of a confusing cloud 400.
	_, _ = setupAdoptWorkspace(t)
	var stdout bytes.Buffer
	err := run([]string{
		"integration", "set-metadata", "jira",
		"--workspace", "demo",
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error when no KEY=VALUE pairs provided")
	}
	if !strings.Contains(err.Error(), "KEY=VALUE") {
		t.Fatalf("expected usage hint in error, got: %v", err)
	}
}

func TestIntegrationSetMetadataRejectsMalformedPair(t *testing.T) {
	_, _ = setupAdoptWorkspace(t)
	var stdout bytes.Buffer
	err := run([]string{
		"integration", "set-metadata", "jira",
		"this-is-not-a-pair",
		"--workspace", "demo",
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error for malformed metadata arg")
	}
	if !strings.Contains(err.Error(), "KEY=VALUE form") {
		t.Fatalf("expected KEY=VALUE form error, got: %v", err)
	}
}

func TestIntegrationSetMetadataRejectsNestedKey(t *testing.T) {
	_, _ = setupAdoptWorkspace(t)
	var stdout bytes.Buffer
	err := run([]string{
		"integration", "set-metadata", "jira",
		"site.cloudId=cloud-1",
		"--workspace", "demo",
		"--yes",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error for nested-looking metadata key")
	}
	if !strings.Contains(err.Error(), "must be flat") {
		t.Fatalf("expected flat key error, got: %v", err)
	}
}

// newConnectJiraTestServer stubs the cloud routes the
// `integration connect jira` path touches: delegated-token bootstrap, the
// connect-session POST, the status GET (which the wait loop polls), the
// accessible-resources GET (post-OAuth picker), and the metadata PUT.
// `sites` controls how many sites accessible-resources returns; `chosen`
// receives the metadata PUT body so the test can assert on what was set.
func newConnectJiraTestServer(t *testing.T, sites []accessibleResourceEntry, chosen *map[string]any) (*httptest.Server, map[string]int) {
	t.Helper()
	seen := map[string]int{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			seen["delegated"]++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_123", "rf_join", "refresh_join")
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/connect-session" && r.Method == http.MethodPost:
			seen["connect-session"]++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected connect-session Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"connectionId":"conn_jira","connectLink":"","backend":"nango"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/status":
			seen["status"]++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"jira","backend":"nango"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/accessible-resources":
			seen["accessible-resources"]++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected accessible-resources Authorization: %q", got)
			}
			payload := struct {
				OK        bool                      `json:"ok"`
				Resources []accessibleResourceEntry `json:"resources"`
			}{OK: true, Resources: sites}
			_ = json.NewEncoder(w).Encode(payload)
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/metadata" && r.Method == http.MethodPut:
			seen["metadata"]++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected metadata Authorization: %q", got)
			}
			var body map[string]map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode metadata body failed: %v", err)
			}
			if chosen != nil {
				*chosen = body["metadata"]
			}
			_, _ = w.Write([]byte(`{"ok":true,"metadata":` + jsonMarshalOrPanic(body["metadata"]) + `}`))
		case r.URL.Path == "/v1/workspaces/ws_123/sync/status":
			// waitForInitialSync calls the relayfile data plane; stub it
			// out with a "complete" reading so the test doesn't hang.
			seen["sync-status"]++
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
				t.Fatalf("unexpected sync Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"jira","status":"ready","lagSeconds":0}]}`))
		case strings.HasPrefix(r.URL.Path, "/v1/workspaces/ws_123/fs/tree"):
			_, _ = w.Write([]byte(`{"entries":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unexpected request"}`))
		}
	}))
	return server, seen
}

func jsonMarshalOrPanic(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestIntegrationConnectJiraAutoSelectsSingleSite(t *testing.T) {
	// One Atlassian site: the picker should auto-bind it without prompting.
	// Importantly this also proves the picker fires at all on jira/confluence
	// (i.e. isAtlassianProvider returns true and runAtlassianSitePicker is
	// dispatched).
	_, _ = setupAdoptWorkspace(t)
	sites := []accessibleResourceEntry{
		{ID: "cloud-only", URL: "https://only.atlassian.net", Name: "Only Site"},
	}
	var picked map[string]any
	server, seen := newConnectJiraTestServer(t, sites, &picked)
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	err := run([]string{
		"integration", "connect", "jira",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
	}, strings.NewReader(""), &stdout, &stdout)
	if err != nil {
		t.Fatalf("integration connect jira failed: %v\noutput:\n%s", err, stdout.String())
	}
	if seen["accessible-resources"] == 0 {
		t.Fatalf("expected accessible-resources to be called for jira")
	}
	if seen["metadata"] == 0 {
		t.Fatalf("expected metadata PUT to fire for the only site")
	}
	if picked["cloudId"] != "cloud-only" || picked["baseUrl"] != "https://only.atlassian.net" {
		t.Fatalf("unexpected auto-selected metadata: %#v", picked)
	}
	if !strings.Contains(stdout.String(), "Auto-selected site") {
		t.Fatalf("expected auto-select log line, got: %q", stdout.String())
	}
}

func TestIntegrationConnectJiraPromptsAndSetsSelectedSite(t *testing.T) {
	// Multi-site path: operator picks site 2 via stdin. We assert (a) the
	// numbered picker rendered, (b) the chosen site's cloudId+baseUrl were
	// PUT to /metadata, and (c) the connect flow completed without hanging.
	_, _ = setupAdoptWorkspace(t)
	sites := []accessibleResourceEntry{
		{ID: "cloud-1", URL: "https://foo.atlassian.net"},
		{ID: "cloud-2", URL: "https://bar.atlassian.net"},
		{ID: "cloud-3", URL: "https://baz.atlassian.net"},
	}
	var picked map[string]any
	server, _ := newConnectJiraTestServer(t, sites, &picked)
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "connect", "jira",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
	}, strings.NewReader("2\n"), &stdout, &stdout); err != nil {
		t.Fatalf("integration connect jira failed: %v\noutput:\n%s", err, stdout.String())
	}
	if picked["cloudId"] != "cloud-2" || picked["baseUrl"] != "https://bar.atlassian.net" {
		t.Fatalf("expected site 2 to be picked, got %#v", picked)
	}
	got := stdout.String()
	if !strings.Contains(got, "Multiple Atlassian sites available") {
		t.Fatalf("expected multi-site prompt header in output: %q", got)
	}
	if !strings.Contains(got, "cloudId=cloud-1") || !strings.Contains(got, "cloudId=cloud-3") {
		t.Fatalf("expected picker to list all sites, got: %q", got)
	}
}

func TestIntegrationConnectJiraDefaultsToFirstSiteOnBlankInput(t *testing.T) {
	// Operator just hits Enter at the picker. The CLI must default to the
	// first site rather than re-prompting or erroring out.
	_, _ = setupAdoptWorkspace(t)
	sites := []accessibleResourceEntry{
		{ID: "cloud-a", URL: "https://a.atlassian.net"},
		{ID: "cloud-b", URL: "https://b.atlassian.net"},
	}
	var picked map[string]any
	server, _ := newConnectJiraTestServer(t, sites, &picked)
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "connect", "jira",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
	}, strings.NewReader("\n"), &stdout, &stdout); err != nil {
		t.Fatalf("integration connect jira failed: %v\noutput:\n%s", err, stdout.String())
	}
	if picked["cloudId"] != "cloud-a" {
		t.Fatalf("expected default site to be picked, got %#v", picked)
	}
}

func TestIntegrationConnectJiraRetriesOnInvalidInputThenErrors(t *testing.T) {
	// Three invalid attempts in a row must abort with a clear error so a
	// dumb-pipe stdin doesn't spin forever. We don't care which exact
	// invalid value we send, only that the CLI gives up after the cap.
	_, _ = setupAdoptWorkspace(t)
	sites := []accessibleResourceEntry{
		{ID: "cloud-1", URL: "https://foo.atlassian.net"},
		{ID: "cloud-2", URL: "https://bar.atlassian.net"},
	}
	server, _ := newConnectJiraTestServer(t, sites, nil)
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	err := run([]string{
		"integration", "connect", "jira",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
	}, strings.NewReader("nope\n9999\nstill-bad\n"), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error after exhausted prompt attempts; stdout=%q", stdout.String())
	}
	if !strings.Contains(err.Error(), "valid site selection") {
		t.Fatalf("expected error to mention site selection, got: %v", err)
	}
}

func TestIntegrationConnectJiraAlreadyConnectedSkipsPicker(t *testing.T) {
	_, localDir := setupAdoptWorkspace(t)
	if err := saveIntegrationConnection(localDir, integrationConnectionState{
		Provider:     "jira",
		ConnectionID: "conn_jira",
		Backend:      "nango",
		ConnectedAt:  time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("save integration connection failed: %v", err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_123", "rf_join", "refresh_join")
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/status":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			if got := r.URL.Query().Get("connectionId"); got != "conn_jira" {
				t.Fatalf("unexpected connectionId: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"jira","backend":"nango"}`))
		case r.URL.Path == "/v1/workspaces/ws_123/sync/status":
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
				t.Fatalf("unexpected sync Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"jira","status":"ready","lagSeconds":0}]}`))
		case strings.Contains(r.URL.Path, "/accessible-resources"):
			t.Fatalf("accessible-resources must not be called for already-connected jira, hit: %s", r.URL.Path)
		case strings.HasSuffix(r.URL.Path, "/metadata"):
			t.Fatalf("metadata PUT must not fire for already-connected jira, hit: %s", r.URL.Path)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "connect", "jira",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("integration connect jira failed: %v\noutput:\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "jira already connected") {
		t.Fatalf("expected already-connected message, got: %q", stdout.String())
	}
}

func TestIntegrationConnectNonAtlassianSkipsPicker(t *testing.T) {
	// Non-Atlassian providers must NOT invoke the picker — the existing
	// connect flow has to keep working unchanged. We verify by stubbing a
	// cloud that fails the test if accessible-resources is ever called.
	_, _ = setupAdoptWorkspace(t)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_123", "rf_join", "refresh_join")
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/connect-session":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected connect-session Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"connectionId":"conn_github","connectLink":"","backend":"nango"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/github/status":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"github","backend":"nango"}`))
		case r.URL.Path == "/v1/workspaces/ws_123/sync/status":
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
				t.Fatalf("unexpected sync Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"github","status":"ready","lagSeconds":0}]}`))
		case strings.Contains(r.URL.Path, "/accessible-resources"):
			t.Fatalf("accessible-resources must not be called for non-Atlassian providers, hit: %s", r.URL.Path)
		case strings.HasSuffix(r.URL.Path, "/metadata"):
			t.Fatalf("metadata PUT must not fire for non-Atlassian providers, hit: %s", r.URL.Path)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "connect", "github",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("integration connect github failed: %v", err)
	}
}

func TestIntegrationConnectKeepsSelectedWorkspaceAndUsesJoinedRelayWorkspaceForSync(t *testing.T) {
	record, _ := setupAdoptWorkspace(t)
	record.ID = "50587328-441d-4acb-b8f3-dbe1b3c5de99"
	if _, err := upsertWorkspaceDetails(record); err != nil {
		t.Fatalf("upsert app workspace record failed: %v", err)
	}

	var server *httptest.Server
	seen := map[string]int{}
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		seen[r.URL.Path]++
		switch r.URL.Path {
		case "/api/v1/workspaces/50587328-441d-4acb-b8f3-dbe1b3c5de99/relayfile/delegated-token":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected delegated-token Authorization: %q", got)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "rw_7ccfea89", "rf_join", "refresh_join")
		case "/api/v1/workspaces/50587328-441d-4acb-b8f3-dbe1b3c5de99/integrations/connect-session":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected connect-session Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"connectionId":"conn_slack","connectLink":"","backend":"nango"}`))
		case "/api/v1/workspaces/50587328-441d-4acb-b8f3-dbe1b3c5de99/integrations/slack/status":
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected status Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"slack","backend":"nango"}`))
		case "/v1/workspaces/rw_7ccfea89/sync/status":
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
				t.Fatalf("unexpected sync Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"rw_7ccfea89","providers":[{"provider":"slack","status":"ready","lagSeconds":0}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)

	var stdout bytes.Buffer
	if err := run([]string{
		"integration", "connect", "slack",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
		"--no-open",
		"--timeout", "5s",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("integration connect slack failed: %v\noutput:\n%s", err, stdout.String())
	}
	if seen["/api/v1/workspaces/50587328-441d-4acb-b8f3-dbe1b3c5de99/integrations/connect-session"] != 1 {
		t.Fatalf("connect-session did not use selected app workspace; seen=%v", seen)
	}
	if seen["/v1/workspaces/rw_7ccfea89/sync/status"] == 0 {
		t.Fatalf("sync wait did not use joined relay workspace; seen=%v", seen)
	}
	persisted, ok := workspaceRecordByName("demo")
	if !ok {
		t.Fatal("expected persisted workspace record")
	}
	if persisted.ID != "50587328-441d-4acb-b8f3-dbe1b3c5de99" {
		t.Fatalf("workspace ID = %q, want selected app workspace", persisted.ID)
	}
	if persisted.RelayWorkspaceID != "rw_7ccfea89" {
		t.Fatalf("relay workspace ID = %q, want rw_7ccfea89", persisted.RelayWorkspaceID)
	}
	if !strings.Contains(stdout.String(), "Relayfile runtime: rw_7ccfea89") {
		t.Fatalf("expected explicit runtime workspace output, got %q", stdout.String())
	}
}

func TestIsWritebackDraftPathSlashSeparated(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/linear/issues/factory-create-abc.json", true},
		{"linear/issues/factory-create-abc.json", true},
		{"/linear/issues/by-state/ready-for-agent/factory-create-9f.json", true},
		{"/linear/issues/AR-1.json", false},
		{"/linear/issues/factory-create-abc.txt", false},
		{"/notion/Docs/notes.md", false},
	}
	for _, tc := range cases {
		if got := isWritebackDraftPath(tc.path); got != tc.want {
			t.Errorf("isWritebackDraftPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestWriteWritebackPushReceiptRedactsTerminalReceipts(t *testing.T) {
	mountRoot := t.TempDir()
	base := writebackPushReceipt{
		CommandID:   "mountcmd_test",
		WorkspaceID: "ws_demo",
		RemotePath:  "/linear/issues/factory-create-x.json",
		ContentType: "application/json",
		Content:     "secret-body",
		Encoding:    "",
		Hash:        "deadbeef",
	}

	// Pending receipt: retains the body (for retry) and is written 0600.
	pending := base
	pending.Status = "pending"
	if err := writeWritebackPushReceipt(mountRoot, pending); err != nil {
		t.Fatalf("write pending receipt: %v", err)
	}
	pendingPath := filepath.Join(mountRoot, ".relay", "outbox", "pending", "mountcmd_test.json")
	info, err := os.Stat(pendingPath)
	if err != nil {
		t.Fatalf("stat pending receipt: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("pending receipt mode = %o, want 600", perm)
	}
	var pendingReceipt writebackPushReceipt
	readReceiptJSON(t, pendingPath, &pendingReceipt)
	if pendingReceipt.Content != "secret-body" {
		t.Fatalf("pending receipt must retain content, got %q", pendingReceipt.Content)
	}

	// Terminal receipts (acked/failed): body stripped, pending copy removed.
	for _, status := range []string{"acked", "failed"} {
		r := base
		r.Status = status
		if err := writeWritebackPushReceipt(mountRoot, r); err != nil {
			t.Fatalf("write %s receipt: %v", status, err)
		}
		p := filepath.Join(mountRoot, ".relay", "outbox", status, "mountcmd_test.json")
		var got writebackPushReceipt
		readReceiptJSON(t, p, &got)
		if got.Content != "" || got.Encoding != "" {
			t.Fatalf("%s receipt must redact content/encoding, got content=%q", status, got.Content)
		}
		if _, err := os.Stat(pendingPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("writing %s receipt should remove the pending copy; stat err=%v", status, err)
		}
		// Re-create the pending copy for the next iteration's removal check.
		if err := writeWritebackPushReceipt(mountRoot, pending); err != nil {
			t.Fatalf("recreate pending receipt: %v", err)
		}
	}
}

func TestIsTerminalWritebackOpStatus(t *testing.T) {
	for _, s := range []string{"failed", "dead_lettered", "canceled", " failed "} {
		if !isTerminalWritebackOpStatus(s) {
			t.Errorf("isTerminalWritebackOpStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "succeeded", "running", "pending", "dispatched"} {
		if isTerminalWritebackOpStatus(s) {
			t.Errorf("isTerminalWritebackOpStatus(%q) = true, want false", s)
		}
	}
}

func readReceiptJSON(t *testing.T, path string, out *writebackPushReceipt) {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read receipt %s: %v", path, err)
	}
	if err := json.Unmarshal(payload, out); err != nil {
		t.Fatalf("decode receipt %s: %v", path, err)
	}
}
