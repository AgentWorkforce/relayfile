package httpapi

import (
	"crypto"
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultRelayAuthJWKSURL = "https://api.relayauth.dev/.well-known/jwks.json"
	defaultJWKSFetchTimeout = 5 * time.Second
	jwksCacheTTL            = 5 * time.Minute
	jwksStaleGraceWindow    = time.Minute
)

type authError struct {
	status  int
	code    string
	message string
}

func (e *authError) Error() string {
	return e.message
}

type tokenClaims struct {
	WorkspaceID string
	AgentName   string
	Scopes      map[string]struct{}
	Exp         int64
}

type bearerVerifier struct {
	jwtSecret        string
	acceptHS256      bool
	jwksURL          string
	jwksFetchTimeout time.Duration
	jwksCache        *jwksCache
}

type jwksCache struct {
	mu      sync.RWMutex
	entries map[string]cachedJWKS
}

type cachedJWKS struct {
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

type jwksDocument struct {
	Keys []jwkKey `json:"keys"`
}

type jwkKey struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func newBearerVerifier(cfg ServerConfig) *bearerVerifier {
	return &bearerVerifier{
		jwtSecret:        cfg.JWTSecret,
		acceptHS256:      cfg.AcceptHS256,
		jwksURL:          cfg.JWKSURL,
		jwksFetchTimeout: cfg.JWKSFetchTimeout,
		jwksCache: &jwksCache{
			entries: map[string]cachedJWKS{},
		},
	}
}

func authorizeBearer(authHeader string, verifier *bearerVerifier, workspaceID, requiredScope, requiredPath string, now time.Time) (tokenClaims, *authError) {
	claims, err := parseBearer(authHeader, verifier, now)
	if err != nil {
		return tokenClaims{}, err
	}
	if workspaceID != "" && claims.WorkspaceID != workspaceID {
		return tokenClaims{}, &authError{
			status:  403,
			code:    "forbidden",
			message: "workspace mismatch",
		}
	}
	if requiredScope != "" {
		if requiredPath == "" {
			if !scopeMatches(claims.Scopes, requiredScope) {
				return tokenClaims{}, &authError{
					status:  403,
					code:    "forbidden",
					message: "missing required scope: " + requiredScope,
				}
			}
		} else if !scopeMatchesPath(claims.Scopes, requiredScope, requiredPath) {
			return tokenClaims{}, &authError{
				status:  403,
				code:    "forbidden",
				message: "missing required scope: " + requiredScope,
			}
		}
	}
	return claims, nil
}

func parseBearer(authHeader string, verifier *bearerVerifier, now time.Time) (tokenClaims, *authError) {
	if !strings.HasPrefix(authHeader, "Bearer ") {
		return tokenClaims{}, &authError{
			status:  401,
			code:    "unauthorized",
			message: "missing or invalid bearer token",
		}
	}
	raw := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return tokenClaims{}, &authError{
			status:  401,
			code:    "unauthorized",
			message: "invalid jwt format",
		}
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid jwt header"}
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid jwt header"}
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid jwt payload"}
	}

	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid jwt signature"}
	}

	signingInput := parts[0] + "." + parts[1]
	switch header.Alg {
	case "HS256":
		if !verifier.acceptHS256 {
			return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "hs256 disabled"}
		}
		mac := hmac.New(sha256.New, []byte(verifier.jwtSecret))
		_, _ = mac.Write([]byte(signingInput))
		expected := mac.Sum(nil)
		if !hmac.Equal(sigBytes, expected) {
			return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "jwt signature mismatch"}
		}
	case "RS256":
		publicKey, authErr := verifier.lookupRSAPublicKey(header.Kid, now)
		if authErr != nil {
			return tokenClaims{}, authErr
		}
		sum := sha256.Sum256([]byte(signingInput))
		if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, sum[:], sigBytes); err != nil {
			return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "jwt signature mismatch"}
		}
	default:
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "unsupported jwt algorithm"}
	}

	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid jwt payload"}
	}

	workspaceID := firstStringClaim(payload, "wks", "workspace_id")
	if workspaceID == "" {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "missing workspace claim"}
	}
	agentName := firstStringClaim(payload, "sub", "agent_name")
	if agentName == "" {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "missing subject claim"}
	}

	exp, err := parseExp(payload["exp"])
	if err != nil {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid exp claim"}
	}
	if now.Unix() >= exp {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "token expired"}
	}
	if !hasAudience(payload["aud"], "relayfile") {
		return tokenClaims{}, &authError{status: 401, code: "unauthorized", message: "invalid aud claim"}
	}

	scopes := parseScopes(payload["scopes"])
	if len(scopes) == 0 {
		return tokenClaims{}, &authError{status: 403, code: "forbidden", message: "no scopes granted"}
	}

	return tokenClaims{
		WorkspaceID: workspaceID,
		AgentName:   agentName,
		Scopes:      scopes,
		Exp:         exp,
	}, nil
}

func firstStringClaim(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := payload[key].(string)
		if ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (v *bearerVerifier) lookupRSAPublicKey(kid string, now time.Time) (*rsa.PublicKey, *authError) {
	if strings.TrimSpace(kid) == "" {
		return nil, &authError{status: 401, code: "unauthorized", message: "missing jwt kid"}
	}

	keys, authErr := v.jwksCache.keysFor(v.jwksURL, v.jwksFetchTimeout, kid, now)
	if authErr != nil {
		return nil, authErr
	}

	publicKey := keys[kid]
	if publicKey == nil {
		return nil, &authError{status: 401, code: "unauthorized", message: "unknown jwt kid"}
	}
	return publicKey, nil
}

func (c *jwksCache) keysFor(url string, timeout time.Duration, kid string, now time.Time) (map[string]*rsa.PublicKey, *authError) {
	entry, ok := c.load(url)
	if ok && now.Sub(entry.fetchedAt) < jwksCacheTTL {
		if kid == "" || entry.keys[kid] != nil {
			return entry.keys, nil
		}
	}

	keys, err := fetchJWKS(url, timeout)
	if err == nil {
		c.store(url, cachedJWKS{
			keys:      keys,
			fetchedAt: now,
		})
		return keys, nil
	}

	if ok {
		age := now.Sub(entry.fetchedAt)
		if age <= jwksCacheTTL+jwksStaleGraceWindow {
			// Keep a brief grace window so transient JWKS outages do not break live traffic,
			// but cap it tightly to avoid trusting rotated keys for long.
			log.Printf("WARNING: serving stale JWKS from %s after refresh failed: %v", url, err)
			return entry.keys, nil
		}
	}

	return nil, &authError{status: 503, code: "unavailable", message: "jwks unreachable"}
}

func (c *jwksCache) load(url string) (cachedJWKS, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[url]
	return entry, ok
}

func (c *jwksCache) store(url string, entry cachedJWKS) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[url] = entry
}

func fetchJWKS(url string, timeout time.Duration) (map[string]*rsa.PublicKey, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("unexpected JWKS status: %s", resp.Status)
	}

	var document jwksDocument
	if err := json.NewDecoder(resp.Body).Decode(&document); err != nil {
		return nil, err
	}

	keys := make(map[string]*rsa.PublicKey, len(document.Keys))
	for _, jwk := range document.Keys {
		if jwk.Kid == "" || !strings.EqualFold(jwk.Kty, "RSA") {
			continue
		}
		publicKey, err := parseRSAPublicKey(jwk)
		if err != nil {
			return nil, err
		}
		keys[jwk.Kid] = publicKey
	}
	return keys, nil
}

func parseRSAPublicKey(jwk jwkKey) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		return nil, err
	}

	modulus := new(big.Int).SetBytes(nBytes)
	exponent := new(big.Int).SetBytes(eBytes)
	if modulus.Sign() <= 0 || exponent.Sign() <= 0 || exponent.BitLen() > 31 {
		return nil, errors.New("invalid rsa jwk")
	}

	return &rsa.PublicKey{
		N: modulus,
		E: int(exponent.Int64()),
	}, nil
}

func parseScopes(v any) map[string]struct{} {
	out := map[string]struct{}{}
	switch typed := v.(type) {
	case []any:
		for _, item := range typed {
			if scope, ok := item.(string); ok && scope != "" {
				out[scope] = struct{}{}
			}
		}
	case []string:
		for _, scope := range typed {
			if scope != "" {
				out[scope] = struct{}{}
			}
		}
	case string:
		for _, scope := range strings.Fields(typed) {
			out[scope] = struct{}{}
		}
	}
	return out
}

func scopeMatches(granted map[string]struct{}, required string) bool {
	if _, ok := granted[required]; ok {
		return true
	}

	parts := strings.SplitN(required, ":", 2)
	if len(parts) != 2 {
		return false
	}
	resource, action := parts[0], parts[1]

	for scope := range granted {
		segments := strings.SplitN(scope, ":", 4)
		if len(segments) < 3 {
			continue
		}

		plane := segments[0]
		res := segments[1]
		act := segments[2]

		if plane != "relayfile" && plane != "*" {
			continue
		}
		if res != resource && res != "*" {
			continue
		}
		if act != action && act != "*" {
			if act != "manage" || (action != "read" && action != "write") {
				continue
			}
		}

		return true
	}

	return false
}

func scopeMatchesPath(granted map[string]struct{}, required string, filePath string) bool {
	if _, ok := granted[required]; ok {
		return true
	}

	filePath = strings.TrimSpace(filePath)
	parts := strings.SplitN(required, ":", 2)
	if len(parts) != 2 {
		return false
	}
	resource, action := parts[0], parts[1]

	for scope := range granted {
		segments := strings.SplitN(scope, ":", 4)
		if len(segments) < 3 {
			continue
		}

		plane, res, act := segments[0], segments[1], segments[2]
		scopePath := "*"
		if len(segments) == 4 {
			scopePath = segments[3]
		}

		if plane != "relayfile" && plane != "*" {
			continue
		}
		if res != resource && res != "*" {
			continue
		}
		if act != action && act != "*" {
			if act != "manage" || (action != "read" && action != "write") {
				continue
			}
		}

		if scopePath == "*" {
			return true
		}
		if filePath == "" {
			return true
		}
		scopeDir := strings.TrimSuffix(scopePath, "/*")
		scopeDir = strings.TrimSuffix(scopeDir, "*")
		if scopePath == filePath {
			return true
		}
		if strings.HasSuffix(scopePath, "/*") && strings.HasPrefix(filePath, scopeDir+"/") {
			return true
		}
		if strings.HasSuffix(scopePath, "*") && strings.HasPrefix(filePath, scopeDir) {
			return true
		}
	}
	return false
}

func hasAudience(v any, required string) bool {
	required = strings.TrimSpace(required)
	if required == "" {
		return false
	}
	switch typed := v.(type) {
	case string:
		return strings.TrimSpace(typed) == required
	case []any:
		for _, item := range typed {
			if aud, ok := item.(string); ok && strings.TrimSpace(aud) == required {
				return true
			}
		}
	case []string:
		for _, aud := range typed {
			if strings.TrimSpace(aud) == required {
				return true
			}
		}
	}
	return false
}

func parseExp(v any) (int64, error) {
	switch typed := v.(type) {
	case float64:
		return int64(typed), nil
	case int64:
		return typed, nil
	case json.Number:
		return typed.Int64()
	default:
		return 0, errors.New("unsupported exp type")
	}
}

func verifyInternalHMAC(secret, timestamp, signature string, body []byte, now time.Time, maxSkew time.Duration) *authError {
	if timestamp == "" || signature == "" {
		return &authError{status: 401, code: "unauthorized", message: "missing internal auth headers"}
	}
	ts, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return &authError{status: 401, code: "unauthorized", message: "invalid internal timestamp"}
	}
	delta := now.Sub(ts)
	if delta < 0 {
		delta = -delta
	}
	if delta > maxSkew {
		return &authError{status: 401, code: "unauthorized", message: "internal request outside replay window"}
	}

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("\n"))
	_, _ = mac.Write(body)
	expectedHex := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(strings.ToLower(signature)), []byte(expectedHex)) {
		return &authError{status: 401, code: "unauthorized", message: "internal signature mismatch"}
	}
	return nil
}
