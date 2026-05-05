package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWritebackStatusReportsFailuresAndJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	dlDir := filepath.Join(localDir, ".relay", "dead-letter")
	if err := os.MkdirAll(dlDir, 0o755); err != nil {
		t.Fatalf("mkdir dead-letter failed: %v", err)
	}
	statePayload := []byte(`{"pendingWriteback":0,"failedWritebacks":2}` + "\n")
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "state.json"), statePayload, 0o644); err != nil {
		t.Fatalf("write state failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dlDir, "op_a.json"), []byte(`{"opId":"op_a","path":"/notion/a.md","lastStatus":400,"ts":"2026-05-05T14:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("write op_a failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dlDir, "op_b.json"), []byte(`{"opId":"op_b","path":"/notion/b.md","lastStatus":409,"ts":"2026-05-05T14:01:00Z"}`), 0o644); err != nil {
		t.Fatalf("write op_b failed: %v", err)
	}

	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if err := saveCredentials(credentials{Server: defaultServerURL, Token: testJWTWithWorkspace("ws_demo")}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var human bytes.Buffer
	err := run([]string{"writeback", "status", "demo"}, strings.NewReader(""), &human, &human)
	if !errors.Is(err, errWritebackFailuresPresent) {
		t.Fatalf("expected errWritebackFailuresPresent, got %v", err)
	}
	if got := human.String(); !strings.Contains(got, "failed: 2") {
		t.Fatalf("expected failed count in human output, got: %q", got)
	}

	var jsonOut bytes.Buffer
	err = run([]string{"writeback", "status", "demo", "--json"}, strings.NewReader(""), &jsonOut, &jsonOut)
	if !errors.Is(err, errWritebackFailuresPresent) {
		t.Fatalf("expected errWritebackFailuresPresent in --json mode, got %v", err)
	}
	var report struct {
		WorkspaceID  string `json:"workspaceId"`
		Pending      int    `json:"pending"`
		Failed       int    `json:"failed"`
		DeadLettered []struct {
			OpID string `json:"opId"`
		} `json:"deadLettered"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &report); err != nil {
		t.Fatalf("parse --json output failed: %v\npayload:\n%s", err, jsonOut.String())
	}
	if report.Failed != 2 {
		t.Fatalf("expected failed=2, got %d", report.Failed)
	}
	if len(report.DeadLettered) != 2 {
		t.Fatalf("expected 2 dead-lettered entries, got %d", len(report.DeadLettered))
	}
}

func TestWritebackStatusNoFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "state.json"), []byte(`{"pendingWriteback":0,"failedWritebacks":0}`+"\n"), 0o644); err != nil {
		t.Fatalf("write state failed: %v", err)
	}

	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
	if err := saveCredentials(credentials{Server: defaultServerURL, Token: testJWTWithWorkspace("ws_demo")}); err != nil {
		t.Fatalf("saveCredentials failed: %v", err)
	}

	var human bytes.Buffer
	if err := run([]string{"writeback", "status", "demo"}, strings.NewReader(""), &human, &human); err != nil {
		t.Fatalf("run writeback status failed: %v", err)
	}
	if got := strings.ToLower(human.String()); !strings.Contains(got, "no failures") {
		t.Fatalf("expected no-failures marker, got: %q", human.String())
	}

	var jsonOut bytes.Buffer
	if err := run([]string{"writeback", "status", "demo", "--json"}, strings.NewReader(""), &jsonOut, &jsonOut); err != nil {
		t.Fatalf("run writeback status --json failed: %v", err)
	}
	var report struct {
		Failed       int        `json:"failed"`
		DeadLettered []struct{} `json:"deadLettered"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &report); err != nil {
		t.Fatalf("parse --json output failed: %v\npayload:\n%s", err, jsonOut.String())
	}
	if report.Failed != 0 {
		t.Fatalf("expected failed=0, got %d", report.Failed)
	}
	if len(report.DeadLettered) != 0 {
		t.Fatalf("expected no dead-letter entries, got %d", len(report.DeadLettered))
	}
}
