package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/digest"
	"github.com/agentworkforce/relayfile/internal/relayfile"
)

func providerWriteBody(ws string) map[string]any {
	return map[string]any{
		"workspaceId": ws,
		"files": []map[string]any{
			{"path": "/github/repos/octo/demo/pulls/7.md", "content": "# PR 7\nstate: merged\n"},
			{"path": "/discovery/github/catalog.json", "content": `{"events":["pull_request.closed"]}`, "contentType": "application/json"},
			{"path": "/LAYOUT.md", "content": "# Layout\n"},
		},
		"event": map[string]any{
			"path":              "/github/repos/octo/demo/pulls/7.md",
			"providerEventType": "pull_request.closed",
			"resourceRef":       "/github/repos/octo/demo/pulls/7.md",
		},
	}
}

// postProviderWrite signs body with the default internal secret and posts it
// to /v1/internal/provider-writes. headers overrides individual headers
// (an empty value removes the header).
func postProviderWrite(t *testing.T, server http.Handler, correlationID string, body []byte, overrides map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	ts := time.Now().UTC().Format(time.RFC3339)
	headers := map[string]string{
		"X-Correlation-Id":  correlationID,
		"X-Relay-Timestamp": ts,
		"X-Relay-Signature": mustHMAC("dev-internal-secret", ts+"\n"+string(body)),
		"Content-Type":      "application/json",
	}
	for k, v := range overrides {
		if v == "" {
			delete(headers, k)
			continue
		}
		headers[k] = v
	}
	return doRawRequest(t, server, rawRequest{
		method:  http.MethodPost,
		path:    "/v1/internal/provider-writes",
		headers: headers,
		body:    body,
	})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

func decodeErrorCode(t *testing.T, resp *httptest.ResponseRecorder) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error body %q: %v", resp.Body.String(), err)
	}
	code, _ := payload["code"].(string)
	return code
}

func waitForFSFile(t *testing.T, server http.Handler, ws, token, path, expected string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		resp := doRequest(t, server, request{
			method: http.MethodGet,
			path:   "/v1/workspaces/" + ws + "/fs/file?path=" + path,
			headers: map[string]string{
				"Authorization":    "Bearer " + token,
				"X-Correlation-Id": "corr_pw_read",
			},
		})
		last = resp.Body.String()
		if resp.Code == http.StatusOK {
			var file relayfile.File
			if err := json.NewDecoder(resp.Body).Decode(&file); err != nil {
				t.Fatalf("decode file: %v", err)
			}
			if file.Content == expected {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected %s to read %q via fs API, last response: %s", path, expected, last)
}

func TestInternalProviderWriteQueuedAndApplied(t *testing.T) {
	store := relayfile.NewStore()
	t.Cleanup(store.Close)
	server := NewServer(store)

	resp := postProviderWrite(t, server, "corr_pw_http_1", mustJSON(t, providerWriteBody("ws_pw_http")), nil)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (%s)", resp.Code, resp.Body.String())
	}
	var queued relayfile.QueuedResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &queued); err != nil {
		t.Fatalf("decode queued: %v", err)
	}
	if queued.Status != "queued" || !strings.HasPrefix(queued.ID, "pw_") || queued.CorrelationID != "corr_pw_http_1" {
		t.Fatalf("unexpected queued response: %+v", queued)
	}

	token := mustTestJWT(t, "dev-secret", "ws_pw_http", "Worker1", []string{"fs:read"}, time.Now().Add(time.Hour))
	waitForFSFile(t, server, "ws_pw_http", token, "/github/repos/octo/demo/pulls/7.md", "# PR 7\nstate: merged\n")
	waitForFSFile(t, server, "ws_pw_http", token, "/discovery/github/catalog.json", `{"events":["pull_request.closed"]}`)
	waitForFSFile(t, server, "ws_pw_http", token, "/LAYOUT.md", "# Layout\n")

	feed, err := store.GetEvents("ws_pw_http", "", "", 100)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	created := map[string]relayfile.Event{}
	for _, event := range feed.Events {
		if event.Type == "file.created" {
			created[event.Path] = event
		}
	}
	for _, path := range []string{"/github/repos/octo/demo/pulls/7.md", "/discovery/github/catalog.json", "/LAYOUT.md"} {
		event, ok := created[path]
		if !ok {
			t.Fatalf("expected file.created for %s, events: %+v", path, feed.Events)
		}
		if event.Origin != "provider_sync" || event.CorrelationID != "corr_pw_http_1" {
			t.Fatalf("unexpected event for %s: %+v", path, event)
		}
	}
}

func TestInternalProviderWriteAuth(t *testing.T) {
	server := NewServer(relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true}))
	body := mustJSON(t, providerWriteBody("ws_pw_auth"))

	t.Run("bad signature", func(t *testing.T) {
		resp := postProviderWrite(t, server, "corr_pw_auth_1", body, map[string]string{"X-Relay-Signature": "deadbeef"})
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%s)", resp.Code, resp.Body.String())
		}
	})
	t.Run("stale timestamp", func(t *testing.T) {
		ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
		resp := postProviderWrite(t, server, "corr_pw_auth_2", body, map[string]string{
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": mustHMAC("dev-internal-secret", ts+"\n"+string(body)),
		})
		if resp.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%s)", resp.Code, resp.Body.String())
		}
	})
	t.Run("missing correlation id", func(t *testing.T) {
		resp := postProviderWrite(t, server, "", body, map[string]string{"X-Correlation-Id": ""})
		if resp.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%s)", resp.Code, resp.Body.String())
		}
	})
	t.Run("replay", func(t *testing.T) {
		ts := time.Now().UTC().Format(time.RFC3339)
		sig := mustHMAC("dev-internal-secret", ts+"\n"+string(body))
		first := postProviderWrite(t, server, "corr_pw_auth_3", body, map[string]string{"X-Relay-Timestamp": ts, "X-Relay-Signature": sig})
		if first.Code != http.StatusAccepted {
			t.Fatalf("expected 202 for first send, got %d (%s)", first.Code, first.Body.String())
		}
		second := postProviderWrite(t, server, "corr_pw_auth_4", body, map[string]string{"X-Relay-Timestamp": ts, "X-Relay-Signature": sig})
		if second.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 for replayed send, got %d (%s)", second.Code, second.Body.String())
		}
		if !strings.Contains(second.Body.String(), "replay") {
			t.Fatalf("expected replay message, got %s", second.Body.String())
		}
	})
	t.Run("wrong method", func(t *testing.T) {
		resp := doRawRequest(t, server, rawRequest{method: http.MethodGet, path: "/v1/internal/provider-writes", headers: map[string]string{"X-Correlation-Id": "corr_pw_auth_5"}})
		if resp.Code == http.StatusOK || resp.Code == http.StatusAccepted {
			t.Fatalf("expected GET to be rejected, got %d", resp.Code)
		}
	})
}

func TestInternalProviderWriteInvalidInput(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	server := NewServer(store)

	cases := []struct {
		name   string
		mutate func(b map[string]any)
		raw    []byte
		want   string
	}{
		{name: "invalid json", raw: []byte("{not json"), want: "invalid JSON"},
		{name: "trailing data", raw: append(mustJSON(t, providerWriteBody("ws_pw_bad")), []byte(" {}")...), want: "trailing data"},
		{name: "unknown top-level field", mutate: func(b map[string]any) { b["correlationId"] = "x" }, want: "invalid JSON"},
		{name: "unknown file field", mutate: func(b map[string]any) {
			b["files"].([]map[string]any)[0]["ifMatch"] = "1"
		}, want: "invalid JSON"},
		{name: "mixed providers", mutate: func(b map[string]any) {
			b["files"].([]map[string]any)[1]["path"] = "/jira/ISSUE-1.md"
		}, want: "lie under /github/"},
		{name: "event path not in files", mutate: func(b map[string]any) {
			b["event"].(map[string]any)["path"] = "/github/repos/octo/demo/pulls/8.md"
			b["event"].(map[string]any)["resourceRef"] = "/github/repos/octo/demo/pulls/8.md"
		}, want: "exactly one file path"},
		{name: "duplicate event path", mutate: func(b map[string]any) {
			b["files"].([]map[string]any)[2]["path"] = "/github/repos/octo/demo/pulls/7.md"
		}, want: "exactly one file path"},
		{name: "bad providerEventType", mutate: func(b map[string]any) {
			b["event"].(map[string]any)["providerEventType"] = "Pull Request"
		}, want: "providerEventType"},
		{name: "op delete", mutate: func(b map[string]any) {
			b["files"].([]map[string]any)[1]["op"] = "delete"
		}, want: "deletes are not accepted"},
		{name: "resourceRef outside provider", mutate: func(b map[string]any) {
			b["event"].(map[string]any)["resourceRef"] = "/slack/C1/x.md"
		}, want: "resourceRef"},
		{name: "path traversal", mutate: func(b map[string]any) {
			b["files"].([]map[string]any)[1]["path"] = "/github/../secrets.md"
		}, want: "files[1]"},
		{name: "event path outside provider roots", mutate: func(b map[string]any) {
			b["event"].(map[string]any)["path"] = "/notion/page.md"
			b["event"].(map[string]any)["resourceRef"] = "/notion/page.md"
			b["files"].([]map[string]any)[0]["path"] = "/notion/page.md"
		}, want: "event.path must start with"},
		{name: "empty files", mutate: func(b map[string]any) { b["files"] = []map[string]any{} }, want: "files must not be empty"},
		{name: "missing workspace", mutate: func(b map[string]any) { delete(b, "workspaceId") }, want: "workspaceId"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.raw
			if raw == nil {
				body := providerWriteBody("ws_pw_bad")
				tc.mutate(body)
				raw = mustJSON(t, body)
			}
			resp := postProviderWrite(t, server, "corr_pw_bad_"+strings.ReplaceAll(tc.name, " ", "_")+"_"+string(rune('a'+i)), raw, nil)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%s)", resp.Code, resp.Body.String())
			}
			if code := decodeErrorCode(t, resp); code != "invalid_input" {
				t.Fatalf("expected invalid_input, got %q (%s)", code, resp.Body.String())
			}
			if !strings.Contains(resp.Body.String(), tc.want) {
				t.Fatalf("expected message containing %q, got %s", tc.want, resp.Body.String())
			}
		})
	}

	feed, err := store.GetEvents("ws_pw_bad", "", "", 10)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(feed.Events) != 0 {
		t.Fatalf("rejected writes must not emit events, got %+v", feed.Events)
	}
}

func TestInternalProviderWritePayloadTooLarge(t *testing.T) {
	server := mustNewServerWithConfig(t, relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true}), ServerConfig{
		InternalHMACSecret: "dev-internal-secret",
		MaxBodyBytes:       256,
	})
	body := providerWriteBody("ws_pw_large")
	body["files"].([]map[string]any)[0]["content"] = strings.Repeat("x", 1024)
	resp := postProviderWrite(t, server, "corr_pw_large", mustJSON(t, body), nil)
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d (%s)", resp.Code, resp.Body.String())
	}
	if code := decodeErrorCode(t, resp); code != "payload_too_large" {
		t.Fatalf("expected payload_too_large, got %q", code)
	}
}

func TestInternalProviderWriteQueueFull(t *testing.T) {
	store := relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true, EnvelopeQueueSize: 1})
	t.Cleanup(store.Close)
	server := NewServer(store)

	first := postProviderWrite(t, server, "corr_pw_full_1", mustJSON(t, providerWriteBody("ws_pw_full")), nil)
	if first.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for first write, got %d (%s)", first.Code, first.Body.String())
	}
	body := providerWriteBody("ws_pw_full")
	body["event"].(map[string]any)["providerEventType"] = "pull_request.reopened"
	second := postProviderWrite(t, server, "corr_pw_full_2", mustJSON(t, body), nil)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for second write, got %d (%s)", second.Code, second.Body.String())
	}
	if code := decodeErrorCode(t, second); code != "queue_full" {
		t.Fatalf("expected queue_full, got %q", code)
	}
	if second.Header().Get("Retry-After") != "1" {
		t.Fatalf("expected Retry-After: 1, got %q", second.Header().Get("Retry-After"))
	}
}

func TestInternalWebhookEnvelopeRejectsReservedKind(t *testing.T) {
	server := NewServer(relayfile.NewStoreWithOptions(relayfile.StoreOptions{DisableWorkers: true}))
	envelope := map[string]any{
		"envelopeId":    "env_kind_1",
		"kind":          "provider_write",
		"workspaceId":   "ws_kind",
		"provider":      "github",
		"deliveryId":    "delivery_kind_1",
		"receivedAt":    time.Now().UTC().Format(time.RFC3339),
		"payload":       map[string]any{"files": []any{}, "event": map[string]any{}},
		"correlationId": "corr_kind_1",
	}
	body := mustJSON(t, envelope)
	ts := time.Now().UTC().Format(time.RFC3339)
	resp := doRawRequest(t, server, rawRequest{
		method: http.MethodPost,
		path:   "/v1/internal/webhook-envelopes",
		headers: map[string]string{
			"X-Correlation-Id":  "corr_kind_1",
			"X-Relay-Timestamp": ts,
			"X-Relay-Signature": mustHMAC("dev-internal-secret", ts+"\n"+string(body)),
			"Content-Type":      "application/json",
		},
		body: body,
	})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for reserved kind, got %d (%s)", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), "kind is reserved") {
		t.Fatalf("unexpected body: %s", resp.Body.String())
	}
}

// TestInternalProviderWriteDigestArtifact covers the digest runtime contract
// for the provider-writes route: the applied files emit ordinary file events
// that feed digest regeneration and yield a today.md listing the canonical
// record, while /digests/* events are excluded so regeneration never recurses.
func TestInternalProviderWriteDigestArtifact(t *testing.T) {
	store := relayfile.NewStore()
	t.Cleanup(store.Close)
	server := NewServer(store)

	resp := postProviderWrite(t, server, "corr_pw_digest", mustJSON(t, providerWriteBody("ws_pw_digest")), nil)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d (%s)", resp.Code, resp.Body.String())
	}
	token := mustTestJWT(t, "dev-secret", "ws_pw_digest", "Worker1", []string{"fs:read"}, time.Now().Add(time.Hour))
	waitForFSFile(t, server, "ws_pw_digest", token, "/LAYOUT.md", "# Layout\n")
	waitForFSFile(t, server, "ws_pw_digest", token, "/github/repos/octo/demo/pulls/7.md", "# PR 7\nstate: merged\n")

	feed, err := store.GetEvents("ws_pw_digest", "", "", 1000)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(feed.Events) == 0 {
		t.Fatalf("expected provider write to emit file events")
	}
	for _, event := range feed.Events {
		if strings.HasPrefix(event.Path, "/digests/") {
			t.Fatalf("provider write ingest must not emit digest-path events, got %+v", event)
		}
	}

	// Simulate one digest artifact write event alongside the provider-write
	// events and verify the regeneration source excludes it (non-recursion).
	sourceEvents := append([]relayfile.Event(nil), feed.Events...)
	sourceEvents = append(sourceEvents, relayfile.Event{
		Type:      "file.updated",
		Path:      "/digests/today.md",
		Provider:  "digests",
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	})
	var changeEvents []digest.ChangeEvent
	for _, event := range sourceEvents {
		rel := strings.TrimPrefix(event.Path, "/")
		if digest.IsDigestPath(rel) {
			continue
		}
		ts, parseErr := time.Parse(time.RFC3339Nano, event.Timestamp)
		if parseErr != nil {
			t.Fatalf("parse event timestamp: %v", parseErr)
		}
		verb := "updated"
		if event.Type == "file.created" {
			verb = "created"
		}
		changeEvents = append(changeEvents, digest.ChangeEvent{
			Provider:      event.Provider,
			Timestamp:     ts,
			Identifier:    rel,
			Verb:          verb,
			CanonicalPath: rel,
		})
	}
	for _, event := range changeEvents {
		if strings.HasPrefix(event.CanonicalPath, "digests/") {
			t.Fatalf("digest source must exclude /digests/* events, got %+v", event)
		}
	}

	mountRoot := t.TempDir()
	now := time.Now().UTC()
	if _, err := digest.WriteToday(context.Background(), mountRoot, digest.SliceSource{Items: changeEvents}, []string{"github"}, now, time.UTC, digest.BuildOptions{}); err != nil {
		t.Fatalf("write today digest: %v", err)
	}
	todayBytes, err := os.ReadFile(filepath.Join(mountRoot, "digests", "today.md"))
	if err != nil {
		t.Fatalf("read today.md: %v", err)
	}
	today := string(todayBytes)
	if !strings.Contains(today, "github/repos/octo/demo/pulls/7.md") {
		t.Fatalf("expected today.md to cover the provider-write canonical path, got:\n%s", today)
	}
	if strings.Contains(today, "digests/today.md") {
		t.Fatalf("today.md must not list digest artifacts as activity, got:\n%s", today)
	}
}
