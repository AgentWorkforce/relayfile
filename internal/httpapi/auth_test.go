package httpapi

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestScopeMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		required string
		granted  map[string]struct{}
		want     bool
	}{
		{
			name:     "exact scope matches",
			required: "fs:read",
			granted:  map[string]struct{}{"fs:read": {}},
			want:     true,
		},
		{
			name:     "relayfile scoped read wildcard matches",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:*": {}},
			want:     true,
		},
		{
			name:     "relayfile read wildcard does not match write",
			required: "fs:write",
			granted:  map[string]struct{}{"relayfile:fs:read:*": {}},
			want:     false,
		},
		{
			name:     "relayfile full wildcard matches",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:*:*:*": {}},
			want:     true,
		},
		{
			name:     "global wildcard matches",
			required: "fs:read",
			granted:  map[string]struct{}{"*:*:*:*": {}},
			want:     true,
		},
		{
			name:     "manage implies write",
			required: "fs:write",
			granted:  map[string]struct{}{"relayfile:fs:manage:*": {}},
			want:     true,
		},
		{
			name:     "wrong plane does not match",
			required: "fs:read",
			granted:  map[string]struct{}{"relaycast:fs:read:*": {}},
			want:     false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := scopeMatches(tt.granted, tt.required)
			if got != tt.want {
				t.Fatalf("scopeMatches(%v, %q) = %v, want %v", tt.granted, tt.required, got, tt.want)
			}
		})
	}
}

func TestScopeMatchesPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		required string
		granted  map[string]struct{}
		path     string
		want     bool
	}{
		{
			name:     "exact match",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:/src/app.ts": {}},
			path:     "/src/app.ts",
			want:     true,
		},
		{
			name:     "path prefix",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:/src/*": {}},
			path:     "/src/components/App.tsx",
			want:     true,
		},
		{
			name:     "wildcard",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:*": {}},
			path:     "/docs/readme.md",
			want:     true,
		},
		{
			name:     "wrong plane",
			required: "fs:read",
			granted:  map[string]struct{}{"relaycast:fs:read:/src/*": {}},
			path:     "/src/app.ts",
			want:     false,
		},
		{
			name:     ".env denied when only /src/app.ts scoped",
			required: "fs:read",
			granted:  map[string]struct{}{"relayfile:fs:read:/src/app.ts": {}},
			path:     "/.env",
			want:     false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := scopeMatchesPath(tt.granted, tt.required, tt.path)
			if got != tt.want {
				t.Fatalf(
					"scopeMatchesPath(%v, %q, %q) = %v, want %v",
					tt.granted,
					tt.required,
					tt.path,
					got,
					tt.want,
				)
			}
		})
	}

	t.Run("manage implies read/write", func(t *testing.T) {
		t.Parallel()

		granted := map[string]struct{}{
			"relayfile:fs:manage:/src/*": {},
		}

		for _, action := range []string{"read", "write"} {
			action := action
			t.Run(action, func(t *testing.T) {
				t.Parallel()

				got := scopeMatchesPath(granted, "fs:"+action, "/src/app.ts")
				if got != true {
					t.Fatalf(
						"scopeMatchesPath(%v, %q, %q) = %v, want %v",
						granted,
						"fs:"+action,
						"/src/app.ts",
						got,
						true,
					)
				}
			})
		}
	})
}

func TestParseBearerRS256HappyPath(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	privateKey := mustRSATestKey(t)

	var hits atomic.Int32
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(jwksDocument{
			Keys: []jwkKey{mustRSATestJWK("kid-1", &privateKey.PublicKey)},
		})
	}))
	defer jwksServer.Close()

	token := mustTestRS256JWT(t, privateKey, "kid-1", map[string]any{
		"wks":    "ws-rs",
		"sub":    "RelayfileGoWorker",
		"scopes": []string{"fs:read"},
		"exp":    now.Add(time.Hour).Unix(),
		"aud":    "relayfile",
	})

	claims, authErr := parseBearer("Bearer "+token, newBearerVerifier(ServerConfig{
		JWKSURL:          jwksServer.URL,
		JWKSFetchTimeout: time.Second,
	}), now)
	if authErr != nil {
		t.Fatalf("parseBearer returned auth error: %+v", authErr)
	}
	if claims.WorkspaceID != "ws-rs" {
		t.Fatalf("expected workspace ws-rs, got %q", claims.WorkspaceID)
	}
	if claims.AgentName != "RelayfileGoWorker" {
		t.Fatalf("expected subject RelayfileGoWorker, got %q", claims.AgentName)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected one JWKS fetch, got %d", hits.Load())
	}
}

func TestParseBearerRS256WrongKidReturnsUnauthorized(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	jwksKey := mustRSATestKey(t)
	tokenKey := mustRSATestKey(t)

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jwksDocument{
			Keys: []jwkKey{mustRSATestJWK("kid-1", &jwksKey.PublicKey)},
		})
	}))
	defer jwksServer.Close()

	token := mustTestRS256JWT(t, tokenKey, "kid-2", map[string]any{
		"wks":    "ws-rs",
		"sub":    "RelayfileGoWorker",
		"scopes": []string{"fs:read"},
		"exp":    now.Add(time.Hour).Unix(),
		"aud":    "relayfile",
	})

	_, authErr := parseBearer("Bearer "+token, newBearerVerifier(ServerConfig{
		JWKSURL:          jwksServer.URL,
		JWKSFetchTimeout: time.Second,
	}), now)
	if authErr == nil {
		t.Fatal("expected auth error for unknown kid")
	}
	if authErr.status != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown kid, got %d", authErr.status)
	}
}

func TestParseBearerRS256ExpiredToken(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	privateKey := mustRSATestKey(t)

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jwksDocument{
			Keys: []jwkKey{mustRSATestJWK("kid-1", &privateKey.PublicKey)},
		})
	}))
	defer jwksServer.Close()

	token := mustTestRS256JWT(t, privateKey, "kid-1", map[string]any{
		"wks":    "ws-rs",
		"sub":    "RelayfileGoWorker",
		"scopes": []string{"fs:read"},
		"exp":    now.Add(-time.Minute).Unix(),
		"aud":    "relayfile",
	})

	_, authErr := parseBearer("Bearer "+token, newBearerVerifier(ServerConfig{
		JWKSURL:          jwksServer.URL,
		JWKSFetchTimeout: time.Second,
	}), now)
	if authErr == nil {
		t.Fatal("expected auth error for expired token")
	}
	if authErr.message != "token expired" {
		t.Fatalf("expected token expired message, got %q", authErr.message)
	}
}

func TestParseBearerHS256AcceptedWhenEnabled(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	token := mustTestHS256JWT(t, "test-secret", map[string]any{
		"workspace_id": "ws-hs",
		"agent_name":   "LegacyWorker",
		"scopes":       []string{"fs:read"},
		"exp":          now.Add(time.Hour).Unix(),
		"aud":          "relayfile",
	})

	claims, authErr := parseBearer("Bearer "+token, newBearerVerifier(ServerConfig{
		JWTSecret:   "test-secret",
		AcceptHS256: true,
	}), now)
	if authErr != nil {
		t.Fatalf("parseBearer returned auth error: %+v", authErr)
	}
	if claims.WorkspaceID != "ws-hs" || claims.AgentName != "LegacyWorker" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestParseBearerHS256RejectedWhenDisabled(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	token := mustTestHS256JWT(t, "test-secret", map[string]any{
		"workspace_id": "ws-hs",
		"agent_name":   "LegacyWorker",
		"scopes":       []string{"fs:read"},
		"exp":          now.Add(time.Hour).Unix(),
		"aud":          "relayfile",
	})

	_, authErr := parseBearer("Bearer "+token, newBearerVerifier(ServerConfig{
		JWTSecret: "test-secret",
	}), now)
	if authErr == nil {
		t.Fatal("expected auth error when HS256 is disabled")
	}
	if authErr.message != "hs256 disabled" {
		t.Fatalf("expected hs256 disabled message, got %q", authErr.message)
	}
}

func TestParseBearerClaimNormalization(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	verifier := newBearerVerifier(ServerConfig{
		JWTSecret:   "test-secret",
		AcceptHS256: true,
	})

	tests := []struct {
		name          string
		payload       map[string]any
		wantWorkspace string
		wantAgent     string
	}{
		{
			name: "wks only",
			payload: map[string]any{
				"wks":    "ws-new",
				"sub":    "subject-new",
				"scopes": []string{"fs:read"},
				"exp":    now.Add(time.Hour).Unix(),
				"aud":    "relayfile",
			},
			wantWorkspace: "ws-new",
			wantAgent:     "subject-new",
		},
		{
			name: "workspace_id only",
			payload: map[string]any{
				"workspace_id": "ws-legacy",
				"agent_name":   "agent-legacy",
				"scopes":       []string{"fs:read"},
				"exp":          now.Add(time.Hour).Unix(),
				"aud":          "relayfile",
			},
			wantWorkspace: "ws-legacy",
			wantAgent:     "agent-legacy",
		},
		{
			name: "wks wins over workspace_id and sub wins over agent_name",
			payload: map[string]any{
				"wks":          "ws-preferred",
				"workspace_id": "ws-legacy",
				"sub":          "subject-preferred",
				"agent_name":   "agent-legacy",
				"scopes":       []string{"fs:read"},
				"exp":          now.Add(time.Hour).Unix(),
				"aud":          "relayfile",
			},
			wantWorkspace: "ws-preferred",
			wantAgent:     "subject-preferred",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			token := mustTestHS256JWT(t, "test-secret", tt.payload)
			claims, authErr := parseBearer("Bearer "+token, verifier, now)
			if authErr != nil {
				t.Fatalf("parseBearer returned auth error: %+v", authErr)
			}
			if claims.WorkspaceID != tt.wantWorkspace {
				t.Fatalf("expected workspace %q, got %q", tt.wantWorkspace, claims.WorkspaceID)
			}
			if claims.AgentName != tt.wantAgent {
				t.Fatalf("expected agent %q, got %q", tt.wantAgent, claims.AgentName)
			}
		})
	}
}

func TestJWKSCacheHitDoesNotRefetchWithinTTL(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	privateKey := mustRSATestKey(t)

	var hits atomic.Int32
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(jwksDocument{
			Keys: []jwkKey{mustRSATestJWK("kid-1", &privateKey.PublicKey)},
		})
	}))
	defer jwksServer.Close()

	verifier := newBearerVerifier(ServerConfig{
		JWKSURL:          jwksServer.URL,
		JWKSFetchTimeout: time.Second,
	})
	token := mustTestRS256JWT(t, privateKey, "kid-1", map[string]any{
		"wks":    "ws-rs",
		"sub":    "RelayfileGoWorker",
		"scopes": []string{"fs:read"},
		"exp":    now.Add(time.Hour).Unix(),
		"aud":    "relayfile",
	})

	for i := 0; i < 2; i++ {
		if _, authErr := parseBearer("Bearer "+token, verifier, now.Add(time.Duration(i)*time.Second)); authErr != nil {
			t.Fatalf("parseBearer returned auth error on pass %d: %+v", i+1, authErr)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("expected a single JWKS fetch, got %d", hits.Load())
	}
}

func TestJWKSKidMissForcesRefetch(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	keyOne := mustRSATestKey(t)
	keyTwo := mustRSATestKey(t)

	var hits atomic.Int32
	var mu sync.RWMutex
	keys := map[string]*rsa.PrivateKey{"kid-1": keyOne}

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)

		mu.RLock()
		currentKeys := make([]jwkKey, 0, len(keys))
		for kid, privateKey := range keys {
			currentKeys = append(currentKeys, mustRSATestJWK(kid, &privateKey.PublicKey))
		}
		mu.RUnlock()

		_ = json.NewEncoder(w).Encode(jwksDocument{Keys: currentKeys})
	}))
	defer jwksServer.Close()

	verifier := newBearerVerifier(ServerConfig{
		JWKSURL:          jwksServer.URL,
		JWKSFetchTimeout: time.Second,
	})

	tokenOne := mustTestRS256JWT(t, keyOne, "kid-1", map[string]any{
		"wks":    "ws-rs",
		"sub":    "RelayfileGoWorker",
		"scopes": []string{"fs:read"},
		"exp":    now.Add(time.Hour).Unix(),
		"aud":    "relayfile",
	})
	if _, authErr := parseBearer("Bearer "+tokenOne, verifier, now); authErr != nil {
		t.Fatalf("first parseBearer returned auth error: %+v", authErr)
	}

	mu.Lock()
	keys["kid-2"] = keyTwo
	mu.Unlock()

	tokenTwo := mustTestRS256JWT(t, keyTwo, "kid-2", map[string]any{
		"wks":    "ws-rs",
		"sub":    "RelayfileGoWorker",
		"scopes": []string{"fs:read"},
		"exp":    now.Add(time.Hour).Unix(),
		"aud":    "relayfile",
	})
	if _, authErr := parseBearer("Bearer "+tokenTwo, verifier, now.Add(time.Second)); authErr != nil {
		t.Fatalf("second parseBearer returned auth error: %+v", authErr)
	}

	if hits.Load() != 2 {
		t.Fatalf("expected JWKS refetch on kid miss, got %d fetches", hits.Load())
	}
}

func mustRSATestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	return privateKey
}

func mustTestHS256JWT(t *testing.T, secret string, payload map[string]any) string {
	t.Helper()

	return mustTestJWTWithClaims(t, map[string]any{
		"alg": "HS256",
		"typ": "JWT",
	}, payload, func(signingInput string) []byte {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(signingInput))
		return mac.Sum(nil)
	})
}

func mustTestRS256JWT(t *testing.T, privateKey *rsa.PrivateKey, kid string, payload map[string]any) string {
	t.Helper()

	return mustTestJWTWithClaims(t, map[string]any{
		"alg": "RS256",
		"typ": "JWT",
		"kid": kid,
	}, payload, func(signingInput string) []byte {
		sum := sha256.Sum256([]byte(signingInput))
		signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, sum[:])
		if err != nil {
			t.Fatalf("sign rs256 token: %v", err)
		}
		return signature
	})
}

func mustTestJWTWithClaims(t *testing.T, header map[string]any, payload map[string]any, sign func(string) []byte) string {
	t.Helper()

	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal jwt header: %v", err)
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal jwt payload: %v", err)
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(payloadBytes)
	signature := sign(signingInput)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func mustRSATestJWK(kid string, publicKey *rsa.PublicKey) jwkKey {
	exponent := big.NewInt(int64(publicKey.E)).Bytes()
	return jwkKey{
		Kid: kid,
		Kty: "RSA",
		Alg: "RS256",
		Use: "sig",
		N:   base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
		E:   base64.RawURLEncoding.EncodeToString(exponent),
	}
}
