package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIntegrationConnectGitLabTokenVariants(t *testing.T) {
	for _, configKey := range []string{"gitlab-pat", "gitlab-group-token"} {
		t.Run(configKey, func(t *testing.T) {
			_, localDir := setupAdoptWorkspace(t)
			seen := map[string]int{}
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				seen[r.URL.Path]++
				switch r.URL.Path {
				case "/api/v1/workspaces/ws_123/relayfile/delegated-token":
					writeDelegatedBundleResponse(t, w, server.URL, "ws_123", "rf_join", "refresh_join")
				case "/api/v1/workspaces/ws_123/integrations/connect-session":
					var body cloudConnectSessionRequest
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if len(body.AllowedIntegrations) != 1 || body.AllowedIntegrations[0] != configKey || body.RequestedBackend != "nango" {
						t.Errorf("auth variant lost in connect request: %#v", body)
					}
					_ = json.NewEncoder(w).Encode(map[string]string{"connectionId": "token-connection", "backend": "nango", "backendIntegrationId": configKey})
				case "/api/v1/workspaces/ws_123/integrations/gitlab/status":
					_ = json.NewEncoder(w).Encode(map[string]any{"ready": true, "provider": "gitlab", "currentConnectionId": "token-connection", "configKey": configKey, "connectionMatched": true})
				case "/v1/workspaces/ws_123/sync/status":
					_, _ = w.Write([]byte(`{"providers":[{"provider":"gitlab","status":"ready","lagSeconds":0}]}`))
				default:
					http.Error(w, "unexpected path", http.StatusNotFound)
				}
			}))
			defer server.Close()
			pointAdoptCloudCredentials(t, server.URL)
			var stdout bytes.Buffer
			err := run([]string{"integration", "connect", configKey, "--workspace", "demo", "--cloud-api-url", server.URL, "--backend", "nango", "--no-open", "--wait-sync", "--timeout", "1s"}, strings.NewReader(""), &stdout, &stdout)
			if err != nil {
				t.Fatalf("token connect failed: %v\n%s", err, stdout.String())
			}
			if seen["/api/v1/workspaces/ws_123/integrations/gitlab/status"] == 0 || seen["/v1/workspaces/ws_123/sync/status"] == 0 {
				t.Fatalf("canonical GitLab status/sync not reached: %v", seen)
			}
			state := loadSavedConnection(localDir, "gitlab")
			if state.Provider != "gitlab" || state.ConnectionID != "token-connection" || state.Backend != "nango" {
				t.Fatalf("expected canonical saved GitLab connection, got %#v", state)
			}
			payload, err := os.ReadFile(integrationConnectionPath(localDir, "gitlab"))
			if err != nil {
				t.Fatal(err)
			}
			var saved map[string]any
			_ = json.Unmarshal(payload, &saved)
			if saved["providerConfigKey"] != configKey {
				t.Fatalf("saved auth variant missing: %s", payload)
			}
			if got := setupAgentPromptPath(localDir, configKey); got != filepath.Join(localDir, "gitlab") {
				t.Fatalf("token prompt points at %q", got)
			}
		})
	}
}

func TestGitLabTokenConnectDoesNotReuseOAuthConnection(t *testing.T) {
	_, localDir := setupAdoptWorkspace(t)
	if err := saveIntegrationConnection(localDir, integrationConnectionState{Provider: "gitlab", ConnectionID: "oauth-connection", Backend: "nango"}); err != nil {
		t.Fatal(err)
	}
	seenConnect := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/connect-session") {
			seenConnect = true
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"already_connected"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ready":true}`))
	}))
	defer server.Close()
	_, err := ensureCloudIntegration(server.URL, "ws_123", "cloud-token", "gitlab-pat", "nango", localDir, time.Second, false, &bytes.Buffer{})
	if err == nil || !seenConnect || !strings.Contains(err.Error(), "Disconnect") {
		t.Fatalf("token request must reach Cloud instead of reusing OAuth: err=%v connect=%v", err, seenConnect)
	}
	if state := loadSavedConnection(localDir, "gitlab"); state.ConnectionID != "oauth-connection" {
		t.Fatalf("failed auth switch overwrote the existing state: %#v", state)
	}
}

func TestGitLabStatusFailureDoesNotReportConnected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/connect-session") {
			_, _ = w.Write([]byte(`{"connectionId":"pending-token","backend":"nango","backendIntegrationId":"gitlab-pat"}`))
			return
		}
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"message":"private server detail"}`))
	}))
	defer server.Close()
	var stdout bytes.Buffer
	err := connectCloudIntegration(server.URL, "ws_123", "cloud-token", "gitlab-pat", "nango", t.TempDir(), time.Second, false, &stdout)
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 500 || strings.Contains(stdout.String(), "gitlab connected") || strings.Contains(err.Error(), "private server detail") {
		t.Fatalf("status failure was hidden: err=%v output=%s", err, stdout.String())
	}
}

func TestGitLabConnectReadsCloudStatusBinding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	localDir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/connect-session") {
			_, _ = w.Write([]byte(`{"connectionId":"session-id","backend":"nango","backendIntegrationId":"gitlab-pat"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ready": false, "state": "pending", "configKey": "gitlab-pat",
			"connectionMatched":   r.URL.Query().Get("connectionId") == "",
			"currentConnectionId": "actual-nango-id", "oauth": map[string]bool{"connected": true},
		})
	}))
	defer server.Close()
	err := connectCloudIntegration(server.URL, "ws_123", "cloud-token", "gitlab-pat", "nango", localDir, time.Second, false, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if got := loadSavedConnection(localDir, "gitlab").ConnectionID; got != "actual-nango-id" {
		t.Fatalf("saved session id instead of Cloud binding: %q", got)
	}
}

func TestGitLabConnectRejectsStatusForDifferentAuthConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/connect-session") {
			_, _ = w.Write([]byte(`{"connectionId":"pending-oauth","backend":"nango","backendIntegrationId":"gitlab-relay"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ready": true, "state": "ready", "configKey": "gitlab-pat",
			"connectionMatched":   r.URL.Query().Get("connectionId") == "",
			"currentConnectionId": "old-token-id", "oauth": map[string]bool{"connected": true},
		})
	}))
	defer server.Close()
	var stdout bytes.Buffer
	err := connectCloudIntegration(server.URL, "ws_123", "cloud-token", "gitlab", "nango", t.TempDir(), 100*time.Millisecond, false, &stdout)
	if err == nil || strings.Contains(stdout.String(), "gitlab connected") {
		t.Fatalf("wrong auth configuration satisfied OAuth: err=%v output=%s", err, stdout.String())
	}
}

func TestGitLabConnectionReuseRequiresMatchingAuthConfig(t *testing.T) {
	for _, tc := range []struct {
		requested, saved string
		reuse            bool
	}{
		{"gitlab", "gitlab-pat", false},
		{"gitlab", "gitlab-group-token", false},
		{"gitlab", "gitlab-relay", true},
		{"gitlab", "", true},
		{"gitlab-pat", "gitlab-pat", true},
		{"gitlab-group-token", "gitlab-group-token", true},
	} {
		t.Run(tc.requested+"_from_"+tc.saved, func(t *testing.T) {
			_, localDir := setupAdoptWorkspace(t)
			if err := saveIntegrationConnection(localDir, integrationConnectionState{Provider: "gitlab", ProviderConfigKey: tc.saved, ConnectionID: "existing-connection", Backend: "nango"}); err != nil {
				t.Fatal(err)
			}
			seenConnect := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/connect-session") {
					seenConnect = true
					var body cloudConnectSessionRequest
					_ = json.NewDecoder(r.Body).Decode(&body)
					if len(body.AllowedIntegrations) != 1 || body.AllowedIntegrations[0] != tc.requested {
						t.Errorf("wrong auth request: %#v", body)
					}
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"error":"already_connected"}`))
					return
				}
				configKey := tc.saved
				if configKey == "" {
					configKey = "gitlab-relay"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ready": true, "configKey": configKey, "connectionMatched": true})
			}))
			defer server.Close()
			created, err := ensureCloudIntegration(server.URL, "ws_123", "cloud-token", tc.requested, "nango", localDir, time.Second, false, &bytes.Buffer{})
			if tc.reuse {
				if err != nil || created || seenConnect {
					t.Fatalf("matching auth was not reused: err=%v created=%v connect=%v", err, created, seenConnect)
				}
			} else if err == nil || !seenConnect || !strings.Contains(err.Error(), "Disconnect") {
				t.Fatalf("OAuth request silently reused token auth: err=%v connect=%v", err, seenConnect)
			}
		})
	}
}

func TestAdoptGitLabTokenPreservesAuthConfig(t *testing.T) {
	_, localDir := setupAdoptWorkspace(t)
	seen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workspaces/ws_123/integrations/gitlab/adopt" {
			http.Error(w, "unexpected path", 404)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = body["providerConfigKey"] == "gitlab-pat"
		_, _ = w.Write([]byte(`{"ok":true,"connectionId":"adopted-token"}`))
	}))
	defer server.Close()
	pointAdoptCloudCredentials(t, server.URL)
	var stdout bytes.Buffer
	err := run([]string{"integration", "adopt", "gitlab-pat", "--connection-id", "adopted-token", "--workspace", "demo", "--cloud-api-url", server.URL, "--yes"}, strings.NewReader(""), &stdout, &stdout)
	if err != nil || !seen || loadSavedConnection(localDir, "gitlab").ConnectionID != "adopted-token" {
		t.Fatalf("adopt lost auth config/canonical identity: err=%v seen=%v output=%s", err, seen, stdout.String())
	}
}

func TestSetupPreservesGitLabGroupTokenSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	localDir := t.TempDir()
	seenToken := false
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces":
			_, _ = w.Write([]byte(`{"workspaceId":"ws_123","name":"demo"}`))
		case "/api/v1/workspaces/ws_123/resolve":
			_, _ = w.Write([]byte(`{"cloudWorkspaceId":"ws_123","relayfileWorkspaceId":"ws_123","provisioned":true}`))
		case "/api/v1/workspaces/ws_123/relayfile/delegated-token":
			writeDelegatedBundleResponse(t, w, server.URL, "ws_123", "rf_join", "refresh_join")
		case "/api/v1/workspaces/ws_123/integrations/connect-session":
			var body cloudConnectSessionRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			seenToken = len(body.AllowedIntegrations) == 1 && body.AllowedIntegrations[0] == "gitlab-group-token"
			_, _ = w.Write([]byte(`{"connectionId":"token-connection","backend":"nango","backendIntegrationId":"gitlab-group-token"}`))
		case "/api/v1/workspaces/ws_123/integrations/gitlab/status":
			_, _ = w.Write([]byte(`{"ready":true,"configKey":"gitlab-group-token","currentConnectionId":"token-connection","connectionMatched":true}`))
		default:
			http.Error(w, "unexpected path", 404)
		}
	}))
	defer server.Close()
	var stdout bytes.Buffer
	err := run([]string{"setup", "--cloud-api-url", server.URL, "--cloud-token", "cloud-token", "--workspace", "demo", "--provider", "gitlab-group-token", "--local-dir", localDir, "--no-open", "--skip-mount"}, strings.NewReader(""), &stdout, &stdout)
	if err != nil || !seenToken || loadSavedConnection(localDir, "gitlab").ConnectionID != "token-connection" {
		t.Fatalf("setup lost the auth key or canonical state: err=%v token=%v output=%s", err, seenToken, stdout.String())
	}
}

func TestConnectSessionErrorsAreActionableAndBounded(t *testing.T) {
	for _, tc := range []struct {
		name, payload, want string
		status              int
		invalidate          bool
	}{
		{"unsupported400", `{"error":"unknown_provider","message":"private server detail","token":"never-print-me"}`, "integration available --refresh", 400, true},
		{"unsupported409", `{"code":"unknown_provider","message":"private server detail"}`, "integration available --refresh", 409, true},
		{"alreadyConnected", `{"error":"already_connected","message":"private server detail"}`, "Disconnect", 409, false},
		{"lifecycle", `{"error":"lifecycle_conflict","message":"private server detail"}`, "lifecycle state", 409, false},
		{"nearMatch", `{"error":"unknown_provider_suffix","message":"private server detail"}`, "lifecycle state", 409, false},
		{"upstreamUnknown", `{"error":"upstream_unavailable","message":"private server detail"}`, "Retry", 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			clearRelayfileEnv(t)
			if err := ensureConfigDir(); err != nil {
				t.Fatal(err)
			}
			if err := writeIntegrationCatalogCache(integrationCatalogCacheEntry{APIURL: "https://cloud.test", Providers: []integrationCatalogEntry{{ID: "gitlab"}}}); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.payload))
			}))
			defer server.Close()
			var stdout bytes.Buffer
			err := connectCloudIntegration(server.URL, "ws_123", "cloud-token", "gitlab-pat", "nango", t.TempDir(), time.Second, false, &stdout)
			var apiErr *apiError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status {
				t.Fatalf("typed HTTP status lost: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error()+stdout.String(), "private server detail") || strings.Contains(err.Error()+stdout.String(), "never-print-me") {
				t.Fatalf("unexpected error/output: %v\n%s", err, stdout.String())
			}
			_, statErr := os.Stat(integrationCatalogCachePath(""))
			if os.IsNotExist(statErr) != tc.invalidate {
				t.Fatalf("catalog invalidation=%v, want %v", os.IsNotExist(statErr), tc.invalidate)
			}
		})
	}
}
