package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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

// Regression for Codex/CodeRabbit feedback on PR #84:
// `failedWritebacks` is a lifetime counter that only ever increments,
// so once any transient 429/5xx fires, the previous gate
// (`Failed > 0 || len(DeadLettered) > 0`) would keep `writeback status`
// exiting non-zero forever — even after retries succeed and the
// dead-letter queue is empty. The fix drives the exit code from
// `len(DeadLettered) > 0` only; this test pins that contract.
func TestWritebackStatusLifetimeCounterDoesNotFailExitCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	// Lifetime counter is non-zero (a transient failure happened in the
	// past) but no dead-letter files remain — retries succeeded.
	statePayload := []byte(`{"pendingWriteback":0,"failedWritebacks":7}` + "\n")
	if err := os.WriteFile(filepath.Join(localDir, ".relay", "state.json"), statePayload, 0o644); err != nil {
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

	// Deliberately do NOT save credentials — `writeback status` against
	// a known-local workspace must work offline.
	var human bytes.Buffer
	if err := run([]string{"writeback", "status", "demo"}, strings.NewReader(""), &human, &human); err != nil {
		t.Fatalf("expected exit 0 when only the lifetime counter is non-zero, got %v", err)
	}
	got := human.String()
	if !strings.Contains(got, "dead-lettered: 0") {
		t.Fatalf("expected dead-lettered: 0 in output, got: %q", got)
	}
	// Lifetime counter still surfaces in the report for observability —
	// it's just not a gating condition.
	if !strings.Contains(got, "failed: 7") {
		t.Fatalf("expected lifetime counter (failed: 7) to still surface in output, got: %q", got)
	}
}

// Spec P4.3: "Both subcommands handle a missing dead-letter dir / state.json
// gracefully — print 'no failures' and exit 0, do NOT panic." The
// no-failures test above exercises the missing dead-letter dir path
// (it creates state.json but no .relay/dead-letter). This test
// covers the other half: state.json itself is absent.
func TestWritebackStatusMissingStateJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	// Deliberately do NOT create state.json.
	if _, err := os.Stat(filepath.Join(localDir, ".relay", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("expected state.json to be absent, stat err=%v", err)
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
		t.Fatalf("expected no error when state.json is missing, got %v", err)
	}
	if got := strings.ToLower(human.String()); !strings.Contains(got, "no failures") {
		t.Fatalf("expected no-failures marker when state is absent, got: %q", human.String())
	}
}

// Spec P4.2: `relayfile writeback retry --opId OP` re-enqueues a
// dead-lettered op. Unknown opId must error loudly (and exit non-zero)
// without panicking on the missing file.
func TestWritebackRetryUnknownOpIDFailsCleanly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
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

	var stderr bytes.Buffer
	err := run(
		[]string{"writeback", "retry", "--opId", "op_does_not_exist", "demo"},
		strings.NewReader(""),
		io.Discard,
		&stderr,
	)
	if err == nil {
		t.Fatalf("expected error for unknown opId, got nil")
	}
	if !strings.Contains(err.Error(), "unknown dead-letter op") {
		t.Fatalf("expected 'unknown dead-letter op' in error, got %q", err.Error())
	}
}

// Spec P4.2: retry happy path — dead-letter file exists, the
// re-enqueue call goes through, the file is removed on success. The
// full HTTP-stubbed end-to-end path is invasive (requires faking the
// cloud writeback API plus a local file matching the dead-letter
// record's path) and overlaps heavily with TestWritebackDaemonDeadLetters
// from writeback_daemon_test.go which already exercises the
// failure-injection side of the same code path. This test stops at
// the contract surface: confirm that a malformed dead-letter file is
// rejected with a clear error rather than crashing the CLI.
func TestWritebackRetryRejectsMalformedRecord(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dlDir, "op_garbled.json"), []byte("not json at all"), 0o644); err != nil {
		t.Fatalf("write garbled record failed: %v", err)
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

	err := run(
		[]string{"writeback", "retry", "--opId", "op_garbled", "demo"},
		strings.NewReader(""),
		io.Discard,
		io.Discard,
	)
	if err == nil {
		t.Fatalf("expected error for malformed record, got nil")
	}
	if !strings.Contains(err.Error(), "invalid dead-letter record") {
		t.Fatalf("expected 'invalid dead-letter record' error, got %q", err.Error())
	}
	// The malformed file must NOT be removed — the user can inspect it.
	// Use a strict nil-check on stat: any error (not just IsNotExist) is
	// a regression worth surfacing. CodeRabbit flagged on PR #84 that
	// the original IsNotExist-only check would silently pass on other
	// stat errors.
	if _, statErr := os.Stat(filepath.Join(dlDir, "op_garbled.json")); statErr != nil {
		t.Fatalf("malformed dead-letter file unexpectedly inaccessible after retry failure: %v", statErr)
	}
}
