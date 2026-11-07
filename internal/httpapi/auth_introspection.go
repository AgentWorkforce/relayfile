package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Auth modes for bearer verification.
//
//   - AuthModeJWKS (the default, also selected by an empty string): every
//     bearer is an RS256 JWT verified against RelayAuth's JWKS. Unchanged.
//   - AuthModeIntrospection: every bearer is sent to relaycontrol's
//     in-cluster token introspection endpoint. JWKS is never fetched.
//   - AuthModeDual: relaycontrol-prefixed tokens (relaycontrolTokenPrefixes)
//     go to introspection only; JWT-shaped tokens go to JWKS only; anything
//     else is rejected. There is no cross-fallback between the verifiers.
const (
	AuthModeJWKS          = "jwks"
	AuthModeIntrospection = "introspection"
	AuthModeDual          = "dual"
)

const (
	// IntrospectionPath is relaycontrol's token introspection route.
	IntrospectionPath = "/internal/v1/auth/introspect"

	defaultIntrospectionTimeout  = 3 * time.Second
	defaultIntrospectionCacheTTL = 60 * time.Second
	// maxIntrospectionCacheTTL bounds how long a revoked token can keep
	// being accepted from the positive cache.
	maxIntrospectionCacheTTL = 60 * time.Second
	// maxIntrospectionTokenBytes keeps the request well under relaycontrol's
	// 16 KiB body limit.
	maxIntrospectionTokenBytes    = 8 << 10
	maxIntrospectionResponseBytes = 64 << 10
	maxIntrospectionCacheEntries  = 10000
)

// relaycontrolTokenPrefixes are the token-class prefixes minted by
// relaycontrol that relayfile routes to introspection in dual mode.
// Pinned to AgentWorkforce/relay-control 28e9d95 (PR #20, contract v1.2):
// `sh_at_` is the user access token prefix and `sh_wk_` the workspace API
// token prefix (the proposed `relayfile-agent` purpose). Introspection itself
// answers `wrong-kind` for every relaycontrol token it does not accept, so
// listing a prefix here never widens authority.
var relaycontrolTokenPrefixes = []string{
	"sh_at_",
	"sh_wk_",
}

// knownInactiveReasons are the `reason` values of contract v1.2. Anything
// else is logged as "other" so upstream text never reaches our logs.
var knownInactiveReasons = map[string]struct{}{
	"expired":                    {},
	"revoked":                    {},
	"unknown":                    {},
	"wrong-kind":                 {},
	"audience":                   {},
	"workspace":                  {},
	"machine_not_owned":          {},
	"machine_unclaimed":          {},
	"machine_workspace_mismatch": {},
}

func hasRelaycontrolTokenPrefix(raw string) bool {
	for _, prefix := range relaycontrolTokenPrefixes {
		if strings.HasPrefix(raw, prefix) {
			return true
		}
	}
	return false
}

// normalizedOrigin mirrors relaycontrol's normalizedOrigin
// (src/auth/introspect.ts): scheme://host[:port], lowercase, default port
// dropped. Returns false when the value is not an http(s) URL with a host
// or carries userinfo.
func normalizedOrigin(value string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if parsed.User != nil {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", false
	}
	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		return scheme + "://" + host + ":" + port, true
	}
	return scheme + "://" + host, true
}

// validateIntrospectionURL accepts an absolute http(s) URL with a host and
// no userinfo, query or fragment.
func validateIntrospectionURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid introspection URL: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return errors.New("introspection URL must use http or https")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("introspection URL must include a host")
	}
	if parsed.User != nil {
		return errors.New("introspection URL must not include userinfo")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(raw, "?") {
		return errors.New("introspection URL must not include a query")
	}
	if parsed.Fragment != "" || strings.Contains(raw, "#") {
		return errors.New("introspection URL must not include a fragment")
	}
	return nil
}

// normalizeAuthConfig validates and defaults the auth-related fields of cfg
// in place. Mode "jwks" (or empty) keeps the historical JWKS default.
func normalizeAuthConfig(cfg *ServerConfig) error {
	mode := strings.ToLower(strings.TrimSpace(cfg.AuthMode))
	if mode == "" {
		mode = AuthModeJWKS
	}
	switch mode {
	case AuthModeJWKS:
		cfg.AuthMode = mode
		if cfg.JWKSURL == "" {
			cfg.JWKSURL = defaultRelayAuthJWKSURL
		}
		return nil
	case AuthModeIntrospection, AuthModeDual:
	default:
		return fmt.Errorf("invalid auth mode %q (want jwks, introspection or dual)", cfg.AuthMode)
	}
	cfg.AuthMode = mode

	cfg.IntrospectionURL = strings.TrimSpace(cfg.IntrospectionURL)
	if cfg.IntrospectionURL == "" {
		return fmt.Errorf("auth mode %s requires an introspection URL", mode)
	}
	if err := validateIntrospectionURL(cfg.IntrospectionURL); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.IntrospectionSecret) == "" {
		return fmt.Errorf("auth mode %s requires an introspection secret", mode)
	}
	if strings.TrimSpace(cfg.IntrospectionAudience) == "" {
		return fmt.Errorf("auth mode %s requires an introspection audience (relayfile public origin)", mode)
	}
	audience, ok := normalizedOrigin(cfg.IntrospectionAudience)
	if !ok {
		return errors.New("introspection audience must be an http(s) origin without userinfo")
	}
	cfg.IntrospectionAudience = audience

	if cfg.IntrospectionTimeout <= 0 {
		cfg.IntrospectionTimeout = defaultIntrospectionTimeout
	}
	switch {
	case cfg.IntrospectionCacheTTL == 0:
		cfg.IntrospectionCacheTTL = defaultIntrospectionCacheTTL
	case cfg.IntrospectionCacheTTL < 0:
		// Negative disables the positive cache.
		cfg.IntrospectionCacheTTL = -1
	case cfg.IntrospectionCacheTTL > maxIntrospectionCacheTTL:
		cfg.IntrospectionCacheTTL = maxIntrospectionCacheTTL
	}

	if mode == AuthModeDual {
		// No default: an air-gapped cluster must never dial api.relayauth.dev.
		if strings.TrimSpace(cfg.JWKSURL) == "" {
			return errors.New("auth mode dual requires an explicit JWKS URL")
		}
	}
	return nil
}

// introspectionVerifier verifies bearer tokens by calling relaycontrol's
// POST /internal/v1/auth/introspect (contract v1.2, relay-control PR #20 @
// 28e9d957). Every failure fails closed: only a well-formed 200
// {"active":true,...} response produces claims.
//
// Successful verdicts are cached for at most IntrospectionCacheTTL (hard cap
// 60s) and never past the token's exp, so a revocation in relaycontrol can
// take up to the TTL to be observed by relayfile. Inactive answers and
// errors are never cached.
type introspectionVerifier struct {
	endpoint string
	secret   string
	audience string // normalized origin
	client   *http.Client
	cacheTTL time.Duration // <= 0 disables the cache

	mu         sync.Mutex
	cache      map[string]introspectionCacheEntry
	maxEntries int
}

type introspectionCacheEntry struct {
	claims    tokenClaims
	expiresAt time.Time
}

type introspectionRequestBody struct {
	Token string `json:"token"`
	Aud   string `json:"aud"`
}

var errIntrospectionRedirect = errors.New("introspection redirect refused")

func newIntrospectionVerifier(cfg ServerConfig) *introspectionVerifier {
	timeout := cfg.IntrospectionTimeout
	if timeout <= 0 {
		timeout = defaultIntrospectionTimeout
	}
	return &introspectionVerifier{
		endpoint: cfg.IntrospectionURL,
		secret:   cfg.IntrospectionSecret,
		audience: cfg.IntrospectionAudience,
		client: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errIntrospectionRedirect
			},
		},
		cacheTTL:   cfg.IntrospectionCacheTTL,
		cache:      map[string]introspectionCacheEntry{},
		maxEntries: maxIntrospectionCacheEntries,
	}
}

func introspectionUnavailable(message string) *authError {
	return &authError{status: http.StatusServiceUnavailable, code: "unavailable", message: message}
}

func introspectionUnauthorized(message string) *authError {
	return &authError{status: http.StatusUnauthorized, code: "unauthorized", message: message}
}

// validIntrospectionToken refuses tokens that cannot be a relaycontrol
// token before any request is made.
func validIntrospectionToken(raw string) bool {
	if raw == "" || len(raw) > maxIntrospectionTokenBytes {
		return false
	}
	for _, r := range raw {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == unicode.ReplacementChar {
			return false
		}
	}
	return true
}

func (v *introspectionVerifier) cacheKey(raw string) string {
	sum := sha256.Sum256([]byte(raw + "\x00" + v.audience))
	return hex.EncodeToString(sum[:])
}

func (v *introspectionVerifier) cacheGet(key string, now time.Time) (tokenClaims, bool) {
	if v.cacheTTL <= 0 {
		return tokenClaims{}, false
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	entry, ok := v.cache[key]
	if !ok {
		return tokenClaims{}, false
	}
	if !now.Before(entry.expiresAt) || now.Unix() >= entry.claims.Exp {
		delete(v.cache, key)
		return tokenClaims{}, false
	}
	return entry.claims, true
}

func (v *introspectionVerifier) cachePut(key string, claims tokenClaims, now time.Time) {
	if v.cacheTTL <= 0 {
		return
	}
	expiresAt := now.Add(v.cacheTTL)
	if tokenExp := time.Unix(claims.Exp, 0); tokenExp.Before(expiresAt) {
		expiresAt = tokenExp
	}
	if !now.Before(expiresAt) {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, exists := v.cache[key]; !exists && len(v.cache) >= v.maxEntries {
		for k, entry := range v.cache {
			if !now.Before(entry.expiresAt) {
				delete(v.cache, k)
			}
		}
		if len(v.cache) >= v.maxEntries {
			// Still full of live entries: serve the fresh verdict uncached.
			return
		}
	}
	v.cache[key] = introspectionCacheEntry{claims: claims, expiresAt: expiresAt}
}

// verify introspects raw (the bearer exactly as presented, without any
// prefix stripping). It never logs the token, the secret, or bodies.
func (v *introspectionVerifier) verify(raw string, now time.Time) (tokenClaims, *authError) {
	if !validIntrospectionToken(raw) {
		return tokenClaims{}, introspectionUnauthorized("invalid token")
	}
	key := v.cacheKey(raw)
	if claims, ok := v.cacheGet(key, now); ok {
		return claims, nil
	}

	payload, err := v.call(raw)
	if err != nil {
		return tokenClaims{}, err
	}
	claims, authErr := v.claimsFromResponse(payload, now)
	if authErr != nil {
		return tokenClaims{}, authErr
	}
	v.cachePut(key, claims, now)
	return claims, nil
}

func (v *introspectionVerifier) endpointHost() string {
	if parsed, err := url.Parse(v.endpoint); err == nil {
		return parsed.Host
	}
	return "invalid-url"
}

// call performs the HTTP exchange and returns the decoded JSON object of a
// 200 response. Every other outcome is a 503.
func (v *introspectionVerifier) call(raw string) (map[string]any, *authError) {
	body, err := json.Marshal(introspectionRequestBody{Token: raw, Aud: v.audience})
	if err != nil {
		return nil, introspectionUnavailable("introspection unreachable")
	}
	req, err := http.NewRequest(http.MethodPost, v.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, introspectionUnavailable("introspection unreachable")
	}
	req.Header.Set("Authorization", "Bearer "+v.secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		log.Printf("WARNING: relaycontrol introspection request to %s failed: %s", v.endpointHost(), describeTransportError(err))
		return nil, introspectionUnavailable("introspection unreachable")
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxIntrospectionResponseBytes+1))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxIntrospectionResponseBytes))

	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized:
		log.Printf("WARNING: relaycontrol introspection at %s rejected relayfile's service credential (HTTP 401); check RELAYCONTROL_INTROSPECT_SECRET", v.endpointHost())
		return nil, introspectionUnavailable("introspection rejected service credential")
	case resp.StatusCode == http.StatusNotFound:
		log.Printf("WARNING: relaycontrol introspection at %s returned HTTP 404 (endpoint disabled or wrong URL)", v.endpointHost())
		return nil, introspectionUnavailable("introspection disabled or unreachable")
	case resp.StatusCode == http.StatusBadRequest:
		log.Printf("WARNING: relaycontrol introspection at %s rejected the request body (HTTP 400)", v.endpointHost())
		return nil, introspectionUnavailable("introspection request rejected")
	default:
		log.Printf("WARNING: relaycontrol introspection at %s returned HTTP %d", v.endpointHost(), resp.StatusCode)
		return nil, introspectionUnavailable("introspection unreachable")
	}

	if readErr != nil || len(respBody) > maxIntrospectionResponseBytes {
		log.Printf("WARNING: relaycontrol introspection at %s returned an unreadable or oversize body", v.endpointHost())
		return nil, introspectionUnavailable("introspection unreachable")
	}
	var decoded any
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		log.Printf("WARNING: relaycontrol introspection at %s returned non-JSON", v.endpointHost())
		return nil, introspectionUnavailable("introspection unreachable")
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		log.Printf("WARNING: relaycontrol introspection at %s returned a non-object body", v.endpointHost())
		return nil, introspectionUnavailable("introspection unreachable")
	}
	if _, ok := object["active"].(bool); !ok {
		log.Printf("WARNING: relaycontrol introspection at %s returned no boolean active field", v.endpointHost())
		return nil, introspectionUnavailable("introspection unreachable")
	}
	return object, nil
}

func describeTransportError(err error) string {
	var netErr net.Error
	switch {
	case errors.Is(err, errIntrospectionRedirect):
		return "redirect refused"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	default:
		return "transport error"
	}
}

func (v *introspectionVerifier) claimsFromResponse(object map[string]any, now time.Time) (tokenClaims, *authError) {
	if active, _ := object["active"].(bool); !active {
		reason, _ := object["reason"].(string)
		if _, known := knownInactiveReasons[reason]; !known {
			reason = "other"
		}
		log.Printf("relaycontrol introspection: token inactive (reason=%s)", reason)
		return tokenClaims{}, introspectionUnauthorized("token inactive")
	}

	workspaceID, _ := object["workspace"].(string)
	if strings.TrimSpace(workspaceID) == "" {
		return tokenClaims{}, introspectionUnauthorized("missing workspace claim")
	}

	agentName := ""
	if name, ok := object["agent_name"].(string); ok && strings.TrimSpace(name) != "" {
		agentName = name
	} else if sub, ok := object["sub"].(string); ok && strings.TrimSpace(sub) != "" {
		agentName = sub
	}
	if agentName == "" {
		return tokenClaims{}, introspectionUnauthorized("missing subject claim")
	}

	expValue, ok := object["exp"].(float64)
	if !ok || math.IsNaN(expValue) || math.IsInf(expValue, 0) {
		return tokenClaims{}, introspectionUnauthorized("invalid exp claim")
	}
	exp := int64(math.Floor(expValue))
	if exp <= now.Unix() {
		return tokenClaims{}, introspectionUnauthorized("token expired")
	}

	aud, _ := object["aud"].(string)
	normalizedAud, ok := normalizedOrigin(aud)
	if !ok || normalizedAud != v.audience {
		return tokenClaims{}, introspectionUnauthorized("introspection audience mismatch")
	}

	scopes := parseScopes(object["scopes"])
	if len(scopes) == 0 {
		return tokenClaims{}, &authError{status: http.StatusForbidden, code: "forbidden", message: "no scopes granted"}
	}

	tokenID, _ := object["token_id"].(string)
	principalType, _ := object["principal_type"].(string)
	return tokenClaims{
		WorkspaceID:   workspaceID,
		AgentName:     agentName,
		Scopes:        scopes,
		Exp:           exp,
		TokenID:       tokenID,
		PrincipalType: principalType,
	}, nil
}
