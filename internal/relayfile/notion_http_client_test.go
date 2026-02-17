package relayfile

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPNotionWriteClientUpsertSendsExpectedRequest(t *testing.T) {
	var capturedAuth string
	var capturedVersion string
	var capturedPath string
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		capturedVersion = r.Header.Get("Notion-Version")
		capturedPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewHTTPNotionWriteClient(NotionHTTPClientOptions{
		BaseURL: server.URL,
		TokenProvider: func(ctx context.Context) (string, error) {
			return "token_123", nil
		},
		HTTPClient: server.Client(),
	})
	err := client.UpsertPage(context.Background(), NotionUpsertRequest{
		WorkspaceID:      "ws_notion_http",
		Path:             "/notion/Doc.md",
		Revision:         "rev_1",
		ContentType:      "text/markdown",
		Content:          "# doc",
		ProviderObjectID: "obj_1",
		CorrelationID:    "corr_http_1",
	})
	if err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if capturedPath != "/v1/notion/pages/upsert" {
		t.Fatalf("expected upsert path, got %s", capturedPath)
	}
	if capturedAuth != "Bearer token_123" {
		t.Fatalf("expected bearer auth, got %q", capturedAuth)
	}
	if capturedVersion == "" {
		t.Fatalf("expected Notion-Version header")
	}
	if capturedBody["workspaceId"] != "ws_notion_http" {
		t.Fatalf("expected workspaceId in body, got %+v", capturedBody)
	}
}

func TestHTTPNotionWriteClientRetriesTransientFailure(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt32(&calls, 1)
		if current == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":"unavailable","message":"try again"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewHTTPNotionWriteClient(NotionHTTPClientOptions{
		BaseURL: server.URL,
		TokenProvider: func(ctx context.Context) (string, error) {
			return "token_123", nil
		},
		HTTPClient: server.Client(),
		BaseDelay:  5 * time.Millisecond,
		MaxDelay:   20 * time.Millisecond,
		MaxRetries: 2,
	})
	err := client.DeletePage(context.Background(), NotionDeleteRequest{
		WorkspaceID:      "ws_notion_http",
		Path:             "/notion/Doc.md",
		Revision:         "rev_1",
		ProviderObjectID: "obj_1",
		CorrelationID:    "corr_http_2",
	})
	if err != nil {
		t.Fatalf("expected retry to recover from transient failure, got %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected one retry, got %d calls", atomic.LoadInt32(&calls))
	}
}

func TestHTTPNotionWriteClientReturnsErrorOnPermanentFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_request","message":"bad payload"}`))
	}))
	defer server.Close()

	client := NewHTTPNotionWriteClient(NotionHTTPClientOptions{
		BaseURL: server.URL,
		TokenProvider: func(ctx context.Context) (string, error) {
			return "token_123", nil
		},
		HTTPClient: server.Client(),
	})
	err := client.UpsertPage(context.Background(), NotionUpsertRequest{
		WorkspaceID:      "ws_notion_http",
		Path:             "/notion/Doc.md",
		Revision:         "rev_1",
		ContentType:      "text/markdown",
		Content:          "# doc",
		ProviderObjectID: "obj_1",
		CorrelationID:    "corr_http_3",
	})
	if err == nil {
		t.Fatalf("expected permanent error")
	}
	if !strings.Contains(err.Error(), "invalid_request") {
		t.Fatalf("expected error to include response code, got %v", err)
	}
}
