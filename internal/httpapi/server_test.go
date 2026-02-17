package httpapi

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/relayfile"
)

func TestAuthRequired(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces/ws_1/fs/tree?path=/", nil)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestLifecycleAndConflicts(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	token := mustTestJWT(t, "dev-secret", "ws_1", "Worker1", []string{"fs:read", "fs:write", "ops:read", "sync:read"}, time.Now().Add(time.Hour))

	writeResp := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_1/fs/file?path=/notion/Engineering/Auth.md",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_1",
			"If-Match":        "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# v1",
		},
	})
	if writeResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on create write, got %d (%s)", writeResp.Code, writeResp.Body.String())
	}

	var queued relayfile.WriteResult
	if err := json.NewDecoder(writeResp.Body).Decode(&queued); err != nil {
		t.Fatalf("decode write response: %v", err)
	}

	readResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_1/fs/file?path=/notion/Engineering/Auth.md",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_2",
		},
	})
	if readResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on read, got %d (%s)", readResp.Code, readResp.Body.String())
	}
	var file relayfile.File
	if err := json.NewDecoder(readResp.Body).Decode(&file); err != nil {
		t.Fatalf("decode read response: %v", err)
	}

	staleResp := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_1/fs/file?path=/notion/Engineering/Auth.md",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_3",
			"If-Match":        "rev_stale",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# stale",
		},
	})
	if staleResp.Code != http.StatusConflict {
		t.Fatalf("expected 409 on stale write, got %d (%s)", staleResp.Code, staleResp.Body.String())
	}
	var stalePayload map[string]any
	if err := json.NewDecoder(staleResp.Body).Decode(&stalePayload); err != nil {
		t.Fatalf("decode stale payload: %v", err)
	}
	if stalePayload["expectedRevision"] == nil || stalePayload["currentRevision"] == nil {
		t.Fatalf("expected revision metadata in conflict payload, got %v", stalePayload)
	}
	if stalePayload["currentContentPreview"] == nil {
		t.Fatalf("expected currentContentPreview in conflict payload, got %v", stalePayload)
	}

	delResp := doRequest(t, server, request{
		method: http.MethodDelete,
		path:   "/v1/workspaces/ws_1/fs/file?path=/notion/Engineering/Auth.md",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_4",
			"If-Match":        file.Revision,
		},
	})
	if delResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on delete, got %d (%s)", delResp.Code, delResp.Body.String())
	}

	opResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_1/ops/" + queued.OpID,
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_5",
		},
	})
	if opResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on op status, got %d (%s)", opResp.Code, opResp.Body.String())
	}
	var opPayload map[string]any
	if err := json.NewDecoder(opResp.Body).Decode(&opPayload); err != nil {
		t.Fatalf("decode op response: %v", err)
	}
	if opPayload["action"] != "file_upsert" {
		t.Fatalf("expected op action file_upsert, got %v", opPayload["action"])
	}
	if opPayload["provider"] != "notion" {
		t.Fatalf("expected op provider notion, got %v", opPayload["provider"])
	}
	if opPayload["path"] != "/notion/Engineering/Auth.md" {
		t.Fatalf("expected op path to be set, got %v", opPayload["path"])
	}
	if opPayload["revision"] == nil || opPayload["revision"] == "" {
		t.Fatalf("expected op revision to be set, got %v", opPayload["revision"])
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		opsListResp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_1/ops?status=succeeded&limit=10",
			headers: map[string]string{
				"Authorization":   "Bearer " + token,
				"X-Correlation-Id": "corr_6",
			},
		})
		if opsListResp.Code != http.StatusOK {
			t.Fatalf("expected 200 on ops list, got %d (%s)", opsListResp.Code, opsListResp.Body.String())
		}
		var opsFeed map[string]any
		if err := json.NewDecoder(opsListResp.Body).Decode(&opsFeed); err != nil {
			t.Fatalf("decode ops list response: %v", err)
		}
		items, _ := opsFeed["items"].([]any)
		if len(items) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if time.Now().After(deadline) {
		t.Fatalf("expected ops list to include at least one succeeded operation")
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		opsDeleteListResp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_1/ops?status=succeeded&action=file_delete&limit=10",
			headers: map[string]string{
				"Authorization":   "Bearer " + token,
				"X-Correlation-Id": "corr_7",
			},
		})
		if opsDeleteListResp.Code != http.StatusOK {
			t.Fatalf("expected 200 on action-filtered ops list, got %d (%s)", opsDeleteListResp.Code, opsDeleteListResp.Body.String())
		}
		var deleteFeed relayfile.OperationFeed
		if err := json.NewDecoder(opsDeleteListResp.Body).Decode(&deleteFeed); err != nil {
			t.Fatalf("decode action-filtered ops list response: %v", err)
		}
		if len(deleteFeed.Items) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		for _, item := range deleteFeed.Items {
			if item.Action != "file_delete" {
				t.Fatalf("expected only file_delete actions, got %s", item.Action)
			}
		}
		return
	}
	t.Fatalf("expected at least one file_delete operation in filtered list")
}

func TestOpsListProviderFilterEndpoint(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		Adapters: []relayfile.ProviderAdapter{
			serverStaticAdapter{
				provider: "custom",
				actions: []relayfile.ApplyAction{
					{
						Type:             relayfile.ActionFileUpsert,
						Path:             "/custom/OpsProvider.md",
						Content:          "# custom",
						ContentType:      "text/markdown",
						ProviderObjectID: "custom_ops_provider_1",
					},
				},
			},
		},
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	ingestEnvelope := map[string]any{
		"envelopeId":    "env_ops_provider_api_1",
		"workspaceId":   "ws_ops_provider_api",
		"provider":      "custom",
		"deliveryId":    "delivery_ops_provider_api_1",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339Nano),
		"payload":       map[string]any{"type": "custom.upsert"},
		"correlationId": "corr_ops_provider_api_1",
	}
	body, err := json.Marshal(ingestEnvelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	sig := mustHMAC("dev-internal-secret", ts+"\n"+string(body))
	ingest := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_ops_provider_api_1",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": sig,
			"Content-Type":      "application/json",
		},
		body: body,
	})
	if ingest.Code != http.StatusAccepted {
		t.Fatalf("expected 202 custom ingest, got %d (%s)", ingest.Code, ingest.Body.String())
	}

	token := mustTestJWT(t, "dev-secret", "ws_ops_provider_api", "Worker1", []string{"fs:read", "fs:write", "ops:read"}, time.Now().Add(time.Hour))
	var customFile relayfile.File
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		readResp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_ops_provider_api/fs/file?path=/custom/OpsProvider.md",
			headers: map[string]string{
				"Authorization":   "Bearer " + token,
				"X-Correlation-Id": "corr_ops_provider_api_2",
			},
		})
		if readResp.Code == http.StatusOK {
			if err := json.NewDecoder(readResp.Body).Decode(&customFile); err != nil {
				t.Fatalf("decode custom file: %v", err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if customFile.Revision == "" {
		t.Fatalf("expected custom file to be available before provider filter test")
	}

	customWrite := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_ops_provider_api/fs/file?path=/custom/OpsProvider.md",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_ops_provider_api_3",
			"If-Match":        customFile.Revision,
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# custom updated",
		},
	})
	if customWrite.Code != http.StatusAccepted {
		t.Fatalf("expected custom write accepted, got %d (%s)", customWrite.Code, customWrite.Body.String())
	}

	notionWrite := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_ops_provider_api/fs/file?path=/notion/OpsProvider.md",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_ops_provider_api_4",
			"If-Match":        "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# notion",
		},
	})
	if notionWrite.Code != http.StatusAccepted {
		t.Fatalf("expected notion write accepted, got %d (%s)", notionWrite.Code, notionWrite.Body.String())
	}

	customOps := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_ops_provider_api/ops?provider=custom&limit=20",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_ops_provider_api_5",
		},
	})
	if customOps.Code != http.StatusOK {
		t.Fatalf("expected custom provider ops list 200, got %d (%s)", customOps.Code, customOps.Body.String())
	}
	var customFeed relayfile.OperationFeed
	if err := json.NewDecoder(customOps.Body).Decode(&customFeed); err != nil {
		t.Fatalf("decode custom provider feed: %v", err)
	}
	if len(customFeed.Items) == 0 {
		t.Fatalf("expected provider-filtered custom ops to be non-empty")
	}
	for _, item := range customFeed.Items {
		if item.Provider != "custom" {
			t.Fatalf("expected only custom provider ops, got %s", item.Provider)
		}
	}

	notionOps := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_ops_provider_api/ops?provider=notion&limit=20",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_ops_provider_api_6",
		},
	})
	if notionOps.Code != http.StatusOK {
		t.Fatalf("expected notion provider ops list 200, got %d (%s)", notionOps.Code, notionOps.Body.String())
	}
	var notionFeed relayfile.OperationFeed
	if err := json.NewDecoder(notionOps.Body).Decode(&notionFeed); err != nil {
		t.Fatalf("decode notion provider feed: %v", err)
	}
	if len(notionFeed.Items) == 0 {
		t.Fatalf("expected provider-filtered notion ops to be non-empty")
	}
	for _, item := range notionFeed.Items {
		if item.Provider != "notion" {
			t.Fatalf("expected only notion provider ops, got %s", item.Provider)
		}
	}
}

func TestEventsAndSyncStatus(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	token := mustTestJWT(t, "dev-secret", "ws_2", "Worker1", []string{"fs:read", "fs:write", "ops:read", "sync:read"}, time.Now().Add(time.Hour))

	_ = doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_2/fs/file?path=/notion/Product/Roadmap.md",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_evt_1",
			"If-Match":        "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# roadmap",
		},
	})

	eventsResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_2/fs/events?limit=10",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_evt_2",
		},
	})
	if eventsResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on events, got %d (%s)", eventsResp.Code, eventsResp.Body.String())
	}
	var feed relayfile.EventFeed
	if err := json.NewDecoder(eventsResp.Body).Decode(&feed); err != nil {
		t.Fatalf("decode events response: %v", err)
	}
	if len(feed.Events) == 0 {
		t.Fatalf("expected events to be present")
	}

	syncResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_2/sync/status",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_evt_3",
		},
	})
	if syncResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on sync status, got %d (%s)", syncResp.Code, syncResp.Body.String())
	}
}

func TestSyncStatusEndpointReportsLaggingProvider(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		DisableWorkers: true,
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	envelope := map[string]any{
		"envelopeId":    "env_sync_status_lag_api_1",
		"workspaceId":   "ws_sync_status_lag_api",
		"provider":      "notion",
		"deliveryId":    "delivery_sync_status_lag_api_1",
		"receivedAt":    time.Now().UTC().Add(-2 * time.Minute).Format(time.RFC3339Nano),
		"payload":       map[string]any{"type": "sync"},
		"correlationId": "corr_sync_status_lag_api_1",
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	sig := mustHMAC("dev-internal-secret", ts+"\n"+string(body))
	ingest := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_sync_status_lag_api_1",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": sig,
			"Content-Type":      "application/json",
		},
		body: body,
	})
	if ingest.Code != http.StatusAccepted {
		t.Fatalf("expected 202 ingest, got %d (%s)", ingest.Code, ingest.Body.String())
	}

	token := mustTestJWT(t, "dev-secret", "ws_sync_status_lag_api", "Worker1", []string{"sync:read"}, time.Now().Add(time.Hour))
	resp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_sync_status_lag_api/sync/status?provider=notion",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_sync_status_lag_api_2",
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 on sync status, got %d (%s)", resp.Code, resp.Body.String())
	}
	var payload relayfile.SyncStatus
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode sync status: %v", err)
	}
	if len(payload.Providers) != 1 {
		t.Fatalf("expected one provider, got %d", len(payload.Providers))
	}
	if payload.Providers[0].Status != "lagging" {
		t.Fatalf("expected lagging provider status, got %s", payload.Providers[0].Status)
	}
	if payload.Providers[0].LagSeconds < 100 {
		t.Fatalf("expected lagSeconds >= 100, got %d", payload.Providers[0].LagSeconds)
	}
}

func TestSyncStatusEndpointReportsErrorFromDeadLetter(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		MaxEnvelopeAttempts: 1,
		Adapters: []relayfile.ProviderAdapter{
			serverFailingAdapter{provider: "custom"},
		},
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	envelope := map[string]any{
		"envelopeId":    "env_sync_status_error_api_1",
		"workspaceId":   "ws_sync_status_error_api",
		"provider":      "custom",
		"deliveryId":    "delivery_sync_status_error_api_1",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339Nano),
		"payload":       map[string]any{"type": "custom.dead"},
		"correlationId": "corr_sync_status_error_api_1",
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	sig := mustHMAC("dev-internal-secret", ts+"\n"+string(body))
	ingest := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_sync_status_error_api_1",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": sig,
			"Content-Type":      "application/json",
		},
		body: body,
	})
	if ingest.Code != http.StatusAccepted {
		t.Fatalf("expected 202 ingest, got %d (%s)", ingest.Code, ingest.Body.String())
	}

	token := mustTestJWT(t, "dev-secret", "ws_sync_status_error_api", "Worker1", []string{"sync:read"}, time.Now().Add(time.Hour))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_sync_status_error_api/sync/status?provider=custom",
			headers: map[string]string{
				"Authorization":   "Bearer " + token,
				"X-Correlation-Id": "corr_sync_status_error_api_2",
			},
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200 on sync status, got %d (%s)", resp.Code, resp.Body.String())
		}
		var payload relayfile.SyncStatus
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			t.Fatalf("decode sync status: %v", err)
		}
		if len(payload.Providers) == 1 && payload.Providers[0].Status == "error" {
			if payload.Providers[0].LastError == nil || *payload.Providers[0].LastError == "" {
				t.Fatalf("expected lastError for provider status=error")
			}
			if payload.Providers[0].FailureCodes["unknown"] < 1 {
				t.Fatalf("expected failureCodes aggregation for provider status=error, got %+v", payload.Providers[0].FailureCodes)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected provider sync status to transition to error from dead-letter")
}

func TestScopeAndWorkspaceClaimsEnforced(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	readOnlyToken := mustTestJWT(t, "dev-secret", "ws_3", "Worker1", []string{"fs:read"}, time.Now().Add(time.Hour))

	deniedWrite := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_3/fs/file?path=/notion/X.md",
		headers: map[string]string{
			"Authorization":   "Bearer " + readOnlyToken,
			"X-Correlation-Id": "corr_scope_1",
			"If-Match":        "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# denied",
		},
	})
	if deniedWrite.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing fs:write, got %d (%s)", deniedWrite.Code, deniedWrite.Body.String())
	}

	wrongWorkspaceToken := mustTestJWT(t, "dev-secret", "ws_other", "Worker1", []string{"fs:read"}, time.Now().Add(time.Hour))
	wrongWorkspace := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_3/fs/tree?path=/",
		headers: map[string]string{
			"Authorization":   "Bearer " + wrongWorkspaceToken,
			"X-Correlation-Id": "corr_scope_2",
		},
	})
	if wrongWorkspace.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for workspace mismatch, got %d (%s)", wrongWorkspace.Code, wrongWorkspace.Body.String())
	}

	wrongAudience := mustTestJWTWithAudience(
		t,
		"dev-secret",
		"ws_3",
		"Worker1",
		[]string{"fs:read"},
		"time-travel",
		time.Now().Add(time.Hour),
	)
	badAudResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_3/fs/tree?path=/",
		headers: map[string]string{
			"Authorization":   "Bearer " + wrongAudience,
			"X-Correlation-Id": "corr_scope_3",
		},
	})
	if badAudResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid audience, got %d (%s)", badAudResp.Code, badAudResp.Body.String())
	}
}

func TestInternalWebhookIngressHMAC(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	body := map[string]any{
		"envelopeId":    "env_1",
		"workspaceId":   "ws_4",
		"provider":      "notion",
		"deliveryId":    "del_1",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"type": "sync"},
		"correlationId": "corr_internal_1",
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	sig := mustHMAC("dev-internal-secret", ts+"\n"+string(bodyBytes))

	okResp := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_internal_1",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": sig,
			"Content-Type":      "application/json",
		},
		body: bodyBytes,
	})
	if okResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for valid internal ingress, got %d (%s)", okResp.Code, okResp.Body.String())
	}

	badResp := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_internal_2",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": "bad_signature",
			"Content-Type":      "application/json",
		},
		body: bodyBytes,
	})
	if badResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid internal signature, got %d (%s)", badResp.Code, badResp.Body.String())
	}

	staleTs := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	staleSig := mustHMAC("dev-internal-secret", staleTs+"\n"+string(bodyBytes))
	staleResp := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_internal_3",
			"X-Relay-Timestamp": staleTs,
			"X-Relay-Signature": staleSig,
			"Content-Type":      "application/json",
		},
		body: bodyBytes,
	})
	if staleResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for stale internal timestamp, got %d (%s)", staleResp.Code, staleResp.Body.String())
	}
}

func TestInternalWebhookIngressQueueFull(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		DisableWorkers:    true,
		EnvelopeQueueSize: 1,
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	first := map[string]any{
		"envelopeId":    "env_qf_1",
		"workspaceId":   "ws_qf",
		"provider":      "notion",
		"deliveryId":    "delivery_qf_1",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"type": "sync"},
		"correlationId": "corr_qf_1",
	}
	firstBody, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first envelope: %v", err)
	}
	firstTs := time.Now().UTC().Format(time.RFC3339)
	firstSig := mustHMAC("dev-internal-secret", firstTs+"\n"+string(firstBody))
	firstResp := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_qf_1",
			"X-Relay-Timestamp": firstTs,
			"X-Relay-Signature": firstSig,
			"Content-Type":      "application/json",
		},
		body: firstBody,
	})
	if firstResp.Code != http.StatusAccepted {
		t.Fatalf("expected first ingress accepted, got %d (%s)", firstResp.Code, firstResp.Body.String())
	}

	second := map[string]any{
		"envelopeId":    "env_qf_2",
		"workspaceId":   "ws_qf",
		"provider":      "notion",
		"deliveryId":    "delivery_qf_2",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"type": "sync"},
		"correlationId": "corr_qf_2",
	}
	secondBody, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second envelope: %v", err)
	}
	secondTs := time.Now().UTC().Format(time.RFC3339)
	secondSig := mustHMAC("dev-internal-secret", secondTs+"\n"+string(secondBody))
	secondResp := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_qf_2",
			"X-Relay-Timestamp": secondTs,
			"X-Relay-Signature": secondSig,
			"Content-Type":      "application/json",
		},
		body: secondBody,
	})
	if secondResp.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 when ingress queue is full, got %d (%s)", secondResp.Code, secondResp.Body.String())
	}
	var errPayload map[string]any
	if err := json.NewDecoder(secondResp.Body).Decode(&errPayload); err != nil {
		t.Fatalf("decode queue-full response: %v", err)
	}
	if errPayload["code"] != "queue_full" {
		t.Fatalf("expected queue_full code, got %v", errPayload["code"])
	}
}

func TestSyncIngressStatusEndpoint(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		DisableWorkers:    true,
		EnvelopeQueueSize: 1,
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	first := map[string]any{
		"envelopeId":    "env_stats_1",
		"workspaceId":   "ws_stats",
		"provider":      "notion",
		"deliveryId":    "delivery_stats_1",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"type": "sync"},
		"correlationId": "corr_stats_1",
	}
	firstBody, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first envelope: %v", err)
	}
	firstTs := time.Now().UTC().Format(time.RFC3339)
	firstSig := mustHMAC("dev-internal-secret", firstTs+"\n"+string(firstBody))
	_ = doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_stats_1",
			"X-Relay-Timestamp": firstTs,
			"X-Relay-Signature": firstSig,
			"Content-Type":      "application/json",
		},
		body: firstBody,
	})

	second := map[string]any{
		"envelopeId":    "env_stats_2",
		"workspaceId":   "ws_stats",
		"provider":      "notion",
		"deliveryId":    "delivery_stats_2",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"type": "sync"},
		"correlationId": "corr_stats_2",
	}
	secondBody, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second envelope: %v", err)
	}
	secondTs := time.Now().UTC().Format(time.RFC3339)
	secondSig := mustHMAC("dev-internal-secret", secondTs+"\n"+string(secondBody))
	_ = doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_stats_2",
			"X-Relay-Timestamp": secondTs,
			"X-Relay-Signature": secondSig,
			"Content-Type":      "application/json",
		},
		body: secondBody,
	})

	token := mustTestJWT(t, "dev-secret", "ws_stats", "Worker1", []string{"sync:read"}, time.Now().Add(time.Hour))
	resp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_stats/sync/ingress",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_stats_3",
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for sync ingress status, got %d (%s)", resp.Code, resp.Body.String())
	}
	var status relayfile.IngressStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode ingress status: %v", err)
	}
	if status.WorkspaceID != "ws_stats" {
		t.Fatalf("expected workspace ws_stats, got %s", status.WorkspaceID)
	}
	if status.QueueCapacity != 1 || status.QueueDepth != 1 {
		t.Fatalf("expected queue depth/capacity 1/1, got %d/%d", status.QueueDepth, status.QueueCapacity)
	}
	if status.QueueUtilization != 1 {
		t.Fatalf("expected queue utilization 1, got %f", status.QueueUtilization)
	}
	if status.PendingTotal != 1 {
		t.Fatalf("expected one pending envelope, got %d", status.PendingTotal)
	}
	if status.DeadLetterTotal != 0 {
		t.Fatalf("expected zero dead letters, got %d", status.DeadLetterTotal)
	}
	if len(status.DeadLetterByProvider) != 0 {
		t.Fatalf("expected empty dead-letter breakdown, got %+v", status.DeadLetterByProvider)
	}
	if status.AcceptedTotal != 1 || status.DroppedTotal != 1 || status.DedupedTotal != 0 || status.CoalescedTotal != 0 || status.SuppressedTotal != 0 || status.StaleTotal != 0 {
		t.Fatalf("unexpected counters: %+v", status)
	}
	if status.DedupeRate != 0 || status.CoalesceRate != 0 {
		t.Fatalf("expected zero dedupe/coalesce rates, got dedupe=%f coalesce=%f", status.DedupeRate, status.CoalesceRate)
	}
	notionIngress, ok := status.IngressByProvider["notion"]
	if !ok || notionIngress.AcceptedTotal != 1 || notionIngress.DroppedTotal != 1 || notionIngress.PendingTotal != 1 {
		t.Fatalf("expected notion ingress breakdown accepted=1 dropped=1, got %+v", status.IngressByProvider)
	}
}

func TestSyncDeadLetterEndpoint(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		MaxEnvelopeAttempts: 1,
		Adapters: []relayfile.ProviderAdapter{
			serverFailingAdapter{provider: "custom"},
		},
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	envelope := map[string]any{
		"envelopeId":    "env_dead_api_1",
		"workspaceId":   "ws_dead_api",
		"provider":      "custom",
		"deliveryId":    "delivery_dead_api_1",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"type": "custom.dead"},
		"correlationId": "corr_dead_api_1",
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	sig := mustHMAC("dev-internal-secret", ts+"\n"+string(body))
	ingest := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_dead_api_1",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": sig,
			"Content-Type":      "application/json",
		},
		body: body,
	})
	if ingest.Code != http.StatusAccepted {
		t.Fatalf("expected 202 ingest, got %d (%s)", ingest.Code, ingest.Body.String())
	}

	token := mustTestJWT(t, "dev-secret", "ws_dead_api", "Worker1", []string{"sync:read"}, time.Now().Add(time.Hour))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_dead_api/sync/dead-letter?limit=10",
			headers: map[string]string{
				"Authorization":   "Bearer " + token,
				"X-Correlation-Id": "corr_dead_api_2",
			},
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200 on dead-letter endpoint, got %d (%s)", resp.Code, resp.Body.String())
		}
		var feed relayfile.DeadLetterFeed
		if err := json.NewDecoder(resp.Body).Decode(&feed); err != nil {
			t.Fatalf("decode dead-letter feed: %v", err)
		}
		if len(feed.Items) == 1 {
			if feed.Items[0].EnvelopeID != "env_dead_api_1" {
				t.Fatalf("unexpected dead-letter envelope id: %s", feed.Items[0].EnvelopeID)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected dead-letter endpoint to return one failed envelope")
}

func TestSyncDeadLetterItemEndpoint(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		MaxEnvelopeAttempts: 1,
		Adapters: []relayfile.ProviderAdapter{
			serverFailingAdapter{provider: "custom"},
		},
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	envelope := map[string]any{
		"envelopeId":    "env_dead_item_api_1",
		"workspaceId":   "ws_dead_item_api",
		"provider":      "custom",
		"deliveryId":    "delivery_dead_item_api_1",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"type": "custom.dead"},
		"correlationId": "corr_dead_item_api_1",
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	sig := mustHMAC("dev-internal-secret", ts+"\n"+string(body))
	ingest := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_dead_item_api_1",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": sig,
			"Content-Type":      "application/json",
		},
		body: body,
	})
	if ingest.Code != http.StatusAccepted {
		t.Fatalf("expected 202 ingest, got %d (%s)", ingest.Code, ingest.Body.String())
	}

	token := mustTestJWT(t, "dev-secret", "ws_dead_item_api", "Worker1", []string{"sync:read"}, time.Now().Add(time.Hour))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_dead_item_api/sync/dead-letter/env_dead_item_api_1",
			headers: map[string]string{
				"Authorization":   "Bearer " + token,
				"X-Correlation-Id": "corr_dead_item_api_2",
			},
		})
		if resp.Code == http.StatusOK {
			var item relayfile.EnvelopeDeadLetter
			if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
				t.Fatalf("decode dead-letter item: %v", err)
			}
			if item.EnvelopeID != "env_dead_item_api_1" {
				t.Fatalf("unexpected envelope id: %s", item.EnvelopeID)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected dead-letter item endpoint to return failed envelope")
}

func TestSyncDeadLetterEndpointProviderFilter(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		MaxEnvelopeAttempts: 1,
		Adapters: []relayfile.ProviderAdapter{
			serverFailingAdapter{provider: "custom"},
			serverFailingAdapter{provider: "custom2"},
		},
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	envelopes := []map[string]any{
		{
			"envelopeId":    "env_dead_filter_api_1",
			"workspaceId":   "ws_dead_filter_api",
			"provider":      "custom",
			"deliveryId":    "delivery_dead_filter_api_1",
			"receivedAt":    time.Now().UTC().Format(time.RFC3339),
			"payload":       map[string]any{"type": "custom.dead"},
			"correlationId": "corr_dead_filter_api_1",
		},
		{
			"envelopeId":    "env_dead_filter_api_2",
			"workspaceId":   "ws_dead_filter_api",
			"provider":      "custom2",
			"deliveryId":    "delivery_dead_filter_api_2",
			"receivedAt":    time.Now().UTC().Format(time.RFC3339),
			"payload":       map[string]any{"type": "custom2.dead"},
			"correlationId": "corr_dead_filter_api_2",
		},
	}
	for _, envelope := range envelopes {
		body, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		ts := time.Now().UTC().Format(time.RFC3339)
		sig := mustHMAC("dev-internal-secret", ts+"\n"+string(body))
		ingest := doRawRequest(t, server, rawRequest{
			method: http.MethodPost,
			path:   "/v1/internal/webhook-envelopes",
			headers: map[string]string{
				"X-Correlation-Id": envelope["correlationId"].(string),
				"X-Relay-Timestamp": ts,
				"X-Relay-Signature": sig,
				"Content-Type":      "application/json",
			},
			body: body,
		})
		if ingest.Code != http.StatusAccepted {
			t.Fatalf("expected 202 ingest, got %d (%s)", ingest.Code, ingest.Body.String())
		}
	}

	token := mustTestJWT(t, "dev-secret", "ws_dead_filter_api", "Worker1", []string{"sync:read"}, time.Now().Add(time.Hour))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_dead_filter_api/sync/dead-letter?provider=custom2&limit=10",
			headers: map[string]string{
				"Authorization":   "Bearer " + token,
				"X-Correlation-Id": "corr_dead_filter_api_3",
			},
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200 dead-letter list, got %d (%s)", resp.Code, resp.Body.String())
		}
		var feed relayfile.DeadLetterFeed
		if err := json.NewDecoder(resp.Body).Decode(&feed); err != nil {
			t.Fatalf("decode dead-letter feed: %v", err)
		}
		if len(feed.Items) == 1 {
			if feed.Items[0].Provider != "custom2" {
				t.Fatalf("expected provider custom2, got %s", feed.Items[0].Provider)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected provider-filtered dead-letter endpoint result")
}

func TestSyncDeadLetterReplayEndpoint(t *testing.T) {
	var allowReplay atomic.Bool
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		MaxEnvelopeAttempts: 1,
		Adapters: []relayfile.ProviderAdapter{
			serverToggleAdapter{provider: "custom", allowReplay: &allowReplay},
		},
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	envelope := map[string]any{
		"envelopeId":    "env_dead_replay_api_1",
		"workspaceId":   "ws_dead_replay_api",
		"provider":      "custom",
		"deliveryId":    "delivery_dead_replay_api_1",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"type": "custom.dead"},
		"correlationId": "corr_dead_replay_api_1",
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	sig := mustHMAC("dev-internal-secret", ts+"\n"+string(body))
	ingest := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_dead_replay_api_1",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": sig,
			"Content-Type":      "application/json",
		},
		body: body,
	})
	if ingest.Code != http.StatusAccepted {
		t.Fatalf("expected 202 ingest, got %d (%s)", ingest.Code, ingest.Body.String())
	}

	readToken := mustTestJWT(t, "dev-secret", "ws_dead_replay_api", "Worker1", []string{"sync:read", "fs:read"}, time.Now().Add(time.Hour))
	replayToken := mustTestJWT(t, "dev-secret", "ws_dead_replay_api", "Worker1", []string{"sync:trigger"}, time.Now().Add(time.Hour))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_dead_replay_api/sync/dead-letter?limit=10",
			headers: map[string]string{
				"Authorization":   "Bearer " + readToken,
				"X-Correlation-Id": "corr_dead_replay_api_2",
			},
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("expected 200 listing dead letters, got %d (%s)", resp.Code, resp.Body.String())
		}
		var feed relayfile.DeadLetterFeed
		if err := json.NewDecoder(resp.Body).Decode(&feed); err != nil {
			t.Fatalf("decode dead-letter feed: %v", err)
		}
		if len(feed.Items) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	deniedReplay := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_dead_replay_api/sync/dead-letter/env_dead_replay_api_1/replay",
		headers: map[string]string{
			"Authorization":   "Bearer " + readToken,
			"X-Correlation-Id": "corr_dead_replay_api_denied",
		},
	})
	if deniedReplay.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when missing sync:trigger, got %d (%s)", deniedReplay.Code, deniedReplay.Body.String())
	}

	allowReplay.Store(true)
	replay := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_dead_replay_api/sync/dead-letter/env_dead_replay_api_1/replay",
		headers: map[string]string{
			"Authorization":   "Bearer " + replayToken,
			"X-Correlation-Id": "corr_dead_replay_api_3",
		},
	})
	if replay.Code != http.StatusAccepted {
		t.Fatalf("expected 202 replay endpoint, got %d (%s)", replay.Code, replay.Body.String())
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		readResp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_dead_replay_api/fs/file?path=/custom/Replayed.md",
			headers: map[string]string{
				"Authorization":   "Bearer " + readToken,
				"X-Correlation-Id": "corr_dead_replay_api_4",
			},
		})
		if readResp.Code == http.StatusOK {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected replayed dead-letter envelope to apply file update")
}

func TestSyncDeadLetterAckEndpoint(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		MaxEnvelopeAttempts: 1,
		Adapters: []relayfile.ProviderAdapter{
			serverFailingAdapter{provider: "custom"},
		},
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	envelope := map[string]any{
		"envelopeId":    "env_dead_ack_api_1",
		"workspaceId":   "ws_dead_ack_api",
		"provider":      "custom",
		"deliveryId":    "delivery_dead_ack_api_1",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"type": "custom.dead"},
		"correlationId": "corr_dead_ack_api_1",
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	sig := mustHMAC("dev-internal-secret", ts+"\n"+string(body))
	ingest := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_dead_ack_api_1",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": sig,
			"Content-Type":      "application/json",
		},
		body: body,
	})
	if ingest.Code != http.StatusAccepted {
		t.Fatalf("expected 202 ingest, got %d (%s)", ingest.Code, ingest.Body.String())
	}

	readToken := mustTestJWT(t, "dev-secret", "ws_dead_ack_api", "Worker1", []string{"sync:read"}, time.Now().Add(time.Hour))
	triggerToken := mustTestJWT(t, "dev-secret", "ws_dead_ack_api", "Worker1", []string{"sync:trigger"}, time.Now().Add(time.Hour))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_dead_ack_api/sync/dead-letter?limit=10",
			headers: map[string]string{
				"Authorization":   "Bearer " + readToken,
				"X-Correlation-Id": "corr_dead_ack_api_2",
			},
		})
		if resp.Code == http.StatusOK {
			var feed relayfile.DeadLetterFeed
			if err := json.NewDecoder(resp.Body).Decode(&feed); err == nil && len(feed.Items) == 1 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	deniedAck := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_dead_ack_api/sync/dead-letter/env_dead_ack_api_1/ack",
		headers: map[string]string{
			"Authorization":   "Bearer " + readToken,
			"X-Correlation-Id": "corr_dead_ack_api_denied",
		},
	})
	if deniedAck.Code != http.StatusForbidden {
		t.Fatalf("expected 403 ack without sync:trigger, got %d (%s)", deniedAck.Code, deniedAck.Body.String())
	}

	ackResp := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_dead_ack_api/sync/dead-letter/env_dead_ack_api_1/ack",
		headers: map[string]string{
			"Authorization":   "Bearer " + triggerToken,
			"X-Correlation-Id": "corr_dead_ack_api_3",
		},
	})
	if ackResp.Code != http.StatusOK {
		t.Fatalf("expected 200 ack response, got %d (%s)", ackResp.Code, ackResp.Body.String())
	}

	afterResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_dead_ack_api/sync/dead-letter?limit=10",
		headers: map[string]string{
			"Authorization":   "Bearer " + readToken,
			"X-Correlation-Id": "corr_dead_ack_api_4",
		},
	})
	if afterResp.Code != http.StatusOK {
		t.Fatalf("expected 200 dead-letter list after ack, got %d (%s)", afterResp.Code, afterResp.Body.String())
	}
	var feed relayfile.DeadLetterFeed
	if err := json.NewDecoder(afterResp.Body).Decode(&feed); err != nil {
		t.Fatalf("decode dead-letter feed after ack: %v", err)
	}
	if len(feed.Items) != 0 {
		t.Fatalf("expected no dead letters after ack, got %d", len(feed.Items))
	}
}

func TestWorkspaceOperationReplayEndpoint(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		MaxWritebackAttempts: 1,
		WritebackDelay:       5 * time.Millisecond,
		ProviderWrite: func(workspaceID, path, revision string) error {
			return fmt.Errorf("forced writeback failure")
		},
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	writeToken := mustTestJWT(t, "dev-secret", "ws_ops_replay", "Worker1", []string{"fs:write", "ops:read", "ops:replay"}, time.Now().Add(time.Hour))
	writeResp := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_ops_replay/fs/file?path=/notion/ReplayOp.md",
		headers: map[string]string{
			"Authorization":   "Bearer " + writeToken,
			"X-Correlation-Id": "corr_ops_replay_1",
			"If-Match":        "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# replay-op",
		},
	})
	if writeResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on write, got %d (%s)", writeResp.Code, writeResp.Body.String())
	}
	var queued relayfile.WriteResult
	if err := json.NewDecoder(writeResp.Body).Decode(&queued); err != nil {
		t.Fatalf("decode queued write: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		opResp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_ops_replay/ops/" + queued.OpID,
			headers: map[string]string{
				"Authorization":   "Bearer " + writeToken,
				"X-Correlation-Id": "corr_ops_replay_2",
			},
		})
		if opResp.Code == http.StatusOK {
			var op relayfile.OperationStatus
			if err := json.NewDecoder(opResp.Body).Decode(&op); err == nil && op.Status == "dead_lettered" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	replayResp := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_ops_replay/ops/" + queued.OpID + "/replay",
		headers: map[string]string{
			"Authorization":   "Bearer " + writeToken,
			"X-Correlation-Id": "corr_ops_replay_3",
		},
	})
	if replayResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on workspace op replay, got %d (%s)", replayResp.Code, replayResp.Body.String())
	}

	readOnlyToken := mustTestJWT(t, "dev-secret", "ws_ops_replay", "Worker1", []string{"ops:read"}, time.Now().Add(time.Hour))
	denied := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_ops_replay/ops/" + queued.OpID + "/replay",
		headers: map[string]string{
			"Authorization":   "Bearer " + readOnlyToken,
			"X-Correlation-Id": "corr_ops_replay_4",
		},
	})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when ops:replay is missing, got %d (%s)", denied.Code, denied.Body.String())
	}
}

func TestWorkspaceOperationReplayEndpointRejectsNonDeadLetteredOp(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	token := mustTestJWT(t, "dev-secret", "ws_ops_replay_state", "Worker1", []string{"fs:write", "ops:replay", "ops:read"}, time.Now().Add(time.Hour))

	writeResp := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_ops_replay_state/fs/file?path=/notion/ReplayState.md",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_ops_replay_state_1",
			"If-Match":        "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# replay-state",
		},
	})
	if writeResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on write, got %d (%s)", writeResp.Code, writeResp.Body.String())
	}
	var queued relayfile.WriteResult
	if err := json.NewDecoder(writeResp.Body).Decode(&queued); err != nil {
		t.Fatalf("decode queued write: %v", err)
	}

	waitDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(waitDeadline) {
		opResp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_ops_replay_state/ops/" + queued.OpID,
			headers: map[string]string{
				"Authorization":   "Bearer " + token,
				"X-Correlation-Id": "corr_ops_replay_state_2",
			},
		})
		if opResp.Code == http.StatusOK {
			var op relayfile.OperationStatus
			if err := json.NewDecoder(opResp.Body).Decode(&op); err == nil && op.Status == "succeeded" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	replayResp := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_ops_replay_state/ops/" + queued.OpID + "/replay",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_ops_replay_state_3",
		},
	})
	if replayResp.Code != http.StatusConflict {
		t.Fatalf("expected 409 for replaying non-dead-letter op, got %d (%s)", replayResp.Code, replayResp.Body.String())
	}
}

func TestListEndpointsRejectInvalidCursor(t *testing.T) {
	server := NewServer(relayfile.NewStore())

	opsToken := mustTestJWT(t, "dev-secret", "ws_cursor", "Worker1", []string{"ops:read"}, time.Now().Add(time.Hour))
	opsResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_cursor/ops?cursor=op_missing",
		headers: map[string]string{
			"Authorization":   "Bearer " + opsToken,
			"X-Correlation-Id": "corr_cursor_1",
		},
	})
	if opsResp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid ops cursor, got %d (%s)", opsResp.Code, opsResp.Body.String())
	}

	deadResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_cursor/sync/dead-letter?cursor=env_missing",
		headers: map[string]string{
			"Authorization":   "Bearer " + mustTestJWT(t, "dev-secret", "ws_cursor", "Worker1", []string{"sync:read"}, time.Now().Add(time.Hour)),
			"X-Correlation-Id": "corr_cursor_2",
		},
	})
	if deadResp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid dead-letter cursor, got %d (%s)", deadResp.Code, deadResp.Body.String())
	}
}

func TestAdminReplayAndSyncRefresh(t *testing.T) {
	server := NewServer(relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		MaxWritebackAttempts: 1,
		WritebackDelay:       5 * time.Millisecond,
		ProviderWrite: func(workspaceID, path, revision string) error {
			return fmt.Errorf("forced dead-letter for replay")
		},
	}))

	// Seed an envelope through internal ingress.
	envelope := map[string]any{
		"envelopeId":    "env_admin_1",
		"workspaceId":   "ws_admin",
		"provider":      "notion",
		"deliveryId":    "delivery_admin_1",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"type": "sync"},
		"correlationId": "corr_admin_internal_1",
	}
	envelopeBytes, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	sig := mustHMAC("dev-internal-secret", ts+"\n"+string(envelopeBytes))

	seed := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_admin_internal_1",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": sig,
			"Content-Type":      "application/json",
		},
		body: envelopeBytes,
	})
	if seed.Code != http.StatusAccepted {
		t.Fatalf("expected 202 seeding envelope, got %d (%s)", seed.Code, seed.Body.String())
	}

	adminToken := mustTestJWT(
		t,
		"dev-secret",
		"ws_admin",
		"Lead",
		[]string{"admin:replay", "sync:trigger", "sync:read", "fs:read", "ops:read"},
		time.Now().Add(time.Hour),
	)
	replay := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/admin/replay/envelopes/env_admin_1",
		headers: map[string]string{
			"Authorization":   "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_1",
		},
	})
	if replay.Code != http.StatusAccepted {
		t.Fatalf("expected 202 replay envelope, got %d (%s)", replay.Code, replay.Body.String())
	}

	noAdmin := mustTestJWT(
		t,
		"dev-secret",
		"ws_admin",
		"Lead",
		[]string{"fs:read"},
		time.Now().Add(time.Hour),
	)
	replayDenied := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/admin/replay/envelopes/env_admin_1",
		headers: map[string]string{
			"Authorization":   "Bearer " + noAdmin,
			"X-Correlation-Id": "corr_admin_2",
		},
	})
	if replayDenied.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing admin:replay scope, got %d (%s)", replayDenied.Code, replayDenied.Body.String())
	}

	syncRefresh := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_admin/sync/refresh",
		headers: map[string]string{
			"Authorization":   "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_3",
		},
		body: map[string]any{
			"provider": "notion",
			"reason":   "manual",
		},
	})
	if syncRefresh.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for sync refresh, got %d (%s)", syncRefresh.Code, syncRefresh.Body.String())
	}

	write := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_admin/fs/file?path=/notion/Admin.md",
		headers: map[string]string{
			"Authorization":   "Bearer " + mustTestJWT(t, "dev-secret", "ws_admin", "Lead", []string{"fs:write"}, time.Now().Add(time.Hour)),
			"X-Correlation-Id": "corr_admin_4",
			"If-Match":        "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# admin",
		},
	})
	if write.Code != http.StatusAccepted {
		t.Fatalf("expected 202 creating op for replay, got %d (%s)", write.Code, write.Body.String())
	}
	var queued relayfile.WriteResult
	if err := json.NewDecoder(write.Body).Decode(&queued); err != nil {
		t.Fatalf("decode write queued: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		opResp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_admin/ops/" + queued.OpID,
			headers: map[string]string{
				"Authorization":   "Bearer " + adminToken,
				"X-Correlation-Id": "corr_admin_4b",
			},
		})
		if opResp.Code == http.StatusOK {
			var op relayfile.OperationStatus
			if err := json.NewDecoder(opResp.Body).Decode(&op); err == nil && op.Status == "dead_lettered" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	replayOp := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/admin/replay/ops/" + queued.OpID,
		headers: map[string]string{
			"Authorization":   "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_5",
		},
	})
	if replayOp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 replay op, got %d (%s)", replayOp.Code, replayOp.Body.String())
	}
}

func TestInternalIngressAppliesToFilesystemAPI(t *testing.T) {
	server := NewServer(relayfile.NewStore())

	envelope := map[string]any{
		"envelopeId":  "env_apply_api_1",
		"workspaceId": "ws_apply_api",
		"provider":    "notion",
		"deliveryId":  "delivery_apply_api_1",
		"receivedAt":  time.Now().UTC().Format(time.RFC3339),
		"payload": map[string]any{
			"type":     "notion.page.upsert",
			"objectId": "notion_obj_api_1",
			"path":     "/notion/Engineering/Ingress.md",
			"content":  "# ingress",
		},
		"correlationId": "corr_apply_api_1",
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	sig := mustHMAC("dev-internal-secret", ts+"\n"+string(body))

	ingestResp := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id": "corr_apply_api_1",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": sig,
			"Content-Type":      "application/json",
		},
		body: body,
	})
	if ingestResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 ingest, got %d (%s)", ingestResp.Code, ingestResp.Body.String())
	}

	readToken := mustTestJWT(t, "dev-secret", "ws_apply_api", "Worker1", []string{"fs:read"}, time.Now().Add(time.Hour))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		readResp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_apply_api/fs/file?path=/notion/Engineering/Ingress.md",
			headers: map[string]string{
				"Authorization":   "Bearer " + readToken,
				"X-Correlation-Id": "corr_apply_api_2",
			},
		})
		if readResp.Code == http.StatusOK {
			var file relayfile.File
			if err := json.NewDecoder(readResp.Body).Decode(&file); err != nil {
				t.Fatalf("decode file: %v", err)
			}
			if file.Content == "# ingress" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected ingested envelope to be visible via fs read API")
}

func TestRateLimitingByWorkspaceAndAgent(t *testing.T) {
	server := NewServerWithConfig(relayfile.NewStore(), ServerConfig{
		JWTSecret:          "dev-secret",
		InternalHMACSecret: "dev-internal-secret",
		RateLimitMax:       2,
		RateLimitWindow:    time.Minute,
	})
	token := mustTestJWT(t, "dev-secret", "ws_rate", "Worker1", []string{"fs:read"}, time.Now().Add(time.Hour))

	for i := 0; i < 2; i++ {
		resp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_rate/fs/tree?path=/",
			headers: map[string]string{
				"Authorization":   "Bearer " + token,
				"X-Correlation-Id": fmt.Sprintf("corr_rate_%d", i),
			},
		})
		if resp.Code != http.StatusOK {
			t.Fatalf("expected request %d to be allowed, got %d (%s)", i, resp.Code, resp.Body.String())
		}
	}

	denied := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_rate/fs/tree?path=/",
		headers: map[string]string{
			"Authorization":   "Bearer " + token,
			"X-Correlation-Id": "corr_rate_denied",
		},
	})
	if denied.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after rate limit exceeded, got %d (%s)", denied.Code, denied.Body.String())
	}
}

type request struct {
	method  string
	path    string
	headers map[string]string
	body    map[string]any
}

type rawRequest struct {
	method  string
	path    string
	headers map[string]string
	body    []byte
}

func doRequest(t *testing.T, server http.Handler, r request) *httptest.ResponseRecorder {
	t.Helper()
	var bodyBytes []byte
	if r.body != nil {
		data, err := json.Marshal(r.body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyBytes = data
	}
	req := httptest.NewRequest(r.method, r.path, bytes.NewReader(bodyBytes))
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func doRawRequest(t *testing.T, server http.Handler, r rawRequest) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(r.method, r.path, bytes.NewReader(r.body))
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func mustTestJWT(t *testing.T, secret, workspaceID, agentName string, scopes []string, exp time.Time) string {
	return mustTestJWTWithAudience(t, secret, workspaceID, agentName, scopes, "relayfile", exp)
}

func mustTestJWTWithAudience(t *testing.T, secret, workspaceID, agentName string, scopes []string, aud string, exp time.Time) string {
	t.Helper()
	headerBytes, err := json.Marshal(map[string]any{
		"alg": "HS256",
		"typ": "JWT",
	})
	if err != nil {
		t.Fatalf("marshal jwt header: %v", err)
	}
	payloadBytes, err := json.Marshal(map[string]any{
		"workspace_id": workspaceID,
		"agent_name":   agentName,
		"scopes":       scopes,
		"exp":          exp.Unix(),
		"aud":          aud,
	})
	if err != nil {
		t.Fatalf("marshal jwt payload: %v", err)
	}
	h := base64.RawURLEncoding.EncodeToString(headerBytes)
	p := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signingInput := h + "." + p
	sig := mustHMAC(secret, signingInput)
	sigBytes, err := hexToBytes(sig)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	jwtSig := base64.RawURLEncoding.EncodeToString(sigBytes)
	return signingInput + "." + jwtSig
}

func mustHMAC(secret, data string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(data))
	return fmt.Sprintf("%x", mac.Sum(nil))
}

func hexToBytes(h string) ([]byte, error) {
	if len(h)%2 != 0 {
		return nil, fmt.Errorf("invalid hex")
	}
	out := make([]byte, len(h)/2)
	for i := 0; i < len(h); i += 2 {
		var b byte
		for j := 0; j < 2; j++ {
			ch := h[i+j]
			b <<= 4
			switch {
			case ch >= '0' && ch <= '9':
				b |= ch - '0'
			case ch >= 'a' && ch <= 'f':
				b |= ch - 'a' + 10
			case ch >= 'A' && ch <= 'F':
				b |= ch - 'A' + 10
			default:
				return nil, fmt.Errorf("invalid hex char")
			}
		}
		out[i/2] = b
	}
	return out, nil
}

type serverFailingAdapter struct {
	provider string
}

func (a serverFailingAdapter) Provider() string {
	return a.provider
}

func (a serverFailingAdapter) ParseEnvelope(req relayfile.WebhookEnvelopeRequest) ([]relayfile.ApplyAction, error) {
	return nil, fmt.Errorf("intentional parse failure")
}

type serverToggleAdapter struct {
	provider    string
	allowReplay *atomic.Bool
}

func (a serverToggleAdapter) Provider() string {
	return a.provider
}

func (a serverToggleAdapter) ParseEnvelope(req relayfile.WebhookEnvelopeRequest) ([]relayfile.ApplyAction, error) {
	if a.allowReplay == nil || !a.allowReplay.Load() {
		return nil, fmt.Errorf("blocked until replay enabled")
	}
	return []relayfile.ApplyAction{
		{
			Type:        relayfile.ActionFileUpsert,
			Path:        "/custom/Replayed.md",
			Content:     "# replayed",
			ContentType: "text/markdown",
		},
	}, nil
}

type serverStaticAdapter struct {
	provider string
	actions  []relayfile.ApplyAction
}

func (a serverStaticAdapter) Provider() string {
	return a.provider
}

func (a serverStaticAdapter) ParseEnvelope(req relayfile.WebhookEnvelopeRequest) ([]relayfile.ApplyAction, error) {
	return append([]relayfile.ApplyAction(nil), a.actions...), nil
}
