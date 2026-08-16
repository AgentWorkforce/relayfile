package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Agent Relay's cloud session lives in exactly one place, and relayfile reads
// it the same way every other Agent Relay client does:
//
//   1. the CLOUD_API_* environment overrides (CI / non-interactive), then
//   2. the canonical credential file written by `agent-relay cloud login`.
//
// This mirrors, field for field, relay's packages/cloud/src/auth.ts
// (readStoredAuth / requestStoredAuthRefresh / writeStoredAuth) and relayfile's
// own packages/agents/src/connect.ts (readCloudCreds). It is deliberately NOT a
// third credential store: the file below is relay's file, the refresh endpoint
// below is relay's endpoint, and the rotated tokens are written back so the
// next `agent-relay` command sees them.
//
// relayfile used to obtain this session by shelling out to
// `agent-relay cloud session --json --reveal-token`. That coupled a routine
// token expiry to the ability to locate and execute a Node CLI, and the binary
// it executed was chosen by AGENT_RELAY_BIN — a variable relay uses for the
// *broker*, not the CLI. Under any relay-spawned agent that variable points at
// agent-relay-broker, which has no `cloud` subcommand, so auto-recovery from an
// expired access token failed and reported itself as a CLI version problem.

const (
	// Same windows as relay packages/cloud/src/types.ts.
	agentRelayAccessTokenRefreshWindow  = 5 * time.Minute
	agentRelayRefreshTokenRefreshWindow = 24 * time.Hour

	// relay's DEFAULT_REFRESH_TIMEOUT_MS (packages/cloud/src/types.ts:278).
	// This MUST stay well below agentRelayAuthLockStaleAfter: the lock's mtime
	// is not heartbeaten while the holder waits on the refresh HTTP call, so a
	// holder that can outlive the stale window would have its lock reclaimed
	// mid-flight and two processes would refresh the same single-use token.
	// relay keeps a 3x margin (10s vs 30s); matching its constant restores it.
	agentRelayCloudRefreshTimeout = 10 * time.Second

	// Same lock discipline as relay packages/cloud/src/auth.ts, so a relayfile
	// refresh and an agent-relay refresh cannot interleave on the same file.
	// These must stay equal to relay's: a longer stale window here would let
	// relayfile reclaim a lock relay still considers live, and a shorter one
	// would let relay reclaim relayfile's.
	agentRelayAuthLockRetryDelay = 50 * time.Millisecond
	agentRelayAuthLockStaleAfter = 30 * time.Second
	agentRelayAuthLockTimeout    = 30 * time.Second
)

// agentRelayStoredAuth is the on-disk shape of cloud-auth.json.
type agentRelayStoredAuth struct {
	APIURL                string `json:"apiUrl"`
	AccessToken           string `json:"accessToken"`
	RefreshToken          string `json:"refreshToken"`
	AccessTokenExpiresAt  string `json:"accessTokenExpiresAt"`
	RefreshTokenExpiresAt string `json:"refreshTokenExpiresAt,omitempty"`
}

// agentRelayCloudSessionSource records where a session came from, so error
// messages and `relayfile status` can name it.
type agentRelayCloudSessionSource string

const (
	agentRelayCloudSessionFromEnv  agentRelayCloudSessionSource = "CLOUD_API_* environment"
	agentRelayCloudSessionFromFile agentRelayCloudSessionSource = "cloud-auth.json"
)

// agentRelayCloudAuthPath resolves the canonical credential file. It returns an
// error rather than a relative fallback when the home directory cannot be
// resolved: a relative path would read and, after a refresh, *write* Cloud
// tokens into the process's working directory — usually a repository — and that
// copy would be invisible to `agent-relay`, silently forking the session.
func agentRelayCloudAuthPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate the Agent Relay cloud session: resolve the home directory: %w", err)
	}
	return filepath.Join(home, ".agentworkforce", "relay", "cloud-auth.json"), nil
}

func agentRelayCloudAuthLockPath() (string, error) {
	path, err := agentRelayCloudAuthPath()
	if err != nil {
		return "", err
	}
	return path + ".lock", nil
}

func (a agentRelayStoredAuth) valid() bool {
	if strings.TrimSpace(a.AccessToken) == "" ||
		strings.TrimSpace(a.RefreshToken) == "" ||
		strings.TrimSpace(a.AccessTokenExpiresAt) == "" ||
		strings.TrimSpace(a.APIURL) == "" {
		return false
	}
	if _, ok := parseRFC3339(a.AccessTokenExpiresAt); !ok {
		return false
	}
	if strings.TrimSpace(a.RefreshTokenExpiresAt) != "" {
		if _, ok := parseRFC3339(a.RefreshTokenExpiresAt); !ok {
			return false
		}
	}
	return true
}

// needsRefresh mirrors relay's shouldRefreshStoredAuth: roll the access token
// inside its window, and roll the pair early when the refresh token itself is
// within a day of expiring.
func (a agentRelayStoredAuth) needsRefresh(now time.Time) bool {
	// Never attempt a refresh without a refresh credential to present. This
	// matters for the access-token-only environment shape, where there is
	// nothing to roll and rolling would fail rather than recover.
	if strings.TrimSpace(a.RefreshToken) == "" {
		return false
	}
	expiresAt, ok := parseRFC3339(a.AccessTokenExpiresAt)
	if !ok {
		return true
	}
	if expiresAt.Sub(now) <= agentRelayAccessTokenRefreshWindow {
		return true
	}
	if strings.TrimSpace(a.RefreshTokenExpiresAt) == "" {
		return false
	}
	refreshExpiresAt, ok := parseRFC3339(a.RefreshTokenExpiresAt)
	if !ok {
		return true
	}
	return refreshExpiresAt.Sub(now) <= agentRelayRefreshTokenRefreshWindow
}

// agentRelayStoredAuthFromEnv reads the non-interactive escape hatch. The
// contract is relayfile's own, from packages/agents/src/connect.ts:93-108: an
// access token alone is a usable session. CI commonly exports only
// CLOUD_API_ACCESS_TOKEN, having no refresh token to give and no need of one.
//
// This is deliberately looser than relay's readEnvAuth, which requires the full
// quartet. relay can demand it because a partial set there falls through to a
// login flow; here it would fall through to an unrelated credential file, or
// to "no session exists" while the caller has plainly supplied one.
func agentRelayStoredAuthFromEnv() (agentRelayStoredAuth, bool) {
	accessToken := strings.TrimSpace(os.Getenv("CLOUD_API_ACCESS_TOKEN"))
	if accessToken == "" {
		return agentRelayStoredAuth{}, false
	}
	auth := agentRelayStoredAuth{
		APIURL:                strings.TrimSpace(os.Getenv("CLOUD_API_URL")),
		AccessToken:           accessToken,
		RefreshToken:          strings.TrimSpace(os.Getenv("CLOUD_API_REFRESH_TOKEN")),
		AccessTokenExpiresAt:  strings.TrimSpace(os.Getenv("CLOUD_API_ACCESS_TOKEN_EXPIRES_AT")),
		RefreshTokenExpiresAt: strings.TrimSpace(os.Getenv("CLOUD_API_REFRESH_TOKEN_EXPIRES_AT")),
	}
	if auth.APIURL == "" {
		auth.APIURL = defaultCloudAPIURL
	}
	if _, ok := parseRFC3339(auth.AccessTokenExpiresAt); !ok {
		// Same defaulting as connect.ts: with no refresh token there is nothing
		// to roll, so pin the expiry far out rather than treating an unstated
		// expiry as "expired" and failing a session that works.
		if auth.RefreshToken == "" {
			auth.AccessTokenExpiresAt = time.Now().Add(365 * 24 * time.Hour).UTC().Format(time.RFC3339)
		} else {
			auth.AccessTokenExpiresAt = time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
		}
	}
	return auth, true
}

func readAgentRelayStoredAuthFile() (agentRelayStoredAuth, error) {
	path, err := agentRelayCloudAuthPath()
	if err != nil {
		return agentRelayStoredAuth{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return agentRelayStoredAuth{}, err
	}
	var auth agentRelayStoredAuth
	if err := json.Unmarshal(data, &auth); err != nil {
		return agentRelayStoredAuth{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return auth, nil
}

func writeAgentRelayStoredAuthFile(auth agentRelayStoredAuth) error {
	path, err := agentRelayCloudAuthPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileAtomically(path, payload, 0o600)
}

func acquireAgentRelayAuthLock(ctx context.Context) (func(), error) {
	lockPath, err := agentRelayCloudAuthLockPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	release := func() { _ = os.RemoveAll(lockPath) }
	deadline := time.Now().Add(agentRelayAuthLockTimeout)
	for {
		if err := os.Mkdir(lockPath, 0o700); err == nil {
			return release, nil
		} else if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		// Reclaim a lock whose owner died mid-refresh.
		if info, err := os.Stat(lockPath); err == nil && time.Since(info.ModTime()) >= agentRelayAuthLockStaleAfter {
			_ = os.RemoveAll(lockPath)
			continue
		} else if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for the Agent Relay cloud auth lock at %s", lockPath)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(agentRelayAuthLockRetryDelay):
		}
	}
}

// refreshAgentRelayStoredAuth calls the same endpoint relay's CLI and the
// relayfile TypeScript SDK call: POST <apiUrl>/api/v1/auth/token/refresh.
func refreshAgentRelayStoredAuth(ctx context.Context, auth agentRelayStoredAuth) (agentRelayStoredAuth, error) {
	apiURL := strings.TrimRight(strings.TrimSpace(auth.APIURL), "/")
	if apiURL == "" {
		apiURL = defaultCloudAPIURL
	}
	endpoint := apiURL + "/api/v1/auth/token/refresh"

	body, err := json.Marshal(map[string]string{"refreshToken": auth.RefreshToken})
	if err != nil {
		return agentRelayStoredAuth{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, agentRelayCloudRefreshTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return agentRelayStoredAuth{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "relayfile-cli/"+relayfileVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return agentRelayStoredAuth{}, fmt.Errorf("refresh the Agent Relay cloud session at %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	var payload agentRelayStoredAuth
	decodeErr := json.NewDecoder(resp.Body).Decode(&payload)
	if resp.StatusCode != http.StatusOK {
		refreshErr := fmt.Errorf("refresh the stored Agent Relay cloud login at %s: HTTP %d", endpoint, resp.StatusCode)
		switch resp.StatusCode {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden:
			return agentRelayStoredAuth{}, fmt.Errorf("%w: %v", ErrCloudRefreshExpired, refreshErr)
		default:
			return agentRelayStoredAuth{}, refreshErr
		}
	}
	if decodeErr != nil {
		return agentRelayStoredAuth{}, fmt.Errorf("parse the refresh response from %s: %w", endpoint, decodeErr)
	}
	if strings.TrimSpace(payload.AccessToken) == "" ||
		strings.TrimSpace(payload.RefreshToken) == "" ||
		strings.TrimSpace(payload.AccessTokenExpiresAt) == "" {
		return agentRelayStoredAuth{}, fmt.Errorf("the refresh response from %s did not include a complete token set", endpoint)
	}
	next := agentRelayStoredAuth{
		APIURL:               firstNonEmpty(strings.TrimSpace(payload.APIURL), apiURL),
		AccessToken:          strings.TrimSpace(payload.AccessToken),
		RefreshToken:         strings.TrimSpace(payload.RefreshToken),
		AccessTokenExpiresAt: strings.TrimSpace(payload.AccessTokenExpiresAt),
		// Deliberately NOT inherited from the previous token when the server
		// omits it. refreshTokenExpiresAt describes a specific refresh token,
		// and the response carries a *new* one. Carrying the old token's expiry
		// forward would re-arm the 24-hour refresh-token window immediately, so
		// every subsequent command would rotate again — an endless refresh loop
		// that also multiplies rotation races. Absent means unknown, and
		// needsRefresh treats unknown as "do not force a refresh".
		RefreshTokenExpiresAt: strings.TrimSpace(payload.RefreshTokenExpiresAt),
	}
	return next, nil
}

// ensureAgentRelayCloudSession resolves a usable cloud session, rolling the
// access token in place when it is inside its refresh window. Rotated tokens
// are written back to the canonical file, because relay rotates the refresh
// token on every refresh: keeping the new pair to ourselves would invalidate
// the copy `agent-relay` reads.
func ensureAgentRelayCloudSession(ctx context.Context) (agentRelayStoredAuth, agentRelayCloudSessionSource, error) {
	if auth, ok := agentRelayStoredAuthFromEnv(); ok {
		// Environment-supplied sessions are owned by whoever exported them;
		// never write them to disk, and never rotate them out from under that
		// owner. Refresh in memory only when they are already stale.
		if !auth.needsRefresh(time.Now()) {
			return auth, agentRelayCloudSessionFromEnv, nil
		}
		refreshed, err := refreshAgentRelayStoredAuth(ctx, auth)
		if err != nil {
			return agentRelayStoredAuth{}, agentRelayCloudSessionFromEnv, err
		}
		return refreshed, agentRelayCloudSessionFromEnv, nil
	}

	path, err := agentRelayCloudAuthPath()
	if err != nil {
		return agentRelayStoredAuth{}, agentRelayCloudSessionFromFile, err
	}
	auth, err := readAgentRelayStoredAuthFile()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return agentRelayStoredAuth{}, agentRelayCloudSessionFromFile, fmt.Errorf(
				"%w: no Agent Relay cloud session; %s does not exist (the CLOUD_API_* environment is the non-interactive alternative)",
				ErrCloudRefreshExpired, path,
			)
		}
		return agentRelayStoredAuth{}, agentRelayCloudSessionFromFile, err
	}
	if !auth.valid() {
		return agentRelayStoredAuth{}, agentRelayCloudSessionFromFile, fmt.Errorf(
			"%w: the Agent Relay cloud session at %s is incomplete (needs apiUrl, accessToken, refreshToken and an RFC3339 accessTokenExpiresAt)",
			ErrCloudRefreshExpired, path,
		)
	}
	if !auth.needsRefresh(time.Now()) {
		return auth, agentRelayCloudSessionFromFile, nil
	}

	release, err := acquireAgentRelayAuthLock(ctx)
	if err != nil {
		return agentRelayStoredAuth{}, agentRelayCloudSessionFromFile, err
	}
	defer release()

	// Another process may have refreshed — or a fresh `agent-relay cloud login`
	// may have replaced the session entirely — while we waited for the lock.
	// Re-read and ADOPT whatever is on disk: we hold the lock, so the file is
	// now the authoritative session, and the pre-lock copy's refresh token may
	// already have been rotated out from under us. Refreshing the stale copy
	// would present a dead refresh token and, on success, overwrite the newer
	// session — the lost update this double-check exists to prevent.
	if latest, readErr := readAgentRelayStoredAuthFile(); readErr == nil && latest.valid() {
		if !latest.needsRefresh(time.Now()) {
			return latest, agentRelayCloudSessionFromFile, nil
		}
		auth = latest
	}

	refreshed, err := refreshAgentRelayStoredAuth(ctx, auth)
	if err != nil {
		return agentRelayStoredAuth{}, agentRelayCloudSessionFromFile, err
	}
	if err := writeAgentRelayStoredAuthFile(refreshed); err != nil {
		return agentRelayStoredAuth{}, agentRelayCloudSessionFromFile, fmt.Errorf("persist the refreshed Agent Relay cloud session to %s: %w", path, err)
	}
	return refreshed, agentRelayCloudSessionFromFile, nil
}
