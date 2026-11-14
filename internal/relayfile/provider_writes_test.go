package relayfile

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func providerWriteFixture(ws string) ProviderWriteRequest {
	return ProviderWriteRequest{
		WorkspaceID: ws,
		Files: []ProviderWriteFile{
			{Path: "/github/repos/octo/demo/pulls/7.md", Content: "# PR 7\nstate: merged\n"},
			{Path: "/discovery/github/catalog.json", Content: `{"events":["pull_request.closed"]}`, ContentType: "application/json"},
			{Path: "/LAYOUT.md", Content: "# Layout\n"},
		},
		Event: ProviderWriteEvent{
			Path:              "/github/repos/octo/demo/pulls/7.md",
			ProviderEventType: "pull_request.closed",
			ResourceRef:       "/github/repos/octo/demo/pulls/7.md",
		},
		CorrelationID: "corr_pw_1",
	}
}

func TestValidateProviderWrite(t *testing.T) {
	base := providerWriteFixture("ws_pw")
	provider, err := ValidateProviderWrite(base)
	if err != nil {
		t.Fatalf("expected valid request, got %v", err)
	}
	if provider != "github" {
		t.Fatalf("expected provider github, got %q", provider)
	}

	cases := []struct {
		name   string
		mutate func(r *ProviderWriteRequest)
		want   string
	}{
		{"missing workspace", func(r *ProviderWriteRequest) { r.WorkspaceID = " " }, "workspaceId"},
		{"event path outside provider roots", func(r *ProviderWriteRequest) {
			r.Event.Path = "/documents/x.md"
			r.Files[0].Path = "/documents/x.md"
		}, "event.path must start with"},
		{"event path traversal", func(r *ProviderWriteRequest) {
			r.Event.Path = "/github/../etc/passwd"
			r.Files[0].Path = "/github/../etc/passwd"
		}, "safe absolute path"},
		{"event path too long", func(r *ProviderWriteRequest) {
			p := "/github/" + strings.Repeat("a", providerWriteMaxPathLength)
			r.Event.Path = p
			r.Files[0].Path = p
		}, "safe absolute path"},
		{"bad providerEventType", func(r *ProviderWriteRequest) { r.Event.ProviderEventType = "Pull-Request" }, "providerEventType"},
		{"providerEventType too many segments", func(r *ProviderWriteRequest) { r.Event.ProviderEventType = "a.b.c" }, "providerEventType"},
		{"resourceRef outside provider", func(r *ProviderWriteRequest) { r.Event.ResourceRef = "/jira/ISSUE-1.md" }, "resourceRef"},
		{"no files", func(r *ProviderWriteRequest) { r.Files = nil }, "files must not be empty"},
		{"event path not among files", func(r *ProviderWriteRequest) { r.Files[0].Path = "/github/repos/octo/demo/pulls/8.md" }, "exactly one file path"},
		{"event path duplicated", func(r *ProviderWriteRequest) { r.Files[2] = r.Files[0] }, "exactly one file path"},
		{"mixed providers", func(r *ProviderWriteRequest) { r.Files[1].Path = "/jira/ISSUE-1.md" }, "lie under /github/"},
		{"foreign discovery root", func(r *ProviderWriteRequest) { r.Files[1].Path = "/discovery/slack/catalog.json" }, "lie under /github/"},
		{"relative file path", func(r *ProviderWriteRequest) { r.Files[1].Path = "github/x.md" }, "files[1]"},
		{"op delete", func(r *ProviderWriteRequest) { r.Files[1].Op = "delete" }, "deletes are not accepted"},
		{"unknown op", func(r *ProviderWriteRequest) { r.Files[1].Op = "move" }, "unsupported op"},
		{"unknown encoding", func(r *ProviderWriteRequest) { r.Files[0].Encoding = "hex" }, "unsupported encoding"},
		{"invalid base64", func(r *ProviderWriteRequest) {
			r.Files[0].Encoding = "base64"
			r.Files[0].Content = "not*base64"
		}, "not valid base64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := providerWriteFixture("ws_pw")
			tc.mutate(&req)
			_, err := ValidateProviderWrite(req)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %q", tc.want, err.Error())
			}
		})
	}

	t.Run("op upsert and utf-8 encoding accepted", func(t *testing.T) {
		req := providerWriteFixture("ws_pw")
		req.Files[0].Op = "upsert"
		req.Files[0].Encoding = "utf-8"
		if _, err := ValidateProviderWrite(req); err != nil {
			t.Fatalf("expected valid, got %v", err)
		}
	})
}

func TestIngestProviderWriteAppliesFilesAndEmitsEvents(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)

	queued, err := store.IngestProviderWrite(providerWriteFixture("ws_pw_apply"))
	if err != nil {
		t.Fatalf("ingest provider write: %v", err)
	}
	if queued.Status != "queued" || !strings.HasPrefix(queued.ID, "pw_") || queued.CorrelationID != "corr_pw_1" {
		t.Fatalf("unexpected queued response: %+v", queued)
	}

	waitForFileContent(t, store, "ws_pw_apply", "/github/repos/octo/demo/pulls/7.md", "# PR 7\nstate: merged\n")
	waitForFileContent(t, store, "ws_pw_apply", "/discovery/github/catalog.json", `{"events":["pull_request.closed"]}`)
	waitForFileContent(t, store, "ws_pw_apply", "/LAYOUT.md", "# Layout\n")

	canonical, err := store.ReadFile("ws_pw_apply", "/github/repos/octo/demo/pulls/7.md")
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if canonical.Provider != "github" || canonical.ContentType != "text/markdown" || canonical.Encoding != "" {
		t.Fatalf("unexpected canonical file metadata: %+v", canonical)
	}
	discovery, err := store.ReadFile("ws_pw_apply", "/discovery/github/catalog.json")
	if err != nil {
		t.Fatalf("read discovery: %v", err)
	}
	if discovery.Provider != "github" || discovery.ContentType != "application/json" {
		t.Fatalf("unexpected discovery file metadata: %+v", discovery)
	}
	layout, err := store.ReadFile("ws_pw_apply", "/LAYOUT.md")
	if err != nil {
		t.Fatalf("read layout: %v", err)
	}
	if layout.Provider != "" {
		t.Fatalf("expected /LAYOUT.md without provider attribution, got %q", layout.Provider)
	}

	feed, err := store.GetEvents("ws_pw_apply", "", "", 100)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	byPath := map[string]Event{}
	for _, event := range feed.Events {
		if strings.HasPrefix(event.Path, "/digests/") {
			t.Fatalf("provider write must not emit digest-path events, got %+v", event)
		}
		if event.Type == "file.created" {
			byPath[event.Path] = event
		}
	}
	for _, path := range []string{"/github/repos/octo/demo/pulls/7.md", "/discovery/github/catalog.json", "/LAYOUT.md"} {
		event, ok := byPath[path]
		if !ok {
			t.Fatalf("expected file.created event for %s, got %+v", path, feed.Events)
		}
		if event.Origin != "provider_sync" || event.CorrelationID != "corr_pw_1" {
			t.Fatalf("unexpected event metadata for %s: %+v", path, event)
		}
		wantProvider := "github"
		if path == "/LAYOUT.md" {
			wantProvider = ""
		}
		if event.Provider != wantProvider {
			t.Fatalf("expected provider %q on event for %s, got %q", wantProvider, path, event.Provider)
		}
	}

	// A later write of the same canonical path (terminal state as data) is an
	// update, never a delete.
	second := providerWriteFixture("ws_pw_apply")
	second.Files = []ProviderWriteFile{{Path: "/github/repos/octo/demo/pulls/7.md", Content: "# PR 7\nstate: closed\n"}}
	second.Event.ProviderEventType = "pull_request.closed"
	second.CorrelationID = "corr_pw_2"
	if _, err := store.IngestProviderWrite(second); err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	waitForFileContent(t, store, "ws_pw_apply", "/github/repos/octo/demo/pulls/7.md", "# PR 7\nstate: closed\n")
	feed, err = store.GetEvents("ws_pw_apply", "", "", 100)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	sawUpdate := false
	for _, event := range feed.Events {
		if event.Type == "file.deleted" {
			t.Fatalf("terminal provider state must not be a delete: %+v", event)
		}
		if event.Type == "file.updated" && event.Path == "/github/repos/octo/demo/pulls/7.md" && event.CorrelationID == "corr_pw_2" {
			sawUpdate = true
		}
	}
	if !sawUpdate {
		t.Fatalf("expected file.updated for the second write, got %+v", feed.Events)
	}
	// /LAYOUT.md and the discovery file from the first write are untouched.
	waitForFileContent(t, store, "ws_pw_apply", "/LAYOUT.md", "# Layout\n")
}

func TestIngestProviderWriteRejectsInvalidInput(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	req := providerWriteFixture("ws_pw_invalid")
	req.Files[0].Op = "delete"
	if _, err := store.IngestProviderWrite(req); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	feed, err := store.GetEvents("ws_pw_invalid", "", "", 10)
	if err != nil {
		t.Fatalf("get events: %v", err)
	}
	if len(feed.Events) != 0 {
		t.Fatalf("invalid write must not emit events, got %+v", feed.Events)
	}
}

func TestIngestProviderWriteQueueFull(t *testing.T) {
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true, EnvelopeQueueSize: 1})
	t.Cleanup(store.Close)
	if _, err := store.IngestProviderWrite(providerWriteFixture("ws_pw_full")); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if _, err := store.IngestProviderWrite(providerWriteFixture("ws_pw_full")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}
}

func TestIngestProviderWriteBase64Content(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)
	raw := []byte{0x89, 'P', 'N', 'G', 0, 1, 2}
	encoded := base64.StdEncoding.EncodeToString(raw)
	req := ProviderWriteRequest{
		WorkspaceID: "ws_pw_b64",
		Files: []ProviderWriteFile{{
			Path:        "/slack/C123/files/logo.png",
			Content:     encoded,
			ContentType: "image/png",
			Encoding:    "base64",
		}},
		Event:         ProviderWriteEvent{Path: "/slack/C123/files/logo.png", ProviderEventType: "file_shared"},
		CorrelationID: "corr_pw_b64",
	}
	if _, err := store.IngestProviderWrite(req); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	waitForFileContent(t, store, "ws_pw_b64", "/slack/C123/files/logo.png", encoded)
	file, err := store.ReadFile("ws_pw_b64", "/slack/C123/files/logo.png")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if file.Encoding != "base64" || file.ContentType != "image/png" || file.Provider != "slack" {
		t.Fatalf("unexpected file metadata: %+v", file)
	}
	if file.ContentHash != contentHashForEncodedContent(encoded, "base64") {
		t.Fatalf("content hash must cover the decoded bytes, got %q", file.ContentHash)
	}
}

func TestIngestProviderWriteSlackPathsBypassAliasCanonicalization(t *testing.T) {
	// Slack webhook envelopes have their paths rewritten by the provider
	// adapter; provider writes carry authoritative paths, so /LAYOUT.md and the
	// Slack discovery contract must land exactly where the caller put them.
	store := NewStore()
	t.Cleanup(store.Close)
	req := ProviderWriteRequest{
		WorkspaceID: "ws_pw_slack",
		Files: []ProviderWriteFile{
			{Path: "/slack/C123/2026-11-08.md", Content: "# channel log\n"},
			{Path: "/discovery/slack/catalog.json", Content: "{}", ContentType: "application/json"},
			{Path: "/LAYOUT.md", Content: "# Layout\n"},
		},
		Event:         ProviderWriteEvent{Path: "/slack/C123/2026-11-08.md", ProviderEventType: "message"},
		CorrelationID: "corr_pw_slack",
	}
	if _, err := store.IngestProviderWrite(req); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	waitForFileContent(t, store, "ws_pw_slack", "/slack/C123/2026-11-08.md", "# channel log\n")
	waitForFileContent(t, store, "ws_pw_slack", "/discovery/slack/catalog.json", "{}")
	waitForFileContent(t, store, "ws_pw_slack", "/LAYOUT.md", "# Layout\n")
}

func TestReplayProviderWriteEnvelope(t *testing.T) {
	store := NewStore()
	t.Cleanup(store.Close)
	req := providerWriteFixture("ws_pw_replay")
	queued, err := store.IngestProviderWrite(req)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	waitForFileContent(t, store, "ws_pw_replay", "/github/repos/octo/demo/pulls/7.md", "# PR 7\nstate: merged\n")

	// Overwrite through the regular filesystem path, then replay the
	// persisted provider_write envelope and expect its content restored.
	current, err := store.ReadFile("ws_pw_replay", "/github/repos/octo/demo/pulls/7.md")
	if err != nil {
		t.Fatalf("read before manual edit: %v", err)
	}
	if _, err := store.WriteFile(WriteRequest{
		WorkspaceID:   "ws_pw_replay",
		Path:          "/github/repos/octo/demo/pulls/7.md",
		IfMatch:       current.Revision,
		ContentType:   "text/markdown",
		Content:       "manual edit",
		CorrelationID: "corr_pw_manual",
	}); err != nil {
		t.Fatalf("manual write: %v", err)
	}
	waitForFileContent(t, store, "ws_pw_replay", "/github/repos/octo/demo/pulls/7.md", "manual edit")
	time.Sleep(5 * time.Millisecond)
	replayed, err := store.ReplayEnvelope(queued.ID, "corr_pw_replay")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replayed.Status != "queued" {
		t.Fatalf("unexpected replay response: %+v", replayed)
	}
	waitForFileContent(t, store, "ws_pw_replay", "/github/repos/octo/demo/pulls/7.md", "# PR 7\nstate: merged\n")
}

func TestParseProviderWriteEnvelopeRejectsTamperedPayload(t *testing.T) {
	req := providerWriteFixture("ws_pw_tamper")
	store := NewStoreWithOptions(StoreOptions{DisableWorkers: true})
	t.Cleanup(store.Close)
	payload, err := providerWritePayload(req)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	envelope := WebhookEnvelopeRequest{
		EnvelopeID:  "pw_tamper",
		Kind:        EnvelopeKindProviderWrite,
		WorkspaceID: "ws_pw_tamper",
		Provider:    "github",
		DeliveryID:  "pw_tamper",
		ReceivedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Payload:     payload,
	}
	actions, err := ParseProviderWriteEnvelope(envelope)
	if err != nil {
		t.Fatalf("parse valid envelope: %v", err)
	}
	if len(actions) != 3 || actions[0].Type != ActionFileUpsert || actions[0].Path != "/github/repos/octo/demo/pulls/7.md" {
		t.Fatalf("unexpected actions: %+v", actions)
	}

	// A file outside the provider root smuggled into a stored payload is
	// rejected at parse time rather than applied.
	files := payload["files"].([]any)
	files[1].(map[string]any)["path"] = "/jira/ISSUE-1.md"
	if _, err := ParseProviderWriteEnvelope(envelope); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for tampered payload, got %v", err)
	}
	files[1].(map[string]any)["path"] = "/discovery/github/catalog.json"
	envelope.Provider = "jira"
	if _, err := ParseProviderWriteEnvelope(envelope); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput for provider mismatch, got %v", err)
	}
}
