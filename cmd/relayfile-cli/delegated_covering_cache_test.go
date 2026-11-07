package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/delegatedauth"
)

func coveringCacheTestToken(workspace string, scopes []string, expires time.Time) string {
	claims, _ := parseJWTClaims(testJWTWithWorkspaceAndScopes(workspace, "cached-test", scopes))
	claims["iat"] = time.Now().Add(-time.Minute).Unix()
	claims["exp"] = expires.Unix()
	payload, _ := json.Marshal(claims)
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".fixture"
}

func setupCoveringCacheTest(t *testing.T, server string, catalogScopes []string) (delegatedauth.Bundle, string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveWorkspaceCatalog(workspaceCatalog{Default: "cloud_cover", Workspaces: []workspaceRecord{{Name: "Default", ID: "cloud_cover", RelayWorkspaceID: "rw_cover", Server: server, Scopes: catalogScopes}}}); err != nil {
		t.Fatal(err)
	}
	writeAgentRelayCloudAuthForTest(t, server, "cloud_fixture")
	marker := filepath.Join(t.TempDir(), "legacy-bootstrap-called")
	installFakeAgentRelay(t, fmt.Sprintf("printf called > %q\necho 'No active Agent Relay workspace found' >&2\nexit 1", marker))
	grants := []string{"relayfile:fs:read:*", "relayfile:fs:write:*", "relayfile:ops:read:*", "relayfile:sync:trigger:*"}
	bundle := delegatedauth.Bundle{
		RelayfileURL: server, RelayfileWorkspaceID: "rw_cover",
		RelayfileToken:          coveringCacheTestToken("rw_cover", grants, time.Now().Add(time.Hour)),
		RelayfileTokenExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RelayfileRefreshToken:   "refresh_fixture", RelayauthURL: server + "/auth",
		RefreshURL: server + "/auth/v1/tokens/refresh",
		Scopes:     append([]string(nil), defaultJoinScopes...), RelayfileScopes: grants,
	}
	path := delegatedCredentialsPathForRequest("Default", defaultJoinScopes)
	if err := delegatedauth.SaveAtomic(path, bundle); err != nil {
		t.Fatal(err)
	}
	return bundle, path, marker
}

func TestReadUsesCatalogCoveringCacheWithoutLegacyBootstrap(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "GET" || r.URL.Path != "/v1/workspaces/rw_cover/fs/file" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": "cached read succeeded", "encoding": "utf-8"})
	}))
	defer server.Close()
	_, path, marker := setupCoveringCacheTest(t, server.URL, defaultJoinScopes)
	before, _ := os.ReadFile(path)
	requestedPath := delegatedCredentialsPathForRequest("Default", defaultInspectScopes)
	var output, notices bytes.Buffer
	if err := runRead([]string{"Default", "/slack/test.json"}, &output, &notices); err != nil {
		t.Fatalf("valid covering grant must avoid unavailable legacy workspace bootstrap: %v", err)
	}
	if !strings.Contains(output.String(), "cached read succeeded") || requests.Load() != 1 {
		t.Fatalf("read did not use the existing grant: requests=%d output=%q", requests.Load(), output.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("legacy workspace bootstrap ran despite a valid covering grant")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("fresh covering grant was rewritten")
	}
	if _, err := os.Stat(requestedPath); !os.IsNotExist(err) {
		t.Fatal("covering grant was copied into another scope shard")
	}
}

func TestCoveringCacheRejectsUnprovenGrants(t *testing.T) {
	for _, name := range []string{"wrong bundle workspace", "wrong JWT workspace", "insufficient JWT scopes", "metadata scope escalation", "expired JWT", "missing JWT expiry", "malformed JWT", "malformed JSON", "wrong server", "unsafe auth URL", "wrong refresh origin", "wrong refresh path", "refresh query"} {
		t.Run(name, func(t *testing.T) {
			bundle, path, _ := setupCoveringCacheTest(t, "https://relayfile.test", defaultJoinScopes)
			switch name {
			case "wrong bundle workspace":
				bundle.RelayfileWorkspaceID = "rw_other"
			case "wrong JWT workspace":
				bundle.RelayfileToken = coveringCacheTestToken("rw_other", bundle.RelayfileScopes, time.Now().Add(time.Hour))
			case "insufficient JWT scopes":
				bundle.RelayfileToken = coveringCacheTestToken("rw_cover", []string{"relayfile:fs:write:*"}, time.Now().Add(time.Hour))
			case "metadata scope escalation":
				bundle.Scopes = append(bundle.Scopes, "admin:all")
			case "expired JWT":
				bundle.RelayfileToken = coveringCacheTestToken("rw_cover", bundle.RelayfileScopes, time.Now().Add(-time.Hour))
			case "missing JWT expiry":
				bundle.RelayfileToken = testJWTWithWorkspaceAndScopes("rw_cover", "cached-test", bundle.RelayfileScopes)
			case "malformed JWT":
				bundle.RelayfileToken = "not-a-jwt"
			case "wrong server":
				bundle.RelayfileURL = "https://another.example"
			case "unsafe auth URL":
				bundle.RelayauthURL = "http://public.example"
				bundle.RefreshURL = "http://public.example/v1/tokens/refresh"
			case "wrong refresh origin":
				bundle.RefreshURL = "https://other.example/v1/tokens/refresh"
			case "wrong refresh path":
				bundle.RefreshURL = bundle.RelayauthURL + "/another"
			case "refresh query":
				bundle.RefreshURL += "?redirect=unexpected"
			}
			if name == "malformed JSON" {
				if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := delegatedauth.SaveAtomic(path, bundle); err != nil {
				t.Fatal(err)
			}
			loaded, _, err := loadOrBootstrapDelegatedCredentials("Default", defaultInspectScopes)
			if err == nil || loaded.BearerToken() != "" {
				t.Fatalf("unproven grant selected for %s", name)
			}
		})
	}
}

func TestCoveringCacheRejectsAmbiguousCandidates(t *testing.T) {
	catalogScopes := []string{"fs:read", "ops:read"}
	bundle, first, marker := setupCoveringCacheTest(t, "https://relayfile.test", catalogScopes)
	second := delegatedCredentialsPathForRequest("Default", catalogScopes)
	if first == second {
		t.Fatal("fixture needs two known scope shards")
	}
	if err := delegatedauth.SaveAtomic(second, bundle); err != nil {
		t.Fatal(err)
	}
	_, _, err := loadOrBootstrapDelegatedCredentials("Default", defaultInspectScopes)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous covering grants accepted: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("ambiguous cache triggered legacy bootstrap")
	}
}

func TestCoveringCacheKeepsExplicitFileOverride(t *testing.T) {
	_, _, _ = setupCoveringCacheTest(t, "https://relayfile.test", defaultJoinScopes)
	path := filepath.Join(t.TempDir(), "explicit.json")
	bundle := delegatedauth.Bundle{RelayfileURL: "https://explicit.test", RelayfileWorkspaceID: "rw_cover", AccessToken: "explicit-opaque-token"}
	if err := delegatedauth.SaveAtomic(path, bundle); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELAYFILE_DELEGATED_CREDENTIALS_FILE", path)
	loaded, actualPath, err := loadOrBootstrapDelegatedCredentials("Default", defaultInspectScopes)
	if err != nil || loaded.BearerToken() != bundle.BearerToken() || actualPath != path {
		t.Fatalf("explicit file override changed: err=%v path=%q", err, actualPath)
	}
}

func TestCoveringCacheUsesExistingForcedRefreshOnOriginalPath(t *testing.T) {
	var reads, refreshes atomic.Int32
	var rotated string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workspaces/rw_cover/fs/file":
			if reads.Add(1) == 1 {
				w.WriteHeader(401)
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+rotated {
				t.Error("retry did not use rotated fixture token")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"content": "after refresh", "encoding": "utf-8"})
		case "/auth/v1/tokens/refresh":
			refreshes.Add(1)
			_ = json.NewEncoder(w).Encode(delegatedauth.TokenPair{AccessToken: rotated, RefreshToken: "rotated_fixture", AccessTokenExpiresAt: time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339), RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)})
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	bundle, path, marker := setupCoveringCacheTest(t, server.URL, defaultJoinScopes)
	rotated = coveringCacheTestToken("rw_cover", bundle.RelayfileScopes, time.Now().Add(2*time.Hour))
	var output, notices bytes.Buffer
	if err := runRead([]string{"Default", "/slack/test.json"}, &output, &notices); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != 2 || refreshes.Load() != 1 || !strings.Contains(output.String(), "after refresh") {
		t.Fatalf("forced refresh failed: reads=%d refreshes=%d", reads.Load(), refreshes.Load())
	}
	loaded, err := delegatedauth.Load(path)
	if err != nil || loaded.BearerToken() != rotated {
		t.Fatal("rotation did not persist to the original grant path")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("forced refresh invoked legacy workspace bootstrap")
	}
	if _, err := os.Stat(delegatedCredentialsPathForRequest("Default", defaultInspectScopes)); !os.IsNotExist(err) {
		t.Fatal("forced refresh copied credentials to the read shard")
	}
}
