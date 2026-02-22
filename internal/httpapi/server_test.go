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
	"strings"
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

func TestDashboardRoute(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	resp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/dashboard",
	})

	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("expected text/html content type, got %q", got)
	}
	if !strings.Contains(resp.Body.String(), "RelayFile Control Surface") {
		t.Fatalf("expected dashboard html title in response body")
	}
}

func TestDashboardRouteRejectsNonGET(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	resp := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/dashboard",
	})

	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d (%s)", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	if payload["code"] != "not_found" {
		t.Fatalf("expected not_found code, got %v", payload["code"])
	}
}

func TestLifecycleAndConflicts(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	token := mustTestJWT(t, "dev-secret", "ws_1", "Worker1", []string{"fs:read", "fs:write", "ops:read", "sync:read"}, time.Now().Add(time.Hour))

	writeResp := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_1/fs/file?path=/notion/Engineering/Auth.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_1",
			"If-Match":         "0",
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
			"Authorization":    "Bearer " + token,
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
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_3",
			"If-Match":         "rev_stale",
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
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_4",
			"If-Match":         file.Revision,
		},
	})
	if delResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on delete, got %d (%s)", delResp.Code, delResp.Body.String())
	}

	opResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_1/ops/" + queued.OpID,
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
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
				"Authorization":    "Bearer " + token,
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
				"Authorization":    "Bearer " + token,
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

func TestWriteFilePayloadTooLarge(t *testing.T) {
	server := NewServerWithConfig(relayfile.NewStore(), ServerConfig{
		MaxBodyBytes: 128,
	})
	token := mustTestJWT(t, "dev-secret", "ws_payload_limit", "Worker1", []string{"fs:write"}, time.Now().Add(time.Hour))
	resp := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_payload_limit/fs/file?path=/notion/Large.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_payload_limit",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     strings.Repeat("x", 1024),
		},
	})
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized write payload, got %d (%s)", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode payload-too-large response: %v", err)
	}
	if payload["code"] != "payload_too_large" {
		t.Fatalf("expected payload_too_large code, got %v", payload["code"])
	}
}

func TestIfMatchQuotedETagAccepted(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	token := mustTestJWT(t, "dev-secret", "ws_ifmatch", "Worker1", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	createResp := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_ifmatch/fs/file?path=/notion/IfMatch.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_ifmatch_1",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# v1",
		},
	})
	if createResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 create, got %d (%s)", createResp.Code, createResp.Body.String())
	}

	readResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_ifmatch/fs/file?path=/notion/IfMatch.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_ifmatch_2",
		},
	})
	if readResp.Code != http.StatusOK {
		t.Fatalf("expected 200 read, got %d (%s)", readResp.Code, readResp.Body.String())
	}
	var file relayfile.File
	if err := json.NewDecoder(readResp.Body).Decode(&file); err != nil {
		t.Fatalf("decode read response: %v", err)
	}

	updateResp := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_ifmatch/fs/file?path=/notion/IfMatch.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_ifmatch_3",
			"If-Match":         `"` + file.Revision + `"`,
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# v2",
		},
	})
	if updateResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 update with quoted If-Match, got %d (%s)", updateResp.Code, updateResp.Body.String())
	}

	readUpdated := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_ifmatch/fs/file?path=/notion/IfMatch.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_ifmatch_4",
		},
	})
	if readUpdated.Code != http.StatusOK {
		t.Fatalf("expected 200 read updated, got %d (%s)", readUpdated.Code, readUpdated.Body.String())
	}
	var updated relayfile.File
	if err := json.NewDecoder(readUpdated.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated response: %v", err)
	}

	deleteResp := doRequest(t, server, request{
		method: http.MethodDelete,
		path:   "/v1/workspaces/ws_ifmatch/fs/file?path=/notion/IfMatch.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_ifmatch_5",
			"If-Match":         `W/"` + updated.Revision + `"`,
		},
	})
	if deleteResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 delete with weak quoted If-Match, got %d (%s)", deleteResp.Code, deleteResp.Body.String())
	}
}

func TestTreeEndpointHonorsDepth(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	token := mustTestJWT(t, "dev-secret", "ws_tree_api", "Worker1", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	writes := []request{
		{
			method: http.MethodPut,
			path:   "/v1/workspaces/ws_tree_api/fs/file?path=/notion/Engineering/Auth.md",
			headers: map[string]string{
				"Authorization":    "Bearer " + token,
				"X-Correlation-Id": "corr_tree_api_1",
				"If-Match":         "0",
			},
			body: map[string]any{
				"contentType": "text/markdown",
				"content":     "# auth",
			},
		},
		{
			method: http.MethodPut,
			path:   "/v1/workspaces/ws_tree_api/fs/file?path=/notion/Engineering/Security/Policy.md",
			headers: map[string]string{
				"Authorization":    "Bearer " + token,
				"X-Correlation-Id": "corr_tree_api_2",
				"If-Match":         "0",
			},
			body: map[string]any{
				"contentType": "text/markdown",
				"content":     "# policy",
			},
		},
		{
			method: http.MethodPut,
			path:   "/v1/workspaces/ws_tree_api/fs/file?path=/notion/Product/Roadmap.md",
			headers: map[string]string{
				"Authorization":    "Bearer " + token,
				"X-Correlation-Id": "corr_tree_api_3",
				"If-Match":         "0",
			},
			body: map[string]any{
				"contentType": "text/markdown",
				"content":     "# roadmap",
			},
		},
	}
	for _, writeReq := range writes {
		resp := doRequest(t, server, writeReq)
		if resp.Code != http.StatusAccepted {
			t.Fatalf("expected 202 write for %s, got %d (%s)", writeReq.path, resp.Code, resp.Body.String())
		}
	}

	depth1Resp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_tree_api/fs/tree?path=/notion&depth=1",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_tree_api_4",
		},
	})
	if depth1Resp.Code != http.StatusOK {
		t.Fatalf("expected 200 depth=1 tree, got %d (%s)", depth1Resp.Code, depth1Resp.Body.String())
	}
	var depth1 relayfile.TreeResponse
	if err := json.NewDecoder(depth1Resp.Body).Decode(&depth1); err != nil {
		t.Fatalf("decode depth=1 tree response: %v", err)
	}
	if len(depth1.Entries) != 2 {
		t.Fatalf("expected 2 depth-1 entries, got %d", len(depth1.Entries))
	}
	if depth1.Entries[0].Path != "/notion/Engineering" || depth1.Entries[0].Type != "dir" {
		t.Fatalf("unexpected first depth-1 entry: %+v", depth1.Entries[0])
	}
	if depth1.Entries[1].Path != "/notion/Product" || depth1.Entries[1].Type != "dir" {
		t.Fatalf("unexpected second depth-1 entry: %+v", depth1.Entries[1])
	}

	depth2Resp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_tree_api/fs/tree?path=/notion&depth=2",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_tree_api_5",
		},
	})
	if depth2Resp.Code != http.StatusOK {
		t.Fatalf("expected 200 depth=2 tree, got %d (%s)", depth2Resp.Code, depth2Resp.Body.String())
	}
	var depth2 relayfile.TreeResponse
	if err := json.NewDecoder(depth2Resp.Body).Decode(&depth2); err != nil {
		t.Fatalf("decode depth=2 tree response: %v", err)
	}
	expected := map[string]string{
		"/notion/Engineering":          "dir",
		"/notion/Engineering/Auth.md":  "file",
		"/notion/Engineering/Security": "dir",
		"/notion/Product":              "dir",
		"/notion/Product/Roadmap.md":   "file",
	}
	if len(depth2.Entries) != len(expected) {
		t.Fatalf("expected %d depth-2 entries, got %d", len(expected), len(depth2.Entries))
	}
	for _, entry := range depth2.Entries {
		expectedType, ok := expected[entry.Path]
		if !ok {
			t.Fatalf("unexpected depth-2 entry: %+v", entry)
		}
		if expectedType != entry.Type {
			t.Fatalf("expected %s to be %s, got %s", entry.Path, expectedType, entry.Type)
		}
	}
}

func TestQueryFilesEndpoint(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	token := mustTestJWT(t, "dev-secret", "ws_query_api", "Worker1", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	writeResp := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_query_api/fs/file?path=/notion/Investments/Seed.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_query_api_1",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# seed",
			"semantics": map[string]any{
				"properties": map[string]any{
					"topic": "investments",
					"stage": "seed",
				},
				"relations":   []string{"page_cap_table"},
				"permissions": []string{"role:finance"},
				"comments":    []string{"comment_a"},
			},
		},
	})
	if writeResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on write, got %d (%s)", writeResp.Code, writeResp.Body.String())
	}

	fileResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_query_api/fs/file?path=/notion/Investments/Seed.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_query_api_2",
		},
	})
	if fileResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on read, got %d (%s)", fileResp.Code, fileResp.Body.String())
	}
	var file relayfile.File
	if err := json.NewDecoder(fileResp.Body).Decode(&file); err != nil {
		t.Fatalf("decode file response: %v", err)
	}
	if got := file.Semantics.Properties["topic"]; got != "investments" {
		t.Fatalf("expected topic semantic to round-trip, got %q", got)
	}

	queryResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_query_api/fs/query?path=/notion&property.topic=investments&relation=page_cap_table&permission=role:finance&comment=comment_a&limit=10",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_query_api_3",
		},
	})
	if queryResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on query, got %d (%s)", queryResp.Code, queryResp.Body.String())
	}
	var queryPayload relayfile.FileQueryResponse
	if err := json.NewDecoder(queryResp.Body).Decode(&queryPayload); err != nil {
		t.Fatalf("decode query response: %v", err)
	}
	if len(queryPayload.Items) != 1 {
		t.Fatalf("expected one query result, got %d", len(queryPayload.Items))
	}
	if queryPayload.Items[0].Path != "/notion/Investments/Seed.md" {
		t.Fatalf("unexpected query path: %s", queryPayload.Items[0].Path)
	}
	if queryPayload.Items[0].Properties["stage"] != "seed" {
		t.Fatalf("expected stage semantic, got %q", queryPayload.Items[0].Properties["stage"])
	}
}

func TestFilePermissionPolicyScopeEnforced(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	ownerToken := mustTestJWT(t, "dev-secret", "ws_perm_scope", "Owner", []string{"fs:read", "fs:write", "finance"}, time.Now().Add(time.Hour))
	limitedToken := mustTestJWT(t, "dev-secret", "ws_perm_scope", "Limited", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	writeResp := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_perm_scope/fs/file?path=/notion/Restricted.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + ownerToken,
			"X-Correlation-Id": "corr_perm_scope_1",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# restricted",
			"semantics": map[string]any{
				"permissions": []string{"scope:finance"},
			},
		},
	})
	if writeResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on restricted write, got %d (%s)", writeResp.Code, writeResp.Body.String())
	}

	ownerRead := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_perm_scope/fs/file?path=/notion/Restricted.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + ownerToken,
			"X-Correlation-Id": "corr_perm_scope_2",
		},
	})
	if ownerRead.Code != http.StatusOK {
		t.Fatalf("expected owner read 200, got %d (%s)", ownerRead.Code, ownerRead.Body.String())
	}
	var restricted relayfile.File
	if err := json.NewDecoder(ownerRead.Body).Decode(&restricted); err != nil {
		t.Fatalf("decode owner read: %v", err)
	}

	limitedRead := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_perm_scope/fs/file?path=/notion/Restricted.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + limitedToken,
			"X-Correlation-Id": "corr_perm_scope_3",
		},
	})
	if limitedRead.Code != http.StatusForbidden {
		t.Fatalf("expected limited read 403, got %d (%s)", limitedRead.Code, limitedRead.Body.String())
	}

	limitedWrite := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_perm_scope/fs/file?path=/notion/Restricted.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + limitedToken,
			"X-Correlation-Id": "corr_perm_scope_4",
			"If-Match":         restricted.Revision,
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# unauthorized",
		},
	})
	if limitedWrite.Code != http.StatusForbidden {
		t.Fatalf("expected limited write 403, got %d (%s)", limitedWrite.Code, limitedWrite.Body.String())
	}

	limitedDelete := doRequest(t, server, request{
		method: http.MethodDelete,
		path:   "/v1/workspaces/ws_perm_scope/fs/file?path=/notion/Restricted.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + limitedToken,
			"X-Correlation-Id": "corr_perm_scope_5",
			"If-Match":         restricted.Revision,
		},
	})
	if limitedDelete.Code != http.StatusForbidden {
		t.Fatalf("expected limited delete 403, got %d (%s)", limitedDelete.Code, limitedDelete.Body.String())
	}

	limitedQuery := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_perm_scope/fs/query?path=/notion&limit=20",
		headers: map[string]string{
			"Authorization":    "Bearer " + limitedToken,
			"X-Correlation-Id": "corr_perm_scope_6",
		},
	})
	if limitedQuery.Code != http.StatusOK {
		t.Fatalf("expected limited query 200, got %d (%s)", limitedQuery.Code, limitedQuery.Body.String())
	}
	var limitedPayload relayfile.FileQueryResponse
	if err := json.NewDecoder(limitedQuery.Body).Decode(&limitedPayload); err != nil {
		t.Fatalf("decode limited query response: %v", err)
	}
	if len(limitedPayload.Items) != 0 {
		t.Fatalf("expected no query items for limited token, got %d", len(limitedPayload.Items))
	}
}

func TestTreeEndpointFiltersUnauthorizedFiles(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	ownerToken := mustTestJWT(t, "dev-secret", "ws_perm_tree", "Owner", []string{"fs:read", "fs:write", "finance"}, time.Now().Add(time.Hour))
	limitedToken := mustTestJWT(t, "dev-secret", "ws_perm_tree", "Limited", []string{"fs:read"}, time.Now().Add(time.Hour))

	restrictedWrite := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_perm_tree/fs/file?path=/notion/private/Secret.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + ownerToken,
			"X-Correlation-Id": "corr_perm_tree_1",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# secret",
			"semantics": map[string]any{
				"permissions": []string{"scope:finance"},
			},
		},
	})
	if restrictedWrite.Code != http.StatusAccepted {
		t.Fatalf("expected restricted write 202, got %d (%s)", restrictedWrite.Code, restrictedWrite.Body.String())
	}

	publicWrite := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_perm_tree/fs/file?path=/notion/public/Guide.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + ownerToken,
			"X-Correlation-Id": "corr_perm_tree_2",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# guide",
		},
	})
	if publicWrite.Code != http.StatusAccepted {
		t.Fatalf("expected public write 202, got %d (%s)", publicWrite.Code, publicWrite.Body.String())
	}

	treeResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_perm_tree/fs/tree?path=/notion&depth=3",
		headers: map[string]string{
			"Authorization":    "Bearer " + limitedToken,
			"X-Correlation-Id": "corr_perm_tree_3",
		},
	})
	if treeResp.Code != http.StatusOK {
		t.Fatalf("expected limited tree 200, got %d (%s)", treeResp.Code, treeResp.Body.String())
	}
	var tree relayfile.TreeResponse
	if err := json.NewDecoder(treeResp.Body).Decode(&tree); err != nil {
		t.Fatalf("decode limited tree response: %v", err)
	}
	paths := map[string]string{}
	for _, entry := range tree.Entries {
		paths[entry.Path] = entry.Type
	}
	if _, ok := paths["/notion/public/Guide.md"]; !ok {
		t.Fatalf("expected public file in tree, got %+v", tree.Entries)
	}
	if _, ok := paths["/notion/public"]; !ok {
		t.Fatalf("expected public dir in tree, got %+v", tree.Entries)
	}
	if _, ok := paths["/notion/private/Secret.md"]; ok {
		t.Fatalf("expected restricted file to be hidden from tree")
	}
	if _, ok := paths["/notion/private"]; ok {
		t.Fatalf("expected restricted dir to be hidden when no accessible descendants")
	}
}

func TestFilePermissionPolicyDenyOverridesAllowAndPublic(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	ownerToken := mustTestJWT(t, "dev-secret", "ws_perm_deny", "Owner", []string{"fs:read", "fs:write", "finance"}, time.Now().Add(time.Hour))
	limitedToken := mustTestJWT(t, "dev-secret", "ws_perm_deny", "Limited", []string{"fs:read", "fs:write", "finance"}, time.Now().Add(time.Hour))

	writeResp := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_perm_deny/fs/file?path=/notion/Deny.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + ownerToken,
			"X-Correlation-Id": "corr_perm_deny_1",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# deny",
			"semantics": map[string]any{
				"permissions": []string{"public", "scope:finance", "deny:agent:Limited"},
			},
		},
	})
	if writeResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on deny file write, got %d (%s)", writeResp.Code, writeResp.Body.String())
	}

	ownerRead := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_perm_deny/fs/file?path=/notion/Deny.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + ownerToken,
			"X-Correlation-Id": "corr_perm_deny_2",
		},
	})
	if ownerRead.Code != http.StatusOK {
		t.Fatalf("expected owner read 200, got %d (%s)", ownerRead.Code, ownerRead.Body.String())
	}

	limitedRead := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_perm_deny/fs/file?path=/notion/Deny.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + limitedToken,
			"X-Correlation-Id": "corr_perm_deny_3",
		},
	})
	if limitedRead.Code != http.StatusForbidden {
		t.Fatalf("expected deny override 403, got %d (%s)", limitedRead.Code, limitedRead.Body.String())
	}
}

func TestFilePermissionPolicyInheritanceFromAclMarker(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	financeToken := mustTestJWT(t, "dev-secret", "ws_perm_inherit", "FinanceAgent", []string{"fs:read", "fs:write", "finance"}, time.Now().Add(time.Hour))
	limitedToken := mustTestJWT(t, "dev-secret", "ws_perm_inherit", "Limited", []string{"fs:read", "fs:write"}, time.Now().Add(time.Hour))

	aclWrite := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_perm_inherit/fs/file?path=/notion/private/.relayfile.acl",
		headers: map[string]string{
			"Authorization":    "Bearer " + financeToken,
			"X-Correlation-Id": "corr_perm_inherit_1",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/plain",
			"content":     "acl marker",
			"semantics": map[string]any{
				"permissions": []string{"scope:finance"},
			},
		},
	})
	if aclWrite.Code != http.StatusAccepted {
		t.Fatalf("expected acl marker write 202, got %d (%s)", aclWrite.Code, aclWrite.Body.String())
	}

	childWrite := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_perm_inherit/fs/file?path=/notion/private/Inherited.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + financeToken,
			"X-Correlation-Id": "corr_perm_inherit_2",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# inherited",
		},
	})
	if childWrite.Code != http.StatusAccepted {
		t.Fatalf("expected child write 202, got %d (%s)", childWrite.Code, childWrite.Body.String())
	}

	limitedRead := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_perm_inherit/fs/file?path=/notion/private/Inherited.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + limitedToken,
			"X-Correlation-Id": "corr_perm_inherit_3",
		},
	})
	if limitedRead.Code != http.StatusForbidden {
		t.Fatalf("expected inherited policy to deny read, got %d (%s)", limitedRead.Code, limitedRead.Body.String())
	}

	financeRead := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_perm_inherit/fs/file?path=/notion/private/Inherited.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + financeToken,
			"X-Correlation-Id": "corr_perm_inherit_4",
		},
	})
	if financeRead.Code != http.StatusOK {
		t.Fatalf("expected finance read 200, got %d (%s)", financeRead.Code, financeRead.Body.String())
	}

	limitedQuery := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_perm_inherit/fs/query?path=/notion/private&permission=scope:finance&limit=20",
		headers: map[string]string{
			"Authorization":    "Bearer " + limitedToken,
			"X-Correlation-Id": "corr_perm_inherit_5",
		},
	})
	if limitedQuery.Code != http.StatusOK {
		t.Fatalf("expected limited query 200, got %d (%s)", limitedQuery.Code, limitedQuery.Body.String())
	}
	var limitedPayload relayfile.FileQueryResponse
	if err := json.NewDecoder(limitedQuery.Body).Decode(&limitedPayload); err != nil {
		t.Fatalf("decode limited inherited query: %v", err)
	}
	if len(limitedPayload.Items) != 0 {
		t.Fatalf("expected no inherited query results for limited agent, got %d", len(limitedPayload.Items))
	}

	financeQuery := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_perm_inherit/fs/query?path=/notion/private&permission=scope:finance&limit=20",
		headers: map[string]string{
			"Authorization":    "Bearer " + financeToken,
			"X-Correlation-Id": "corr_perm_inherit_6",
		},
	})
	if financeQuery.Code != http.StatusOK {
		t.Fatalf("expected finance query 200, got %d (%s)", financeQuery.Code, financeQuery.Body.String())
	}
	var financePayload relayfile.FileQueryResponse
	if err := json.NewDecoder(financeQuery.Body).Decode(&financePayload); err != nil {
		t.Fatalf("decode finance inherited query: %v", err)
	}
	if len(financePayload.Items) != 2 {
		t.Fatalf("expected marker + child for finance query, got %d", len(financePayload.Items))
	}
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
			"X-Correlation-Id":  "corr_ops_provider_api_1",
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
				"Authorization":    "Bearer " + token,
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
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_ops_provider_api_3",
			"If-Match":         customFile.Revision,
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
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_ops_provider_api_4",
			"If-Match":         "0",
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
			"Authorization":    "Bearer " + token,
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

	customOpsMixedCase := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_ops_provider_api/ops?provider=CuStOm&limit=20",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_ops_provider_api_5b",
		},
	})
	if customOpsMixedCase.Code != http.StatusOK {
		t.Fatalf("expected mixed-case custom provider ops list 200, got %d (%s)", customOpsMixedCase.Code, customOpsMixedCase.Body.String())
	}
	var customMixedFeed relayfile.OperationFeed
	if err := json.NewDecoder(customOpsMixedCase.Body).Decode(&customMixedFeed); err != nil {
		t.Fatalf("decode mixed-case custom provider feed: %v", err)
	}
	if len(customMixedFeed.Items) == 0 {
		t.Fatalf("expected mixed-case provider-filtered custom ops to be non-empty")
	}
	for _, item := range customMixedFeed.Items {
		if item.Provider != "custom" {
			t.Fatalf("expected only custom provider ops for mixed-case filter, got %s", item.Provider)
		}
	}

	notionOps := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_ops_provider_api/ops?provider=notion&limit=20",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
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
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_evt_1",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# roadmap",
		},
	})
	_ = doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_2/fs/file?path=/custom/Product/Notes.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_evt_1b",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# notes",
		},
	})

	eventsResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_2/fs/events?limit=10",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
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

	filteredEventsResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_2/fs/events?provider=custom&limit=10",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_evt_2b",
		},
	})
	if filteredEventsResp.Code != http.StatusOK {
		t.Fatalf("expected 200 on provider-filtered events, got %d (%s)", filteredEventsResp.Code, filteredEventsResp.Body.String())
	}
	var filteredFeed relayfile.EventFeed
	if err := json.NewDecoder(filteredEventsResp.Body).Decode(&filteredFeed); err != nil {
		t.Fatalf("decode filtered events response: %v", err)
	}
	if len(filteredFeed.Events) == 0 {
		t.Fatalf("expected custom provider events to be present")
	}
	for _, event := range filteredFeed.Events {
		if event.Provider != "custom" {
			t.Fatalf("expected only custom provider events, got provider=%q path=%q type=%q", event.Provider, event.Path, event.Type)
		}
	}

	syncResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_2/sync/status",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
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
			"X-Correlation-Id":  "corr_sync_status_lag_api_1",
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
			"Authorization":    "Bearer " + token,
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
			"X-Correlation-Id":  "corr_sync_status_error_api_1",
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
				"Authorization":    "Bearer " + token,
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
			if payload.Providers[0].DeadLetteredEnvelopes < 1 {
				t.Fatalf("expected dead-lettered envelope count, got %d", payload.Providers[0].DeadLetteredEnvelopes)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected provider sync status to transition to error from dead-letter")
}

func TestSyncStatusEndpointIncludesProviderFromOperations(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		MaxWritebackAttempts: 1,
		WritebackDelay:       5 * time.Millisecond,
		ProviderWriteAction: func(action relayfile.WritebackAction) error {
			if action.Type == relayfile.WritebackActionFileDelete {
				return fmt.Errorf("delete failure for provider discovery")
			}
			return nil
		},
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	token := mustTestJWT(t, "dev-secret", "ws_sync_ops_provider_api", "Worker1", []string{"fs:write", "fs:read", "sync:read"}, time.Now().Add(time.Hour))
	createResp := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_sync_ops_provider_api/fs/file?path=/custom/OpsProvider.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_sync_ops_provider_api_1",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# custom",
		},
	})
	if createResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 create, got %d (%s)", createResp.Code, createResp.Body.String())
	}

	var file relayfile.File
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		readResp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_sync_ops_provider_api/fs/file?path=/custom/OpsProvider.md",
			headers: map[string]string{
				"Authorization":    "Bearer " + token,
				"X-Correlation-Id": "corr_sync_ops_provider_api_2",
			},
		})
		if readResp.Code == http.StatusOK {
			if err := json.NewDecoder(readResp.Body).Decode(&file); err != nil {
				t.Fatalf("decode file failed: %v", err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if file.Revision == "" {
		t.Fatalf("expected created file to be readable before delete")
	}

	deleteResp := doRequest(t, server, request{
		method: http.MethodDelete,
		path:   "/v1/workspaces/ws_sync_ops_provider_api/fs/file?path=/custom/OpsProvider.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_sync_ops_provider_api_3",
			"If-Match":         file.Revision,
		},
	})
	if deleteResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 delete, got %d (%s)", deleteResp.Code, deleteResp.Body.String())
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		statusResp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/ws_sync_ops_provider_api/sync/status",
			headers: map[string]string{
				"Authorization":    "Bearer " + token,
				"X-Correlation-Id": "corr_sync_ops_provider_api_4",
			},
		})
		if statusResp.Code != http.StatusOK {
			t.Fatalf("expected 200 on sync status, got %d (%s)", statusResp.Code, statusResp.Body.String())
		}
		var status relayfile.SyncStatus
		if err := json.NewDecoder(statusResp.Body).Decode(&status); err != nil {
			t.Fatalf("decode sync status failed: %v", err)
		}
		for _, providerStatus := range status.Providers {
			if providerStatus.Provider != "custom" {
				continue
			}
			if providerStatus.Status == "error" && providerStatus.LastError != nil && *providerStatus.LastError != "" && providerStatus.DeadLetteredOps >= 1 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected sync status to include custom provider from operations")
}

func TestScopeAndWorkspaceClaimsEnforced(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	readOnlyToken := mustTestJWT(t, "dev-secret", "ws_3", "Worker1", []string{"fs:read"}, time.Now().Add(time.Hour))

	deniedWrite := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_3/fs/file?path=/notion/X.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + readOnlyToken,
			"X-Correlation-Id": "corr_scope_1",
			"If-Match":         "0",
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
			"Authorization":    "Bearer " + wrongWorkspaceToken,
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
			"Authorization":    "Bearer " + wrongAudience,
			"X-Correlation-Id": "corr_scope_3",
		},
	})
	if badAudResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid audience, got %d (%s)", badAudResp.Code, badAudResp.Body.String())
	}
}

func TestAudienceArrayClaimAccepted(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	allowed := mustTestJWTWithAudienceClaim(
		t,
		"dev-secret",
		"ws_aud_array",
		"Worker1",
		[]string{"fs:read"},
		[]string{"other-service", "relayfile"},
		time.Now().Add(time.Hour),
	)
	okResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_aud_array/fs/tree?path=/",
		headers: map[string]string{
			"Authorization":    "Bearer " + allowed,
			"X-Correlation-Id": "corr_aud_array_1",
		},
	})
	if okResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for aud array containing relayfile, got %d (%s)", okResp.Code, okResp.Body.String())
	}

	denied := mustTestJWTWithAudienceClaim(
		t,
		"dev-secret",
		"ws_aud_array",
		"Worker1",
		[]string{"fs:read"},
		[]string{"other-service", "not-relayfile"},
		time.Now().Add(time.Hour),
	)
	deniedResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_aud_array/fs/tree?path=/",
		headers: map[string]string{
			"Authorization":    "Bearer " + denied,
			"X-Correlation-Id": "corr_aud_array_2",
		},
	})
	if deniedResp.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for aud array missing relayfile, got %d (%s)", deniedResp.Code, deniedResp.Body.String())
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
			"X-Correlation-Id":  "corr_internal_1",
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
			"X-Correlation-Id":  "corr_internal_2",
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
			"X-Correlation-Id":  "corr_internal_3",
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

func TestInternalWebhookIngressPayloadTooLarge(t *testing.T) {
	server := NewServerWithConfig(relayfile.NewStore(), ServerConfig{
		InternalHMACSecret: "dev-internal-secret",
		MaxBodyBytes:       256,
	})
	body := map[string]any{
		"envelopeId":    "env_oversized",
		"workspaceId":   "ws_oversized",
		"provider":      "notion",
		"deliveryId":    "del_oversized",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"blob": strings.Repeat("z", 4096)},
		"correlationId": "corr_internal_oversized",
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal oversized body: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	sig := mustHMAC("dev-internal-secret", ts+"\n"+string(bodyBytes))

	resp := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id":  "corr_internal_oversized",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": sig,
			"Content-Type":      "application/json",
		},
		body: bodyBytes,
	})
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversized internal ingress payload, got %d (%s)", resp.Code, resp.Body.String())
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode payload-too-large response: %v", err)
	}
	if payload["code"] != "payload_too_large" {
		t.Fatalf("expected payload_too_large code, got %v", payload["code"])
	}
}

func TestInternalWebhookIngressRejectsReplay(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	body := map[string]any{
		"envelopeId":    "env_replay_1",
		"workspaceId":   "ws_replay_guard",
		"provider":      "notion",
		"deliveryId":    "delivery_replay_1",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"type": "sync"},
		"correlationId": "corr_replay_guard_1",
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	sig := mustHMAC("dev-internal-secret", ts+"\n"+string(bodyBytes))

	first := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id":  "corr_replay_guard_1",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": sig,
			"Content-Type":      "application/json",
		},
		body: bodyBytes,
	})
	if first.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for first internal ingress, got %d (%s)", first.Code, first.Body.String())
	}

	replayed := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id":  "corr_replay_guard_2",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": sig,
			"Content-Type":      "application/json",
		},
		body: bodyBytes,
	})
	if replayed.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for replayed internal ingress, got %d (%s)", replayed.Code, replayed.Body.String())
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
			"X-Correlation-Id":  "corr_qf_1",
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
			"X-Correlation-Id":  "corr_qf_2",
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
			"X-Correlation-Id":  "corr_stats_1",
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
			"X-Correlation-Id":  "corr_stats_2",
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
			"Authorization":    "Bearer " + token,
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

func TestSyncIngressStatusEndpointProviderFilter(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		DisableWorkers:    true,
		EnvelopeQueueSize: 4,
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	envelopes := []map[string]any{
		{
			"envelopeId":    "env_stats_provider_1",
			"workspaceId":   "ws_stats_provider",
			"provider":      "notion",
			"deliveryId":    "delivery_stats_provider_1",
			"receivedAt":    time.Now().UTC().Format(time.RFC3339Nano),
			"payload":       map[string]any{"type": "sync"},
			"correlationId": "corr_stats_provider_1",
		},
		{
			"envelopeId":    "env_stats_provider_2",
			"workspaceId":   "ws_stats_provider",
			"provider":      "custom",
			"deliveryId":    "delivery_stats_provider_2",
			"receivedAt":    time.Now().UTC().Format(time.RFC3339Nano),
			"payload":       map[string]any{"type": "sync"},
			"correlationId": "corr_stats_provider_2",
		},
	}
	for _, envelope := range envelopes {
		body, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		ts := time.Now().UTC().Format(time.RFC3339)
		sig := mustHMAC("dev-internal-secret", ts+"\n"+string(body))
		_ = doRawRequest(t, server, rawRequest{
			method: http.MethodPost,
			path:   "/v1/internal/webhook-envelopes",
			headers: map[string]string{
				"X-Correlation-Id":  envelope["correlationId"].(string),
				"X-Relay-Timestamp": ts,
				"X-Relay-Signature": sig,
				"Content-Type":      "application/json",
			},
			body: body,
		})
	}

	token := mustTestJWT(t, "dev-secret", "ws_stats_provider", "Worker1", []string{"sync:read"}, time.Now().Add(time.Hour))
	resp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_stats_provider/sync/ingress?provider=custom",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_stats_provider_3",
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for provider-filtered ingress status, got %d (%s)", resp.Code, resp.Body.String())
	}
	var status relayfile.IngressStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode provider-filtered ingress status: %v", err)
	}
	if status.PendingTotal != 1 || status.AcceptedTotal != 1 || status.QueueDepth != 1 {
		t.Fatalf("unexpected provider-filtered ingress status: %+v", status)
	}
	if len(status.IngressByProvider) != 1 {
		t.Fatalf("expected one provider in ingressByProvider, got %+v", status.IngressByProvider)
	}
	customIngress, ok := status.IngressByProvider["custom"]
	if !ok || customIngress.PendingTotal != 1 || customIngress.AcceptedTotal != 1 {
		t.Fatalf("expected custom-only provider metrics, got %+v", status.IngressByProvider)
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
			"X-Correlation-Id":  "corr_dead_api_1",
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
				"Authorization":    "Bearer " + token,
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
			"X-Correlation-Id":  "corr_dead_item_api_1",
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
				"Authorization":    "Bearer " + token,
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
				"X-Correlation-Id":  envelope["correlationId"].(string),
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
				"Authorization":    "Bearer " + token,
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
			"X-Correlation-Id":  "corr_dead_replay_api_1",
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
				"Authorization":    "Bearer " + readToken,
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
			"Authorization":    "Bearer " + readToken,
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
			"Authorization":    "Bearer " + replayToken,
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
				"Authorization":    "Bearer " + readToken,
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
			"X-Correlation-Id":  "corr_dead_ack_api_1",
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
				"Authorization":    "Bearer " + readToken,
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
			"Authorization":    "Bearer " + readToken,
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
			"Authorization":    "Bearer " + triggerToken,
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
			"Authorization":    "Bearer " + readToken,
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
			"Authorization":    "Bearer " + writeToken,
			"X-Correlation-Id": "corr_ops_replay_1",
			"If-Match":         "0",
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
				"Authorization":    "Bearer " + writeToken,
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
			"Authorization":    "Bearer " + writeToken,
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
			"Authorization":    "Bearer " + readOnlyToken,
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
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_ops_replay_state_1",
			"If-Match":         "0",
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
				"Authorization":    "Bearer " + token,
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
			"Authorization":    "Bearer " + token,
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
			"Authorization":    "Bearer " + opsToken,
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
			"Authorization":    "Bearer " + mustTestJWT(t, "dev-secret", "ws_cursor", "Worker1", []string{"sync:read"}, time.Now().Add(time.Hour)),
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
		"provider":      "external",
		"deliveryId":    "delivery_admin_1",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"event_type": "file.created", "path": "/docs/seed.md", "content": "# seed"},
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
			"X-Correlation-Id":  "corr_admin_internal_1",
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
		path:   "/v1/admin/replay/envelope/env_admin_1",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_1",
		},
	})
	if replay.Code != http.StatusAccepted {
		t.Fatalf("expected 202 replay envelope, got %d (%s)", replay.Code, replay.Body.String())
	}
	replayLegacy := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/admin/replay/envelopes/env_admin_1",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_1b",
		},
	})
	if replayLegacy.Code != http.StatusAccepted {
		t.Fatalf("expected 202 replay envelope (legacy path), got %d (%s)", replayLegacy.Code, replayLegacy.Body.String())
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
		path:   "/v1/admin/replay/envelope/env_admin_1",
		headers: map[string]string{
			"Authorization":    "Bearer " + noAdmin,
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
			"Authorization":    "Bearer " + adminToken,
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

	syncRefreshInvalid := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_admin/sync/refresh",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_3b",
		},
		body: map[string]any{
			"provider": "unknown-provider",
			"reason":   "manual",
		},
	})
	if syncRefreshInvalid.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown provider on sync refresh, got %d (%s)", syncRefreshInvalid.Code, syncRefreshInvalid.Body.String())
	}

	write := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_admin/fs/file?path=/notion/Admin.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + mustTestJWT(t, "dev-secret", "ws_admin", "Lead", []string{"fs:write"}, time.Now().Add(time.Hour)),
			"X-Correlation-Id": "corr_admin_4",
			"If-Match":         "0",
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
				"Authorization":    "Bearer " + adminToken,
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
		path:   "/v1/admin/replay/op/" + queued.OpID,
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_5",
		},
	})
	if replayOp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 replay op, got %d (%s)", replayOp.Code, replayOp.Body.String())
	}
}

func TestAdminBackendsEndpoint(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		DisableWorkers: true,
		StateBackend:   relayfile.NewInMemoryStateBackend(),
		EnvelopeQueue:  relayfile.NewInMemoryEnvelopeQueue(5),
		WritebackQueue: relayfile.NewInMemoryWritebackQueue(6),
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	readToken := mustTestJWT(
		t,
		"dev-secret",
		"ws_admin_backends",
		"Lead",
		[]string{"admin:read"},
		time.Now().Add(time.Hour),
	)
	resp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/backends",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_backends_1",
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin backends endpoint, got %d (%s)", resp.Code, resp.Body.String())
	}
	var payload relayfile.BackendStatus
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode admin backends payload: %v", err)
	}
	if payload.EnvelopeQueueCap != 5 || payload.WritebackQueueCap != 6 {
		t.Fatalf("expected queue capacities 5/6, got %+v", payload)
	}
	if payload.BackendProfile != "custom" {
		t.Fatalf("expected default backend profile custom, got %+v", payload)
	}

	replayToken := mustTestJWT(
		t,
		"dev-secret",
		"ws_admin_backends",
		"Lead",
		[]string{"admin:replay"},
		time.Now().Add(time.Hour),
	)
	replayResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/backends",
		headers: map[string]string{
			"Authorization":    "Bearer " + replayToken,
			"X-Correlation-Id": "corr_admin_backends_1b",
		},
	})
	if replayResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin:replay token on admin backends endpoint, got %d (%s)", replayResp.Code, replayResp.Body.String())
	}

	deniedToken := mustTestJWT(
		t,
		"dev-secret",
		"ws_admin_backends",
		"Worker1",
		[]string{"fs:read"},
		time.Now().Add(time.Hour),
	)
	denied := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/backends",
		headers: map[string]string{
			"Authorization":    "Bearer " + deniedToken,
			"X-Correlation-Id": "corr_admin_backends_2",
		},
	})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing admin scope, got %d (%s)", denied.Code, denied.Body.String())
	}
}

func TestAdminIngressEndpoint(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		DisableWorkers:    true,
		EnvelopeQueueSize: 8,
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	receivedAt := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := store.IngestEnvelope(relayfile.WebhookEnvelopeRequest{
		EnvelopeID:    "env_admin_ingress_1",
		WorkspaceID:   "ws_admin_ingress_1",
		Provider:      "notion",
		DeliveryID:    "delivery_admin_ingress_1",
		ReceivedAt:    receivedAt,
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_admin_ingress_1",
	})
	if err != nil {
		t.Fatalf("ingest ws_admin_ingress_1 failed: %v", err)
	}
	_, err = store.IngestEnvelope(relayfile.WebhookEnvelopeRequest{
		EnvelopeID:    "env_admin_ingress_2",
		WorkspaceID:   "ws_admin_ingress_2",
		Provider:      "custom",
		DeliveryID:    "delivery_admin_ingress_2",
		ReceivedAt:    receivedAt,
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_admin_ingress_2",
	})
	if err != nil {
		t.Fatalf("ingest ws_admin_ingress_2 failed: %v", err)
	}

	readToken := mustTestJWT(
		t,
		"dev-secret",
		"ws_admin_ingress_1",
		"Lead",
		[]string{"admin:read"},
		time.Now().Add(time.Hour),
	)
	resp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_3",
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin ingress endpoint, got %d (%s)", resp.Code, resp.Body.String())
	}
	var payload struct {
		AlertProfile           string   `json:"alertProfile"`
		EffectiveAlertProfile  string   `json:"effectiveAlertProfile"`
		WorkspaceCount         int      `json:"workspaceCount"`
		ReturnedWorkspaceCount int      `json:"returnedWorkspaceCount"`
		WorkspaceIDs           []string `json:"workspaceIds"`
		NextCursor             *string  `json:"nextCursor"`
		PendingTotal           int      `json:"pendingTotal"`
		Thresholds             struct {
			Pending    int     `json:"pending"`
			DeadLetter int     `json:"deadLetter"`
			Stale      int     `json:"stale"`
			DropRate   float64 `json:"dropRate"`
		} `json:"thresholds"`
		AlertTotals struct {
			Total    int            `json:"total"`
			Critical int            `json:"critical"`
			Warning  int            `json:"warning"`
			ByType   map[string]int `json:"byType"`
		} `json:"alertTotals"`
		AlertsTruncated bool                               `json:"alertsTruncated"`
		Alerts          []map[string]any                   `json:"alerts"`
		Workspaces      map[string]relayfile.IngressStatus `json:"workspaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode admin ingress payload: %v", err)
	}
	if payload.WorkspaceCount < 2 {
		t.Fatalf("expected workspaceCount >= 2, got %d", payload.WorkspaceCount)
	}
	if payload.AlertProfile != "balanced" {
		t.Fatalf("expected default alertProfile=balanced, got %q", payload.AlertProfile)
	}
	if payload.EffectiveAlertProfile != "balanced" {
		t.Fatalf("expected default effectiveAlertProfile=balanced, got %q", payload.EffectiveAlertProfile)
	}
	if payload.ReturnedWorkspaceCount < 2 {
		t.Fatalf("expected returnedWorkspaceCount >= 2, got %d", payload.ReturnedWorkspaceCount)
	}
	if len(payload.WorkspaceIDs) != payload.ReturnedWorkspaceCount {
		t.Fatalf("expected workspaceIds length to match returnedWorkspaceCount, got ids=%d count=%d", len(payload.WorkspaceIDs), payload.ReturnedWorkspaceCount)
	}
	if payload.NextCursor != nil {
		t.Fatalf("expected nil nextCursor for full response, got %q", *payload.NextCursor)
	}
	if payload.PendingTotal < 2 {
		t.Fatalf("expected pendingTotal >= 2, got %d", payload.PendingTotal)
	}
	if payload.Thresholds.Pending != 100 || payload.Thresholds.DeadLetter != 1 || payload.Thresholds.Stale != 10 || payload.Thresholds.DropRate != 0.05 {
		t.Fatalf("unexpected default threshold payload: %+v", payload.Thresholds)
	}
	if payload.AlertTotals.Total != 0 || payload.AlertTotals.Critical != 0 || payload.AlertTotals.Warning != 0 || len(payload.AlertTotals.ByType) != 0 {
		t.Fatalf("unexpected zero-alert totals payload: %+v", payload.AlertTotals)
	}
	if payload.AlertsTruncated {
		t.Fatalf("expected alertsTruncated=false for baseline payload")
	}
	if len(payload.Alerts) != 0 {
		t.Fatalf("expected no alerts for baseline admin ingress test payload, got %+v", payload.Alerts)
	}
	summaryOnlyResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?includeWorkspaces=false",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_3_summary_only",
		},
	})
	if summaryOnlyResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for summary-only admin ingress endpoint, got %d (%s)", summaryOnlyResp.Code, summaryOnlyResp.Body.String())
	}
	var summaryOnlyPayload struct {
		WorkspaceCount         int                                `json:"workspaceCount"`
		ReturnedWorkspaceCount int                                `json:"returnedWorkspaceCount"`
		WorkspaceIDs           []string                           `json:"workspaceIds"`
		NextCursor             *string                            `json:"nextCursor"`
		Workspaces             map[string]relayfile.IngressStatus `json:"workspaces"`
	}
	if err := json.NewDecoder(summaryOnlyResp.Body).Decode(&summaryOnlyPayload); err != nil {
		t.Fatalf("decode summary-only admin ingress payload: %v", err)
	}
	if summaryOnlyPayload.WorkspaceCount < 2 || summaryOnlyPayload.ReturnedWorkspaceCount != 0 || len(summaryOnlyPayload.WorkspaceIDs) != 0 || len(summaryOnlyPayload.Workspaces) != 0 || summaryOnlyPayload.NextCursor != nil {
		t.Fatalf("unexpected summary-only payload: %+v", summaryOnlyPayload)
	}
	summaryOnlyWithCursorResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?includeWorkspaces=false&cursor=missing_workspace&limit=1",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_3_summary_only_cursor",
		},
	})
	if summaryOnlyWithCursorResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for summary-only admin ingress with cursor params, got %d (%s)", summaryOnlyWithCursorResp.Code, summaryOnlyWithCursorResp.Body.String())
	}
	var summaryOnlyWithCursorPayload struct {
		ReturnedWorkspaceCount int                                `json:"returnedWorkspaceCount"`
		WorkspaceIDs           []string                           `json:"workspaceIds"`
		NextCursor             *string                            `json:"nextCursor"`
		Workspaces             map[string]relayfile.IngressStatus `json:"workspaces"`
	}
	if err := json.NewDecoder(summaryOnlyWithCursorResp.Body).Decode(&summaryOnlyWithCursorPayload); err != nil {
		t.Fatalf("decode summary-only-with-cursor payload: %v", err)
	}
	if summaryOnlyWithCursorPayload.ReturnedWorkspaceCount != 0 || len(summaryOnlyWithCursorPayload.WorkspaceIDs) != 0 || len(summaryOnlyWithCursorPayload.Workspaces) != 0 || summaryOnlyWithCursorPayload.NextCursor != nil {
		t.Fatalf("unexpected summary-only-with-cursor payload: %+v", summaryOnlyWithCursorPayload)
	}
	ws1, ok := payload.Workspaces["ws_admin_ingress_1"]
	if !ok {
		t.Fatalf("expected ws_admin_ingress_1 in admin ingress payload")
	}
	if ws1.AcceptedTotal != 1 || ws1.PendingTotal != 1 {
		t.Fatalf("unexpected ws_admin_ingress_1 status: %+v", ws1)
	}
	ws2, ok := payload.Workspaces["ws_admin_ingress_2"]
	if !ok {
		t.Fatalf("expected ws_admin_ingress_2 in admin ingress payload")
	}
	if ws2.AcceptedTotal != 1 || ws2.PendingTotal != 1 {
		t.Fatalf("unexpected ws_admin_ingress_2 status: %+v", ws2)
	}

	filteredWorkspace := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?workspaceId=ws_admin_ingress_2",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_3b",
		},
	})
	if filteredWorkspace.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin ingress workspace filter, got %d (%s)", filteredWorkspace.Code, filteredWorkspace.Body.String())
	}
	var workspacePayload struct {
		WorkspaceCount int                                `json:"workspaceCount"`
		PendingTotal   int                                `json:"pendingTotal"`
		Workspaces     map[string]relayfile.IngressStatus `json:"workspaces"`
	}
	if err := json.NewDecoder(filteredWorkspace.Body).Decode(&workspacePayload); err != nil {
		t.Fatalf("decode admin ingress workspace-filter payload: %v", err)
	}
	if workspacePayload.WorkspaceCount != 1 || workspacePayload.PendingTotal != 1 {
		t.Fatalf("unexpected workspace-filter summary: workspaceCount=%d pendingTotal=%d", workspacePayload.WorkspaceCount, workspacePayload.PendingTotal)
	}
	if len(workspacePayload.Workspaces) != 1 {
		t.Fatalf("expected one workspace in admin ingress workspace filter payload, got %d", len(workspacePayload.Workspaces))
	}
	if workspacePayload.Workspaces["ws_admin_ingress_2"].AcceptedTotal != 1 {
		t.Fatalf("expected workspace filter to return ws_admin_ingress_2 acceptedTotal=1, got %+v", workspacePayload.Workspaces)
	}

	filteredProvider := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?provider=custom",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_3c",
		},
	})
	if filteredProvider.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin ingress provider filter, got %d (%s)", filteredProvider.Code, filteredProvider.Body.String())
	}
	var providerPayload struct {
		WorkspaceCount int                                `json:"workspaceCount"`
		PendingTotal   int                                `json:"pendingTotal"`
		Workspaces     map[string]relayfile.IngressStatus `json:"workspaces"`
	}
	if err := json.NewDecoder(filteredProvider.Body).Decode(&providerPayload); err != nil {
		t.Fatalf("decode admin ingress provider-filter payload: %v", err)
	}
	if providerPayload.WorkspaceCount < 2 || providerPayload.PendingTotal != 1 {
		t.Fatalf("unexpected provider-filter summary: workspaceCount=%d pendingTotal=%d", providerPayload.WorkspaceCount, providerPayload.PendingTotal)
	}
	providerStatus, ok := providerPayload.Workspaces["ws_admin_ingress_2"]
	if !ok {
		t.Fatalf("expected ws_admin_ingress_2 in provider-filter payload")
	}
	if providerStatus.AcceptedTotal != 1 || providerStatus.PendingTotal != 1 {
		t.Fatalf("expected provider-filtered ws_admin_ingress_2 metrics, got %+v", providerStatus)
	}
	ws1Filtered, ok := providerPayload.Workspaces["ws_admin_ingress_1"]
	if !ok {
		t.Fatalf("expected ws_admin_ingress_1 in provider-filter payload")
	}
	if ws1Filtered.AcceptedTotal != 0 || ws1Filtered.PendingTotal != 0 {
		t.Fatalf("expected zeroed ws_admin_ingress_1 metrics for custom provider filter, got %+v", ws1Filtered)
	}

	pageOneResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?limit=1",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_3d_page1",
		},
	})
	if pageOneResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin ingress page one, got %d (%s)", pageOneResp.Code, pageOneResp.Body.String())
	}
	var pageOne struct {
		WorkspaceCount         int      `json:"workspaceCount"`
		ReturnedWorkspaceCount int      `json:"returnedWorkspaceCount"`
		WorkspaceIDs           []string `json:"workspaceIds"`
		NextCursor             *string  `json:"nextCursor"`
	}
	if err := json.NewDecoder(pageOneResp.Body).Decode(&pageOne); err != nil {
		t.Fatalf("decode admin ingress page one payload: %v", err)
	}
	if pageOne.WorkspaceCount < 2 || pageOne.ReturnedWorkspaceCount != 1 || len(pageOne.WorkspaceIDs) != 1 || pageOne.NextCursor == nil {
		t.Fatalf("unexpected page one payload: %+v", pageOne)
	}

	pageTwoResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?limit=1&cursor=" + *pageOne.NextCursor,
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_3d_page2",
		},
	})
	if pageTwoResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin ingress page two, got %d (%s)", pageTwoResp.Code, pageTwoResp.Body.String())
	}
	var pageTwo struct {
		ReturnedWorkspaceCount int      `json:"returnedWorkspaceCount"`
		WorkspaceIDs           []string `json:"workspaceIds"`
	}
	if err := json.NewDecoder(pageTwoResp.Body).Decode(&pageTwo); err != nil {
		t.Fatalf("decode admin ingress page two payload: %v", err)
	}
	if pageTwo.ReturnedWorkspaceCount != 1 || len(pageTwo.WorkspaceIDs) != 1 {
		t.Fatalf("unexpected page two payload: %+v", pageTwo)
	}
	if pageTwo.WorkspaceIDs[0] == pageOne.WorkspaceIDs[0] {
		t.Fatalf("expected page two workspace id to differ from page one")
	}

	invalidCursorResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?limit=1&cursor=missing_workspace",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_3d_bad_cursor",
		},
	})
	if invalidCursorResp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid admin ingress cursor, got %d (%s)", invalidCursorResp.Code, invalidCursorResp.Body.String())
	}

	invalidProfileResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?alertProfile=unknown",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_bad_profile",
		},
	})
	if invalidProfileResp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid admin ingress alertProfile, got %d (%s)", invalidProfileResp.Code, invalidProfileResp.Body.String())
	}
	invalidPendingThresholdResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?pendingThreshold=oops",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_bad_pending_threshold",
		},
	})
	if invalidPendingThresholdResp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid pendingThreshold, got %d (%s)", invalidPendingThresholdResp.Code, invalidPendingThresholdResp.Body.String())
	}
	invalidLimitResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?limit=0",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_bad_limit",
		},
	})
	if invalidLimitResp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid limit, got %d (%s)", invalidLimitResp.Code, invalidLimitResp.Body.String())
	}
	invalidIncludeAlertsResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?includeAlerts=maybe",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_bad_include_alerts",
		},
	})
	if invalidIncludeAlertsResp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid includeAlerts boolean, got %d (%s)", invalidIncludeAlertsResp.Code, invalidIncludeAlertsResp.Body.String())
	}

	filteredProviderNonZero := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?provider=custom&nonZeroOnly=true&limit=1",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_3d",
		},
	})
	if filteredProviderNonZero.Code != http.StatusOK {
		t.Fatalf("expected 200 for nonZeroOnly provider filter, got %d (%s)", filteredProviderNonZero.Code, filteredProviderNonZero.Body.String())
	}
	var nonZeroPayload struct {
		WorkspaceCount         int                                `json:"workspaceCount"`
		ReturnedWorkspaceCount int                                `json:"returnedWorkspaceCount"`
		PendingTotal           int                                `json:"pendingTotal"`
		Workspaces             map[string]relayfile.IngressStatus `json:"workspaces"`
	}
	if err := json.NewDecoder(filteredProviderNonZero.Body).Decode(&nonZeroPayload); err != nil {
		t.Fatalf("decode admin ingress non-zero provider payload: %v", err)
	}
	if nonZeroPayload.WorkspaceCount != 1 || nonZeroPayload.ReturnedWorkspaceCount != 1 || nonZeroPayload.PendingTotal != 1 {
		t.Fatalf("unexpected nonZeroOnly summary: workspaceCount=%d returnedWorkspaceCount=%d pendingTotal=%d", nonZeroPayload.WorkspaceCount, nonZeroPayload.ReturnedWorkspaceCount, nonZeroPayload.PendingTotal)
	}
	if len(nonZeroPayload.Workspaces) != 1 {
		t.Fatalf("expected one workspace in nonZeroOnly provider filter payload, got %d", len(nonZeroPayload.Workspaces))
	}
	if _, ok := nonZeroPayload.Workspaces["ws_admin_ingress_2"]; !ok {
		t.Fatalf("expected ws_admin_ingress_2 in nonZeroOnly provider filter payload, got %+v", nonZeroPayload.Workspaces)
	}

	orderedAlertsResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?pendingThreshold=1&deadLetterThreshold=1000000&staleThreshold=1000000&dropRateThreshold=1",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_3e",
		},
	})
	if orderedAlertsResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for ordered alert response, got %d (%s)", orderedAlertsResp.Code, orderedAlertsResp.Body.String())
	}
	var orderedPayload struct {
		Alerts []struct {
			WorkspaceID string `json:"workspaceId"`
			Type        string `json:"type"`
			Severity    string `json:"severity"`
		} `json:"alerts"`
	}
	if err := json.NewDecoder(orderedAlertsResp.Body).Decode(&orderedPayload); err != nil {
		t.Fatalf("decode ordered alert payload: %v", err)
	}
	if len(orderedPayload.Alerts) != 2 {
		t.Fatalf("expected two pending_backlog alerts, got %+v", orderedPayload.Alerts)
	}
	if orderedPayload.Alerts[0].WorkspaceID != "ws_admin_ingress_1" || orderedPayload.Alerts[0].Type != "pending_backlog" || orderedPayload.Alerts[0].Severity != "warning" {
		t.Fatalf("unexpected first sorted alert: %+v", orderedPayload.Alerts[0])
	}
	if orderedPayload.Alerts[1].WorkspaceID != "ws_admin_ingress_2" || orderedPayload.Alerts[1].Type != "pending_backlog" || orderedPayload.Alerts[1].Severity != "warning" {
		t.Fatalf("unexpected second sorted alert: %+v", orderedPayload.Alerts[1])
	}

	truncatedAlertsResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?pendingThreshold=1&deadLetterThreshold=1000000&staleThreshold=1000000&dropRateThreshold=1&maxAlerts=1",
		headers: map[string]string{
			"Authorization":    "Bearer " + readToken,
			"X-Correlation-Id": "corr_admin_ingress_3f",
		},
	})
	if truncatedAlertsResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for truncated alerts response, got %d (%s)", truncatedAlertsResp.Code, truncatedAlertsResp.Body.String())
	}
	var truncatedPayload struct {
		EffectiveAlertProfile string `json:"effectiveAlertProfile"`
		AlertsTruncated       bool   `json:"alertsTruncated"`
		AlertTotals           struct {
			Total int `json:"total"`
		} `json:"alertTotals"`
		Alerts []map[string]any `json:"alerts"`
	}
	if err := json.NewDecoder(truncatedAlertsResp.Body).Decode(&truncatedPayload); err != nil {
		t.Fatalf("decode truncated alert payload: %v", err)
	}
	if !truncatedPayload.AlertsTruncated {
		t.Fatalf("expected alertsTruncated=true for maxAlerts=1 payload")
	}
	if truncatedPayload.EffectiveAlertProfile != "custom" {
		t.Fatalf("expected effectiveAlertProfile=custom when thresholds are overridden, got %q", truncatedPayload.EffectiveAlertProfile)
	}
	if truncatedPayload.AlertTotals.Total != 2 {
		t.Fatalf("expected alertTotals.total=2 despite truncation, got %+v", truncatedPayload.AlertTotals)
	}
	if len(truncatedPayload.Alerts) != 1 {
		t.Fatalf("expected one returned alert after truncation, got %+v", truncatedPayload.Alerts)
	}

	deniedToken := mustTestJWT(
		t,
		"dev-secret",
		"ws_admin_ingress_1",
		"Worker1",
		[]string{"sync:read"},
		time.Now().Add(time.Hour),
	)
	denied := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress",
		headers: map[string]string{
			"Authorization":    "Bearer " + deniedToken,
			"X-Correlation-Id": "corr_admin_ingress_4",
		},
	})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing admin scope on admin ingress endpoint, got %d (%s)", denied.Code, denied.Body.String())
	}
}

func TestAdminIngressEndpointIncludesDeadLetterAlert(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		MaxEnvelopeAttempts: 1,
		EnvelopeRetryDelay:  5 * time.Millisecond,
		Adapters: []relayfile.ProviderAdapter{
			serverFailingAdapter{provider: "customdead"},
		},
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	_, err := store.IngestEnvelope(relayfile.WebhookEnvelopeRequest{
		EnvelopeID:    "env_admin_ingress_alert_1",
		WorkspaceID:   "ws_admin_ingress_alert",
		Provider:      "customdead",
		DeliveryID:    "delivery_admin_ingress_alert_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "customdead.event"},
		CorrelationID: "corr_admin_ingress_alert_1",
	})
	if err != nil {
		t.Fatalf("ingest alert envelope failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := store.GetIngressStatus("ws_admin_ingress_alert")
		if statusErr == nil && status.DeadLetterTotal >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	adminToken := mustTestJWT(
		t,
		"dev-secret",
		"ws_admin_ingress_alert",
		"Lead",
		[]string{"admin:read"},
		time.Now().Add(time.Hour),
	)
	highThresholdResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?workspaceId=ws_admin_ingress_alert&deadLetterThreshold=2",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_ingress_alert_2a",
		},
	})
	if highThresholdResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin ingress alert endpoint with high deadLetterThreshold, got %d (%s)", highThresholdResp.Code, highThresholdResp.Body.String())
	}
	var highThresholdPayload struct {
		AlertProfile          string `json:"alertProfile"`
		EffectiveAlertProfile string `json:"effectiveAlertProfile"`
		Thresholds            struct {
			DeadLetter int `json:"deadLetter"`
		} `json:"thresholds"`
		Alerts []map[string]any `json:"alerts"`
	}
	if err := json.NewDecoder(highThresholdResp.Body).Decode(&highThresholdPayload); err != nil {
		t.Fatalf("decode high-threshold admin ingress payload: %v", err)
	}
	if highThresholdPayload.Thresholds.DeadLetter != 2 {
		t.Fatalf("expected deadLetterThreshold=2 in response, got %+v", highThresholdPayload.Thresholds)
	}
	if highThresholdPayload.AlertProfile != "balanced" {
		t.Fatalf("expected alertProfile=balanced for default profile with deadLetter override, got %q", highThresholdPayload.AlertProfile)
	}
	if highThresholdPayload.EffectiveAlertProfile != "custom" {
		t.Fatalf("expected effectiveAlertProfile=custom for deadLetter override, got %q", highThresholdPayload.EffectiveAlertProfile)
	}
	if len(highThresholdPayload.Alerts) != 0 {
		t.Fatalf("expected no alerts when deadLetterThreshold=2 for single dead letter, got %+v", highThresholdPayload.Alerts)
	}

	relaxedProfileResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?workspaceId=ws_admin_ingress_alert&alertProfile=relaxed",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_ingress_alert_2_relaxed",
		},
	})
	if relaxedProfileResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for relaxed alert profile payload, got %d (%s)", relaxedProfileResp.Code, relaxedProfileResp.Body.String())
	}
	var relaxedProfilePayload struct {
		AlertProfile          string `json:"alertProfile"`
		EffectiveAlertProfile string `json:"effectiveAlertProfile"`
		Thresholds            struct {
			DeadLetter int `json:"deadLetter"`
		} `json:"thresholds"`
		AlertTotals struct {
			Total int `json:"total"`
		} `json:"alertTotals"`
		Alerts []map[string]any `json:"alerts"`
	}
	if err := json.NewDecoder(relaxedProfileResp.Body).Decode(&relaxedProfilePayload); err != nil {
		t.Fatalf("decode relaxed profile payload: %v", err)
	}
	if relaxedProfilePayload.AlertProfile != "relaxed" || relaxedProfilePayload.Thresholds.DeadLetter != 5 {
		t.Fatalf("unexpected relaxed profile payload: %+v", relaxedProfilePayload)
	}
	if relaxedProfilePayload.EffectiveAlertProfile != "relaxed" {
		t.Fatalf("expected effectiveAlertProfile=relaxed without threshold overrides, got %q", relaxedProfilePayload.EffectiveAlertProfile)
	}
	if relaxedProfilePayload.AlertTotals.Total != 0 || len(relaxedProfilePayload.Alerts) != 0 {
		t.Fatalf("expected no alerts for relaxed profile single dead-letter payload, got totals=%+v alerts=%+v", relaxedProfilePayload.AlertTotals, relaxedProfilePayload.Alerts)
	}

	summaryOnlyAlertsResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?workspaceId=ws_admin_ingress_alert&includeAlerts=false",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_ingress_alert_2b",
		},
	})
	if summaryOnlyAlertsResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for includeAlerts=false payload, got %d (%s)", summaryOnlyAlertsResp.Code, summaryOnlyAlertsResp.Body.String())
	}
	var summaryOnlyAlertsPayload struct {
		AlertsTruncated bool `json:"alertsTruncated"`
		AlertTotals     struct {
			Total    int            `json:"total"`
			Critical int            `json:"critical"`
			ByType   map[string]int `json:"byType"`
		} `json:"alertTotals"`
		Alerts []map[string]any `json:"alerts"`
	}
	if err := json.NewDecoder(summaryOnlyAlertsResp.Body).Decode(&summaryOnlyAlertsPayload); err != nil {
		t.Fatalf("decode includeAlerts=false payload: %v", err)
	}
	if summaryOnlyAlertsPayload.AlertsTruncated {
		t.Fatalf("expected alertsTruncated=false when includeAlerts=false")
	}
	if len(summaryOnlyAlertsPayload.Alerts) != 0 {
		t.Fatalf("expected zero alerts when includeAlerts=false, got %+v", summaryOnlyAlertsPayload.Alerts)
	}
	if summaryOnlyAlertsPayload.AlertTotals.Total < 1 || summaryOnlyAlertsPayload.AlertTotals.Critical < 1 || summaryOnlyAlertsPayload.AlertTotals.ByType["dead_letters"] < 1 {
		t.Fatalf("expected alertTotals to remain available when includeAlerts=false, got %+v", summaryOnlyAlertsPayload.AlertTotals)
	}

	resp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/ingress?workspaceId=ws_admin_ingress_alert",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_ingress_alert_2",
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin ingress alert endpoint, got %d (%s)", resp.Code, resp.Body.String())
	}
	var payload struct {
		AlertTotals struct {
			Total    int            `json:"total"`
			Critical int            `json:"critical"`
			Warning  int            `json:"warning"`
			ByType   map[string]int `json:"byType"`
		} `json:"alertTotals"`
		AlertsTruncated bool `json:"alertsTruncated"`
		Alerts          []struct {
			WorkspaceID string `json:"workspaceId"`
			Type        string `json:"type"`
			Severity    string `json:"severity"`
		} `json:"alerts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode admin ingress alert payload: %v", err)
	}
	found := false
	for _, alert := range payload.Alerts {
		if alert.WorkspaceID == "ws_admin_ingress_alert" && alert.Type == "dead_letters" && alert.Severity == "critical" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected dead_letters critical alert in payload, got %+v", payload.Alerts)
	}
	if payload.AlertTotals.Total < 1 || payload.AlertTotals.Critical < 1 || payload.AlertTotals.ByType["dead_letters"] < 1 {
		t.Fatalf("expected dead-letter alert totals in payload, got %+v", payload.AlertTotals)
	}
	if payload.AlertsTruncated {
		t.Fatalf("expected alertsTruncated=false for single-alert payload")
	}
}

func TestAdminSyncEndpoint(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{
		MaxEnvelopeAttempts: 1,
		EnvelopeRetryDelay:  5 * time.Millisecond,
		Adapters: []relayfile.ProviderAdapter{
			serverFailingAdapter{provider: "customdead"},
		},
	})
	t.Cleanup(store.Close)
	server := NewServer(store)

	_, err := store.IngestEnvelope(relayfile.WebhookEnvelopeRequest{
		EnvelopeID:    "env_admin_sync_healthy_1",
		WorkspaceID:   "ws_admin_sync_healthy",
		Provider:      "notion",
		DeliveryID:    "delivery_admin_sync_healthy_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "sync"},
		CorrelationID: "corr_admin_sync_healthy_1",
	})
	if err != nil {
		t.Fatalf("ingest healthy sync envelope failed: %v", err)
	}
	_, err = store.IngestEnvelope(relayfile.WebhookEnvelopeRequest{
		EnvelopeID:    "env_admin_sync_error_1",
		WorkspaceID:   "ws_admin_sync_error",
		Provider:      "customdead",
		DeliveryID:    "delivery_admin_sync_error_1",
		ReceivedAt:    time.Now().UTC().Format(time.RFC3339Nano),
		Payload:       map[string]any{"type": "customdead.event"},
		CorrelationID: "corr_admin_sync_error_1",
	})
	if err != nil {
		t.Fatalf("ingest error sync envelope failed: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, statusErr := store.GetSyncStatus("ws_admin_sync_error", "customdead")
		if statusErr != nil {
			t.Fatalf("sync status lookup failed: %v", statusErr)
		}
		if len(status.Providers) == 1 && status.Providers[0].DeadLetteredEnvelopes >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	adminToken := mustTestJWT(
		t,
		"dev-secret",
		"ws_admin_sync_healthy",
		"Lead",
		[]string{"admin:read"},
		time.Now().Add(time.Hour),
	)
	resp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/sync",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_sync_2",
		},
	})
	if resp.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin sync endpoint, got %d (%s)", resp.Code, resp.Body.String())
	}
	var payload struct {
		WorkspaceCount             int      `json:"workspaceCount"`
		ReturnedWorkspaceCount     int      `json:"returnedWorkspaceCount"`
		WorkspaceIDs               []string `json:"workspaceIds"`
		NextCursor                 *string  `json:"nextCursor"`
		ProviderStatusCount        int      `json:"providerStatusCount"`
		ErrorCount                 int      `json:"errorCount"`
		DeadLetteredEnvelopesTotal int      `json:"deadLetteredEnvelopesTotal"`
		Thresholds                 struct {
			StatusError           int `json:"statusError"`
			LagSeconds            int `json:"lagSeconds"`
			DeadLetteredEnvelopes int `json:"deadLetteredEnvelopes"`
			DeadLetteredOps       int `json:"deadLetteredOps"`
		} `json:"thresholds"`
		AlertTotals struct {
			Total    int            `json:"total"`
			Critical int            `json:"critical"`
			Warning  int            `json:"warning"`
			ByType   map[string]int `json:"byType"`
		} `json:"alertTotals"`
		AlertsTruncated bool                            `json:"alertsTruncated"`
		Alerts          []map[string]any                `json:"alerts"`
		FailureCodes    map[string]int                  `json:"failureCodes"`
		Workspaces      map[string]relayfile.SyncStatus `json:"workspaces"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode admin sync payload: %v", err)
	}
	if payload.WorkspaceCount < 2 {
		t.Fatalf("expected at least two workspaces in admin sync payload, got %d", payload.WorkspaceCount)
	}
	if payload.ReturnedWorkspaceCount < 2 {
		t.Fatalf("expected at least two returned workspaces in admin sync payload, got %d", payload.ReturnedWorkspaceCount)
	}
	if len(payload.WorkspaceIDs) != payload.ReturnedWorkspaceCount || payload.NextCursor != nil {
		t.Fatalf("unexpected workspace pagination payload: ids=%d returned=%d nextCursor=%v", len(payload.WorkspaceIDs), payload.ReturnedWorkspaceCount, payload.NextCursor)
	}
	if payload.ProviderStatusCount < 2 {
		t.Fatalf("expected at least two provider statuses in admin sync payload, got %d", payload.ProviderStatusCount)
	}
	if payload.ErrorCount < 1 || payload.DeadLetteredEnvelopesTotal < 1 || payload.FailureCodes["unknown"] < 1 {
		t.Fatalf("expected aggregated error counters in admin sync payload, got %+v", payload)
	}
	if payload.Thresholds.StatusError != 1 || payload.Thresholds.LagSeconds != 30 || payload.Thresholds.DeadLetteredEnvelopes != 1 || payload.Thresholds.DeadLetteredOps != 1 {
		t.Fatalf("unexpected default admin sync thresholds: %+v", payload.Thresholds)
	}
	if payload.AlertTotals.Total < 2 || payload.AlertTotals.Critical < 2 || payload.AlertTotals.ByType["status_error"] < 1 || payload.AlertTotals.ByType["dead_lettered_envelopes"] < 1 {
		t.Fatalf("expected status_error and dead_lettered_envelopes alerts in admin sync payload, got %+v", payload.AlertTotals)
	}
	if payload.AlertsTruncated {
		t.Fatalf("expected alertsTruncated=false for baseline admin sync payload")
	}
	if len(payload.Alerts) < 2 {
		t.Fatalf("expected at least two admin sync alerts in baseline payload, got %+v", payload.Alerts)
	}
	if _, ok := payload.Workspaces["ws_admin_sync_healthy"]; !ok {
		t.Fatalf("expected ws_admin_sync_healthy in admin sync payload")
	}
	if _, ok := payload.Workspaces["ws_admin_sync_error"]; !ok {
		t.Fatalf("expected ws_admin_sync_error in admin sync payload")
	}
	summaryOnlyResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/sync?includeWorkspaces=false&cursor=missing_workspace&limit=1",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_sync_2_summary_only",
		},
	})
	if summaryOnlyResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for summary-only admin sync endpoint, got %d (%s)", summaryOnlyResp.Code, summaryOnlyResp.Body.String())
	}
	var summaryOnlyPayload struct {
		WorkspaceCount         int                             `json:"workspaceCount"`
		ReturnedWorkspaceCount int                             `json:"returnedWorkspaceCount"`
		WorkspaceIDs           []string                        `json:"workspaceIds"`
		NextCursor             *string                         `json:"nextCursor"`
		Workspaces             map[string]relayfile.SyncStatus `json:"workspaces"`
	}
	if err := json.NewDecoder(summaryOnlyResp.Body).Decode(&summaryOnlyPayload); err != nil {
		t.Fatalf("decode summary-only admin sync payload: %v", err)
	}
	if summaryOnlyPayload.WorkspaceCount < 2 || summaryOnlyPayload.ReturnedWorkspaceCount != 0 || len(summaryOnlyPayload.WorkspaceIDs) != 0 || len(summaryOnlyPayload.Workspaces) != 0 || summaryOnlyPayload.NextCursor != nil {
		t.Fatalf("unexpected summary-only admin sync payload: %+v", summaryOnlyPayload)
	}
	pageOneResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/sync?limit=1",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_sync_2_page_one",
		},
	})
	if pageOneResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for page one admin sync endpoint, got %d (%s)", pageOneResp.Code, pageOneResp.Body.String())
	}
	var pageOnePayload struct {
		WorkspaceCount         int      `json:"workspaceCount"`
		ReturnedWorkspaceCount int      `json:"returnedWorkspaceCount"`
		WorkspaceIDs           []string `json:"workspaceIds"`
		NextCursor             *string  `json:"nextCursor"`
	}
	if err := json.NewDecoder(pageOneResp.Body).Decode(&pageOnePayload); err != nil {
		t.Fatalf("decode page one admin sync payload: %v", err)
	}
	if pageOnePayload.WorkspaceCount < 2 || pageOnePayload.ReturnedWorkspaceCount != 1 || len(pageOnePayload.WorkspaceIDs) != 1 || pageOnePayload.NextCursor == nil {
		t.Fatalf("unexpected page one admin sync payload: %+v", pageOnePayload)
	}
	pageTwoResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/sync?limit=1&cursor=" + *pageOnePayload.NextCursor,
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_sync_2_page_two",
		},
	})
	if pageTwoResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for page two admin sync endpoint, got %d (%s)", pageTwoResp.Code, pageTwoResp.Body.String())
	}
	var pageTwoPayload struct {
		ReturnedWorkspaceCount int      `json:"returnedWorkspaceCount"`
		WorkspaceIDs           []string `json:"workspaceIds"`
	}
	if err := json.NewDecoder(pageTwoResp.Body).Decode(&pageTwoPayload); err != nil {
		t.Fatalf("decode page two admin sync payload: %v", err)
	}
	if pageTwoPayload.ReturnedWorkspaceCount != 1 || len(pageTwoPayload.WorkspaceIDs) != 1 {
		t.Fatalf("unexpected page two admin sync payload: %+v", pageTwoPayload)
	}
	if pageTwoPayload.WorkspaceIDs[0] == pageOnePayload.WorkspaceIDs[0] {
		t.Fatalf("expected page two workspace id to differ from page one for admin sync endpoint")
	}
	invalidCursorResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/sync?limit=1&cursor=missing_workspace",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_sync_bad_cursor",
		},
	})
	if invalidCursorResp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid admin sync cursor, got %d (%s)", invalidCursorResp.Code, invalidCursorResp.Body.String())
	}

	filteredResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/sync?provider=customdead&nonZeroOnly=true",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_sync_3",
		},
	})
	if filteredResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for provider-filtered admin sync endpoint, got %d (%s)", filteredResp.Code, filteredResp.Body.String())
	}
	var filteredPayload struct {
		WorkspaceCount      int                             `json:"workspaceCount"`
		ProviderStatusCount int                             `json:"providerStatusCount"`
		Workspaces          map[string]relayfile.SyncStatus `json:"workspaces"`
	}
	if err := json.NewDecoder(filteredResp.Body).Decode(&filteredPayload); err != nil {
		t.Fatalf("decode provider-filtered admin sync payload: %v", err)
	}
	if filteredPayload.WorkspaceCount != 1 || filteredPayload.ProviderStatusCount != 1 {
		t.Fatalf("unexpected provider-filtered admin sync summary: %+v", filteredPayload)
	}
	if _, ok := filteredPayload.Workspaces["ws_admin_sync_error"]; !ok {
		t.Fatalf("expected ws_admin_sync_error in provider-filtered admin sync payload, got %+v", filteredPayload.Workspaces)
	}
	summaryOnlyAlertsResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/sync?workspaceId=ws_admin_sync_error&includeAlerts=false",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_sync_4",
		},
	})
	if summaryOnlyAlertsResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for includeAlerts=false admin sync endpoint, got %d (%s)", summaryOnlyAlertsResp.Code, summaryOnlyAlertsResp.Body.String())
	}
	var summaryOnlyAlertsPayload struct {
		AlertTotals struct {
			Total    int            `json:"total"`
			Critical int            `json:"critical"`
			ByType   map[string]int `json:"byType"`
		} `json:"alertTotals"`
		AlertsTruncated bool             `json:"alertsTruncated"`
		Alerts          []map[string]any `json:"alerts"`
	}
	if err := json.NewDecoder(summaryOnlyAlertsResp.Body).Decode(&summaryOnlyAlertsPayload); err != nil {
		t.Fatalf("decode includeAlerts=false admin sync payload: %v", err)
	}
	if summaryOnlyAlertsPayload.AlertsTruncated {
		t.Fatalf("expected alertsTruncated=false when includeAlerts=false for admin sync")
	}
	if len(summaryOnlyAlertsPayload.Alerts) != 0 {
		t.Fatalf("expected no alerts when includeAlerts=false for admin sync, got %+v", summaryOnlyAlertsPayload.Alerts)
	}
	if summaryOnlyAlertsPayload.AlertTotals.Total < 2 || summaryOnlyAlertsPayload.AlertTotals.Critical < 2 || summaryOnlyAlertsPayload.AlertTotals.ByType["status_error"] < 1 {
		t.Fatalf("expected alert totals to remain when includeAlerts=false for admin sync, got %+v", summaryOnlyAlertsPayload.AlertTotals)
	}
	truncatedAlertsResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/sync?workspaceId=ws_admin_sync_error&maxAlerts=1",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_sync_5",
		},
	})
	if truncatedAlertsResp.Code != http.StatusOK {
		t.Fatalf("expected 200 for truncated admin sync alert payload, got %d (%s)", truncatedAlertsResp.Code, truncatedAlertsResp.Body.String())
	}
	var truncatedAlertsPayload struct {
		AlertsTruncated bool `json:"alertsTruncated"`
		AlertTotals     struct {
			Total int `json:"total"`
		} `json:"alertTotals"`
		Alerts []map[string]any `json:"alerts"`
	}
	if err := json.NewDecoder(truncatedAlertsResp.Body).Decode(&truncatedAlertsPayload); err != nil {
		t.Fatalf("decode truncated admin sync alert payload: %v", err)
	}
	if !truncatedAlertsPayload.AlertsTruncated {
		t.Fatalf("expected alertsTruncated=true for admin sync maxAlerts=1 payload")
	}
	if truncatedAlertsPayload.AlertTotals.Total < 2 || len(truncatedAlertsPayload.Alerts) != 1 {
		t.Fatalf("expected admin sync alert totals to remain untruncated with one returned alert, got totals=%+v alerts=%+v", truncatedAlertsPayload.AlertTotals, truncatedAlertsPayload.Alerts)
	}

	invalidNonZeroOnlyResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/sync?nonZeroOnly=maybe",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_sync_bad_nonzero",
		},
	})
	if invalidNonZeroOnlyResp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid admin sync nonZeroOnly, got %d (%s)", invalidNonZeroOnlyResp.Code, invalidNonZeroOnlyResp.Body.String())
	}
	invalidIncludeWorkspacesResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/sync?includeWorkspaces=maybe",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_sync_bad_include_workspaces",
		},
	})
	if invalidIncludeWorkspacesResp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid admin sync includeWorkspaces, got %d (%s)", invalidIncludeWorkspacesResp.Code, invalidIncludeWorkspacesResp.Body.String())
	}
	invalidLagThresholdResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/sync?lagSecondsThreshold=0",
		headers: map[string]string{
			"Authorization":    "Bearer " + adminToken,
			"X-Correlation-Id": "corr_admin_sync_bad_lag_threshold",
		},
	})
	if invalidLagThresholdResp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid admin sync lagSecondsThreshold, got %d (%s)", invalidLagThresholdResp.Code, invalidLagThresholdResp.Body.String())
	}

	deniedToken := mustTestJWT(
		t,
		"dev-secret",
		"ws_admin_sync_healthy",
		"Worker1",
		[]string{"sync:read"},
		time.Now().Add(time.Hour),
	)
	deniedResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/admin/sync",
		headers: map[string]string{
			"Authorization":    "Bearer " + deniedToken,
			"X-Correlation-Id": "corr_admin_sync_denied",
		},
	})
	if deniedResp.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing admin scope on admin sync endpoint, got %d (%s)", deniedResp.Code, deniedResp.Body.String())
	}
}

func TestInternalIngressAppliesToFilesystemAPI(t *testing.T) {
	server := NewServer(relayfile.NewStore())

	envelope := map[string]any{
		"envelopeId":  "env_apply_api_1",
		"workspaceId": "ws_apply_api",
		"provider":    "external",
		"deliveryId":  "delivery_apply_api_1",
		"receivedAt":  time.Now().UTC().Format(time.RFC3339),
		"payload": map[string]any{
			"event_type": "file.created",
			"path":       "/documents/Ingress.md",
			"content":    "# ingress",
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
			"X-Correlation-Id":  "corr_apply_api_1",
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
			path:   "/v1/workspaces/ws_apply_api/fs/file?path=/documents/Ingress.md",
			headers: map[string]string{
				"Authorization":    "Bearer " + readToken,
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
				"Authorization":    "Bearer " + token,
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
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_rate_denied",
		},
	})
	if denied.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after rate limit exceeded, got %d (%s)", denied.Code, denied.Body.String())
	}
	if denied.Header().Get("Retry-After") != "60" {
		t.Fatalf("expected Retry-After=60 on rate limit response, got %q", denied.Header().Get("Retry-After"))
	}
}

func TestParseBoundedInt(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		fallback int
		min      int
		max      int
		want     int
	}{
		{
			name:     "empty uses fallback",
			raw:      "",
			fallback: 100,
			min:      1,
			max:      1000,
			want:     100,
		},
		{
			name:     "valid value",
			raw:      "250",
			fallback: 100,
			min:      1,
			max:      1000,
			want:     250,
		},
		{
			name:     "non numeric uses fallback",
			raw:      "abc",
			fallback: 100,
			min:      1,
			max:      1000,
			want:     100,
		},
		{
			name:     "below min uses fallback",
			raw:      "0",
			fallback: 100,
			min:      1,
			max:      1000,
			want:     100,
		},
		{
			name:     "above max clamps",
			raw:      "5000",
			fallback: 100,
			min:      1,
			max:      1000,
			want:     1000,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBoundedInt(tc.raw, tc.fallback, tc.min, tc.max)
			if got != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, got)
			}
		})
	}
}

func TestParseBoundedFloat(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		fallback float64
		min      float64
		max      float64
		want     float64
	}{
		{
			name:     "empty uses fallback",
			raw:      "",
			fallback: 0.05,
			min:      0,
			max:      1,
			want:     0.05,
		},
		{
			name:     "valid value",
			raw:      "0.2",
			fallback: 0.05,
			min:      0,
			max:      1,
			want:     0.2,
		},
		{
			name:     "non numeric uses fallback",
			raw:      "oops",
			fallback: 0.05,
			min:      0,
			max:      1,
			want:     0.05,
		},
		{
			name:     "below min clamps",
			raw:      "-1",
			fallback: 0.05,
			min:      0,
			max:      1,
			want:     0,
		},
		{
			name:     "above max clamps",
			raw:      "5",
			fallback: 0.05,
			min:      0,
			max:      1,
			want:     1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBoundedFloat(tc.raw, tc.fallback, tc.min, tc.max)
			if got != tc.want {
				t.Fatalf("expected %f, got %f", tc.want, got)
			}
		})
	}
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		fallback bool
		want     bool
	}{
		{name: "empty uses fallback", raw: "", fallback: false, want: false},
		{name: "valid true", raw: "true", fallback: false, want: true},
		{name: "valid false", raw: "false", fallback: true, want: false},
		{name: "valid numeric true", raw: "1", fallback: false, want: true},
		{name: "invalid uses fallback", raw: "oops", fallback: true, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBool(tc.raw, tc.fallback)
			if got != tc.want {
				t.Fatalf("expected %t, got %t", tc.want, got)
			}
		})
	}
}

func TestParseOptionalBoundedInt(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		fallback int
		min      int
		max      int
		want     int
		wantErr  bool
	}{
		{name: "empty uses fallback", raw: "", fallback: 3, min: 1, max: 10, want: 3},
		{name: "valid value", raw: "5", fallback: 3, min: 1, max: 10, want: 5},
		{name: "non numeric errors", raw: "oops", fallback: 3, min: 1, max: 10, wantErr: true},
		{name: "below min errors", raw: "0", fallback: 3, min: 1, max: 10, wantErr: true},
		{name: "above max errors", raw: "11", fallback: 3, min: 1, max: 10, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOptionalBoundedInt(tc.raw, tc.fallback, tc.min, tc.max)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (value=%d)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, got)
			}
		})
	}
}

func TestParseOptionalBoundedFloat(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		fallback float64
		min      float64
		max      float64
		want     float64
		wantErr  bool
	}{
		{name: "empty uses fallback", raw: "", fallback: 0.25, min: 0, max: 1, want: 0.25},
		{name: "valid value", raw: "0.75", fallback: 0.25, min: 0, max: 1, want: 0.75},
		{name: "non numeric errors", raw: "oops", fallback: 0.25, min: 0, max: 1, wantErr: true},
		{name: "below min errors", raw: "-0.1", fallback: 0.25, min: 0, max: 1, wantErr: true},
		{name: "above max errors", raw: "1.1", fallback: 0.25, min: 0, max: 1, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOptionalBoundedFloat(tc.raw, tc.fallback, tc.min, tc.max)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (value=%f)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %f, got %f", tc.want, got)
			}
		})
	}
}

func TestParseOptionalBool(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		fallback bool
		want     bool
		wantErr  bool
	}{
		{name: "empty uses fallback", raw: "", fallback: true, want: true},
		{name: "valid true", raw: "true", fallback: false, want: true},
		{name: "valid false", raw: "false", fallback: true, want: false},
		{name: "invalid errors", raw: "maybe", fallback: true, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseOptionalBool(tc.raw, tc.fallback)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (value=%t)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %t, got %t", tc.want, got)
			}
		})
	}
}

// Tests for Phase 1: Generic webhook ingestion API (provider-agnostic)

func TestGenericWebhookIngestion(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	token := mustTestJWT(t, "dev-secret", "ws_1", "Worker1", []string{"fs:read", "fs:write", "sync:read"}, time.Now().Add(time.Hour))

	// Test 1: Ingest generic webhook with provider="notion"
	ingestResp := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_1/webhooks/ingest",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_webhook_1",
		},
		body: map[string]any{
			"provider":   "notion",
			"event_type": "file.updated",
			"path":       "/documents/page123.md",
			"data": map[string]any{
				"content": "# Updated Page",
				"title":   "Page 123",
			},
			"delivery_id": "evt_123",
			"timestamp":   time.Now().UTC().Format(time.RFC3339),
		},
	})

	if ingestResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d (%s)", ingestResp.Code, ingestResp.Body.String())
	}

	var queuedResp relayfile.QueuedResponse
	if err := json.NewDecoder(ingestResp.Body).Decode(&queuedResp); err != nil {
		t.Fatalf("failed to decode queued response: %v", err)
	}
	if queuedResp.Status != "queued" {
		t.Fatalf("expected status 'queued', got %q", queuedResp.Status)
	}
	if queuedResp.ID == "" {
		t.Fatalf("expected envelope ID in response")
	}
}

func TestGenericWebhookIngestionMultipleProviders(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	token := mustTestJWT(t, "dev-secret", "ws_1", "Worker1", []string{"fs:read", "fs:write", "sync:read"}, time.Now().Add(time.Hour))

	testCases := []struct {
		name     string
		provider string
		eventType string
	}{
		{
			name:      "Notion provider",
			provider:  "notion",
			eventType: "file.updated",
		},
		{
			name:      "Salesforce provider",
			provider:  "salesforce",
			eventType: "object.changed",
		},
		{
			name:      "Generic provider",
			provider:  "custom",
			eventType: "custom.event",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ingestResp := doRequest(t, server, request{
				method: http.MethodPost,
				path:   "/v1/workspaces/ws_1/webhooks/ingest",
				headers: map[string]string{
					"Authorization":    "Bearer " + token,
					"X-Correlation-Id": "corr_webhook_" + tc.provider,
				},
				body: map[string]any{
					"provider":   tc.provider,
					"event_type": tc.eventType,
					"path":       "/path/to/file",
					"data": map[string]any{
						"key": "value",
					},
					"delivery_id": "evt_" + tc.provider,
					"timestamp":   time.Now().UTC().Format(time.RFC3339),
				},
			})

			if ingestResp.Code != http.StatusAccepted {
				t.Fatalf("expected 202 Accepted, got %d (%s)", ingestResp.Code, ingestResp.Body.String())
			}
		})
	}
}

func TestGenericWebhookIngestionMissingFields(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	token := mustTestJWT(t, "dev-secret", "ws_1", "Worker1", []string{"fs:read", "fs:write", "sync:read"}, time.Now().Add(time.Hour))

	testCases := []struct {
		name   string
		body   map[string]any
		expect int
	}{
		{
			name: "Missing provider",
			body: map[string]any{
				"event_type": "file.updated",
				"path":       "/doc.md",
				"data":       map[string]any{},
				"delivery_id": "evt_1",
				"timestamp":   time.Now().UTC().Format(time.RFC3339),
			},
			expect: http.StatusBadRequest,
		},
		{
			name: "Missing event_type",
			body: map[string]any{
				"provider":   "notion",
				"path":       "/doc.md",
				"data":       map[string]any{},
				"delivery_id": "evt_1",
				"timestamp":   time.Now().UTC().Format(time.RFC3339),
			},
			expect: http.StatusBadRequest,
		},
		{
			name: "Missing path",
			body: map[string]any{
				"provider":   "notion",
				"event_type": "file.updated",
				"data":       map[string]any{},
				"delivery_id": "evt_1",
				"timestamp":   time.Now().UTC().Format(time.RFC3339),
			},
			expect: http.StatusBadRequest,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp := doRequest(t, server, request{
				method: http.MethodPost,
				path:   "/v1/workspaces/ws_1/webhooks/ingest",
				headers: map[string]string{
					"Authorization":    "Bearer " + token,
					"X-Correlation-Id": "corr_webhook_test",
				},
				body: tc.body,
			})

			if resp.Code != tc.expect {
				t.Fatalf("expected %d, got %d (%s)", tc.expect, resp.Code, resp.Body.String())
			}
		})
	}
}

func TestWritebackQueueAPI(t *testing.T) {
	store := relayfile.NewStore()
	server := NewServer(store)
	token := mustTestJWT(t, "dev-secret", "ws_1", "Worker1", []string{"fs:read", "fs:write", "sync:read"}, time.Now().Add(time.Hour))

	// Test 1: Get empty writeback queue
	pendingResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_1/writeback/pending",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_query_1",
		},
	})

	if pendingResp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d (%s)", pendingResp.Code, pendingResp.Body.String())
	}

	var items []map[string]any
	if err := json.NewDecoder(pendingResp.Body).Decode(&items); err != nil {
		t.Fatalf("failed to decode pending response: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected empty queue, got %d items", len(items))
	}
}

func TestGenericPassthroughForUnknownProvider(t *testing.T) {
	server := NewServer(relayfile.NewStore())
	token := mustTestJWT(t, "dev-secret", "ws_1", "Worker1", []string{"fs:read", "fs:write", "sync:read"}, time.Now().Add(time.Hour))

	// Ingest a webhook for an unknown provider with generic format
	ingestResp := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_1/webhooks/ingest",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_generic_1",
		},
		body: map[string]any{
			"provider":   "custom-system",
			"event_type": "file.updated",
			"path":       "/data/record_123.json",
			"data": map[string]any{
				"content":        `{"id": 123, "name": "Test Record"}`,
				"contentType":    "application/json",
				"providerObjectId": "obj_123",
			},
			"delivery_id": "evt_custom_1",
			"timestamp":   time.Now().UTC().Format(time.RFC3339),
		},
	})

	if ingestResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d (%s)", ingestResp.Code, ingestResp.Body.String())
	}

	var queuedResp relayfile.QueuedResponse
	if err := json.NewDecoder(ingestResp.Body).Decode(&queuedResp); err != nil {
		t.Fatalf("failed to decode queued response: %v", err)
	}
	if queuedResp.Status != "queued" {
		t.Fatalf("expected status 'queued', got %q", queuedResp.Status)
	}

	// Wait for async processing
	time.Sleep(50 * time.Millisecond)

	// Verify the file was created via generic processor
	readResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_1/fs/file?path=/data/record_123.json",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_read_1",
		},
	})

	if readResp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on read, got %d (%s)", readResp.Code, readResp.Body.String())
	}

	var fileResp map[string]any
	if err := json.NewDecoder(readResp.Body).Decode(&fileResp); err != nil {
		t.Fatalf("failed to decode file response: %v", err)
	}
	if fileResp["path"] != "/data/record_123.json" {
		t.Fatalf("expected path /data/record_123.json, got %v", fileResp["path"])
	}
	if fileResp["content"] != `{"id": 123, "name": "Test Record"}` {
		t.Fatalf("expected content preserved, got %v", fileResp["content"])
	}
}

func TestWritebackQueueACK(t *testing.T) {
	store := relayfile.NewStore()
	server := NewServer(store)
	token := mustTestJWT(t, "dev-secret", "ws_1", "Worker1", []string{"fs:read", "fs:write", "sync:read", "sync:trigger", "ops:replay"}, time.Now().Add(time.Hour))

	// Create a file write that will queue a writeback
	writeResp := doRequest(t, server, request{
		method: http.MethodPut,
		path:   "/v1/workspaces/ws_1/fs/file?path=/test.md",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_write_1",
			"If-Match":         "0",
		},
		body: map[string]any{
			"contentType": "text/markdown",
			"content":     "# Test",
		},
	})

	if writeResp.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on write, got %d (%s)", writeResp.Code, writeResp.Body.String())
	}

	var writeResult relayfile.WriteResult
	if err := json.NewDecoder(writeResp.Body).Decode(&writeResult); err != nil {
		t.Fatalf("failed to decode write response: %v", err)
	}

	// Query pending writebacks
	pendingResp := doRequest(t, server, request{
		method: http.MethodGet,
		path:   "/v1/workspaces/ws_1/writeback/pending",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_query_1",
		},
	})

	if pendingResp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d (%s)", pendingResp.Code, pendingResp.Body.String())
	}

	var items []map[string]any
	if err := json.NewDecoder(pendingResp.Body).Decode(&items); err != nil {
		t.Fatalf("failed to decode pending response: %v", err)
	}
	if len(items) == 0 {
		t.Fatalf("expected writeback items in queue")
	}

	// Get first item and acknowledge it
	item := items[0]
	itemID, ok := item["id"].(string)
	if !ok || itemID == "" {
		t.Fatalf("expected item id in response")
	}

	ackResp := doRequest(t, server, request{
		method: http.MethodPost,
		path:   "/v1/workspaces/ws_1/writeback/" + itemID + "/ack",
		headers: map[string]string{
			"Authorization":    "Bearer " + token,
			"X-Correlation-Id": "corr_ack_1",
		},
		body: map[string]any{
			"success": true,
		},
	})

	if ackResp.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on ack, got %d (%s)", ackResp.Code, ackResp.Body.String())
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
	return mustTestJWTWithAudienceClaim(t, secret, workspaceID, agentName, scopes, aud, exp)
}

func mustTestJWTWithAudienceClaim(t *testing.T, secret, workspaceID, agentName string, scopes []string, aud any, exp time.Time) string {
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
