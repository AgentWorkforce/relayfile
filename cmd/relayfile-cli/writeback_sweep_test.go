package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func setupSweepWorkspace(t *testing.T, serverURL string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveCredentials(credentials{Server: serverURL, Token: "rf_token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:      "cloud-prod",
		ID:        "rw_7ccfea89",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		AgentName: "relayfile-cli",
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
}

func TestWritebackSweepDraftsDryRunByDefault(t *testing.T) {
	var seenBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/workspaces/rw_7ccfea89/writeback/sweep-drafts" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer rf_token" {
			t.Fatalf("unexpected Authorization: %q", got)
		}
		if got := r.Header.Get("X-Correlation-Id"); got == "" {
			t.Fatalf("expected correlation id header")
		}
		if err := json.NewDecoder(r.Body).Decode(&seenBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"dryRun": true,
			"scanned": 83,
			"removed": [
				{"path": "/slack/channels/C0ALQ06AAUT/messages/messages 0e89a031-65f0-480e-a823-ab1d94b324ea.json", "reason": "space-uuid-draft"},
				{"path": "/slack/channels/C0AD7UU0J1G__proj-cloud/messages/wb-claude1-ack2.json", "reason": "pattern"}
			],
			"skipped": [
				{"path": "/slack/channels/C0ALQ06AAUT/messages/messages 15250fcf-de54-44b0-a808-c8e514480647.json", "reason": "pending-writeback"}
			]
		}`))
	}))
	defer server.Close()
	setupSweepWorkspace(t, server.URL)

	var stdout bytes.Buffer
	err := run(
		[]string{"writeback", "sweep-drafts", "cloud-prod", "--pattern", "wb-*.json"},
		strings.NewReader(""), &stdout, &stdout,
	)
	if err != nil {
		t.Fatalf("run writeback sweep-drafts failed: %v", err)
	}

	if seenBody == nil {
		t.Fatalf("expected sweep request to reach the server")
	}
	if apply, _ := seenBody["apply"].(bool); apply {
		t.Fatalf("sweep must be a dry run unless --apply is passed, body=%v", seenBody)
	}
	patterns, _ := seenBody["patterns"].([]any)
	if len(patterns) != 1 || patterns[0] != "wb-*.json" {
		t.Fatalf("unexpected patterns in body: %v", seenBody)
	}

	output := stdout.String()
	for _, want := range []string{
		"Sweep (dry-run): scanned 83, 2 removable, 1 skipped",
		"would remove",
		"wb-claude1-ack2.json",
		"pending-writeback",
		"Re-run with --apply to remove.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, output)
		}
	}
}

func TestWritebackSweepDraftsApplyAndJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if apply, _ := body["apply"].(bool); !apply {
			t.Fatalf("expected apply=true, body=%v", body)
		}
		if prefix, _ := body["pathPrefix"].(string); prefix != "/slack/channels/C0ALQ06AAUT" {
			t.Fatalf("unexpected pathPrefix: %v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dryRun": false, "scanned": 80, "removed": [{"path": "/slack/channels/C0ALQ06AAUT/messages/messages 0e89a031-65f0-480e-a823-ab1d94b324ea.json", "reason": "space-uuid-draft"}], "skipped": []}`))
	}))
	defer server.Close()
	setupSweepWorkspace(t, server.URL)

	var stdout bytes.Buffer
	err := run(
		[]string{"writeback", "sweep-drafts", "cloud-prod", "--path-prefix", "/slack/channels/C0ALQ06AAUT", "--apply", "--json"},
		strings.NewReader(""), &stdout, &stdout,
	)
	if err != nil {
		t.Fatalf("run writeback sweep-drafts --apply failed: %v", err)
	}

	var result sweepDraftsResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("expected JSON output, got %q: %v", stdout.String(), err)
	}
	if result.DryRun || result.Scanned != 80 || len(result.Removed) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestWritebackSweepDraftsUsesTokenWorkspaceWithoutPositionalArg(t *testing.T) {
	overrideToken := testJWTWithWorkspace("rw_token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/rw_token/writeback/sweep-drafts" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+overrideToken {
			t.Fatalf("unexpected Authorization: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dryRun": true, "scanned": 0, "removed": [], "skipped": []}`))
	}))
	defer server.Close()

	setupSweepWorkspace(t, server.URL)
	var stdout bytes.Buffer
	err := run(
		[]string{"writeback", "sweep-drafts", "--token", overrideToken},
		strings.NewReader(""), &stdout, &stdout,
	)
	if err != nil {
		t.Fatalf("run writeback sweep-drafts with token override failed: %v", err)
	}
}

func TestWritebackSweepDraftsRejectsExtraArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	var stdout bytes.Buffer
	err := run([]string{"writeback", "sweep-drafts", "ws_a", "ws_b"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil || !strings.Contains(err.Error(), "usage: relayfile writeback sweep-drafts") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestWritebackSweepDraftsResolvesRelayWorkspaceIDFromCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/workspaces/ws_local/writeback/sweep-drafts" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dryRun": true, "scanned": 0, "removed": [], "skipped": []}`))
	}))
	defer server.Close()

	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	if err := saveCredentials(credentials{Server: server.URL, Token: "rf_token"}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:             "cloud-prod",
		ID:               "ws_local",
		RelayWorkspaceID: "rw_legacy",
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		AgentName:        "relayfile-cli",
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}

	var stdout bytes.Buffer
	if err := run(
		[]string{"writeback", "sweep-drafts", "rw_legacy"},
		strings.NewReader(""), &stdout, &stdout,
	); err != nil {
		t.Fatalf("run writeback sweep-drafts failed: %v", err)
	}
}

func TestWritebackMissingSubcommandMentionsSweepDrafts(t *testing.T) {
	var stdout bytes.Buffer
	err := run([]string{"writeback"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil || !strings.Contains(err.Error(), "sweep-drafts") {
		t.Fatalf("expected missing subcommand error to mention sweep-drafts, got %v", err)
	}
}
