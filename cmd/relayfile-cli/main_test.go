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
	"strings"
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
	if got := stdout.String(); !strings.Contains(got, "notion connected") {
		t.Fatalf("unexpected integration connect output: %q", got)
	}
	state := loadSavedConnection(localDir, "notion")
	if state.ConnectionID != "conn_789" || state.Backend != "composio" {
		t.Fatalf("unexpected saved integration connection: %#v", state)
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
		t.Fatalf("expected no cloud credentials file written, got err=%v", err)
	}
}

// TestLoginDefaultsToCloudBrowserFlow covers the new default behavior: when
// no --token is provided, runLogin runs the cloud browser flow. We use
// --cloud-token to skip the actual browser handshake — ensureCloudCredentials
// short-circuits to writing cloud credentials when an explicit token is set.
func TestLoginDefaultsToCloudBrowserFlow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

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
		t.Fatalf("expected no server credentials written, got err=%v", err)
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
}

// TestLoginSkipsWorkspaceRefreshWhenFlagSet covers --skip-workspace-refresh:
// even with a workspace registered, no /join call should fire and
// credentials.json must remain absent.
func TestLoginSkipsWorkspaceRefreshWhenFlagSet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

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

	if _, err := os.Stat(credentialsPath()); !os.IsNotExist(err) {
		t.Fatalf("expected no server credentials written with --skip-workspace-refresh, got err=%v", err)
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
			seen["refresh"]++
			_, _ = w.Write([]byte(`{"apiUrl":"` + server.URL + `","accessToken":"cld_new","refreshToken":"refresh_123","accessTokenExpiresAt":"2030-05-01T00:00:00Z"}`))
		case r.URL.Path == "/api/v1/workspaces/ws_123/join":
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
	creds, _ := loadCloudCredentials()
	creds.APIURL = server.URL
	_ = saveCloudCredentials(creds)

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
	creds, _ := loadCloudCredentials()
	creds.APIURL = server.URL
	_ = saveCloudCredentials(creds)

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
	creds, _ := loadCloudCredentials()
	creds.APIURL = server.URL
	_ = saveCloudCredentials(creds)

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
