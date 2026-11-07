package main

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/agentworkforce/relayfile/internal/delegatedauth"
)

// coveringCredentialBinding retains the requested catalog authority across
// credential-file reloads; a replacement file cannot provide its own binding.
type coveringCredentialBinding struct {
	canonical string
	record    workspaceRecord
	runtimeID string
	scopes    []string
}

func bindCatalogCoveringCredentials(workspaceValue string, requested []string) (*coveringCredentialBinding, error) {
	canonical, known := canonicalWorkspaceShardValueStatus(workspaceValue)
	if !known {
		return nil, delegatedauth.ErrMissingCredentials
	}
	record, known := workspaceRecordForShardValue(canonical)
	if !known || strings.TrimSpace(record.Server) == "" {
		return nil, delegatedauth.ErrMissingCredentials
	}
	runtimeID := strings.TrimSpace(firstNonEmpty(record.RelayWorkspaceID, record.ID))
	if runtimeCanonical, unique := canonicalWorkspaceShardValueStatus(runtimeID); !unique || runtimeCanonical != canonical {
		return nil, errors.New("covering credential workspace is not uniquely bound in the catalog")
	}
	return &coveringCredentialBinding{canonical: canonical, record: record, runtimeID: runtimeID, scopes: append([]string(nil), requested...)}, nil
}

func (binding *coveringCredentialBinding) usable(bundle delegatedauth.Bundle) (bool, error) {
	if cloudID := strings.TrimSpace(bundle.WorkspaceID); cloudID != "" && cloudID != strings.TrimSpace(binding.record.ID) {
		return false, errors.New("credential Cloud workspace does not match the catalog")
	}
	return coveringCredentialUsable(bundle, binding.runtimeID, binding.record.Server, binding.scopes)
}

// loadCatalogCoveringCredentials considers only the catalog's recorded grant
// and the existing default join grant, never a directory scan. Scope coverage
// comes from the JWT claims through the existing CLI scope policy, not from
// the filename or bundle's scope aliases. Decoding is not signature verification:
// this retains the trusted local credential-file model; the server authorizes.
func loadCatalogCoveringCredentials(workspaceValue string, requested []string) (delegatedauth.Bundle, string, bool, error) {
	binding, err := bindCatalogCoveringCredentials(workspaceValue, requested)
	if errors.Is(err, delegatedauth.ErrMissingCredentials) {
		return delegatedauth.Bundle{}, "", false, nil
	}
	if err != nil {
		return delegatedauth.Bundle{}, "", false, err
	}
	paths := map[string]bool{}
	var selected delegatedauth.Bundle
	selectedPath := ""
	for _, scopes := range [][]string{binding.record.Scopes, defaultJoinScopes} {
		if len(scopes) == 0 {
			continue
		}
		path := delegatedCredentialsRawPathForRequest(binding.canonical, scopes)
		if paths[path] {
			continue
		}
		paths[path] = true
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return delegatedauth.Bundle{}, "", false, errors.New("catalog-owned covering credential file is not readable as a regular file")
		}
		bundle, err := delegatedauth.Load(path)
		if err != nil {
			return delegatedauth.Bundle{}, "", false, errors.New("catalog-owned covering credential file is malformed")
		}
		usable, err := binding.usable(bundle)
		if err != nil {
			return delegatedauth.Bundle{}, "", false, fmt.Errorf("catalog-owned covering credentials: %w", err)
		}
		if !usable {
			continue
		}
		if selectedPath != "" {
			return delegatedauth.Bundle{}, "", false, errors.New("ambiguous catalog-owned covering credential grants; select an explicit credentials file")
		}
		selected, selectedPath = bundle, path
	}
	return selected, selectedPath, selectedPath != "", nil
}

func coveringCredentialUsable(bundle delegatedauth.Bundle, runtimeID, expectedServer string, requested []string) (bool, error) {
	if err := bundle.ValidateForUse(); err != nil {
		return false, errors.New("incomplete credential bundle")
	}
	token := bundle.BearerToken()
	if bundle.Workspace() != runtimeID || workspaceIDFromToken(token) != runtimeID {
		return false, errors.New("bundle and JWT must name the catalog's exact Relayfile workspace")
	}
	claims, parsed := parseJWTClaims(token)
	exp, hasExpiry := claims["exp"].(float64)
	if !parsed || !hasExpiry || math.IsNaN(exp) || math.IsInf(exp, 0) || math.Trunc(exp) != exp || exp <= 0 || exp >= float64(math.MaxInt64) {
		return false, errors.New("JWT has no usable expiry")
	}
	needsRenew := relayfileTokenNeedsRefresh(token)
	if nbf, present := claims["nbf"]; present {
		notBefore, ok := nbf.(float64)
		if !ok || math.IsNaN(notBefore) || math.IsInf(notBefore, 0) || math.Trunc(notBefore) != notBefore || notBefore < 0 || notBefore >= float64(math.MaxInt64) || time.Now().Before(time.Unix(int64(notBefore), 0)) {
			return false, errors.New("JWT is not yet valid or has malformed not-before claim")
		}
	}
	if bound := strings.TrimSpace(bundle.BearerExpiresAt()); bound != "" {
		expires, ok := parseRFC3339(bound)
		if !ok {
			return false, errors.New("credential expiry metadata is malformed")
		}
		if !time.Now().Before(expires) && !needsRenew {
			return false, errors.New("credential access expiry disagrees with JWT")
		}
	}
	if bound := strings.TrimSpace(bundle.DelegationNotAfter); bound != "" {
		expires, ok := parseRFC3339(bound)
		if !ok || !time.Now().Before(expires) {
			return false, errors.New("credential delegation has expired or has malformed expiry")
		}
	}
	// Only the SDK's existing renewal window requires refresh-expiry proof.
	// Warm grants retain their existing forced-401 behavior.
	if needsRenew {
		expires, ok := parseRFC3339(bundle.RotationExpiresAt())
		if bundle.RotationToken() == "" || !ok || !time.Now().Before(expires) {
			return false, errors.New("covering credential needs renewal but has no provably unexpired refresh grant")
		}
	}
	// Supply no metadata aliases: this existing authority can see only the
	// token's actual scopes. Metadata must not promise extra rights that the
	// existing refresh/re-mint flow could later request from the cached file.
	claimsOnly := delegatedauth.Bundle{AccessToken: token}
	if !delegatedBundleSatisfiesRequestedScopes(claimsOnly, requested) {
		if needsRenew {
			return false, errors.New("renewal candidate does not cover requested scopes")
		}
		return false, nil
	}
	if !delegatedBundleSatisfiesRequestedScopes(claimsOnly, append(append([]string(nil), bundle.Scopes...), bundle.RelayfileScopes...)) {
		return false, errors.New("credential scope metadata exceeds the JWT grant")
	}
	if !delegatedBundleSatisfiesRequestedScopes(claimsOnly, delegatedBundleMintScopes(bundle)) {
		return false, errors.New("credential re-mint scopes exceed the JWT grant")
	}
	server, validServer := coveringCredentialURL(bundle.ServerURL())
	expected, validExpected := coveringCredentialURL(expectedServer)
	if !validServer || !validExpected || strings.TrimRight(server.String(), "/") != strings.TrimRight(expected.String(), "/") {
		return false, errors.New("credential server does not match the catalog")
	}
	// The cached bundle's RelayAuth URL remains the local trust anchor, as in
	// existing explicit credentials. Do not invent an issuer-host mapping.
	// Restrict refresh to its exact canonical endpoint before selecting a file
	// that the existing SDK may later rotate on a 401.
	auth, validAuth := coveringCredentialURL(bundle.RelayauthURL)
	endpoint, endpointErr := bundle.RefreshEndpoint()
	refresh, validRefresh := coveringCredentialURL(endpoint)
	if !validAuth || endpointErr != nil || !validRefresh {
		return false, errors.New("credential refresh endpoint is not a secure canonical URL")
	}
	auth.Path = strings.TrimRight(auth.Path, "/") + "/v1/tokens/refresh"
	if refresh.String() != auth.String() {
		return false, errors.New("credential refresh endpoint does not match its RelayAuth URL")
	}
	return true, nil
}

// Same HTTPS/loopback policy as the SDK's RelayAuth token provider. Automatic
// cache selection additionally rejects credentials, queries, fragments and
// noncanonical paths; explicit-file behavior is deliberately unchanged.
func coveringCredentialURL(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" {
		return nil, false
	}
	if parsed.Scheme != "https" {
		if parsed.Scheme != "http" {
			return nil, false
		}
		switch strings.ToLower(parsed.Hostname()) {
		case "localhost", "127.0.0.1", "::1":
		default:
			return nil, false
		}
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, false
		}
	}
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed, true
}

// coveringCredentialRefreshClient prevents refresh-token bodies from following
// redirects away from the exact endpoint already validated in the local bundle.
func coveringCredentialRefreshClient() *http.Client {
	client := *http.DefaultClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}
