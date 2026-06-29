package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlPlaneHelloVersionAndCLIVersion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var stdout bytes.Buffer
	if err := run([]string{"--version"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("relayfile --version failed: %v", err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "0.10.17" {
		t.Fatalf("relayfile --version = %q, want 0.10.17", got)
	}

	client, baseURL, cleanup := startControlPlaneTestServer(t)
	defer cleanup()

	var hello helloResponse
	status := controlPlaneJSON(t, client, http.MethodGet, baseURL+"/v1/hello?apiVersion=1", nil, &hello)
	if status != http.StatusOK {
		t.Fatalf("hello status = %d", status)
	}
	if hello.DaemonVersion != "0.10.17" || hello.APIVersion != 1 {
		t.Fatalf("unexpected hello response: %#v", hello)
	}

	var errResp map[string]controlPlaneError
	status = controlPlaneJSONWithoutVersion(t, client, http.MethodGet, baseURL+"/v1/hello?apiVersion=2", nil, &errResp)
	if status != http.StatusUpgradeRequired {
		t.Fatalf("incompatible hello status = %d, want %d", status, http.StatusUpgradeRequired)
	}
	if errResp["error"].Code != controlPlaneErrVersionIncompatible {
		t.Fatalf("unexpected incompatible error: %#v", errResp)
	}
}

func TestControlPlaneBindingConformance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if _, err := upsertWorkspaceDetails(workspaceRecord{Name: "demo", ID: "ws_demo", LocalDir: localDir}); err != nil {
		t.Fatalf("upsert workspace failed: %v", err)
	}
	writeTestMountFile(t, localDir, "slack/channels/by-name/watchdog-test.json", `{"id":"C123","canonicalPath":"/slack/channels/C123__watchdog-test/meta.json"}`)

	client, baseURL, cleanup := startControlPlaneTestServer(t)
	defer cleanup()

	var resolved resolveResourcePathResponse
	status := controlPlaneJSON(t, client, http.MethodPost, baseURL+"/v1/integrations/resolve-path", resolveResourcePathRequest{
		Provider: "slack",
		Resource: "#watchdog-test",
	}, &resolved)
	if status != http.StatusOK {
		t.Fatalf("resolve-path status = %d", status)
	}
	if resolved.PathGlob != "/slack/channels/C123__watchdog-test/**" || !resolved.ResolvedExact {
		t.Fatalf("unexpected resolve-path response: %#v", resolved)
	}

	var bound bindResponse
	status = controlPlaneJSON(t, client, http.MethodPost, baseURL+"/v1/integrations/bind", bindRequest{
		Provider:     "slack",
		Resource:     "#watchdog-test",
		Channel:      "#events",
		WebhookID:    "wh_1",
		WebhookToken: "tok_1",
	}, &bound)
	if status != http.StatusOK {
		t.Fatalf("bind status = %d", status)
	}
	if bound.Binding.PathGlob != resolved.PathGlob || bound.Binding.WebhookToken != "tok_1" {
		t.Fatalf("unexpected bind response: %#v", bound)
	}

	var listed listBindingsResponse
	status = controlPlaneJSON(t, client, http.MethodGet, baseURL+"/v1/integrations/bindings", nil, &listed)
	if status != http.StatusOK {
		t.Fatalf("bindings status = %d", status)
	}
	if len(listed.Bindings) != 1 || listed.Bindings[0].PathGlob != resolved.PathGlob {
		t.Fatalf("unexpected bindings response: %#v", listed)
	}

	var stdout bytes.Buffer
	if err := run([]string{"integration", "bind", "--list", "--json"}, strings.NewReader(""), &stdout, &stdout); err != nil {
		t.Fatalf("bind --list --json failed: %v\n%s", err, stdout.String())
	}
	var cliBindings []relayIntegrationBinding
	if err := json.Unmarshal(stdout.Bytes(), &cliBindings); err != nil {
		t.Fatalf("parse bind --list --json output failed: %v\n%s", err, stdout.String())
	}
	if len(cliBindings) != 1 || cliBindings[0].PathGlob != resolved.PathGlob {
		t.Fatalf("unexpected CLI bindings: %#v", cliBindings)
	}

	var unbound relayIntegrationUnbindResult
	status = controlPlaneJSON(t, client, http.MethodPost, baseURL+"/v1/integrations/unbind", unbindRequest{
		Provider: "slack",
		Resource: "#watchdog-test",
	}, &unbound)
	if status != http.StatusOK {
		t.Fatalf("unbind status = %d", status)
	}
	if unbound.Removed != 1 || unbound.PathGlob != resolved.PathGlob {
		t.Fatalf("unexpected unbind response: %#v", unbound)
	}

	status = controlPlaneJSON(t, client, http.MethodGet, baseURL+"/v1/integrations/bindings", nil, &listed)
	if status != http.StatusOK {
		t.Fatalf("bindings after unbind status = %d", status)
	}
	if len(listed.Bindings) != 0 {
		t.Fatalf("expected empty bindings after unbind, got %#v", listed)
	}
}

func TestControlPlaneCloudIntegrationConformance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := saveWorkspaceCatalog(workspaceCatalog{
		Default: "demo",
		Workspaces: []workspaceRecord{{
			Name:      "demo",
			ID:        "ws_123",
			LocalDir:  localDir,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			Scopes:    append([]string(nil), defaultJoinScopes...),
		}},
	}); err != nil {
		t.Fatalf("save workspace catalog failed: %v", err)
	}

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_123/integrations":
			if r.Method != http.MethodGet {
				t.Fatalf("expected integrations GET, got %s", r.Method)
			}
			_, _ = w.Write([]byte(`[{"provider":"github","status":"ready","connectionId":"conn_123"}]`))
		case "/v1/workspaces/ws_123/sync/status":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","providers":[{"provider":"github","status":"ready","lagSeconds":0}]}`))
		case "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			if r.Method != http.MethodPost {
				t.Fatalf("expected delegated-token POST, got %s", r.Method)
			}
			writeDelegatedBundleResponse(t, w, server.URL, "ws_123", testJWTWithWorkspace("ws_123"), "refresh_join")
		case "/api/v1/workspaces/ws_123/integrations/connect-session":
			if r.Method != http.MethodPost {
				t.Fatalf("expected connect-session POST, got %s", r.Method)
			}
			var body cloudConnectSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode connect-session body failed: %v", err)
			}
			if len(body.AllowedIntegrations) != 1 || body.AllowedIntegrations[0] != "github" {
				t.Fatalf("unexpected connect-session body: %#v", body)
			}
			_, _ = w.Write([]byte(`{"connectLink":"https://connect.test/github","connectionId":"conn_123"}`))
		case "/api/v1/workspaces/ws_123/integrations/github/status":
			if r.URL.Query().Get("connectionId") != "conn_123" {
				t.Fatalf("unexpected connectionId: %q", r.URL.Query().Get("connectionId"))
			}
			_, _ = w.Write([]byte(`{"ready":true,"provider":"github","connectionId":"conn_123","state":"ready"}`))
		case "/v1/workspaces/ws_123/integrations/relay/writeback-secret":
			if r.URL.Query().Get("channel") != "#events" {
				t.Fatalf("unexpected writeback-secret channel: %q", r.URL.Query().Get("channel"))
			}
			_, _ = w.Write([]byte(`{"ok":true,"data":{"url":"https://relay.test/writeback","secret":"sec_123"}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	installFakeAgentRelaySession(t, server.URL, "cld_test", "demo", "ws_123", "ws_123")

	client, baseURL, cleanup := startControlPlaneTestServer(t)
	defer cleanup()

	var providers listProvidersResponse
	status := controlPlaneJSON(t, client, http.MethodGet, baseURL+"/v1/integrations/providers", nil, &providers)
	if status != http.StatusOK {
		t.Fatalf("providers status = %d", status)
	}
	if len(providers.Providers) == 0 {
		t.Fatalf("expected fallback providers")
	}

	var connected connectProviderResponse
	status = controlPlaneJSON(t, client, http.MethodPost, baseURL+"/v1/integrations/connect", connectProviderRequest{
		Provider:  "github",
		Workspace: "demo",
		NoOpen:    true,
		Timeout:   "250ms",
	}, &connected)
	if status != http.StatusOK {
		t.Fatalf("connect status = %d", status)
	}
	if connected.Provider != "github" || !strings.Contains(connected.Output, "github connected") {
		t.Fatalf("unexpected connect response: %#v", connected)
	}

	var statusEntry cloudIntegrationListEntry
	status = controlPlaneJSON(t, client, http.MethodGet, baseURL+"/v1/integrations/provider-status?provider=github&workspace=demo", nil, &statusEntry)
	if status != http.StatusOK {
		t.Fatalf("provider-status status = %d", status)
	}
	if statusEntry.Provider != "github" || statusEntry.Status != "ready" {
		t.Fatalf("unexpected provider status: %#v", statusEntry)
	}

	var secret writebackSecretData
	status = controlPlaneJSON(t, client, http.MethodPost, baseURL+"/v1/integrations/writeback-secret", writebackSecretRequest{
		Workspace: "demo",
		Channel:   "#events",
	}, &secret)
	if status != http.StatusOK {
		t.Fatalf("writeback-secret status = %d", status)
	}
	if secret.URL != "https://relay.test/writeback" || secret.Secret != "sec_123" {
		t.Fatalf("unexpected writeback secret: %#v", secret)
	}
}

func startControlPlaneTestServer(t *testing.T) (*http.Client, string, func()) {
	t.Helper()
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("rfcp-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _ = os.Remove(sock) })
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen unix socket failed: %v", err)
	}
	server := &http.Server{Handler: newControlPlaneHandler()}
	errCh := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", sock)
		},
	}
	client := &http.Client{Transport: transport}
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		transport.CloseIdleConnections()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("control-plane server failed: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for control-plane server shutdown")
		}
		_ = os.Remove(sock)
	}
	return client, "http://relayfile.test", cleanup
}

func controlPlaneJSON(t *testing.T, client *http.Client, method, url string, body any, out any) int {
	t.Helper()
	return controlPlaneJSONWithVersionHeader(t, client, method, url, body, out, true)
}

func controlPlaneJSONWithoutVersion(t *testing.T, client *http.Client, method, url string, body any, out any) int {
	t.Helper()
	return controlPlaneJSONWithVersionHeader(t, client, method, url, body, out, false)
}

func controlPlaneJSONWithVersionHeader(t *testing.T, client *http.Client, method, url string, body any, out any, sendVersion bool) int {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request failed: %v", err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request failed: %v", err)
	}
	if sendVersion {
		req.Header.Set("X-Relayfile-API-Version", "1")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("control-plane request failed: %v", err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode response failed with status %d: %v", resp.StatusCode, err)
		}
	}
	return resp.StatusCode
}
