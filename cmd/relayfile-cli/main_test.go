package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
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

	if got := strings.TrimSpace(stdout.String()); got != "ws_env\n.relay/vfs\nrelay-test" {
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

	if got := strings.TrimSpace(stdout.String()); got != "ws_token" {
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
			_, _ = w.Write([]byte(`{"connectLink":"https://connect.test/notion","connectionId":"conn_789"}`))
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
	if err := run([]string{"ops", "list", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
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
	if err := run([]string{"ops", "list", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("run ops list failed: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "No dead-lettered ops" {
		t.Fatalf("expected empty marker, got: %q", got)
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
