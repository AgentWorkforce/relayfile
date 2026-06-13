package delegatedauth

import (
	"context"
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

func TestRenewFileRotatesAndPersistsDelegatedTokenPair(t *testing.T) {
	var sawRefresh string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tokens/refresh" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var body struct {
			RefreshToken string `json:"refreshToken"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode refresh body: %v", err)
		}
		sawRefresh = body.RefreshToken
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TokenPair{
			AccessToken:           "access_new",
			RefreshToken:          "refresh_new",
			AccessTokenExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
			DelegationNotAfter:    time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "creds", "delegated.json")
	original := Bundle{
		RelayfileURL:          "https://relayfile.test",
		RelayfileWorkspaceID:  "rw_test",
		AccessToken:           "access_old",
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	}
	if err := SaveAtomic(path, original); err != nil {
		t.Fatalf("SaveAtomic failed: %v", err)
	}

	renewed, changed, err := RenewFile(context.Background(), server.Client(), path, time.Second)
	if err != nil {
		t.Fatalf("RenewFile failed: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true")
	}
	if sawRefresh != "refresh_old" {
		t.Fatalf("refresh token sent = %q, want refresh_old", sawRefresh)
	}
	if renewed.BearerToken() != "access_new" || renewed.RotationToken() != "refresh_new" {
		t.Fatalf("unexpected renewed bundle: %#v", renewed)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat rotated file failed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential mode = %o, want 0600", got)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load rotated file failed: %v", err)
	}
	if loaded.BearerToken() != "access_new" || loaded.RotationToken() != "refresh_new" {
		t.Fatalf("rotated file did not persist new pair: %#v", loaded)
	}
}

func TestRenewFileRejectedRefreshDoesNotOverwriteCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"delegation_expired","message":"delegation expired"}`))
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "delegated.json")
	original := Bundle{
		RelayfileURL:          "https://relayfile.test",
		RelayfileWorkspaceID:  "rw_test",
		AccessToken:           "access_old",
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	}
	if err := SaveAtomic(path, original); err != nil {
		t.Fatalf("SaveAtomic failed: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before failed: %v", err)
	}

	_, changed, err := RenewFile(context.Background(), server.Client(), path, time.Second)
	if err == nil {
		t.Fatal("expected rejected refresh error")
	}
	if !strings.Contains(err.Error(), "delegation_expired") {
		t.Fatalf("expected delegation_expired error, got %v", err)
	}
	if changed {
		t.Fatal("expected changed=false after rejected refresh")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after failed: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("credentials changed after rejected refresh\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestRenewFileTransientRefreshFailureIsNotRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"temporary","message":"try again"}`))
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "delegated.json")
	if err := SaveAtomic(path, Bundle{
		RelayfileURL:          "https://relayfile.test",
		RelayfileWorkspaceID:  "rw_test",
		AccessToken:           "access_old",
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	}); err != nil {
		t.Fatalf("SaveAtomic failed: %v", err)
	}

	_, changed, err := RenewFile(context.Background(), server.Client(), path, time.Second)
	if err == nil {
		t.Fatal("expected transient refresh error")
	}
	if errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("transient refresh failure must not be classified as rejected: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false after transient refresh failure")
	}
}

func TestRenewTimeoutIsNotRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	_, changed, err := Renew(context.Background(), server.Client(), Bundle{
		RelayfileURL:          "https://relayfile.test",
		RelayfileWorkspaceID:  "rw_test",
		AccessToken:           "access_old",
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          server.URL,
	}, time.Millisecond)
	if err == nil {
		t.Fatal("expected refresh timeout")
	}
	if errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("refresh timeout must not be classified as rejected: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false after refresh timeout")
	}
}
