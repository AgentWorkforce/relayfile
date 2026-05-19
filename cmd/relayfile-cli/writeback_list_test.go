package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writebackListSDKItem mirrors WritebackItem from
// packages/sdk/typescript/src/types.ts. Field names MUST stay in sync with the
// SDK; this struct is the load-bearing assertion that the CLI emits an
// SDK-compatible JSON shape.
type writebackListSDKItem struct {
	ID               string         `json:"id"`
	WorkspaceID      string         `json:"workspaceId"`
	Path             string         `json:"path"`
	Revision         string         `json:"revision"`
	CorrelationID    string         `json:"correlationId"`
	State            string         `json:"state,omitempty"`
	Provider         string         `json:"provider,omitempty"`
	Action           string         `json:"action,omitempty"`
	TS               string         `json:"ts,omitempty"`
	Code             string         `json:"code,omitempty"`
	Message          string         `json:"message,omitempty"`
	ProviderStatus   int            `json:"providerStatus,omitempty"`
	ProviderResponse map[string]any `json:"providerResponse,omitempty"`
	Attempts         int            `json:"attempts,omitempty"`
	FirstAttemptAt   string         `json:"firstAttemptAt,omitempty"`
	EnqueuedAt       string         `json:"enqueuedAt,omitempty"`
	LastAttemptAt    string         `json:"lastAttemptAt,omitempty"`
	Error            map[string]any `json:"error,omitempty"`
}

func TestWritebackListRequiresState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var stderr bytes.Buffer
	err := run([]string{"writeback", "list"}, strings.NewReader(""), &stderr, &stderr)
	if err == nil {
		t.Fatalf("expected missing state error, got nil")
	}
	if !strings.Contains(err.Error(), "usage: relayfile writeback list --state") {
		t.Fatalf("expected usage in error, got %q", err.Error())
	}
}

func TestWritebackListUnknownStateErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	upsertWritebackListWorkspace(t, localDir)

	var stderr bytes.Buffer
	err := run([]string{"writeback", "list", "--state", "bogus", "--workspace", "demo"}, strings.NewReader(""), &stderr, &stderr)
	if err == nil {
		t.Fatalf("expected unknown state error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown state") {
		t.Fatalf("expected unknown state in error, got %q", err.Error())
	}
}

func TestWritebackListPendingFromDirtyMountState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	mountState := []byte(`{"files":{
  "/linear/issues/LIN-1.json":{"revision":"rev_1","dirty":true},
  "/notion/pages/Page.json":{"revision":"rev_2","dirty":false}
}}`)
	if err := os.WriteFile(filepath.Join(localDir, ".relayfile-mount-state.json"), mountState, 0o644); err != nil {
		t.Fatalf("write mount state failed: %v", err)
	}
	upsertWritebackListWorkspace(t, localDir)

	var out bytes.Buffer
	if err := run([]string{"writeback", "list", "--state", "pending", "--workspace", "demo", "--json"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("run writeback list pending failed: %v", err)
	}
	var items []writebackListSDKItem
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		t.Fatalf("parse pending JSON failed: %v\npayload:\n%s", err, out.String())
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 pending row, got %d: %+v", len(items), items)
	}
	if items[0].State != "pending" || items[0].Path != "/linear/issues/LIN-1.json" || items[0].Provider != "linear" || items[0].Revision != "rev_1" {
		t.Fatalf("unexpected pending row: %+v", items[0])
	}
}

func TestWritebackListPendingIncludesHashDriftMissingAndUntrackedFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	unchangedPath := filepath.Join(localDir, "linear", "issues", "LIN-1.json")
	changedPath := filepath.Join(localDir, "linear", "issues", "LIN-2.json")
	untrackedPath := filepath.Join(localDir, "github", "issues", "draft.json")
	for _, path := range []string{unchangedPath, changedPath, untrackedPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s failed: %v", filepath.Dir(path), err)
		}
	}
	if err := os.WriteFile(unchangedPath, []byte("same"), 0o644); err != nil {
		t.Fatalf("write unchanged failed: %v", err)
	}
	if err := os.WriteFile(changedPath, []byte("new local body"), 0o644); err != nil {
		t.Fatalf("write changed failed: %v", err)
	}
	if err := os.WriteFile(untrackedPath, []byte("new file"), 0o644); err != nil {
		t.Fatalf("write untracked failed: %v", err)
	}
	unchangedHash, err := hashLocalWritebackFile(unchangedPath)
	if err != nil {
		t.Fatalf("hash unchanged failed: %v", err)
	}
	mountState := []byte(`{"files":{
  "/linear/issues/LIN-1.json":{"revision":"rev_1","hash":"` + unchangedHash + `"},
  "/linear/issues/LIN-2.json":{"revision":"rev_2","hash":"old_hash"},
  "/linear/issues/LIN-3.json":{"revision":"rev_3","hash":"deleted_hash"},
  "/linear/issues/LIN-4.json":{"revision":"rev_4","hash":"denied_hash","writeDenied":true}
}}`)
	if err := os.WriteFile(filepath.Join(localDir, ".relayfile-mount-state.json"), mountState, 0o644); err != nil {
		t.Fatalf("write mount state failed: %v", err)
	}
	upsertWritebackListWorkspace(t, localDir)

	var out bytes.Buffer
	if err := run([]string{"writeback", "list", "--state", "pending", "--workspace", "demo", "--json"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("run writeback list pending failed: %v", err)
	}
	var items []writebackListSDKItem
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		t.Fatalf("parse pending JSON failed: %v\npayload:\n%s", err, out.String())
	}
	paths := make([]string, 0, len(items))
	for _, item := range items {
		paths = append(paths, item.Path)
	}
	want := []string{"/github/issues/draft.json", "/linear/issues/LIN-2.json", "/linear/issues/LIN-3.json"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("pending paths = %v, want %v", paths, want)
	}
}

func TestWritebackListPendingUsesRemoteRootForNonRootMount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	changedPath := filepath.Join(localDir, "pages", "page-1.json")
	untrackedPath := filepath.Join(localDir, "pages", "draft.json")
	for _, path := range []string{changedPath, untrackedPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s failed: %v", filepath.Dir(path), err)
		}
	}
	if err := os.WriteFile(changedPath, []byte("new local body"), 0o644); err != nil {
		t.Fatalf("write changed failed: %v", err)
	}
	if err := os.WriteFile(untrackedPath, []byte("new draft"), 0o644); err != nil {
		t.Fatalf("write untracked failed: %v", err)
	}
	mountState := []byte(`{"files":{
  "/notion/pages/page-1.json":{"revision":"rev_1","hash":"old_hash"},
  "/notion/pages/page-2.json":{"revision":"rev_2","hash":"deleted_hash"}
}}`)
	if err := os.WriteFile(filepath.Join(localDir, ".relayfile-mount-state.json"), mountState, 0o644); err != nil {
		t.Fatalf("write mount state failed: %v", err)
	}
	writeWritebackListState(t, localDir, syncStateFile{WorkspaceID: "ws_demo", RemoteRoot: "/notion"})
	upsertWritebackListWorkspace(t, localDir)

	var out bytes.Buffer
	if err := run([]string{"writeback", "list", "--state", "pending", "--workspace", "demo", "--json"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("run writeback list pending failed: %v", err)
	}
	var items []writebackListSDKItem
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		t.Fatalf("parse pending JSON failed: %v\npayload:\n%s", err, out.String())
	}
	paths := make([]string, 0, len(items))
	for _, item := range items {
		paths = append(paths, item.Path)
		if item.Provider != "notion" {
			t.Fatalf("expected notion provider for %+v", item)
		}
	}
	want := []string{"/notion/pages/draft.json", "/notion/pages/page-1.json", "/notion/pages/page-2.json"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("pending paths = %v, want %v", paths, want)
	}
}

func TestWritebackListPendingSkipsReadonlyTrackedFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	readonlyPath := filepath.Join(localDir, "linear", "issues", "LIN-1.json")
	if err := os.MkdirAll(filepath.Dir(readonlyPath), 0o755); err != nil {
		t.Fatalf("mkdir readonly dir failed: %v", err)
	}
	if err := os.WriteFile(readonlyPath, []byte("changed but readonly"), 0o644); err != nil {
		t.Fatalf("write readonly failed: %v", err)
	}
	mountState := []byte(`{"files":{
  "/linear/issues/LIN-1.json":{"revision":"rev_1","hash":"old_hash","readonly":true},
  "/linear/issues/LIN-2.json":{"revision":"rev_2","hash":"deleted_hash","readonly":true}
}}`)
	if err := os.WriteFile(filepath.Join(localDir, ".relayfile-mount-state.json"), mountState, 0o644); err != nil {
		t.Fatalf("write mount state failed: %v", err)
	}
	upsertWritebackListWorkspace(t, localDir)

	var out bytes.Buffer
	if err := run([]string{"writeback", "list", "--state", "pending", "--workspace", "demo", "--json"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("run writeback list pending failed: %v", err)
	}
	var items []writebackListSDKItem
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		t.Fatalf("parse pending JSON failed: %v\npayload:\n%s", err, out.String())
	}
	if len(items) != 0 {
		t.Fatalf("expected no readonly pending rows, got %+v", items)
	}
}

func TestWritebackListDoesNotFabricatePendingFromAggregateCounter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	state := syncStateFile{
		WorkspaceID:      "ws_demo",
		PendingWriteback: 3,
		Providers: []syncStateProvider{
			{Provider: "linear", LastEventAt: "2026-05-12T10:00:00Z"},
		},
	}
	writeWritebackListState(t, localDir, state)
	upsertWritebackListWorkspace(t, localDir)

	var out bytes.Buffer
	if err := run([]string{"writeback", "list", "--state", "pending", "--workspace", "demo", "--json"}, strings.NewReader(""), &out, &out); err != nil {
		t.Fatalf("run writeback list pending failed: %v", err)
	}
	var items []writebackListSDKItem
	if err := json.Unmarshal(out.Bytes(), &items); err != nil {
		t.Fatalf("parse pending JSON failed: %v\npayload:\n%s", err, out.String())
	}
	if len(items) != 0 {
		t.Fatalf("expected no fabricated pending rows, got %+v", items)
	}
}

func TestWritebackListDeadEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	upsertWritebackListWorkspace(t, localDir)

	var human bytes.Buffer
	if err := run([]string{"writeback", "list", "--state", "dead", "--workspace", "demo"}, strings.NewReader(""), &human, &human); err != nil {
		t.Fatalf("run writeback list dead failed: %v", err)
	}
	got := strings.TrimSpace(human.String())
	if got != "op_id\tpath\tstate\tts\tprovider" {
		t.Fatalf("expected header-only output, got %q", got)
	}
}

func TestWritebackListDeadWithRecords(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dlDir, "op_a.json"), []byte(`{"opId":"op_a","path":"/notion/a.md","lastStatus":400,"lastAttemptedAt":"2026-05-12T10:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("write op_a failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dlDir, "op_b.json"), []byte(`{"opId":"op_b","path":"github/b.md","lastStatus":409}`), 0o644); err != nil {
		t.Fatalf("write op_b failed: %v", err)
	}
	upsertWritebackListWorkspace(t, localDir)

	var human bytes.Buffer
	if err := run([]string{"writeback", "list", "--state", "dead", "--workspace", "demo"}, strings.NewReader(""), &human, &human); err != nil {
		t.Fatalf("run writeback list dead failed: %v", err)
	}
	got := human.String()
	for _, fragment := range []string{"op_id\tpath\tstate\tts\tprovider", "op_a", "op_b", "/notion/a.md", "/github/b.md", "dead", "notion", "github"} {
		if !strings.Contains(got, fragment) {
			t.Fatalf("expected %q in output, got %q", fragment, got)
		}
	}
}

func TestWritebackListDeadJSONShape(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dlDir, "op_a.json"), []byte(`{"opId":"op_a","path":"/notion/a.md","lastStatus":400,"lastAttemptedAt":"2026-05-12T10:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("write op_a failed: %v", err)
	}
	upsertWritebackListWorkspace(t, localDir)

	var jsonOut bytes.Buffer
	if err := run([]string{"writeback", "list", "--state", "dead", "--workspace", "demo", "--json"}, strings.NewReader(""), &jsonOut, &jsonOut); err != nil {
		t.Fatalf("run writeback list dead --json failed: %v", err)
	}

	var sdkItems []writebackListSDKItem
	decoder := json.NewDecoder(strings.NewReader(jsonOut.String()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&sdkItems); err != nil {
		t.Fatalf("SDK-shape decode failed: %v\npayload:\n%s", err, jsonOut.String())
	}
	if len(sdkItems) != 1 {
		t.Fatalf("expected 1 dead row, got %d: %+v", len(sdkItems), sdkItems)
	}
	if sdkItems[0].ID != "op_a" || sdkItems[0].WorkspaceID != "ws_demo" || sdkItems[0].Path != "/notion/a.md" {
		t.Fatalf("unexpected SDK row: %+v", sdkItems[0])
	}
	if sdkItems[0].Revision != "2026-05-12T10:00:00Z" {
		t.Fatalf("expected revision from lastAttemptedAt, got %q", sdkItems[0].Revision)
	}
	if sdkItems[0].CorrelationID != "op_a" {
		t.Fatalf("expected correlationId=op_a, got %q", sdkItems[0].CorrelationID)
	}
}

func TestWritebackListDeadJSONHasRequiredSDKKeys(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dlDir, "op_a.json"), []byte(`{"opId":"op_a","path":"/notion/a.md","lastStatus":400,"lastAttemptedAt":"2026-05-12T10:00:00Z"}`), 0o644); err != nil {
		t.Fatalf("write op_a failed: %v", err)
	}
	upsertWritebackListWorkspace(t, localDir)

	var jsonOut bytes.Buffer
	if err := run([]string{"writeback", "list", "--state", "dead", "--workspace", "demo", "--json"}, strings.NewReader(""), &jsonOut, &jsonOut); err != nil {
		t.Fatalf("run writeback list dead --json failed: %v", err)
	}
	var items []map[string]any
	if err := json.Unmarshal(jsonOut.Bytes(), &items); err != nil {
		t.Fatalf("parse --json output failed: %v\npayload:\n%s", err, jsonOut.String())
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 row, got %d", len(items))
	}
	for _, key := range []string{"id", "workspaceId", "path", "revision", "correlationId"} {
		if _, ok := items[0][key]; !ok {
			t.Fatalf("missing required SDK key %q in row %+v", key, items[0])
		}
	}
}

func TestWritebackListDeadJSONMergesErrorSidecar(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(dlDir, "op_a.json"), []byte(`{"opId":"op_a","path":"/notion/a.md","lastStatus":400}`), 0o644); err != nil {
		t.Fatalf("write op_a failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dlDir, "op_a.error.json"), []byte(`{"code":"schema_violation","message":"body must include event","providerStatus":422,"providerResponse":{"code":"bad_writeback"},"attempts":4,"firstAttemptAt":"2026-05-12T09:00:00Z","lastAttemptAt":"2026-05-12T10:00:00Z","opId":"op_a"}`), 0o644); err != nil {
		t.Fatalf("write op_a sidecar failed: %v", err)
	}
	upsertWritebackListWorkspace(t, localDir)

	var jsonOut bytes.Buffer
	if err := run([]string{"writeback", "list", "--state", "dead", "--workspace", "demo", "--json"}, strings.NewReader(""), &jsonOut, &jsonOut); err != nil {
		t.Fatalf("run writeback list dead --json failed: %v", err)
	}
	var items []writebackListItem
	if err := json.Unmarshal(jsonOut.Bytes(), &items); err != nil {
		t.Fatalf("parse --json output failed: %v\npayload:\n%s", err, jsonOut.String())
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 dead row, got %d", len(items))
	}
	if items[0].Code != "schema_violation" || items[0].Message != "body must include event" || items[0].ProviderStatus != 422 || items[0].Attempts != 4 {
		t.Fatalf("expected sidecar fields merged inline, got %+v", items[0])
	}
	if items[0].FirstAttemptAt != "2026-05-12T09:00:00Z" || items[0].LastAttemptAt != "2026-05-12T10:00:00Z" {
		t.Fatalf("expected sidecar timestamps, got %+v", items[0])
	}
	if items[0].Error == nil || items[0].Error.Code != "schema_violation" || items[0].Error.OpID != "op_a" {
		t.Fatalf("expected SDK error object from sidecar, got %+v", items[0].Error)
	}
	if !json.Valid(items[0].ProviderResponse) || !strings.Contains(string(items[0].ProviderResponse), "bad_writeback") {
		t.Fatalf("expected providerResponse merged, got %s", string(items[0].ProviderResponse))
	}
}

func upsertWritebackListWorkspace(t *testing.T, localDir string) {
	t.Helper()
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
}

func writeWritebackListState(t *testing.T, localDir string, state syncStateFile) {
	t.Helper()
	dir := filepath.Join(localDir, ".relay")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .relay failed: %v", err)
	}
	payload, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("marshal state failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), payload, 0o644); err != nil {
		t.Fatalf("write state failed: %v", err)
	}
}
