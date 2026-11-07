package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

const (
	testIntrospectSecret   = "test-introspect-secret-value"
	testIntrospectAudience = "https://files.example.test"
	testUserAccessToken    = "sh_at_0123456789abcdefghijklmnopqrstuv"
)

// fakeRelaycontrol is a stand-in for relaycontrol's introspection endpoint.
type fakeRelaycontrol struct {
	server *httptest.Server
	calls  atomic.Int32

	mu      sync.Mutex
	handler func(w http.ResponseWriter, r *http.Request, body map[string]any)
	last    capturedIntrospection
}

type capturedIntrospection struct {
	method        string
	path          string
	authorization string
	contentType   string
	rawBody       []byte
	body          map[string]any
}

func newFakeRelaycontrol(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body map[string]any)) *fakeRelaycontrol {
	t.Helper()
	fake := &fakeRelaycontrol{handler: handler}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		fake.mu.Lock()
		fake.last = capturedIntrospection{
			method:        r.Method,
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			contentType:   r.Header.Get("Content-Type"),
			rawBody:       raw,
			body:          body,
		}
		h := fake.handler
		fake.mu.Unlock()
		h(w, r, body)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeRelaycontrol) setHandler(handler func(w http.ResponseWriter, r *http.Request, body map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = handler
}

func (f *fakeRelaycontrol) lastRequest() capturedIntrospection {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

func (f *fakeRelaycontrol) endpoint() string {
	return f.server.URL + IntrospectionPath
}

func activeIntrospection(now time.Time, overrides map[string]any) map[string]any {
	response := map[string]any{
		"active":         true,
		"sub":            "usr_123",
		"email":          "user@example.test",
		"principal_type": "desktop-user",
		"agent_name":     nil,
		"machine_id":     nil,
		"org":            "org_1",
		"workspace":      "ws_a",
		"scopes":         []string{"fs:read", "fs:write"},
		"aud":            testIntrospectAudience,
		"iat":            now.Add(-time.Minute).Unix(),
		"exp":            now.Add(time.Hour).Unix(),
		"token_id":       "tok_0123456789abcdef",
	}
	for key, value := range overrides {
		response[key] = value
	}
	return response
}

func respondJSON(response any) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}
}

func respondStatus(status int, body string) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func introspectionTestConfig(endpoint string) ServerConfig {
	return ServerConfig{
		AuthMode:              AuthModeIntrospection,
		IntrospectionURL:      endpoint,
		IntrospectionSecret:   testIntrospectSecret,
		IntrospectionAudience: testIntrospectAudience,
		IntrospectionTimeout:  2 * time.Second,
	}
}

func mustIntrospectionBearerVerifier(t *testing.T, cfg ServerConfig) *bearerVerifier {
	t.Helper()
	if err := normalizeAuthConfig(&cfg); err != nil {
		t.Fatalf("normalizeAuthConfig: %v", err)
	}
	return newBearerVerifier(cfg)
}

func TestIntrospectionHappyPathRequestShape(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	fake := newFakeRelaycontrol(t, respondJSON(activeIntrospection(now, nil)))
	verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))

	claims, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now)
	if authErr != nil {
		t.Fatalf("parseBearer returned auth error: %+v", authErr)
	}
	if fake.calls.Load() != 1 {
		t.Fatalf("expected one introspection call, got %d", fake.calls.Load())
	}
	got := fake.lastRequest()
	if got.method != http.MethodPost {
		t.Fatalf("method = %q, want POST", got.method)
	}
	if got.path != "/internal/v1/auth/introspect" {
		t.Fatalf("path = %q", got.path)
	}
	if got.authorization != "Bearer "+testIntrospectSecret {
		t.Fatalf("authorization header does not carry the service secret as a plain bearer")
	}
	if got.contentType != "application/json" {
		t.Fatalf("content-type = %q", got.contentType)
	}
	if len(got.body) != 2 {
		t.Fatalf("expected exactly token and aud in body, got keys %v", mapKeys(got.body))
	}
	if got.body["token"] != testUserAccessToken {
		t.Fatalf("body token does not match the presented bearer")
	}
	if got.body["aud"] != testIntrospectAudience {
		t.Fatalf("body aud = %v, want %q", got.body["aud"], testIntrospectAudience)
	}
	for _, forbidden := range []string{"workspace", "machine"} {
		if _, present := got.body[forbidden]; present {
			t.Fatalf("request body must not include %q", forbidden)
		}
	}

	if claims.WorkspaceID != "ws_a" || claims.AgentName != "usr_123" {
		t.Fatalf("unexpected claims: workspace=%q agent=%q", claims.WorkspaceID, claims.AgentName)
	}
	if claims.TokenID != "tok_0123456789abcdef" || claims.PrincipalType != "desktop-user" {
		t.Fatalf("unexpected token metadata: token_id=%q principal_type=%q", claims.TokenID, claims.PrincipalType)
	}
	if _, ok := claims.Scopes["fs:read"]; !ok {
		t.Fatalf("expected fs:read scope, got %v", claims.Scopes)
	}
}

func TestIntrospectionInactiveReasonsAreUnauthorized(t *testing.T) {
	t.Parallel()

	reasons := []string{
		"expired", "revoked", "unknown", "wrong-kind", "audience", "workspace",
		"machine_not_owned", "machine_unclaimed", "machine_workspace_mismatch",
		"some-future-reason",
	}
	for _, reason := range reasons {
		reason := reason
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			fake := newFakeRelaycontrol(t, respondJSON(map[string]any{"active": false, "reason": reason}))
			verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))

			_, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, time.Now().UTC())
			if authErr == nil || authErr.status != http.StatusUnauthorized || authErr.message != "token inactive" {
				t.Fatalf("expected 401 token inactive, got %+v", authErr)
			}
		})
	}
}

func TestIntrospectionClaimMapping(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	tests := []struct {
		name        string
		overrides   map[string]any
		wantStatus  int
		wantMessage string
		wantAgent   string
	}{
		{name: "null agent_name falls back to sub", overrides: nil, wantAgent: "usr_123"},
		{name: "agent_name preferred over sub", overrides: map[string]any{"agent_name": "MountSync"}, wantAgent: "MountSync"},
		{name: "empty agent_name falls back to sub", overrides: map[string]any{"agent_name": ""}, wantAgent: "usr_123"},
		{name: "audience compared as normalized origin", overrides: map[string]any{"aud": "HTTPS://Files.Example.Test:443/"}, wantAgent: "usr_123"},
		{name: "audience mismatch", overrides: map[string]any{"aud": "https://other.example.test"}, wantStatus: 401, wantMessage: "introspection audience mismatch"},
		{name: "audience missing", overrides: map[string]any{"aud": nil}, wantStatus: 401, wantMessage: "introspection audience mismatch"},
		{name: "audience not a string", overrides: map[string]any{"aud": []string{testIntrospectAudience}}, wantStatus: 401, wantMessage: "introspection audience mismatch"},
		{name: "exp in the past", overrides: map[string]any{"exp": now.Add(-time.Second).Unix()}, wantStatus: 401, wantMessage: "token expired"},
		{name: "exp equal to now", overrides: map[string]any{"exp": now.Unix()}, wantStatus: 401, wantMessage: "token expired"},
		{name: "exp not a number", overrides: map[string]any{"exp": "soon"}, wantStatus: 401, wantMessage: "invalid exp claim"},
		{name: "missing workspace", overrides: map[string]any{"workspace": ""}, wantStatus: 401, wantMessage: "missing workspace claim"},
		{name: "missing subject", overrides: map[string]any{"sub": "", "agent_name": nil}, wantStatus: 401, wantMessage: "missing subject claim"},
		{name: "no scopes", overrides: map[string]any{"scopes": []string{}}, wantStatus: 403, wantMessage: "no scopes granted"},
		{name: "unknown fields ignored", overrides: map[string]any{"future_field": map[string]any{"x": 1}}, wantAgent: "usr_123"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newFakeRelaycontrol(t, respondJSON(activeIntrospection(now, tt.overrides)))
			verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))

			claims, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now)
			if tt.wantStatus == 0 {
				if authErr != nil {
					t.Fatalf("unexpected auth error: %+v", authErr)
				}
				if claims.AgentName != tt.wantAgent {
					t.Fatalf("AgentName = %q, want %q", claims.AgentName, tt.wantAgent)
				}
				return
			}
			if authErr == nil || authErr.status != tt.wantStatus || authErr.message != tt.wantMessage {
				t.Fatalf("expected %d %q, got %+v", tt.wantStatus, tt.wantMessage, authErr)
			}
		})
	}
}

func TestIntrospectionUpstreamFailuresFailClosed(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	oversize := `{"active":true,"pad":"` + strings.Repeat("x", maxIntrospectionResponseBytes) + `"}`
	tests := []struct {
		name        string
		handler     func(http.ResponseWriter, *http.Request, map[string]any)
		wantMessage string
	}{
		{name: "500", handler: respondStatus(500, `{"active":true}`), wantMessage: "introspection unreachable"},
		{name: "502", handler: respondStatus(502, ``), wantMessage: "introspection unreachable"},
		{name: "401 service secret rejected", handler: respondStatus(401, `{"error":"unauthorized"}`), wantMessage: "introspection rejected service credential"},
		{name: "404 endpoint disabled", handler: respondStatus(404, `{"error":"not_found"}`), wantMessage: "introspection disabled or unreachable"},
		{name: "400 malformed request", handler: respondStatus(400, `{"error":"invalid_request"}`), wantMessage: "introspection request rejected"},
		{name: "201 unexpected success code", handler: respondStatus(201, mustJSON(activeIntrospection(now, nil))), wantMessage: "introspection unreachable"},
		{name: "non-JSON", handler: respondStatus(200, `<html>ok</html>`), wantMessage: "introspection unreachable"},
		{name: "array body", handler: respondStatus(200, `[{"active":true}]`), wantMessage: "introspection unreachable"},
		{name: "active as string", handler: respondJSON(activeIntrospection(now, map[string]any{"active": "true"})), wantMessage: "introspection unreachable"},
		{name: "active missing", handler: respondStatus(200, `{"sub":"usr_123","workspace":"ws_a"}`), wantMessage: "introspection unreachable"},
		{name: "null body", handler: respondStatus(200, `null`), wantMessage: "introspection unreachable"},
		{name: "trailing garbage", handler: respondStatus(200, mustJSON(activeIntrospection(now, nil))+`{}`), wantMessage: "introspection unreachable"},
		{name: "oversize body", handler: respondStatus(200, oversize), wantMessage: "introspection unreachable"},
		{
			name: "redirect",
			handler: func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
				http.Redirect(w, r, "/elsewhere", http.StatusFound)
			},
			wantMessage: "introspection unreachable",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var redirected atomic.Int32
			fake := newFakeRelaycontrol(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				if r.URL.Path == "/elsewhere" {
					redirected.Add(1)
					respondJSON(activeIntrospection(now, nil))(w, r, body)
					return
				}
				tt.handler(w, r, body)
			})
			verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))

			claims, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now)
			if authErr == nil {
				t.Fatalf("expected fail-closed 503, got claims for workspace %q", claims.WorkspaceID)
			}
			if authErr.status != http.StatusServiceUnavailable || authErr.code != "unavailable" || authErr.message != tt.wantMessage {
				t.Fatalf("expected 503 unavailable %q, got %+v", tt.wantMessage, authErr)
			}
			if redirected.Load() != 0 {
				t.Fatalf("redirect was followed")
			}
		})
	}
}

func TestIntrospectionTimeoutFailsClosed(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	fake := newFakeRelaycontrol(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
			return
		}
		respondJSON(activeIntrospection(now, nil))(w, r, body)
	})
	cfg := introspectionTestConfig(fake.endpoint())
	cfg.IntrospectionTimeout = 100 * time.Millisecond
	verifier := mustIntrospectionBearerVerifier(t, cfg)

	start := time.Now()
	_, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now)
	if authErr == nil || authErr.status != http.StatusServiceUnavailable || authErr.message != "introspection unreachable" {
		t.Fatalf("expected 503 introspection unreachable, got %+v", authErr)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("timeout not enforced: took %s", elapsed)
	}
}

func TestIntrospectionUnreachableEndpointFailsClosed(t *testing.T) {
	t.Parallel()

	fake := newFakeRelaycontrol(t, respondJSON(map[string]any{"active": true}))
	endpoint := fake.endpoint()
	fake.server.Close()
	verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(endpoint))

	_, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, time.Now().UTC())
	if authErr == nil || authErr.status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %+v", authErr)
	}
}

func TestIntrospectionRejectsUnsendableTokensWithoutCalling(t *testing.T) {
	t.Parallel()

	fake := newFakeRelaycontrol(t, respondJSON(activeIntrospection(time.Now().UTC(), nil)))
	verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))

	tokens := map[string]string{
		"oversize":       "sh_at_" + strings.Repeat("a", maxIntrospectionTokenBytes),
		"interior space": "sh_at_abc def",
		"tab":            "sh_at_abc\tdef",
		"control char":   "sh_at_abc\x01def",
		"DEL":            "sh_at_abc\x7fdef",
		"invalid utf8":   "sh_at_abc\xffdef",
	}
	for name, token := range tokens {
		_, authErr := parseBearer("Bearer "+token, verifier, time.Now().UTC())
		if authErr == nil || authErr.status != http.StatusUnauthorized || authErr.message != "invalid token" {
			t.Fatalf("%s: expected 401 invalid token, got %+v", name, authErr)
		}
	}
	// A token of exactly the limit is still sent.
	exact := "sh_at_" + strings.Repeat("a", maxIntrospectionTokenBytes-len("sh_at_"))
	if _, authErr := parseBearer("Bearer "+exact, verifier, time.Now().UTC()); authErr != nil {
		t.Fatalf("token at the size limit rejected: %+v", authErr)
	}
	if got := fake.calls.Load(); got != 1 {
		t.Fatalf("expected exactly one upstream call (the at-limit token), got %d", got)
	}
}

func TestIntrospectionCache(t *testing.T) {
	t.Parallel()

	t.Run("hit within TTL makes one upstream call", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		fake := newFakeRelaycontrol(t, respondJSON(activeIntrospection(now, nil)))
		verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))
		for i := 0; i < 3; i++ {
			if _, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now.Add(time.Duration(i)*10*time.Second)); authErr != nil {
				t.Fatalf("pass %d: %+v", i, authErr)
			}
		}
		if got := fake.calls.Load(); got != 1 {
			t.Fatalf("expected 1 upstream call, got %d", got)
		}
	})

	t.Run("miss after TTL refetches", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		fake := newFakeRelaycontrol(t, respondJSON(activeIntrospection(now, nil)))
		cfg := introspectionTestConfig(fake.endpoint())
		cfg.IntrospectionCacheTTL = 30 * time.Second
		verifier := mustIntrospectionBearerVerifier(t, cfg)
		for _, offset := range []time.Duration{0, 29 * time.Second, 30 * time.Second} {
			if _, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now.Add(offset)); authErr != nil {
				t.Fatalf("offset %s: %+v", offset, authErr)
			}
		}
		if got := fake.calls.Load(); got != 2 {
			t.Fatalf("expected 2 upstream calls, got %d", got)
		}
	})

	t.Run("TTL above 60s is capped", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		fake := newFakeRelaycontrol(t, respondJSON(activeIntrospection(now, nil)))
		cfg := introspectionTestConfig(fake.endpoint())
		cfg.IntrospectionCacheTTL = 10 * time.Minute
		verifier := mustIntrospectionBearerVerifier(t, cfg)
		for _, offset := range []time.Duration{0, 59 * time.Second, 60 * time.Second} {
			if _, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now.Add(offset)); authErr != nil {
				t.Fatalf("offset %s: %+v", offset, authErr)
			}
		}
		if got := fake.calls.Load(); got != 2 {
			t.Fatalf("expected 2 upstream calls with 60s cap, got %d", got)
		}
	})

	t.Run("entry capped by exp when exp is sooner than TTL", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		exp := now.Add(10 * time.Second).Unix()
		fake := newFakeRelaycontrol(t, respondJSON(activeIntrospection(now, map[string]any{"exp": exp})))
		verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))
		if _, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now); authErr != nil {
			t.Fatalf("first call: %+v", authErr)
		}
		// After exp the cached entry must not be served; the refetch sees
		// the same (now past) exp and rejects.
		_, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, time.Unix(exp, 0).Add(time.Second))
		if authErr == nil || authErr.status != http.StatusUnauthorized || authErr.message != "token expired" {
			t.Fatalf("expected 401 token expired after exp, got %+v", authErr)
		}
		if got := fake.calls.Load(); got != 2 {
			t.Fatalf("expected the cache entry to lapse at exp (2 calls), got %d", got)
		}
	})

	t.Run("replay after revocation persists until TTL then rejects", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		fake := newFakeRelaycontrol(t, respondJSON(activeIntrospection(now, nil)))
		verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))
		if _, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now); authErr != nil {
			t.Fatalf("first call: %+v", authErr)
		}
		fake.setHandler(respondJSON(map[string]any{"active": false, "reason": "revoked"}))

		if _, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now.Add(59*time.Second)); authErr != nil {
			t.Fatalf("expected cached accept within TTL, got %+v", authErr)
		}
		_, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now.Add(60*time.Second))
		if authErr == nil || authErr.status != http.StatusUnauthorized || authErr.message != "token inactive" {
			t.Fatalf("expected 401 token inactive after TTL, got %+v", authErr)
		}
		// Inactive verdicts are not cached.
		_, _ = parseBearer("Bearer "+testUserAccessToken, verifier, now.Add(61*time.Second))
		if got := fake.calls.Load(); got != 3 {
			t.Fatalf("expected 3 upstream calls, got %d", got)
		}
	})

	t.Run("negative TTL disables the cache", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		fake := newFakeRelaycontrol(t, respondJSON(activeIntrospection(now, nil)))
		cfg := introspectionTestConfig(fake.endpoint())
		cfg.IntrospectionCacheTTL = -1
		verifier := mustIntrospectionBearerVerifier(t, cfg)
		for i := 0; i < 3; i++ {
			if _, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now); authErr != nil {
				t.Fatalf("pass %d: %+v", i, authErr)
			}
		}
		if got := fake.calls.Load(); got != 3 {
			t.Fatalf("expected 3 upstream calls with cache disabled, got %d", got)
		}
	})

	t.Run("errors are not cached", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		fake := newFakeRelaycontrol(t, respondStatus(500, ``))
		verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))
		if _, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now); authErr == nil {
			t.Fatalf("expected 503")
		}
		fake.setHandler(respondJSON(activeIntrospection(now, nil)))
		if _, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, now); authErr != nil {
			t.Fatalf("expected accept after recovery, got %+v", authErr)
		}
		if got := fake.calls.Load(); got != 2 {
			t.Fatalf("expected 2 upstream calls, got %d", got)
		}
	})

	t.Run("bounded: full cache of live entries serves fresh verdicts uncached", func(t *testing.T) {
		t.Parallel()
		now := time.Now().UTC()
		fake := newFakeRelaycontrol(t, respondJSON(activeIntrospection(now, nil)))
		verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))
		verifier.introspection.maxEntries = 2

		tokens := []string{"sh_at_one", "sh_at_two", "sh_at_three"}
		for _, token := range tokens {
			if _, authErr := parseBearer("Bearer "+token, verifier, now); authErr != nil {
				t.Fatalf("%s: %+v", token, authErr)
			}
		}
		if got := len(verifier.introspection.cache); got != 2 {
			t.Fatalf("expected cache capped at 2 entries, got %d", got)
		}
		// The uncached third token still works, at the cost of a call.
		if _, authErr := parseBearer("Bearer sh_at_three", verifier, now); authErr != nil {
			t.Fatalf("uncached token rejected: %+v", authErr)
		}
		if got := fake.calls.Load(); got != 4 {
			t.Fatalf("expected 4 upstream calls, got %d", got)
		}
		// Once the live entries expire, inserting sweeps them.
		later := now.Add(2 * time.Minute)
		if _, authErr := parseBearer("Bearer sh_at_four", verifier, later); authErr != nil {
			t.Fatalf("sh_at_four: %+v", authErr)
		}
		if got := len(verifier.introspection.cache); got != 1 {
			t.Fatalf("expected expired entries swept (1 entry), got %d", got)
		}
	})
}

func TestIntrospectionSecretRotationFailsClosedAsUnavailable(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	fake := newFakeRelaycontrol(t, respondJSON(activeIntrospection(now, nil)))
	cfg := introspectionTestConfig(fake.endpoint())
	cfg.IntrospectionCacheTTL = -1
	server, err := NewServerWithConfig(relayfile.NewStore(), cfg)
	if err != nil {
		t.Fatalf("NewServerWithConfig: %v", err)
	}
	if rec := serveWithBearer(server, http.MethodGet, "/v1/workspaces/ws_a/fs/tree?path=/", testUserAccessToken); rec.Code != http.StatusOK {
		t.Fatalf("expected 200 before rotation, got %d: %s", rec.Code, rec.Body.String())
	}

	// relaycontrol rotated RELAYCONTROL_INTROSPECT_SECRET; ours is now stale.
	fake.setHandler(respondStatus(401, `{"error":"unauthorized","message":"Authentication required"}`))
	rec := serveWithBearer(server, http.MethodGet, "/v1/workspaces/ws_a/fs/tree?path=/", testUserAccessToken)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 after secret rotation, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIntrospectionScopeEnforcementThroughServer(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	tests := []struct {
		name       string
		response   map[string]any
		method     string
		path       string
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "cli:auth-only desktop token cannot read fs",
			response:   activeIntrospection(now, map[string]any{"scopes": []string{"cli:auth", "rth:read", "rth:sync"}}),
			method:     http.MethodGet,
			path:       "/v1/workspaces/ws_a/fs/tree?path=/",
			wantStatus: http.StatusForbidden,
			wantMsg:    "missing required scope: fs:read",
		},
		{
			name:       "cli:auth-only desktop token cannot read a file",
			response:   activeIntrospection(now, map[string]any{"scopes": []string{"cli:auth"}}),
			method:     http.MethodGet,
			path:       "/v1/workspaces/ws_a/fs/file?path=/notes.md",
			wantStatus: http.StatusForbidden,
			wantMsg:    "missing required scope: fs:read",
		},
		{
			name:       "cli:auth-only desktop token cannot read sync status",
			response:   activeIntrospection(now, map[string]any{"scopes": []string{"cli:auth"}}),
			method:     http.MethodGet,
			path:       "/v1/workspaces/ws_a/sync/status",
			wantStatus: http.StatusForbidden,
			wantMsg:    "missing required scope: sync:read",
		},
		{
			name:       "empty scopes",
			response:   activeIntrospection(now, map[string]any{"scopes": []string{}}),
			method:     http.MethodGet,
			path:       "/v1/workspaces/ws_a/fs/tree?path=/",
			wantStatus: http.StatusForbidden,
			wantMsg:    "no scopes granted",
		},
		{
			name:       "cross-tenant: response workspace ws_a used on ws_b",
			response:   activeIntrospection(now, nil),
			method:     http.MethodGet,
			path:       "/v1/workspaces/ws_b/fs/tree?path=/",
			wantStatus: http.StatusForbidden,
			wantMsg:    "workspace mismatch",
		},
		{
			name:       "path-scoped grant denies outside subtree",
			response:   activeIntrospection(now, map[string]any{"scopes": []string{"relayfile:fs:read:/docs/**"}}),
			method:     http.MethodGet,
			path:       "/v1/workspaces/ws_a/fs/file?path=/secrets/key.md",
			wantStatus: http.StatusForbidden,
			wantMsg:    "missing required scope: fs:read",
		},
		{
			name:       "relayfile-scoped token with agent_name reads its workspace",
			response:   activeIntrospection(now, map[string]any{"agent_name": "MountSync", "scopes": []string{"fs:read"}}),
			method:     http.MethodGet,
			path:       "/v1/workspaces/ws_a/fs/tree?path=/",
			wantStatus: http.StatusOK,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newFakeRelaycontrol(t, respondJSON(tt.response))
			server, err := NewServerWithConfig(relayfile.NewStore(), introspectionTestConfig(fake.endpoint()))
			if err != nil {
				t.Fatalf("NewServerWithConfig: %v", err)
			}
			rec := serveWithBearer(server, tt.method, tt.path, testUserAccessToken)
			if rec.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tt.wantStatus, rec.Code, rec.Body.String())
			}
			if tt.wantMsg != "" {
				var body map[string]any
				_ = json.Unmarshal(rec.Body.Bytes(), &body)
				if body["message"] != tt.wantMsg {
					t.Fatalf("expected message %q, got %v", tt.wantMsg, body["message"])
				}
			}
			// The workspace in the request path is never sent upstream: the
			// response workspace is the only one that gets bound.
			if got := fake.lastRequest(); bytes.Contains(got.rawBody, []byte("ws_b")) || bytes.Contains(got.rawBody, []byte("workspace")) {
				t.Fatalf("client-supplied workspace leaked into the introspection request")
			}
		})
	}
}

func TestIntrospectionBindsResponseWorkspace(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	fake := newFakeRelaycontrol(t, respondJSON(activeIntrospection(now, nil)))
	verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))

	if _, authErr := authorizeBearer("Bearer "+testUserAccessToken, verifier, "ws_b", "fs:read", "/", now); authErr == nil || authErr.status != http.StatusForbidden {
		t.Fatalf("expected 403 workspace mismatch on ws_b, got %+v", authErr)
	}
	claims, authErr := authorizeBearer("Bearer "+testUserAccessToken, verifier, "ws_a", "fs:read", "/", now)
	if authErr != nil {
		t.Fatalf("expected ws_a to authorize, got %+v", authErr)
	}
	if claims.WorkspaceID != "ws_a" {
		t.Fatalf("bound workspace = %q, want ws_a from the response", claims.WorkspaceID)
	}
}

func TestDualModeRouting(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	privateKey := mustRSATestKey(t)
	var jwksHits atomic.Int32
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwksHits.Add(1)
		_ = json.NewEncoder(w).Encode(jwksDocument{Keys: []jwkKey{mustRSATestJWK("kid-1", &privateKey.PublicKey)}})
	}))
	defer jwksServer.Close()

	jwt := mustTestRS256JWT(t, privateKey, "kid-1", map[string]any{
		"wks":    "ws_jwt",
		"sub":    "JWTAgent",
		"scopes": []string{"fs:read"},
		"exp":    now.Add(time.Hour).Unix(),
		"aud":    "relayfile",
	})

	tests := []struct {
		name              string
		token             string
		wantStatus        int
		wantMessage       string
		wantWorkspace     string
		wantJWKSHits      int32
		wantIntrospection int32
	}{
		{name: "JWT goes to JWKS only", token: jwt, wantWorkspace: "ws_jwt", wantJWKSHits: 1},
		{name: "relay_pa_ wrapped JWT goes to JWKS only", token: "relay_pa_" + jwt, wantWorkspace: "ws_jwt", wantJWKSHits: 1},
		{name: "sh_at_ goes to introspection only", token: testUserAccessToken, wantWorkspace: "ws_a", wantIntrospection: 1},
		{name: "sh_wk_ goes to introspection only", token: "sh_wk_0123456789abcdefghijklmnopqrstuv", wantWorkspace: "ws_a", wantIntrospection: 1},
		{name: "garbage is rejected without calls", token: "not-a-token", wantStatus: 401, wantMessage: "invalid token"},
		{name: "two-part token is rejected without calls", token: "abc.def", wantStatus: 401, wantMessage: "invalid token"},
		{name: "relayauth prefix on non-JWT is rejected without calls", token: "relay_ws_opaque", wantStatus: 401, wantMessage: "invalid token"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			// Not parallel: jwksHits is shared across subtests.
			jwksHits.Store(0)
			fake := newFakeRelaycontrol(t, respondJSON(activeIntrospection(now, nil)))
			cfg := introspectionTestConfig(fake.endpoint())
			cfg.AuthMode = AuthModeDual
			cfg.JWKSURL = jwksServer.URL
			cfg.JWKSFetchTimeout = time.Second
			verifier := mustIntrospectionBearerVerifier(t, cfg)

			claims, authErr := parseBearer("Bearer "+tt.token, verifier, now)
			if tt.wantStatus == 0 {
				if authErr != nil {
					t.Fatalf("unexpected auth error: %+v", authErr)
				}
				if claims.WorkspaceID != tt.wantWorkspace {
					t.Fatalf("workspace = %q, want %q", claims.WorkspaceID, tt.wantWorkspace)
				}
			} else if authErr == nil || authErr.status != tt.wantStatus || authErr.message != tt.wantMessage {
				t.Fatalf("expected %d %q, got %+v", tt.wantStatus, tt.wantMessage, authErr)
			}
			if got := jwksHits.Load(); got != tt.wantJWKSHits {
				t.Fatalf("JWKS fetches = %d, want %d", got, tt.wantJWKSHits)
			}
			if got := fake.calls.Load(); got != tt.wantIntrospection {
				t.Fatalf("introspection calls = %d, want %d", got, tt.wantIntrospection)
			}
		})
	}
}

func TestDualModeIntrospectionFailureDoesNotFallBackToJWKS(t *testing.T) {
	t.Parallel()

	var jwksHits atomic.Int32
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwksHits.Add(1)
		_ = json.NewEncoder(w).Encode(jwksDocument{})
	}))
	defer jwksServer.Close()

	fake := newFakeRelaycontrol(t, respondStatus(500, ``))
	cfg := introspectionTestConfig(fake.endpoint())
	cfg.AuthMode = AuthModeDual
	cfg.JWKSURL = jwksServer.URL
	verifier := mustIntrospectionBearerVerifier(t, cfg)

	if _, authErr := parseBearer("Bearer "+testUserAccessToken, verifier, time.Now().UTC()); authErr == nil || authErr.status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %+v", authErr)
	}
	if jwksHits.Load() != 0 {
		t.Fatalf("introspection failure fell back to JWKS")
	}
}

func TestIntrospectionModeSendsJWTShapedTokensToIntrospection(t *testing.T) {
	t.Parallel()

	var jwksHits atomic.Int32
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwksHits.Add(1)
	}))
	defer jwksServer.Close()

	fake := newFakeRelaycontrol(t, respondJSON(map[string]any{"active": false, "reason": "wrong-kind"}))
	cfg := introspectionTestConfig(fake.endpoint())
	cfg.JWKSURL = jwksServer.URL // ignored in introspection mode
	verifier := mustIntrospectionBearerVerifier(t, cfg)

	token := "relay_pa_eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"
	_, authErr := parseBearer("Bearer "+token, verifier, time.Now().UTC())
	if authErr == nil || authErr.status != http.StatusUnauthorized || authErr.message != "token inactive" {
		t.Fatalf("expected 401 token inactive, got %+v", authErr)
	}
	if fake.calls.Load() != 1 {
		t.Fatalf("expected one introspection call, got %d", fake.calls.Load())
	}
	if got := fake.lastRequest().body["token"]; got != token {
		t.Fatalf("token was altered before introspection (relay_ prefix must not be stripped)")
	}
	if jwksHits.Load() != 0 {
		t.Fatalf("introspection mode fetched JWKS")
	}
}

func TestIntrospectionLogsNeverContainTokenOrSecret(t *testing.T) {
	// Not parallel: swaps the global logger output.
	var buf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prevOutput)

	now := time.Now().UTC()
	handlers := []func(http.ResponseWriter, *http.Request, map[string]any){
		respondJSON(map[string]any{"active": false, "reason": "revoked " + testUserAccessToken}),
		respondStatus(401, ``),
		respondStatus(404, ``),
		respondStatus(400, ``),
		respondStatus(500, `{"echo":"`+testUserAccessToken+`"}`),
		respondStatus(200, `not json `+testUserAccessToken),
		respondJSON(activeIntrospection(now, nil)),
	}
	for _, handler := range handlers {
		fake := newFakeRelaycontrol(t, handler)
		verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))
		_, _ = parseBearer("Bearer "+testUserAccessToken, verifier, now)
	}
	output := buf.String()
	for _, secret := range []string{testUserAccessToken, testIntrospectSecret} {
		if strings.Contains(output, secret) {
			t.Fatalf("log output leaked a credential")
		}
	}
	if !strings.Contains(output, "reason=other") {
		t.Fatalf("expected an unrecognised inactive reason to be logged as other, got %q", output)
	}
}

func TestAuthConfigValidation(t *testing.T) {
	t.Parallel()

	valid := func() ServerConfig {
		return ServerConfig{
			AuthMode:              AuthModeIntrospection,
			IntrospectionURL:      "http://relaycontrol.relay.svc.cluster.local:8080/internal/v1/auth/introspect",
			IntrospectionSecret:   "s3cret",
			IntrospectionAudience: "https://files.example.test",
		}
	}
	tests := []struct {
		name    string
		mutate  func(*ServerConfig)
		wantErr string
	}{
		{name: "valid introspection", mutate: func(*ServerConfig) {}},
		{name: "valid dual", mutate: func(c *ServerConfig) { c.AuthMode = "dual"; c.JWKSURL = "https://jwks.example.test/jwks.json" }},
		{name: "mode is case-insensitive", mutate: func(c *ServerConfig) { c.AuthMode = " Introspection " }},
		{name: "jwks mode ignores introspection fields", mutate: func(c *ServerConfig) { c.AuthMode = "jwks"; c.IntrospectionURL = "ftp://x" }},
		{name: "bad mode", mutate: func(c *ServerConfig) { c.AuthMode = "oauth" }, wantErr: "invalid auth mode"},
		{name: "missing url", mutate: func(c *ServerConfig) { c.IntrospectionURL = "" }, wantErr: "requires an introspection URL"},
		{name: "missing secret", mutate: func(c *ServerConfig) { c.IntrospectionSecret = "  " }, wantErr: "requires an introspection secret"},
		{name: "missing audience", mutate: func(c *ServerConfig) { c.IntrospectionAudience = "" }, wantErr: "requires an introspection audience"},
		{name: "bad url scheme", mutate: func(c *ServerConfig) { c.IntrospectionURL = "ftp://relaycontrol/internal/v1/auth/introspect" }, wantErr: "http or https"},
		{name: "relative url", mutate: func(c *ServerConfig) { c.IntrospectionURL = "/internal/v1/auth/introspect" }, wantErr: "http or https"},
		{name: "url userinfo", mutate: func(c *ServerConfig) { c.IntrospectionURL = "http://user:pw@relaycontrol/internal/v1/auth/introspect" }, wantErr: "userinfo"},
		{name: "url query", mutate: func(c *ServerConfig) { c.IntrospectionURL = "http://relaycontrol/internal/v1/auth/introspect?x=1" }, wantErr: "query"},
		{name: "url empty query", mutate: func(c *ServerConfig) { c.IntrospectionURL = "http://relaycontrol/internal/v1/auth/introspect?" }, wantErr: "query"},
		{name: "url fragment", mutate: func(c *ServerConfig) { c.IntrospectionURL = "http://relaycontrol/internal/v1/auth/introspect#frag" }, wantErr: "fragment"},
		{name: "url without host", mutate: func(c *ServerConfig) { c.IntrospectionURL = "http:///internal/v1/auth/introspect" }, wantErr: "host"},
		{name: "audience bad scheme", mutate: func(c *ServerConfig) { c.IntrospectionAudience = "files.example.test" }, wantErr: "http(s) origin"},
		{name: "audience userinfo", mutate: func(c *ServerConfig) { c.IntrospectionAudience = "https://u@files.example.test" }, wantErr: "http(s) origin"},
		{name: "dual without JWKS URL", mutate: func(c *ServerConfig) { c.AuthMode = "dual" }, wantErr: "explicit JWKS URL"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := valid()
			tt.mutate(&cfg)
			_, err := NewServerWithConfig(relayfile.NewStore(), cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestAuthConfigDefaults(t *testing.T) {
	t.Parallel()

	jwks := ServerConfig{}
	if err := normalizeAuthConfig(&jwks); err != nil {
		t.Fatalf("default config: %v", err)
	}
	if jwks.AuthMode != AuthModeJWKS || jwks.JWKSURL != defaultRelayAuthJWKSURL {
		t.Fatalf("default config changed: mode=%q jwks=%q", jwks.AuthMode, jwks.JWKSURL)
	}

	introspection := ServerConfig{
		AuthMode:              AuthModeIntrospection,
		IntrospectionURL:      "http://relaycontrol:8080/internal/v1/auth/introspect",
		IntrospectionSecret:   "s3cret",
		IntrospectionAudience: "HTTPS://Files.Example.Test:443/some/path",
	}
	if err := normalizeAuthConfig(&introspection); err != nil {
		t.Fatalf("introspection config: %v", err)
	}
	if introspection.JWKSURL != "" {
		t.Fatalf("introspection mode must not default a JWKS URL, got %q", introspection.JWKSURL)
	}
	if introspection.IntrospectionAudience != "https://files.example.test" {
		t.Fatalf("audience not normalized: %q", introspection.IntrospectionAudience)
	}
	if introspection.IntrospectionTimeout != 3*time.Second || introspection.IntrospectionCacheTTL != 60*time.Second {
		t.Fatalf("unexpected defaults: timeout=%s ttl=%s", introspection.IntrospectionTimeout, introspection.IntrospectionCacheTTL)
	}
}

func TestNormalizedOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{in: "https://files.example.test", want: "https://files.example.test", wantOK: true},
		{in: " HTTPS://Files.Example.TEST:443/path?q#f ", want: "https://files.example.test", wantOK: true},
		{in: "http://files.example.test:80", want: "http://files.example.test", wantOK: true},
		{in: "http://files.example.test:443", want: "http://files.example.test:443", wantOK: true},
		{in: "https://files.example.test:8443", want: "https://files.example.test:8443", wantOK: true},
		{in: "http://[::1]:8080", want: "http://[::1]:8080", wantOK: true},
		{in: "ftp://files.example.test"},
		{in: "files.example.test"},
		{in: "https://user:pw@files.example.test"},
		{in: "https://"},
		{in: ""},
	}
	for _, tt := range tests {
		got, ok := normalizedOrigin(tt.in)
		if ok != tt.wantOK || got != tt.want {
			t.Fatalf("normalizedOrigin(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func serveWithBearer(server *Server, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Correlation-Id", "corr-introspection-test")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

// Pinned to relay-control selfhost/relayfile-agent-tokens @ bf529f8 (draft
// PR #22, v1.3): a `sh_wk_` workspace API token minted with purpose
// `relayfile-agent` introspects as principal_type "agent" with sub =
// token_id, email/machine_id null, agent_name set, the published scope list
// fs:read fs:write sync:read sync:trigger, and a numeric exp (expires_at, or
// now+3600 recomputed per call for neverExpires tokens).
func TestIntrospectionRelayfileAgentTokenClass(t *testing.T) {
	t.Parallel()

	const agentToken = "sh_wk_0123456789abcdefghijklmnopqrstuv"
	now := time.Now().UTC()
	agentResponse := func(overrides map[string]any) map[string]any {
		base := map[string]any{
			"sub":            "wkt_0123456789abcdef",
			"email":          nil,
			"principal_type": "agent",
			"agent_name":     "nabis-worker-1",
			"machine_id":     nil,
			"scopes":         []string{"fs:read", "fs:write", "sync:read", "sync:trigger"},
			"token_id":       "wkt_0123456789abcdef",
		}
		for key, value := range overrides {
			base[key] = value
		}
		return activeIntrospection(now, base)
	}

	t.Run("claims map to the agent principal", func(t *testing.T) {
		t.Parallel()
		fake := newFakeRelaycontrol(t, respondJSON(agentResponse(nil)))
		verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))

		claims, authErr := parseBearer("Bearer "+agentToken, verifier, now)
		if authErr != nil {
			t.Fatalf("unexpected auth error: %+v", authErr)
		}
		if claims.WorkspaceID != "ws_a" || claims.AgentName != "nabis-worker-1" {
			t.Fatalf("unexpected claims: workspace=%q agent=%q", claims.WorkspaceID, claims.AgentName)
		}
		if claims.TokenID != "wkt_0123456789abcdef" || claims.PrincipalType != "agent" {
			t.Fatalf("unexpected token metadata: token_id=%q principal_type=%q", claims.TokenID, claims.PrincipalType)
		}
		for _, scope := range []string{"fs:read", "fs:write", "sync:read", "sync:trigger"} {
			if _, ok := claims.Scopes[scope]; !ok {
				t.Fatalf("expected scope %q, got %v", scope, claims.Scopes)
			}
		}
		if got := fake.lastRequest(); got.body["token"] != agentToken {
			t.Fatalf("introspection body token = %v, want the sh_wk_ token", got.body["token"])
		}
	})

	t.Run("rolling exp window is honored as the cache cap", func(t *testing.T) {
		t.Parallel()
		// neverExpires tokens answer exp = now + 3600 on every call.
		fake := newFakeRelaycontrol(t, respondJSON(agentResponse(map[string]any{"exp": now.Add(3600 * time.Second).Unix()})))
		cfg := introspectionTestConfig(fake.endpoint())
		cfg.IntrospectionCacheTTL = 30 * time.Second
		verifier := mustIntrospectionBearerVerifier(t, cfg)

		if _, authErr := parseBearer("Bearer "+agentToken, verifier, now); authErr != nil {
			t.Fatalf("unexpected auth error: %+v", authErr)
		}
		if _, authErr := parseBearer("Bearer "+agentToken, verifier, now.Add(29*time.Second)); authErr != nil {
			t.Fatalf("unexpected auth error on cached call: %+v", authErr)
		}
		if got := fake.calls.Load(); got != 1 {
			t.Fatalf("expected one upstream call within the cache TTL, got %d", got)
		}
		if _, authErr := parseBearer("Bearer "+agentToken, verifier, now.Add(31*time.Second)); authErr != nil {
			t.Fatalf("unexpected auth error after TTL: %+v", authErr)
		}
		if got := fake.calls.Load(); got != 2 {
			t.Fatalf("expected a re-introspection after the cache TTL, got %d calls", got)
		}
	})

	t.Run("expired and audience answers are inactive", func(t *testing.T) {
		t.Parallel()
		for _, reason := range []string{"expired", "audience", "revoked", "wrong-kind"} {
			reason := reason
			t.Run(reason, func(t *testing.T) {
				t.Parallel()
				fake := newFakeRelaycontrol(t, respondJSON(map[string]any{"active": false, "reason": reason}))
				verifier := mustIntrospectionBearerVerifier(t, introspectionTestConfig(fake.endpoint()))
				_, authErr := parseBearer("Bearer "+agentToken, verifier, now)
				if authErr == nil || authErr.status != http.StatusUnauthorized || authErr.message != "token inactive" {
					t.Fatalf("expected 401 token inactive for %s, got %+v", reason, authErr)
				}
			})
		}
	})

	t.Run("server routes", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name       string
			method     string
			path       string
			wantStatus int
			wantMsg    string
		}{
			{name: "fs:read on own workspace", method: http.MethodGet, path: "/v1/workspaces/ws_a/fs/tree?path=/", wantStatus: http.StatusOK},
			{name: "sync:read on own workspace", method: http.MethodGet, path: "/v1/workspaces/ws_a/sync/status", wantStatus: http.StatusOK},
			// 412 comes from the handler itself (If-Match is required), so the
			// fs:write scope check has already passed.
			{name: "fs:write reaches the handler", method: http.MethodDelete, path: "/v1/workspaces/ws_a/fs/file?path=/missing.md", wantStatus: http.StatusPreconditionFailed, wantMsg: "missing If-Match header"},
			{name: "cross-tenant workspace is refused", method: http.MethodGet, path: "/v1/workspaces/ws_b/fs/tree?path=/", wantStatus: http.StatusForbidden, wantMsg: "workspace mismatch"},
			{name: "admin routes are not agent authority", method: http.MethodGet, path: "/v1/admin/backends", wantStatus: http.StatusForbidden, wantMsg: "missing required scope: admin:read"},
		}
		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()
				fake := newFakeRelaycontrol(t, respondJSON(agentResponse(nil)))
				server, err := NewServerWithConfig(relayfile.NewStore(), introspectionTestConfig(fake.endpoint()))
				if err != nil {
					t.Fatalf("NewServerWithConfig: %v", err)
				}
				rec := serveWithBearer(server, tt.method, tt.path, agentToken)
				if rec.Code != tt.wantStatus {
					t.Fatalf("expected %d, got %d: %s", tt.wantStatus, rec.Code, rec.Body.String())
				}
				if tt.wantMsg != "" {
					var body map[string]any
					_ = json.Unmarshal(rec.Body.Bytes(), &body)
					if body["message"] != tt.wantMsg {
						t.Fatalf("expected message %q, got %v", tt.wantMsg, body["message"])
					}
				}
			})
		}
	})
}
