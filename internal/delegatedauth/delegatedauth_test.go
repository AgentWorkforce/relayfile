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

func TestSaveAtomicIfMissingCreatesCredentialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds", "delegated.json")
	created, err := SaveAtomicIfMissing(path, Bundle{
		RelayfileURL:         "https://relayfile.test",
		RelayfileWorkspaceID: "rw_test",
		AccessToken:          "access_new",
		RefreshToken:         "refresh_new",
	})
	if err != nil {
		t.Fatalf("SaveAtomicIfMissing failed: %v", err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.BearerToken() != "access_new" || loaded.RotationToken() != "refresh_new" {
		t.Fatalf("unexpected saved bundle: %#v", loaded)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat saved file failed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential mode = %o, want 0600", got)
	}
}

func TestSaveAtomicIfMissingDoesNotOverwriteExistingCredentialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegated.json")
	if err := SaveAtomic(path, Bundle{
		RelayfileURL:         "https://relayfile.test",
		RelayfileWorkspaceID: "rw_test",
		AccessToken:          "access_existing",
		RefreshToken:         "refresh_existing",
	}); err != nil {
		t.Fatalf("SaveAtomic failed: %v", err)
	}

	created, err := SaveAtomicIfMissing(path, Bundle{
		RelayfileURL:         "https://relayfile.test",
		RelayfileWorkspaceID: "rw_test",
		AccessToken:          "access_late",
		RefreshToken:         "refresh_late",
	})
	if err != nil {
		t.Fatalf("SaveAtomicIfMissing failed: %v", err)
	}
	if created {
		t.Fatal("created = true, want false")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.BearerToken() != "access_existing" || loaded.RotationToken() != "refresh_existing" {
		t.Fatalf("existing bundle was overwritten: %#v", loaded)
	}
}

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

func TestRenewFileGenericFailureDoesNotOverwriteCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"relayauth_unavailable","message":"temporary outage"}`))
	}))
	defer server.Close()

	path, before := writeRejectedRefreshFixture(t, server.URL)
	_, changed, err := RenewFile(context.Background(), server.Client(), path, time.Second)
	assertRejectedRefreshPreservedFile(t, path, before, changed, err)
	if !strings.Contains(err.Error(), "relayauth_unavailable") {
		t.Fatalf("expected generic rejection code in error, got %v", err)
	}
}

func TestRenewFileTimeoutDoesNotOverwriteCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(TokenPair{
			AccessToken:           "access_late",
			RefreshToken:          "refresh_late",
			AccessTokenExpiresAt:  time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer server.Close()

	path, before := writeRejectedRefreshFixture(t, server.URL)
	_, changed, err := RenewFile(context.Background(), server.Client(), path, 10*time.Millisecond)
	assertRejectedRefreshPreservedFile(t, path, before, changed, err)
	if !strings.Contains(err.Error(), "refresh timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func writeRejectedRefreshFixture(t *testing.T, relayauthURL string) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "delegated.json")
	original := Bundle{
		RelayfileURL:          "https://relayfile.test",
		RelayfileWorkspaceID:  "rw_test",
		AccessToken:           "access_old",
		RefreshToken:          "refresh_old",
		AccessTokenExpiresAt:  time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		RelayauthURL:          relayauthURL,
	}
	if err := SaveAtomic(path, original); err != nil {
		t.Fatalf("SaveAtomic failed: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before failed: %v", err)
	}
	return path, before
}

func assertRejectedRefreshPreservedFile(t *testing.T, path string, before []byte, changed bool, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected rejected refresh error")
	}
	if !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("expected ErrRefreshRejected, got %v", err)
	}
	if changed {
		t.Fatal("expected changed=false after rejected refresh")
	}
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read after failed: %v", readErr)
	}
	if string(after) != string(before) {
		t.Fatalf("credentials changed after rejected refresh\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
