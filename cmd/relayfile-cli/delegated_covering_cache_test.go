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
		RelayfileRefreshToken:   "refresh_fixture", RelayfileRefreshTokenExpiry: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339), RelayauthURL: server + "/auth",
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
	bundle, path, marker := setupCoveringCacheTest(t, server.URL, defaultJoinScopes)
	// A warm usable grant needs no refresh expiry metadata until renewal.
	bundle.RelayfileRefreshTokenExpiry = ""
	if err := delegatedauth.SaveAtomic(path, bundle); err != nil {
		t.Fatal(err)
	}
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
				bundle.RelayfileRefreshTokenExpiry = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
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

func TestCoveringCacheRejectsUnsafeRefreshBeforeDataPlane(t *testing.T) {
	var reads, refreshes atomic.Int32
	dataPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reads.Add(1); w.WriteHeader(401) }))
	defer dataPlane.Close()
	otherOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { refreshes.Add(1); w.WriteHeader(500) }))
	defer otherOrigin.Close()
	bundle, path, _ := setupCoveringCacheTest(t, dataPlane.URL, defaultJoinScopes)
	bundle.RefreshURL = otherOrigin.URL + "/auth/v1/tokens/refresh"
	if err := delegatedauth.SaveAtomic(path, bundle); err != nil {
		t.Fatal(err)
	}
	var out, notices bytes.Buffer
	if err := runRead([]string{"Default", "/slack/test.json"}, &out, &notices); err == nil {
		t.Fatal("unsafe refresh origin was selected")
	}
	if reads.Load() != 0 || refreshes.Load() != 0 {
		t.Fatalf("untrusted candidate reached network: reads=%d refreshes=%d", reads.Load(), refreshes.Load())
	}
}

func TestCoveringCacheDoesNotRescueMissingExplicitFile(t *testing.T) {
	_, _, marker := setupCoveringCacheTest(t, "https://relayfile.test", defaultJoinScopes)
	explicit := filepath.Join(t.TempDir(), "missing-explicit.json")
	t.Setenv("RELAYFILE_DELEGATED_CREDENTIALS_FILE", explicit)
	var output, notices bytes.Buffer
	err := runRead([]string{"Default", "/slack/test.json"}, &output, &notices)
	if err == nil || !strings.Contains(err.Error(), "missing-explicit.json") {
		t.Fatalf("missing explicit credential file fell back to a covering grant: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("missing explicit credential file triggered bootstrap")
	}
}

func TestReadRenewsExpiredCatalogCoveringCacheWithoutBootstrap(t *testing.T) {
	var reads, refreshes atomic.Int32
	var rotated string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/v1/tokens/refresh":
			if r.Method != "POST" {
				t.Error("SDK refresh must POST")
			}
			var body map[string]string
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["refreshToken"] != "refresh_fixture" {
				t.Error("refresh changed its existing grant request")
			}
			refreshes.Add(1)
			_ = json.NewEncoder(w).Encode(delegatedauth.TokenPair{AccessToken: rotated, RefreshToken: "cold_rotated", AccessTokenExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339), RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)})
		case "/v1/workspaces/rw_cover/fs/file":
			reads.Add(1)
			if refreshes.Load() != 1 || r.Header.Get("Authorization") != "Bearer "+rotated {
				t.Error("read did not wait for SDK rotation")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"content": "cold cached read succeeded", "encoding": "utf-8"})
		default:
			t.Errorf("unexpected network endpoint %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	bundle, path, marker := setupCoveringCacheTest(t, server.URL, defaultJoinScopes)
	bundle.RelayfileToken = coveringCacheTestToken("rw_cover", bundle.RelayfileScopes, time.Now().Add(-time.Hour))
	bundle.RelayfileTokenExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	rotated = coveringCacheTestToken("rw_cover", bundle.RelayfileScopes, time.Now().Add(time.Hour))
	if err := delegatedauth.SaveAtomic(path, bundle); err != nil {
		t.Fatal(err)
	}
	var output, notices bytes.Buffer
	if err := runRead([]string{"Default", "/slack/test.json"}, &output, &notices); err != nil {
		t.Fatalf("refreshable expired grant must avoid legacy bootstrap: %v", err)
	}
	if reads.Load() != 1 || refreshes.Load() != 1 || !strings.Contains(output.String(), "cold cached read succeeded") {
		t.Fatalf("cold refresh/read counts: refresh=%d read=%d", refreshes.Load(), reads.Load())
	}
	loaded, err := delegatedauth.Load(path)
	if err != nil || loaded.BearerToken() != rotated || loaded.RotationToken() != "cold_rotated" {
		t.Fatal("SDK did not rotate the original grant file")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("cold read invoked unavailable legacy workspace")
	}
	if _, err := os.Stat(delegatedCredentialsPathForRequest("Default", defaultInspectScopes)); !os.IsNotExist(err) {
		t.Fatal("cold read copied credentials into a new scope shard")
	}
}

func TestReadRejectsUnusableRenewalCoveringGrantBeforeAnyFallback(t *testing.T) {
	for _, name := range []string{"missing refresh", "missing refresh expiry", "expired refresh", "malformed refresh expiry", "expired delegation", "malformed delegation", "wrong workspace", "insufficient JWT", "unsafe origin", "near expiry without refresh proof"} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
			defer server.Close()
			bundle, path, marker := setupCoveringCacheTest(t, server.URL, defaultJoinScopes)
			bundle.RelayfileToken = coveringCacheTestToken("rw_cover", bundle.RelayfileScopes, time.Now().Add(-time.Hour))
			bundle.RelayfileTokenExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
			switch name {
			case "missing refresh":
				bundle.RelayfileRefreshToken = ""
			case "missing refresh expiry":
				bundle.RelayfileRefreshTokenExpiry = ""
			case "expired refresh":
				bundle.RelayfileRefreshTokenExpiry = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
			case "malformed refresh expiry":
				bundle.RelayfileRefreshTokenExpiry = "not-a-date"
			case "expired delegation":
				bundle.DelegationNotAfter = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
			case "malformed delegation":
				bundle.DelegationNotAfter = "not-a-date"
			case "wrong workspace":
				bundle.RelayfileToken = coveringCacheTestToken("rw_other", bundle.RelayfileScopes, time.Now().Add(-time.Hour))
			case "insufficient JWT":
				bundle.RelayfileToken = coveringCacheTestToken("rw_cover", []string{"relayfile:fs:write:*"}, time.Now().Add(-time.Hour))
			case "unsafe origin":
				bundle.RefreshURL = "https://other.example/v1/tokens/refresh"
			case "near expiry without refresh proof":
				bundle.RelayfileToken = coveringCacheTestToken("rw_cover", bundle.RelayfileScopes, time.Now().Add(time.Minute))
				bundle.RelayfileTokenExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
				bundle.RelayfileRefreshTokenExpiry = ""
			}
			if err := delegatedauth.SaveAtomic(path, bundle); err != nil {
				t.Fatal(err)
			}
			// A saved direct token must not hide covering-cache validation errors.
			if err := saveCredentials(credentials{Server: server.URL, Token: coveringCacheTestToken("rw_cover", defaultInspectScopes, time.Now().Add(time.Hour))}); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			var output, notices bytes.Buffer
			if err := runRead([]string{"Default", "/slack/test.json"}, &output, &notices); err == nil {
				t.Fatal("invalid renewal candidate was accepted")
			}
			if requests.Load() != 0 {
				t.Fatalf("network request before rejection: %d", requests.Load())
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("legacy bootstrap ran for invalid renewal grant")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("invalid original grant changed")
			}
		})
	}
}

func TestReadRejectsUnsafeCoveringRefreshWithoutLoginFallback(t *testing.T) {
	for _, name := range []string{"trailing slash", "empty metadata remint escalation", "future not-before"} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
			defer server.Close()
			bundle, path, marker := setupCoveringCacheTest(t, server.URL, defaultJoinScopes)
			switch name {
			case "trailing slash":
				bundle.RefreshURL += "/"
			case "empty metadata remint escalation":
				bundle.Scopes = nil
				bundle.RelayfileScopes = nil
				bundle.RelayfileToken = coveringCacheTestToken("rw_cover", defaultInspectScopes, time.Now().Add(time.Hour))
			case "future not-before":
				claims, _ := parseJWTClaims(bundle.BearerToken())
				claims["nbf"] = time.Now().Add(time.Hour).Unix()
				data, _ := json.Marshal(claims)
				parts := strings.Split(bundle.RelayfileToken, ".")
				parts[1] = base64.RawURLEncoding.EncodeToString(data)
				bundle.RelayfileToken = strings.Join(parts, ".")
			}
			if err := delegatedauth.SaveAtomic(path, bundle); err != nil {
				t.Fatal(err)
			}
			if err := saveCredentials(credentials{Server: server.URL, Token: coveringCacheTestToken("rw_cover", defaultInspectScopes, time.Now().Add(time.Hour))}); err != nil {
				t.Fatal(err)
			}
			var output, notices bytes.Buffer
			if err := runRead([]string{"Default", "/slack/test.json"}, &output, &notices); err == nil {
				t.Fatal("unsafe covering cache was accepted")
			}
			if requests.Load() != 0 {
				t.Fatalf("network requests despite validation failure: %d", requests.Load())
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("legacy bootstrap ran")
			}
		})
	}
}

func TestReadCoveringRefreshDoesNotForwardTokenAcrossRedirect(t *testing.T) {
	var leaked, refreshes atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1); w.WriteHeader(500) }))
	defer sink.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/v1/tokens/refresh" {
			refreshes.Add(1)
			http.Redirect(w, r, sink.URL+"/stolen", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(500)
	}))
	defer server.Close()
	bundle, path, _ := setupCoveringCacheTest(t, server.URL, defaultJoinScopes)
	bundle.RelayfileToken = coveringCacheTestToken("rw_cover", bundle.RelayfileScopes, time.Now().Add(time.Minute))
	bundle.RelayfileTokenExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	if err := delegatedauth.SaveAtomic(path, bundle); err != nil {
		t.Fatal(err)
	}
	var output, notices bytes.Buffer
	if err := runRead([]string{"Default", "/slack/test.json"}, &output, &notices); err == nil {
		t.Fatal("redirected refresh unexpectedly succeeded")
	}
	if refreshes.Load() != 1 || leaked.Load() != 0 {
		t.Fatalf("refresh=%d leaked=%d", refreshes.Load(), leaked.Load())
	}
	after, err := delegatedauth.Load(path)
	if err != nil || after.RotationToken() != bundle.RotationToken() {
		t.Fatal("failed refresh changed original grant")
	}
}

func TestReadCovering401ReloadRemainsBoundToCatalogServer(t *testing.T) {
	var redirectedRequests, originalReads atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedRequests.Add(1)
		if r.URL.Path == "/auth/v1/tokens/refresh" {
			_ = json.NewEncoder(w).Encode(delegatedauth.TokenPair{
				AccessToken:  coveringCacheTestToken("rw_cover", []string{"relayfile:fs:read:*", "relayfile:fs:write:*", "relayfile:ops:read:*", "relayfile:sync:trigger:*"}, time.Now().Add(time.Hour)),
				RefreshToken: "wrong_server_rotation", AccessTokenExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": "wrong server accepted", "encoding": "utf-8"})
	}))
	defer other.Close()
	var bundle delegatedauth.Bundle
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originalReads.Add(1)
		// Simulate another local credential writer replacing the cached file
		// between its initial selection and the normal 401-triggered reload.
		replaced := bundle
		replaced.RelayfileURL = other.URL
		replaced.RelayauthURL = other.URL + "/auth"
		replaced.RefreshURL = other.URL + "/auth/v1/tokens/refresh"
		if err := delegatedauth.SaveAtomic(path, replaced); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	bundle, path, _ = setupCoveringCacheTest(t, server.URL, defaultJoinScopes)
	var output, notices bytes.Buffer
	if err := runRead([]string{"Default", "/slack/test.json"}, &output, &notices); err == nil {
		t.Error("401 reload switched a covering grant away from the catalog server")
	}
	if originalReads.Load() != 1 || redirectedRequests.Load() != 0 {
		t.Fatalf("original requests=%d other-server requests=%d; expected one 401 and no refresh/retry to another server", originalReads.Load(), redirectedRequests.Load())
	}
	record, ok := workspaceRecordByName("Default")
	if !ok || record.Server != server.URL {
		t.Fatal("401 reload changed the catalog server")
	}
}
