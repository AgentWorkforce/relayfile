package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	if got := err.Error(); !strings.Contains(got, "create, join, use, list, current, or delete") {
		t.Fatalf("workspace subcommand error = %q", got)
	}
}

func TestWorkspaceUseSetsDefaultWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "use", "ws_cloud"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace use failed: %v", err)
	}

	catalog, err := loadWorkspaceCatalog()
	if err != nil {
		t.Fatalf("loadWorkspaceCatalog failed: %v", err)
	}
	if catalog.Default != "ws_cloud" {
		t.Fatalf("expected default workspace ws_cloud, got %q", catalog.Default)
	}
	if got := stdout.String(); !strings.Contains(got, "Default workspace set to ws_cloud") {
		t.Fatalf("unexpected workspace use output: %q", got)
	}
}

func TestWorkspaceJoinMintsReadOnlyCloudToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if err := saveCloudCredentials(cloudCredentials{
		APIURL:      "http://placeholder.test",
		AccessToken: "cld_access",
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}

	var seenJoin bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/ws_cloud/join" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		seenJoin = true
		if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
			t.Fatalf("unexpected join Authorization: %q", got)
		}
		var body cloudWorkspaceJoinRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode join body: %v", err)
		}
		if got, want := strings.Join(body.Scopes, ","), "relayfile:fs:read:*"; got != want {
			t.Fatalf("join scopes = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspaceId":"ws_cloud","token":"rf_read","relayfileUrl":"https://relayfile.test"}`))
	}))
	defer server.Close()
	cloudCreds, err := loadCloudCredentials()
	if err != nil {
		t.Fatalf("loadCloudCredentials failed: %v", err)
	}
	cloudCreds.APIURL = server.URL
	if err := saveCloudCredentials(cloudCreds); err != nil {
		t.Fatalf("saveCloudCredentials(server URL) failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"workspace", "join", "ws_cloud", "--name", "cloud-prod", "--cloud-api-url", server.URL}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run workspace join failed: %v", err)
	}
	if !seenJoin {
		t.Fatalf("expected cloud join call")
	}
	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials failed: %v", err)
	}
	if creds.Token != "rf_read" || creds.Server != "https://relayfile.test" {
		t.Fatalf("unexpected persisted credentials: %#v", creds)
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

	if err := saveCloudCredentials(cloudCredentials{
		APIURL:      "http://placeholder.test",
		AccessToken: "cld_access",
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:      "cloud-prod",
		ID:        "ws_cloud",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		AgentName: "relayfile-cli",
		Scopes:    append([]string(nil), defaultJoinScopes...),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	var joinCount int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_cloud/join":
			joinCount++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
				t.Fatalf("unexpected join Authorization: %q", got)
			}
			var body cloudWorkspaceJoinRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode join body: %v", err)
			}
			if got, want := strings.Join(body.Scopes, ","), "relayfile:fs:read:*"; got != want {
				t.Fatalf("join scopes = %q, want %q", got, want)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"workspaceId":"rw_cloud","token":"rf_join","relayfileUrl":"` + server.URL + `"}`))
		case "/v1/workspaces/rw_cloud/fs/tree":
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
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
	cloudCreds, err := loadCloudCredentials()
	if err != nil {
		t.Fatalf("loadCloudCredentials failed: %v", err)
	}
	cloudCreds.APIURL = server.URL
	if err := saveCloudCredentials(cloudCreds); err != nil {
		t.Fatalf("saveCloudCredentials(server URL) failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"tree", "ws_cloud", "/google-mail"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}
	if joinCount != 1 {
		t.Fatalf("expected 1 join, got %d", joinCount)
	}
	if got := stdout.String(); !strings.Contains(got, "Tree /google-mail") {
		t.Fatalf("unexpected stdout: %q", got)
	}
}

func TestTreeRefreshesExpiredCloudWorkspaceTokenAndRetriesCanonicalWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if err := saveCloudCredentials(cloudCredentials{
		APIURL:      "http://placeholder.test",
		AccessToken: "cld_access",
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}
	oldToken := testJWTWithWorkspaceAndAgent("ws_cloud", "old")
	if err := saveCredentials(credentials{
		Server: "http://placeholder.test",
		Token:  oldToken,
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:      "cloud-prod",
		ID:        "ws_cloud",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		AgentName: "relayfile-cli",
		Scopes:    append([]string(nil), defaultInspectScopes...),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if _, err := setDefaultWorkspace("cloud-prod"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}

	var joinCount int
	var treeCount int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_cloud/join":
			joinCount++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"workspaceId":"rw_cloud","token":"rf_new","relayfileUrl":"` + server.URL + `"}`))
		case "/v1/workspaces/ws_cloud/fs/tree":
			treeCount++
			if got := r.Header.Get("Authorization"); got != "Bearer "+oldToken {
				t.Fatalf("expected first tree to use old token, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Token has expired"}`))
		case "/v1/workspaces/rw_cloud/fs/tree":
			treeCount++
			if got := r.Header.Get("Authorization"); got != "Bearer rf_new" {
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

	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials failed: %v", err)
	}
	creds.Server = server.URL
	if err := saveCredentials(creds); err != nil {
		t.Fatalf("saveCredentials(server URL) failed: %v", err)
	}
	cloudCreds, err := loadCloudCredentials()
	if err != nil {
		t.Fatalf("loadCloudCredentials failed: %v", err)
	}
	cloudCreds.APIURL = server.URL
	if err := saveCloudCredentials(cloudCreds); err != nil {
		t.Fatalf("saveCloudCredentials(server URL) failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"tree", "cloud-prod", "/google-mail"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run tree failed: %v", err)
	}
	if joinCount != 1 || treeCount != 2 {
		t.Fatalf("joinCount/treeCount = %d/%d, want 1/2", joinCount, treeCount)
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
	if record.CloudAPIURL != server.URL {
		t.Fatalf("workspace record CloudAPIURL = %q, want %q", record.CloudAPIURL, server.URL)
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

func TestReadRefreshesCloudWorkspaceTokenAfterUnauthorized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if err := saveCloudCredentials(cloudCredentials{
		APIURL:      "http://placeholder.test",
		AccessToken: "cld_access",
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}
	oldToken := testJWTWithWorkspaceAndAgent("ws_cloud", "old")
	if err := saveCredentials(credentials{
		Server: "http://placeholder.test",
		Token:  oldToken,
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:      "cloud-prod",
		ID:        "ws_cloud",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		AgentName: "relayfile-cli",
		Scopes:    append([]string(nil), defaultInspectScopes...),
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
	var joinCount int
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_cloud/join":
			joinCount++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
				t.Fatalf("unexpected join Authorization: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"workspaceId":"rw_cloud","token":"rf_new","relayfileUrl":"` + server.URL + `"}`))
		case "/v1/workspaces/ws_cloud/fs/file":
			readCount++
			if got := r.Header.Get("Authorization"); got != "Bearer "+oldToken {
				t.Fatalf("expected first read to use old token, got %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Token has expired"}`))
			return
		case "/v1/workspaces/rw_cloud/fs/file":
			readCount++
			if got := r.Header.Get("Authorization"); got != "Bearer rf_new" {
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

	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials failed: %v", err)
	}
	creds.Server = server.URL
	if err := saveCredentials(creds); err != nil {
		t.Fatalf("saveCredentials(server URL) failed: %v", err)
	}
	cloudCreds, err := loadCloudCredentials()
	if err != nil {
		t.Fatalf("loadCloudCredentials failed: %v", err)
	}
	cloudCreds.APIURL = server.URL
	if err := saveCloudCredentials(cloudCreds); err != nil {
		t.Fatalf("saveCloudCredentials(server URL) failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"read", "cloud-prod", "/google-mail/msg.json"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run read failed: %v", err)
	}
	if joinCount != 1 || readCount != 2 {
		t.Fatalf("joinCount/readCount = %d/%d, want 1/2", joinCount, readCount)
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
	if record.CloudAPIURL != server.URL {
		t.Fatalf("workspace record CloudAPIURL = %q, want %q", record.CloudAPIURL, server.URL)
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

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  token,
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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
		case "/api/v1/workspaces/ws_123/join":
			seen["join"] = true
			if r.Method != http.MethodPost {
				t.Fatalf("expected join POST, got %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cld_test" {
				t.Fatalf("unexpected join Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","token":"rf_join","relayfileUrl":"https://relayfile.test","wsUrl":"wss://relayfile.test/ws","relaycastApiKey":"rc_test"}`))
		case "/api/v1/workspaces/ws_123/integrations/connect-session":
			seen["connect"] = true
			if r.Method != http.MethodPost {
				t.Fatalf("expected connect POST, got %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
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
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
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
	for _, key := range []string{"create", "join", "connect", "status"} {
		if !seen[key] {
			t.Fatalf("expected %s request", key)
		}
	}

	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials failed: %v", err)
	}
	if creds.Server != "https://relayfile.test" || creds.Token != "rf_join" {
		t.Fatalf("unexpected saved credentials: %#v", creds)
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
		case "/api/v1/workspaces/ws_123/join":
			seen["join"] = true
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","token":"rf_join","relayfileUrl":"https://relayfile.test","wsUrl":"wss://relayfile.test/ws","relaycastApiKey":"rc_test"}`))
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
	for _, key := range []string{"create", "join", "connect", "status", "accessible-resources", "metadata"} {
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
	if err := saveCloudCredentials(cloudCredentials{
		APIURL:               "https://stale.example.test",
		AccessToken:          "cld_old",
		RefreshToken:         "refresh_123",
		AccessTokenExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}

	seen := map[string]bool{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/token/refresh":
			seen["refresh"] = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_old" {
				t.Fatalf("unexpected refresh Authorization: %q", got)
			}
			var body cloudTokenRefreshRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode refresh body failed: %v", err)
			}
			if body.RefreshToken != "refresh_123" {
				t.Fatalf("unexpected refresh token: %q", body.RefreshToken)
			}
			_, _ = w.Write([]byte(`{"apiUrl":"` + server.URL + `","accessToken":"cld_new","refreshToken":"refresh_123","accessTokenExpiresAt":"2030-05-01T00:00:00Z"}`))
		case "/api/v1/workspaces/ws_123/join":
			seen["join"] = true
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Fatalf("unexpected join Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","token":"rf_join","relayfileUrl":"` + server.URL + `"}`))
		case "/api/v1/workspaces/ws_123/integrations/connect-session":
			seen["connect"] = true
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
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
			if got := r.URL.Query().Get("connectionId"); got != "conn_789" {
				t.Fatalf("unexpected connectionId: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true}`))
		case "/v1/workspaces/ws_123/sync/status":
			seen["sync"] = true
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"notion","status":"ready","lagSeconds":4,"watermarkTs":"2026-05-02T18:00:00Z"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	creds, err := loadCloudCredentials()
	if err != nil {
		t.Fatalf("loadCloudCredentials failed: %v", err)
	}
	creds.APIURL = server.URL
	if err := saveCloudCredentials(creds); err != nil {
		t.Fatalf("saveCloudCredentials(update) failed: %v", err)
	}

	var stdout bytes.Buffer
	err = run([]string{
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
	for _, key := range []string{"refresh", "join", "connect", "status", "sync"} {
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
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"ready","lagSeconds":4,"watermarkTs":"2026-05-02T18:00:00Z"}]}`))
	}))
	defer server.Close()

	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials(update) failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
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

func TestStatusRendersExpiredCloudSessionAuthLine(t *testing.T) {
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
	if err := saveCredentials(credentials{Server: server.URL, Token: "token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}
	if err := saveCloudCredentials(cloudCredentials{
		APIURL:                "https://cloud.relayfile.test",
		AccessToken:           "cloud_token",
		AccessTokenExpiresAt:  time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
		RefreshToken:          "refresh_token",
		RefreshTokenExpiresAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "auth: cloud session expired - run 'relayfile login'") {
		t.Fatalf("expected expired auth line, got %q", got)
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
	if err := saveCredentials(credentials{Server: server.URL, Token: "token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}
	loginTime := startedAt.Add(30 * time.Minute)
	if err := os.Chtimes(credentialsPath(), loginTime, loginTime); err != nil {
		t.Fatalf("chtimes credentials failed: %v", err)
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
	if err := saveCredentials(credentials{Server: server.URL, Token: "expired"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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
	if err := saveCredentials(credentials{
		Server: server.URL,
		Token:  "token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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

	err := run([]string{"mount", "demo", "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
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

	if err := run([]string{"mount", "demo", "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
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

	if err := run([]string{"mount", "demo", "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
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

	err := run([]string{"mount", "demo", otherDir, "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
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

	err := run([]string{"mount", "demo", otherDir, "--rehome", "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
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

	err := run([]string{"mount", "demo", otherDir, "--rehome", "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
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

	if err := run([]string{"mount", "demo", otherDir, "--rehome", "--once", "--websocket=false"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
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

func TestMountOnceRejoinsWorkspaceTokenAfterUnauthorized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	oldToken := testJWTWithWorkspaceAndAgent("ws_refresh", "old")
	newToken := testJWTWithWorkspaceAndAgent("ws_refresh", "new")
	if err := saveCloudCredentials(cloudCredentials{
		APIURL:      defaultCloudAPIURL,
		AccessToken: "cld_access",
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}
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
	joinCalls := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_refresh/join":
			joinCalls++
			if got := r.Header.Get("Authorization"); got != "Bearer cld_access" {
				t.Fatalf("unexpected join Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_refresh","token":"` + newToken + `","relayfileUrl":"` + server.URL + `"}`))
		case "/v1/workspaces/ws_refresh/fs/events":
			eventCalls++
			gotAuth := r.Header.Get("Authorization")
			if gotAuth != "Bearer "+oldToken && gotAuth != "Bearer "+newToken {
				t.Fatalf("unexpected events Authorization: %q", gotAuth)
			}
			_, _ = w.Write([]byte(`{"events":[]}`))
		case "/v1/workspaces/ws_refresh/fs/export":
			exportCalls++
			gotAuth := r.Header.Get("Authorization")
			if exportCalls == 1 {
				if gotAuth != "Bearer "+oldToken {
					t.Fatalf("unexpected initial export Authorization: %q", gotAuth)
				}
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":"unauthorized","message":"expired"}`))
				return
			}
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

	cloudCreds, err := loadCloudCredentials()
	if err != nil {
		t.Fatalf("loadCloudCredentials failed: %v", err)
	}
	cloudCreds.APIURL = server.URL
	if err := saveCloudCredentials(cloudCreds); err != nil {
		t.Fatalf("saveCloudCredentials(update) failed: %v", err)
	}

	if err := run([]string{
		"mount", "ws_refresh", localDir,
		"--server", server.URL,
		"--token", oldToken,
		"--once",
		"--websocket=false",
	}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run mount failed: %v", err)
	}
	if joinCalls != 1 || exportCalls != 2 {
		t.Fatalf("expected 1 join and 2 export calls, got join=%d events=%d export=%d", joinCalls, eventCalls, exportCalls)
	}
	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials failed: %v", err)
	}
	if creds.Token != newToken {
		t.Fatalf("expected refreshed token to be persisted, got %q", creds.Token)
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
}

func testJWTWithWorkspace(workspaceID string) string {
	return testJWTWithWorkspaceAndAgent(workspaceID, "test")
}

func testJWTWithWorkspaceAndAgent(workspaceID, agentName string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"workspace_id":"` + workspaceID + `","agent_name":"` + agentName + `","aud":"relayfile"}`))
	return header + "." + payload + ".sig"
}

func TestRefreshCloudCredentialsReturnsSentinelOnInvalidGrant(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/token/refresh" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"invalid_grant","message":"refresh token expired"}`))
	}))
	defer server.Close()

	_, err := refreshCloudCredentials(cloudCredentials{
		APIURL:       server.URL,
		AccessToken:  "cld_old",
		RefreshToken: "rt_expired",
	})
	if !errors.Is(err, ErrCloudRefreshExpired) {
		t.Fatalf("expected ErrCloudRefreshExpired, got: %v", err)
	}
}

func TestRefreshCloudCredentialsRetriesWithReloadedDiskTokenOnAuthRejection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var refreshTokens []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/token/refresh" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		var body cloudTokenRefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode refresh body failed: %v", err)
		}
		refreshTokens = append(refreshTokens, body.RefreshToken)
		switch body.RefreshToken {
		case "rt_stale":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"invalid_grant","message":"Invalid or expired refresh token"}`))
		case "rt_disk":
			_, _ = w.Write([]byte(`{"apiUrl":"` + server.URL + `","accessToken":"cld_new","refreshToken":"rt_new","accessTokenExpiresAt":"2030-05-01T00:00:00Z","refreshTokenExpiresAt":"2030-06-01T00:00:00Z"}`))
		default:
			t.Fatalf("unexpected refresh token: %q", body.RefreshToken)
		}
	}))
	defer server.Close()

	if err := saveCloudCredentials(cloudCredentials{
		APIURL:               server.URL,
		AccessToken:          "cld_disk_old",
		RefreshToken:         "rt_disk",
		AccessTokenExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}

	refreshed, err := refreshCloudCredentials(cloudCredentials{
		APIURL:               server.URL,
		AccessToken:          "cld_stale",
		RefreshToken:         "rt_stale",
		AccessTokenExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("refreshCloudCredentials failed: %v", err)
	}
	if refreshed.AccessToken != "cld_new" || refreshed.RefreshToken != "rt_new" {
		t.Fatalf("unexpected refreshed credentials: %#v", refreshed)
	}
	if got := strings.Join(refreshTokens, ","); got != "rt_stale,rt_disk" {
		t.Fatalf("unexpected refresh token attempts: %s", got)
	}

	diskCreds, err := loadCloudCredentials()
	if err != nil {
		t.Fatalf("loadCloudCredentials failed: %v", err)
	}
	if diskCreds.AccessToken != "cld_new" || diskCreds.RefreshToken != "rt_new" {
		t.Fatalf("expected refreshed credentials on disk, got: %#v", diskCreds)
	}
}

func TestRefreshCloudCredentialsReturnsSentinelOnUnauthorized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"unauthorized","message":"unauthenticated"}`))
	}))
	defer server.Close()

	_, err := refreshCloudCredentials(cloudCredentials{
		APIURL:       server.URL,
		AccessToken:  "cld_old",
		RefreshToken: "rt_expired",
	})
	if !errors.Is(err, ErrCloudRefreshExpired) {
		t.Fatalf("expected ErrCloudRefreshExpired, got: %v", err)
	}
}

func TestRefreshCloudCredentialsReturnsSentinelWithoutRefreshToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	_, err := refreshCloudCredentials(cloudCredentials{
		APIURL:      "https://cloud.example.test",
		AccessToken: "cld_old",
	})
	if !errors.Is(err, ErrCloudRefreshExpired) {
		t.Fatalf("expected ErrCloudRefreshExpired, got: %v", err)
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
		StallReason: "cloud session expired — run 'relayfile login' to refresh",
	}
	if err := writeMirrorStateFile(localDir, persisted); err != nil {
		t.Fatalf("writeMirrorStateFile failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","providers":[{"provider":"notion","status":"ready","lagSeconds":4,"watermarkTs":"2026-05-02T18:00:00Z"}]}`))
	}))
	defer server.Close()

	if err := saveCredentials(credentials{Server: server.URL, Token: "token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{"status", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run status failed: %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "stall: cloud session expired") {
		t.Fatalf("expected degraded stall reason in status output, got: %q", got)
	}
	if !strings.Contains(got, "relayfile login") {
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

	if err := saveCredentials(credentials{Server: server.URL, Token: "token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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

	if err := saveCredentials(credentials{Server: server.URL, Token: "token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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

	if err := saveCredentials(credentials{Server: server.URL, Token: "token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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

	if err := saveCredentials(credentials{Server: server.URL, Token: "token"}); err != nil {
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

	if err := saveCredentials(credentials{Server: server.URL, Token: "token"}); err != nil {
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

	if err := saveCredentials(credentials{Server: server.URL, Token: "token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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

	if err := saveCredentials(credentials{Server: server.URL, Token: "token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

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
		case "/api/v1/workspaces/ws_demo/join":
			seen["join"] = true
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","token":"rf_join","relayfileUrl":"https://relayfile.test"}`))
		case "/api/v1/workspaces/ws_demo/ops/op_abc/replay":
			seen["replay"] = true
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected replay method: %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
				t.Fatalf("unexpected replay Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"status":"queued","id":"op_abc"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	if err := saveCloudCredentials(cloudCredentials{
		APIURL:               server.URL,
		AccessToken:          "cld_token",
		AccessTokenExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{
		"ops", "replay", "op_abc",
		"--workspace", "demo",
		"--cloud-api-url", server.URL,
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run ops replay failed: %v\noutput:\n%s", err, stdout.String())
	}
	for _, key := range []string{"join", "replay"} {
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
	if err := saveCloudCredentials(cloudCredentials{
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

// TestLoginDefaultsToCloudBrowserFlow covers the new default behavior: when
// no --token is provided, runLogin runs the cloud browser flow. We use
// --cloud-token to skip the actual browser handshake — ensureCloudCredentials
// short-circuits to writing cloud credentials when an explicit token is set.
func TestLoginDefaultsToCloudBrowserFlow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveCredentials(credentials{
		Server: "https://relayfile-old.test",
		Token:  "stale_server_token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{
		"login",
		"--cloud-api-url", "https://cloud.relayfile.test",
		"--cloud-token", "cld_browser_token",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run login failed: %v\noutput:\n%s", err, stdout.String())
	}

	got := stdout.String()
	if !strings.Contains(got, "Signed in to Relayfile Cloud") {
		t.Fatalf("expected cloud sign-in confirmation, got %q", got)
	}
	creds, err := loadCloudCredentials()
	if err != nil {
		t.Fatalf("loadCloudCredentials failed: %v", err)
	}
	if creds.AccessToken != "cld_browser_token" {
		t.Fatalf("expected stored cloud access token, got %q", creds.AccessToken)
	}
	if creds.APIURL != "https://cloud.relayfile.test" {
		t.Fatalf("expected APIURL stored, got %q", creds.APIURL)
	}
	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected stale server credentials removed, got err=%v", err)
	}
}

// TestLoginRefreshesWorkspaceTokenForDefaultWorkspace covers the fix for
// the bug where `relayfile login` only refreshed cloud-credentials.json
// even when a workspace was already registered locally. After login, the
// data-plane JWT in credentials.json should also be refreshed via the
// cloud's workspace join endpoint.
func TestLoginRefreshesWorkspaceTokenForDefaultWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_demo/join":
			seen["join"] = true
			if r.Method != http.MethodPost {
				t.Fatalf("expected join POST, got %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cld_browser_token" {
				t.Fatalf("unexpected join Authorization: %q", got)
			}
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","token":"rf_refreshed","relayfileUrl":"https://relayfile.test","wsUrl":"wss://relayfile.test/ws","relaycastApiKey":"rc_test"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name: "demo",
		ID:   "ws_demo",
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if _, err := setDefaultWorkspace("demo"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{
		"login",
		"--cloud-api-url", server.URL,
		"--cloud-token", "cld_browser_token",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run login failed: %v\noutput:\n%s", err, stdout.String())
	}

	if !seen["join"] {
		t.Fatalf("expected /workspaces/ws_demo/join request, none seen. output:\n%s", stdout.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "Refreshed workspace token for demo") {
		t.Fatalf("expected refresh confirmation, got %q", got)
	}
	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials failed: %v", err)
	}
	if creds.Token != "rf_refreshed" {
		t.Fatalf("expected refreshed workspace token, got %q", creds.Token)
	}
	if creds.Server != "https://relayfile.test" {
		t.Fatalf("expected refreshed server URL, got %q", creds.Server)
	}
	if strings.TrimSpace(creds.UpdatedAt) == "" {
		t.Fatalf("expected credentials updatedAt to be set")
	}
	cloud, err := loadCloudCredentials()
	if err != nil {
		t.Fatalf("loadCloudCredentials failed: %v", err)
	}
	if cloud.AccessToken != "cld_browser_token" {
		t.Fatalf("expected refreshed cloud token to remain stored, got %q", cloud.AccessToken)
	}
	if strings.TrimSpace(cloud.UpdatedAt) == "" {
		t.Fatalf("expected cloud credentials updatedAt to be set")
	}
}

// TestLoginSkipsWorkspaceRefreshWhenFlagSet covers --skip-workspace-refresh:
// even with a workspace registered, no /join call should fire and
// existing credentials.json must be preserved.
func TestLoginSkipsWorkspaceRefreshWhenFlagSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveCredentials(credentials{
		Server: "https://relayfile-old.test",
		Token:  "stale_server_token",
	}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	if _, err := upsertWorkspaceDetails(workspaceRecord{Name: "demo", ID: "ws_demo"}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if _, err := setDefaultWorkspace("demo"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{
		"login",
		"--cloud-api-url", server.URL,
		"--cloud-token", "cld_browser_token",
		"--skip-workspace-refresh",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run login failed: %v\noutput:\n%s", err, stdout.String())
	}

	creds, err := loadCredentials()
	if err != nil {
		t.Fatalf("loadCredentials failed: %v", err)
	}
	if creds.Server != "https://relayfile-old.test" {
		t.Fatalf("expected existing server credentials preserved, got server %q", creds.Server)
	}
	if creds.Token != "stale_server_token" {
		t.Fatalf("expected existing server token preserved, got %q", creds.Token)
	}
	if strings.Contains(stdout.String(), "Run 'relayfile setup'") {
		t.Fatalf("expected no workspace setup hint with --skip-workspace-refresh, got %q", stdout.String())
	}
}

// TestLoginRejectsWorkspaceWithSkipFlag asserts that passing both
// --workspace and --skip-workspace-refresh fails fast with a clear
// error. The two are contradictory — --skip-workspace-refresh silently
// winning would make an explicit --workspace a no-op while the command
// still reports success.
func TestLoginRejectsWorkspaceWithSkipFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	var stdout bytes.Buffer
	err := run([]string{
		"login",
		"--cloud-api-url", server.URL,
		"--cloud-token", "cld_browser_token",
		"--workspace", "demo",
		"--skip-workspace-refresh",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error for contradictory flag combo, got nil\noutput:\n%s", stdout.String())
	}
	if !strings.Contains(err.Error(), "--workspace cannot be used with --skip-workspace-refresh") {
		t.Fatalf("expected mutually-exclusive flag error, got %v", err)
	}
}

// TestLoginExplicitWorkspaceReturnsErrorWhenPersistFails asserts that with
// an explicit --workspace target, a failure to write the refreshed
// workspace token to credentials.json surfaces as a non-zero exit / error
// — symmetric with the join and resolve failure branches. Before this
// guard, a persist failure was downgraded to a warning even under
// --workspace, so the command reported success while leaving the on-disk
// JWT stale.
func TestLoginExplicitWorkspaceReturnsErrorWhenPersistFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearRelayfileEnv(t)

	seen := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_demo/join":
			seen["join"] = true
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","token":"rf_refreshed","relayfileUrl":"https://relayfile.test","wsUrl":"wss://relayfile.test/ws","relaycastApiKey":"rc_test"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	if _, err := upsertWorkspaceDetails(workspaceRecord{Name: "demo", ID: "ws_demo"}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if _, err := setDefaultWorkspace("demo"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}

	// Force only the credentials.json rename to fail by pre-creating
	// that exact path as a directory. writeFileAtomically still
	// succeeds at CreateTemp/Write/Chmod/Close; only the final
	// os.Rename(tmp, credentialsPath()) errors because the destination
	// is a non-empty directory. cloud-credentials.json (different
	// path) writes normally.
	if err := os.MkdirAll(credentialsPath(), 0o755); err != nil {
		t.Fatalf("pre-create credentials path as dir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credentialsPath(), "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker file failed: %v", err)
	}

	var stdout bytes.Buffer
	err := run([]string{
		"login",
		"--cloud-api-url", server.URL,
		"--cloud-token", "cld_browser_token",
		"--workspace", "demo",
	}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected error for explicit --workspace with persist failure, got nil\noutput:\n%s", stdout.String())
	}
	if !strings.Contains(err.Error(), "persist refreshed credentials") {
		t.Fatalf("expected persist failure in error, got %v", err)
	}
	if !seen["join"] {
		t.Fatalf("expected /workspaces/ws_demo/join to have been called before persist; seen=%v", seen)
	}
}

// TestLoginDefaultWorkspaceWarnsWhenPersistFails confirms the unchanged
// behavior for the implicit default-workspace path: persist failures
// remain a warning so the cloud login itself still counts as successful.
func TestLoginDefaultWorkspaceWarnsWhenPersistFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearRelayfileEnv(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_demo/join":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_demo","token":"rf_refreshed","relayfileUrl":"https://relayfile.test","wsUrl":"wss://relayfile.test/ws","relaycastApiKey":"rc_test"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	if _, err := upsertWorkspaceDetails(workspaceRecord{Name: "demo", ID: "ws_demo"}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if _, err := setDefaultWorkspace("demo"); err != nil {
		t.Fatalf("setDefaultWorkspace failed: %v", err)
	}

	if err := os.MkdirAll(credentialsPath(), 0o755); err != nil {
		t.Fatalf("pre-create credentials path as dir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credentialsPath(), "blocker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker file failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{
		"login",
		"--cloud-api-url", server.URL,
		"--cloud-token", "cld_browser_token",
	}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("default-workspace login should not error on persist failure: %v\noutput:\n%s", err, stdout.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "warning: workspace token refresh succeeded but persisting it failed") {
		t.Fatalf("expected persist-failure warning in output, got %q", got)
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

// adoptTestServer wires the cloud endpoints the adopt verb exercises:
// token refresh (so credential bootstrap succeeds), workspace join (mints
// the relayfile JWT for the workspace), and the new POST .../adopt route.
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
		case r.URL.Path == "/api/v1/auth/token/refresh":
			if r.Method != http.MethodPost {
				t.Errorf("expected refresh POST, got %s", r.Method)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cld_old" {
				t.Errorf("unexpected refresh Authorization: %q", got)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var body cloudTokenRefreshRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode refresh body failed: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if body.RefreshToken != "refresh_123" {
				t.Errorf("unexpected refresh token: %q", body.RefreshToken)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			seen["refresh"]++
			_, _ = w.Write([]byte(`{"apiUrl":"` + server.URL + `","accessToken":"cld_new","refreshToken":"refresh_123","accessTokenExpiresAt":"2030-05-01T00:00:00Z"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/join":
			if r.Method != http.MethodPost {
				t.Errorf("expected join POST, got %s", r.Method)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
				t.Errorf("unexpected join Authorization: %q", got)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			var body cloudWorkspaceJoinRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode join body failed: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if body.AgentName != "relayfile-cli" {
				t.Errorf("unexpected join agentName: %q", body.AgentName)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if len(body.Scopes) != len(defaultJoinScopes) || body.Scopes[0] != defaultJoinScopes[0] || body.Scopes[1] != defaultJoinScopes[1] {
				t.Errorf("unexpected join scopes: %#v", body.Scopes)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			seen["join"]++
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","token":"rf_join","relayfileUrl":"` + server.URL + `"}`))
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
	if err := saveCloudCredentials(cloudCredentials{
		APIURL:               "https://stale.example.test",
		AccessToken:          "cld_old",
		RefreshToken:         "refresh_123",
		AccessTokenExpiresAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("saveCloudCredentials failed: %v", err)
	}
	return record, localDir
}

func pointAdoptCloudCredentials(t *testing.T, apiURL string) {
	t.Helper()
	creds, err := loadCloudCredentials()
	if err != nil {
		t.Fatalf("loadCloudCredentials failed: %v", err)
	}
	creds.APIURL = apiURL
	if err := saveCloudCredentials(creds); err != nil {
		t.Fatalf("saveCloudCredentials(update) failed: %v", err)
	}
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
		if got := r.Header.Get("Authorization"); got != "Bearer rf_join" {
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
		case r.URL.Path == "/api/v1/auth/token/refresh":
			seen["refresh"]++
			_, _ = w.Write([]byte(`{"apiUrl":"` + server.URL + `","accessToken":"cld_new","refreshToken":"refresh_123","accessTokenExpiresAt":"2030-05-01T00:00:00Z"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/join":
			seen["join"]++
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","token":"rf_join","relayfileUrl":"` + server.URL + `"}`))
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
// `integration connect jira` path touches: refresh+join, the
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
		case r.URL.Path == "/api/v1/auth/token/refresh":
			seen["refresh"]++
			_, _ = w.Write([]byte(`{"apiUrl":"` + server.URL + `","accessToken":"cld_new","refreshToken":"refresh_123","accessTokenExpiresAt":"2030-05-01T00:00:00Z"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/join":
			seen["join"]++
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","token":"rf_join","relayfileUrl":"` + server.URL + `"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/connect-session" && r.Method == http.MethodPost:
			seen["connect-session"]++
			_, _ = w.Write([]byte(`{"connectionId":"conn_jira","connectLink":"","backend":"nango"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/status":
			seen["status"]++
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"jira","backend":"nango"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/accessible-resources":
			seen["accessible-resources"]++
			payload := struct {
				OK        bool                      `json:"ok"`
				Resources []accessibleResourceEntry `json:"resources"`
			}{OK: true, Resources: sites}
			_ = json.NewEncoder(w).Encode(payload)
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/metadata" && r.Method == http.MethodPut:
			seen["metadata"]++
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
		case r.URL.Path == "/api/v1/auth/token/refresh":
			_, _ = w.Write([]byte(`{"apiUrl":"` + server.URL + `","accessToken":"cld_new","refreshToken":"refresh_123","accessTokenExpiresAt":"2030-05-01T00:00:00Z"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/join":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","token":"rf_join","relayfileUrl":"` + server.URL + `"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/status":
			if got := r.URL.Query().Get("connectionId"); got != "conn_jira" {
				t.Fatalf("unexpected connectionId: %q", got)
			}
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"jira","backend":"nango"}`))
		case r.URL.Path == "/v1/workspaces/ws_123/sync/status":
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
		case r.URL.Path == "/api/v1/auth/token/refresh":
			_, _ = w.Write([]byte(`{"apiUrl":"` + server.URL + `","accessToken":"cld_new","refreshToken":"refresh_123","accessTokenExpiresAt":"2030-05-01T00:00:00Z"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/join":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","token":"rf_join","relayfileUrl":"` + server.URL + `"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/connect-session":
			_, _ = w.Write([]byte(`{"connectionId":"conn_github","connectLink":"","backend":"nango"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/github/status":
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"github","backend":"nango"}`))
		case r.URL.Path == "/v1/workspaces/ws_123/sync/status":
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
		case "/api/v1/auth/token/refresh":
			_, _ = w.Write([]byte(`{"apiUrl":"` + server.URL + `","accessToken":"cld_new","refreshToken":"refresh_123","accessTokenExpiresAt":"2030-05-01T00:00:00Z"}`))
		case "/api/v1/workspaces/50587328-441d-4acb-b8f3-dbe1b3c5de99/join":
			_, _ = w.Write([]byte(`{"workspaceId":"rw_7ccfea89","token":"rf_join","relayfileUrl":"` + server.URL + `"}`))
		case "/api/v1/workspaces/50587328-441d-4acb-b8f3-dbe1b3c5de99/integrations/connect-session":
			_, _ = w.Write([]byte(`{"connectionId":"conn_slack","connectLink":"","backend":"nango"}`))
		case "/api/v1/workspaces/50587328-441d-4acb-b8f3-dbe1b3c5de99/integrations/slack/status":
			_, _ = w.Write([]byte(`{"ready":true,"state":"ready","provider":"slack","backend":"nango"}`))
		case "/v1/workspaces/rw_7ccfea89/sync/status":
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
