package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// disconnectTestCloud is a minimal Cloud stand-in for the disconnect flow:
// DELETE and GET on .../integrations/jira/status plus GET on the workspace
// integration list. It records every request so tests can assert the exact
// scope the CLI sent, and lets each test script Cloud's answers.
type disconnectTestCloud struct {
	t *testing.T

	mu       sync.Mutex
	requests []string // "METHOD path?query"

	deleteStatus int
	deleteBody   string
	// statusAfterDelete is returned by GET .../status; a nil body means 404.
	statusAfterDelete *string
	listBody          string
	listStatus        int
}

func newDisconnectTestCloud(t *testing.T) (*disconnectTestCloud, *httptest.Server) {
	t.Helper()
	cloud := &disconnectTestCloud{
		t:            t,
		deleteStatus: http.StatusOK,
		deleteBody:   `{"success":true}`,
		listBody:     `[]`,
		listStatus:   http.StatusOK,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cld_new" {
			t.Errorf("unexpected Authorization header %q", got)
		}
		cloud.mu.Lock()
		cloud.requests = append(cloud.requests, r.Method+" "+r.URL.RequestURI())
		cloud.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/status" && r.Method == http.MethodDelete:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(cloud.deleteStatus)
			_, _ = w.Write([]byte(cloud.deleteBody))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations/jira/status" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			if cloud.statusAfterDelete == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"not_found","message":"integration not found"}`))
				return
			}
			_, _ = w.Write([]byte(*cloud.statusAfterDelete))
		case r.URL.Path == "/api/v1/workspaces/ws_123/integrations" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(cloud.listStatus)
			_, _ = w.Write([]byte(cloud.listBody))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return cloud, server
}

func (c *disconnectTestCloud) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.requests...)
}

func strPtr(s string) *string { return &s }

// seedDisconnectLocalState gives the mirror a provider root and a saved
// connection file so tests can tell whether the CLI touched local state.
func seedDisconnectLocalState(t *testing.T, localDir string) (providerRoot, connectionFile, marker string) {
	t.Helper()
	providerRoot = filepath.Join(localDir, providerRootDir("jira"))
	if err := os.MkdirAll(providerRoot, 0o755); err != nil {
		t.Fatalf("mkdir provider root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(providerRoot, "issue.md"), []byte("mirrored\n"), 0o644); err != nil {
		t.Fatalf("seed provider file: %v", err)
	}
	connectionFile = integrationConnectionPath(localDir, "jira")
	if err := os.MkdirAll(filepath.Dir(connectionFile), 0o755); err != nil {
		t.Fatalf("mkdir connection dir: %v", err)
	}
	if err := os.WriteFile(connectionFile, []byte(`{"provider":"jira","connectionId":"conn_default"}`+"\n"), 0o644); err != nil {
		t.Fatalf("seed connection file: %v", err)
	}
	marker = filepath.Join(localDir, ".relay", "disconnected", "jira.json")
	return providerRoot, connectionFile, marker
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	t.Fatalf("stat %s: %v", path, err)
	return false
}

func runDisconnect(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(append([]string{"integration", "disconnect"}, args...), strings.NewReader(""), &stdout, &stderr)
	return stdout.String() + stderr.String(), err
}

func TestIntegrationDisconnectDefaultConnectionMarksLocalStateAfterVerifiedDelete(t *testing.T) {
	_, localDir := setupAdoptWorkspace(t)
	cloud, server := newDisconnectTestCloud(t)
	pointAdoptCloudCredentials(t, server.URL)
	providerRoot, connectionFile, marker := seedDisconnectLocalState(t, localDir)
	cloud.statusAfterDelete = nil // Cloud no longer knows the row

	out, err := runDisconnect(t, "jira", "--workspace", "demo", "--yes", "--cloud-api-url", server.URL)
	if err != nil {
		t.Fatalf("disconnect failed: %v\n%s", err, out)
	}
	want := []string{
		"DELETE /api/v1/workspaces/ws_123/integrations/jira/status",
		"GET /api/v1/workspaces/ws_123/integrations/jira/status",
		"GET /api/v1/workspaces/ws_123/integrations",
	}
	if got := cloud.recorded(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected cloud requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if fileExists(t, providerRoot) {
		t.Fatalf("provider root %s should have been removed", providerRoot)
	}
	if fileExists(t, connectionFile) {
		t.Fatalf("connection file %s should have been removed", connectionFile)
	}
	if !fileExists(t, marker) {
		t.Fatalf("disconnect marker %s should have been written", marker)
	}
	if !strings.Contains(out, "jira disconnected from workspace demo") {
		t.Fatalf("missing confirmation in output:\n%s", out)
	}
	if strings.Contains(out, "named jira connection") {
		t.Fatalf("no named rows were listed, output must not mention them:\n%s", out)
	}
}

func TestIntegrationDisconnectTreatsDisconnectedStatusAsAbsent(t *testing.T) {
	// Cloud keeps answering GET /status after a delete on some deployments
	// (ready:false, no connection). That is "gone", not "still present".
	_, localDir := setupAdoptWorkspace(t)
	cloud, server := newDisconnectTestCloud(t)
	pointAdoptCloudCredentials(t, server.URL)
	_, _, marker := seedDisconnectLocalState(t, localDir)
	cloud.statusAfterDelete = strPtr(`{"ready":false,"provider":"jira","state":"disconnected","currentConnectionId":null,"connectionMatched":false,"oauth":{"connected":false}}`)

	out, err := runDisconnect(t, "jira", "--workspace", "demo", "--yes", "--cloud-api-url", server.URL)
	if err != nil {
		t.Fatalf("disconnect failed: %v\n%s", err, out)
	}
	if !fileExists(t, marker) {
		t.Fatalf("disconnect marker %s should have been written", marker)
	}
}

func TestIntegrationDisconnectRefusesLocalCleanupWhenCloudStillReportsConnection(t *testing.T) {
	// Regression for the named-row bug: Cloud answered {"success":true}
	// three times while the row survived. A 2xx is not proof; the CLI must
	// re-read the status and leave the mirror alone when a connection is
	// still reported.
	_, localDir := setupAdoptWorkspace(t)
	cloud, server := newDisconnectTestCloud(t)
	pointAdoptCloudCredentials(t, server.URL)
	providerRoot, connectionFile, marker := seedDisconnectLocalState(t, localDir)
	cloud.statusAfterDelete = strPtr(`{"ready":true,"provider":"jira","state":"connected","currentConnectionId":"conn_recovery","connectionMatched":true,"oauth":{"connected":true}}`)

	out, err := runDisconnect(t, "jira", "--workspace", "demo", "--yes", "--cloud-api-url", server.URL)
	if err == nil {
		t.Fatalf("expected an error, got success:\n%s", out)
	}
	for _, want := range []string{"still reports connection conn_recovery", "local mirror left untouched"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q should mention %q", err.Error(), want)
		}
	}
	if !fileExists(t, providerRoot) || !fileExists(t, connectionFile) {
		t.Fatalf("local state must be untouched when Cloud still reports a connection")
	}
	if fileExists(t, marker) {
		t.Fatalf("disconnect marker %s must not be written", marker)
	}
	if strings.Contains(out, "disconnected from workspace") {
		t.Fatalf("must not print a success line:\n%s", out)
	}
	// No list call: the CLI stops at the failed verification.
	for _, req := range cloud.recorded() {
		if req == "GET /api/v1/workspaces/ws_123/integrations" {
			t.Fatalf("unexpected list request after failed verification: %v", cloud.recorded())
		}
	}
}

func TestIntegrationDisconnectSurfacesLocalDeletedFalse(t *testing.T) {
	_, localDir := setupAdoptWorkspace(t)
	cloud, server := newDisconnectTestCloud(t)
	pointAdoptCloudCredentials(t, server.URL)
	providerRoot, _, marker := seedDisconnectLocalState(t, localDir)
	cloud.deleteBody = `{"success":true,"localDeleted":false}`
	cloud.statusAfterDelete = nil

	out, err := runDisconnect(t, "jira", "--workspace", "demo", "--yes", "--cloud-api-url", server.URL)
	if err == nil {
		t.Fatalf("expected an error, got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), "localDeleted=false") || !strings.Contains(err.Error(), "local mirror left untouched") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fileExists(t, providerRoot) || fileExists(t, marker) {
		t.Fatalf("local state must be untouched after localDeleted=false")
	}
	if got := cloud.recorded(); len(got) != 1 || !strings.HasPrefix(got[0], "DELETE ") {
		t.Fatalf("CLI must stop at the DELETE response, got %v", got)
	}
}

func TestIntegrationDisconnectListsSurvivingNamedRowsAfterDefaultDisconnect(t *testing.T) {
	_, localDir := setupAdoptWorkspace(t)
	cloud, server := newDisconnectTestCloud(t)
	pointAdoptCloudCredentials(t, server.URL)
	_, _, marker := seedDisconnectLocalState(t, localDir)
	cloud.statusAfterDelete = nil
	cloud.listBody = `[
		{"provider":"jira","name":"relay-full-proof-jira-20261106","status":"connected","connectionId":"conn_full_proof"},
		{"provider":"jira","name":"relay-jira-recovery-20261106","status":"connected","connectionId":"conn_recovery"},
		{"provider":"github","name":"other-provider-named","status":"connected","connectionId":"conn_gh"},
		{"provider":"jira","status":"connected","connectionId":"conn_unnamed_elsewhere"}
	]`

	out, err := runDisconnect(t, "jira", "--workspace", "demo", "--yes", "--cloud-api-url", server.URL)
	if err != nil {
		t.Fatalf("disconnect failed: %v\n%s", err, out)
	}
	if !fileExists(t, marker) {
		t.Fatalf("default disconnect must still mark local state")
	}
	for _, want := range []string{
		"jira disconnected from workspace demo",
		"note: 2 named jira connection(s) remain in workspace demo and were not disconnected:",
		"  relay-full-proof-jira-20261106 (conn_full_proof)",
		"  relay-jira-recovery-20261106 (conn_recovery)",
		"integration disconnect jira --name SLOT --workspace demo",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	for _, reject := range []string{"other-provider-named", "conn_unnamed_elsewhere"} {
		if strings.Contains(out, reject) {
			t.Fatalf("output must not list %q:\n%s", reject, out)
		}
	}
}

func TestIntegrationDisconnectListFailureIsAWarningNotAFailure(t *testing.T) {
	_, localDir := setupAdoptWorkspace(t)
	cloud, server := newDisconnectTestCloud(t)
	pointAdoptCloudCredentials(t, server.URL)
	_, _, marker := seedDisconnectLocalState(t, localDir)
	cloud.statusAfterDelete = nil
	cloud.listStatus = http.StatusBadGateway
	cloud.listBody = `{"code":"upstream","message":"list unavailable"}`

	out, err := runDisconnect(t, "jira", "--workspace", "demo", "--yes", "--cloud-api-url", server.URL)
	if err != nil {
		t.Fatalf("verified disconnect must succeed even when the list fails: %v\n%s", err, out)
	}
	if !fileExists(t, marker) {
		t.Fatalf("default disconnect must still mark local state")
	}
	if !strings.Contains(out, "warning: could not list remaining jira connections") {
		t.Fatalf("missing list warning:\n%s", out)
	}
}

func TestIntegrationDisconnectNamedSlotScopesRequestAndLeavesMirrorAlone(t *testing.T) {
	_, localDir := setupAdoptWorkspace(t)
	cloud, server := newDisconnectTestCloud(t)
	pointAdoptCloudCredentials(t, server.URL)
	providerRoot, connectionFile, marker := seedDisconnectLocalState(t, localDir)
	cloud.statusAfterDelete = nil

	out, err := runDisconnect(t, "jira", "--workspace", "demo", "--name", "relay-jira-recovery-20261106", "--yes", "--cloud-api-url", server.URL)
	if err != nil {
		t.Fatalf("disconnect failed: %v\n%s", err, out)
	}
	want := []string{
		"DELETE /api/v1/workspaces/ws_123/integrations/jira/status?scopeKind=workspace_service_account&scopeName=relay-jira-recovery-20261106",
		"GET /api/v1/workspaces/ws_123/integrations/jira/status?scopeKind=workspace_service_account&scopeName=relay-jira-recovery-20261106",
	}
	if got := cloud.recorded(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected cloud requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The default connection's local mirror belongs to the unnamed row and
	// must survive a named-slot disconnect.
	if !fileExists(t, providerRoot) || !fileExists(t, connectionFile) {
		t.Fatalf("named-slot disconnect must leave the local mirror alone")
	}
	if fileExists(t, marker) {
		t.Fatalf("named-slot disconnect must not write the provider disconnect marker")
	}
	if !strings.Contains(out, `jira connection "relay-jira-recovery-20261106" disconnected from workspace demo (local mirror unchanged)`) {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func TestIntegrationDisconnectNamedSlotFailsWhenRowSurvives(t *testing.T) {
	// Pre-6146 Cloud ignores the scope query and deletes the default row
	// instead; the named status then still reports the named connection.
	// The CLI must report that, not success.
	_, localDir := setupAdoptWorkspace(t)
	cloud, server := newDisconnectTestCloud(t)
	pointAdoptCloudCredentials(t, server.URL)
	providerRoot, _, _ := seedDisconnectLocalState(t, localDir)
	cloud.statusAfterDelete = strPtr(`{"ready":true,"provider":"jira","state":"connected","currentConnectionId":"conn_recovery","connectionMatched":true}`)

	out, err := runDisconnect(t, "jira", "--workspace", "demo", "--name", "relay-jira-recovery-20261106", "--yes", "--cloud-api-url", server.URL)
	if err == nil {
		t.Fatalf("expected an error, got success:\n%s", out)
	}
	if !strings.Contains(err.Error(), `jira connection "relay-jira-recovery-20261106"`) || !strings.Contains(err.Error(), "conn_recovery") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fileExists(t, providerRoot) {
		t.Fatalf("local mirror must be untouched")
	}
}

func TestIntegrationDisconnectRejectsBlankName(t *testing.T) {
	setupAdoptWorkspace(t)
	_, server := newDisconnectTestCloud(t)
	pointAdoptCloudCredentials(t, server.URL)
	out, err := runDisconnect(t, "jira", "--workspace", "demo", "--name", "   ", "--yes", "--cloud-api-url", server.URL)
	if err == nil || !strings.Contains(err.Error(), "--name requires a non-empty") {
		t.Fatalf("expected blank-name usage error, got err=%v out=%s", err, out)
	}
}

func TestIntegrationDisconnectPromptMentionsNamedSlot(t *testing.T) {
	setupAdoptWorkspace(t)
	cloud, server := newDisconnectTestCloud(t)
	pointAdoptCloudCredentials(t, server.URL)
	var stdout, stderr bytes.Buffer
	err := run([]string{"integration", "disconnect", "jira", "--workspace", "demo", "--name", "relay-jira-recovery-20261106", "--cloud-api-url", server.URL}, strings.NewReader("n\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("declined prompt should not error: %v", err)
	}
	if !strings.Contains(stdout.String(), `Disconnect "jira" connection "relay-jira-recovery-20261106" from workspace "demo"? [y/N]:`) {
		t.Fatalf("prompt missing slot name:\n%s", stdout.String())
	}
	if len(cloud.recorded()) != 0 {
		t.Fatalf("declined prompt must not call Cloud: %v", cloud.recorded())
	}
}

func TestCloudIntegrationListEntryDecodesName(t *testing.T) {
	var entries []cloudIntegrationListEntry
	if err := json.Unmarshal([]byte(`[{"provider":"jira","name":"slot-a"},{"provider":"jira"}]`), &entries); err != nil {
		t.Fatal(err)
	}
	if entries[0].Name != "slot-a" || entries[1].Name != "" {
		t.Fatalf("unexpected decode: %+v", entries)
	}
}
