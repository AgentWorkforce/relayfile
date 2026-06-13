package delegatedauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultRefreshTimeout = 10 * time.Second

var (
	ErrMissingCredentials = errors.New("delegated relayfile credentials are required")
	ErrRefreshRejected    = errors.New("delegated relayfile credential refresh rejected")
)

// Bundle is the relayfile-owned delegated credential. It deliberately stores
// only relayauth/relayfile material, never a user cloud-session token.
type Bundle struct {
	RelayfileURL string `json:"relayfileUrl,omitempty"`
	BaseURL      string `json:"baseUrl,omitempty"`
	Server       string `json:"server,omitempty"`

	RelayfileWorkspaceID string `json:"relayfileWorkspaceId,omitempty"`
	WorkspaceID          string `json:"workspaceId,omitempty"`

	AccessToken    string `json:"accessToken,omitempty"`
	Token          string `json:"token,omitempty"`
	RelayfileToken string `json:"relayfileToken,omitempty"`

	RefreshToken          string `json:"refreshToken,omitempty"`
	RelayfileRefreshToken string `json:"relayfileRefreshToken,omitempty"`

	AccessTokenExpiresAt        string `json:"accessTokenExpiresAt,omitempty"`
	RelayfileTokenExpiresAt     string `json:"relayfileTokenExpiresAt,omitempty"`
	RefreshTokenExpiresAt       string `json:"refreshTokenExpiresAt,omitempty"`
	RelayfileRefreshTokenExpiry string `json:"relayfileRefreshTokenExpiresAt,omitempty"`
	DelegationNotAfter          string `json:"delegationNotAfter,omitempty"`

	RelayauthURL string `json:"relayauthUrl,omitempty"`
	RefreshURL   string `json:"refreshUrl,omitempty"`

	Scopes          []string `json:"scopes,omitempty"`
	RelayfileScopes []string `json:"relayfileScopes,omitempty"`
	RelayfilePaths  []string `json:"relayfileMountPaths,omitempty"`
	AgentName       string   `json:"agentName,omitempty"`
	UpdatedAt       string   `json:"updatedAt,omitempty"`
}

type TokenPair struct {
	AccessToken           string `json:"accessToken"`
	RefreshToken          string `json:"refreshToken"`
	AccessTokenExpiresAt  string `json:"accessTokenExpiresAt"`
	RefreshTokenExpiresAt string `json:"refreshTokenExpiresAt"`
	DelegationNotAfter    string `json:"delegationNotAfter,omitempty"`
}

func (b Bundle) ServerURL() string {
	return firstNonEmpty(b.RelayfileURL, b.BaseURL, b.Server)
}

func (b Bundle) Workspace() string {
	return firstNonEmpty(b.RelayfileWorkspaceID, b.WorkspaceID)
}

func (b Bundle) BearerToken() string {
	return firstNonEmpty(b.AccessToken, b.Token, b.RelayfileToken)
}

func (b Bundle) BearerExpiresAt() string {
	return firstNonEmpty(b.AccessTokenExpiresAt, b.RelayfileTokenExpiresAt)
}

func (b Bundle) RotationToken() string {
	return firstNonEmpty(b.RefreshToken, b.RelayfileRefreshToken)
}

func (b Bundle) RotationExpiresAt() string {
	return firstNonEmpty(b.RefreshTokenExpiresAt, b.RelayfileRefreshTokenExpiry)
}

func (b Bundle) RefreshEndpoint() (string, error) {
	if endpoint := strings.TrimSpace(b.RefreshURL); endpoint != "" {
		if err := requireAbsoluteURL(endpoint, "refreshUrl"); err != nil {
			return "", err
		}
		return endpoint, nil
	}
	base := strings.TrimRight(strings.TrimSpace(b.RelayauthURL), "/")
	if base == "" {
		return "", errors.New("delegated relayfile credentials missing relayauthUrl or refreshUrl")
	}
	if err := requireAbsoluteURL(base, "relayauthUrl"); err != nil {
		return "", err
	}
	return base + "/v1/tokens/refresh", nil
}

func requireAbsoluteURL(raw, field string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", field, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("%s must be an absolute URL", field)
	}
	return nil
}

func (b Bundle) ValidateForUse() error {
	if b.BearerToken() == "" {
		return errors.New("delegated relayfile credentials missing access token")
	}
	if b.Workspace() == "" {
		return errors.New("delegated relayfile credentials missing relayfileWorkspaceId")
	}
	if b.ServerURL() == "" {
		return errors.New("delegated relayfile credentials missing relayfileUrl")
	}
	return nil
}

func Load(path string) (Bundle, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return Bundle{}, errors.New("credential path is required")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return Bundle{}, err
	}
	var bundle Bundle
	if err := json.Unmarshal(payload, &bundle); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func SaveAtomic(path string, bundle Bundle) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("credential path is required")
	}
	bundle.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	payload, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".relayfile-credentials-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return os.Chmod(path, 0o600)
}

func RenewFile(ctx context.Context, client *http.Client, path string, timeout time.Duration) (Bundle, bool, error) {
	bundle, err := Load(path)
	if err != nil {
		return Bundle{}, false, err
	}
	renewed, changed, err := Renew(ctx, client, bundle, timeout)
	if err != nil || !changed {
		return renewed, changed, err
	}
	if err := SaveAtomic(path, renewed); err != nil {
		return Bundle{}, false, fmt.Errorf("persist rotated delegated relayfile credentials: %w", err)
	}
	return renewed, true, nil
}

func Renew(ctx context.Context, client *http.Client, bundle Bundle, timeout time.Duration) (Bundle, bool, error) {
	refreshToken := bundle.RotationToken()
	if refreshToken == "" {
		return bundle, false, errors.New("delegated relayfile credentials missing refresh token")
	}
	endpoint, err := bundle.RefreshEndpoint()
	if err != nil {
		return bundle, false, err
	}
	if timeout <= 0 {
		timeout = DefaultRefreshTimeout
	}
	if client == nil {
		client = http.DefaultClient
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := json.Marshal(map[string]string{"refreshToken": refreshToken})
	if err != nil {
		return bundle, false, err
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return bundle, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			return bundle, false, errors.New("delegated relayfile credential refresh timed out")
		}
		return bundle, false, err
	}
	defer resp.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return bundle, false, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return bundle, false, refreshHTTPError(resp.StatusCode, payload)
	}
	var pair TokenPair
	if err := json.Unmarshal(payload, &pair); err != nil {
		return bundle, false, fmt.Errorf("parse delegated relayfile refresh response: %w", err)
	}
	if strings.TrimSpace(pair.AccessToken) == "" || strings.TrimSpace(pair.RefreshToken) == "" {
		return bundle, false, errors.New("delegated relayfile refresh response missing token pair")
	}
	renewed := bundle
	renewed.AccessToken = strings.TrimSpace(pair.AccessToken)
	renewed.Token = ""
	renewed.RelayfileToken = ""
	renewed.RefreshToken = strings.TrimSpace(pair.RefreshToken)
	renewed.RelayfileRefreshToken = ""
	renewed.AccessTokenExpiresAt = strings.TrimSpace(pair.AccessTokenExpiresAt)
	renewed.RelayfileTokenExpiresAt = ""
	renewed.RefreshTokenExpiresAt = strings.TrimSpace(pair.RefreshTokenExpiresAt)
	renewed.RelayfileRefreshTokenExpiry = ""
	if strings.TrimSpace(pair.DelegationNotAfter) != "" {
		renewed.DelegationNotAfter = strings.TrimSpace(pair.DelegationNotAfter)
	}
	return renewed, true, nil
}

func refreshHTTPError(status int, payload []byte) error {
	var parsed struct {
		Code    string `json:"code"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(payload, &parsed)
	detail := firstNonEmpty(parsed.Code, parsed.Error, parsed.Message, http.StatusText(status))
	switch parsed.Code {
	case "delegation_expired", "workspace_token_revoked":
		return fmt.Errorf("%w: %s", ErrRefreshRejected, parsed.Code)
	default:
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return fmt.Errorf("%w: relayauth refresh failed with status %d (%s)", ErrRefreshRejected, status, detail)
		}
		return fmt.Errorf("relayauth refresh failed with status %d (%s)", status, detail)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
