package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

// TestA12CatalogRevalidatedAfterCloudConflict covers productized cloud-mount
// contract A12 (409 force-revalidate path): when the cloud rejects a
// connect-session because the requested provider id is no longer in the
// catalog (HTTP 409), the CLI must drop the cached catalog so the next
// invocation pulls a fresh provider list.
func TestA12CatalogRevalidatedAfterCloudConflict(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	if err := ensureConfigDir(); err != nil {
		t.Fatalf("ensureConfigDir failed: %v", err)
	}
	if err := writeIntegrationCatalogCache(integrationCatalogCacheEntry{
		APIURL:    "https://cloud.example.test",
		FetchedAt: "2026-05-04T11:00:00Z",
		Version:   "v_old",
		Providers: []integrationCatalogEntry{
			{ID: "deprecated-provider", DisplayName: "Old", VFSRoot: "/old"},
		},
	}); err != nil {
		t.Fatalf("seed catalog cache failed: %v", err)
	}
	if _, err := os.Stat(integrationCatalogCachePath()); err != nil {
		t.Fatalf("expected seeded cache file, got err=%v", err)
	}

	var connectCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/workspaces/ws_demo/integrations/connect-session":
			atomic.AddInt32(&connectCalls, 1)
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"unknown_provider","providers":[{"id":"notion","displayName":"Notion","vfsRoot":"/notion"}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}

	err := connectCloudIntegration(server.URL, "ws_demo", "tok", "deprecated-provider", localDir, 0, false, &stubWriter{})
	if err == nil {
		t.Fatalf("expected error from connect-session 409, got nil")
	}
	if !isAPIConflict(err) {
		t.Fatalf("expected 409 to surface as apiError; got: %v", err)
	}
	if got := atomic.LoadInt32(&connectCalls); got != 1 {
		t.Fatalf("expected exactly 1 connect-session call, got %d", got)
	}
	if _, err := os.Stat(integrationCatalogCachePath()); !os.IsNotExist(err) {
		t.Fatalf("expected catalog cache invalidated after 409, got err=%v", err)
	}
}

// TestA12CatalogRefetchedAfterTTLExpiry verifies that a stale catalog cache
// (older than the TTL) is treated as a miss so a freshly added provider
// appears within the contract's "1 h or after a 409" window.
func TestA12CatalogRefetchedAfterTTLExpiry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	apiURL := "https://cloud.example.test"
	if err := ensureConfigDir(); err != nil {
		t.Fatalf("ensureConfigDir failed: %v", err)
	}
	// Backdated cache far past the TTL.
	if err := writeIntegrationCatalogCache(integrationCatalogCacheEntry{
		APIURL:    apiURL,
		FetchedAt: "2024-01-01T00:00:00Z",
		Version:   "v_stale",
		Providers: []integrationCatalogEntry{{ID: "notion", DisplayName: "Notion", VFSRoot: "/notion"}},
	}); err != nil {
		t.Fatalf("seed stale catalog cache failed: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/integrations/catalog" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"providers":[{"id":"notion","displayName":"Notion","vfsRoot":"/notion"},{"id":"github","displayName":"GitHub","vfsRoot":"/github"}],"version":"v_fresh"}`))
	}))
	defer server.Close()

	providers, err := loadIntegrationCatalog(server.URL, "tok")
	if err != nil {
		t.Fatalf("loadIntegrationCatalog failed: %v", err)
	}
	ids := make([]string, 0, len(providers))
	for _, p := range providers {
		ids = append(ids, p.ID)
	}
	hasGitHub := false
	for _, id := range ids {
		if id == "github" {
			hasGitHub = true
		}
	}
	if !hasGitHub {
		t.Fatalf("expected stale cache to be refetched and surface new github provider, got: %v", ids)
	}
}

// stubWriter swallows output during connect-session tests where the
// printed connect-link is not under assertion.
type stubWriter struct{}

func (stubWriter) Write(p []byte) (int, error) { return len(p), nil }

var _ = context.Background // ensure context import is referenced for future tests
