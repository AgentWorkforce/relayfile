package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeDigestRebuilder struct {
	called bool
	opts   digestRebuildOptions
	result digestRebuildResult
	err    error
}

func (f *fakeDigestRebuilder) Rebuild(_ context.Context, opts digestRebuildOptions) (digestRebuildResult, error) {
	f.called = true
	f.opts = opts
	return f.result, f.err
}

func TestDigestRequiresWindow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var stderr bytes.Buffer
	err := run([]string{"digest", "rebuild"}, strings.NewReader(""), &stderr, &stderr)
	if err == nil {
		t.Fatalf("expected missing window error, got nil")
	}
	if !strings.Contains(err.Error(), "usage: relayfile digest rebuild --window") {
		t.Fatalf("expected usage in error, got %q", err.Error())
	}
}

func TestDigestUnknownWindowErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	upsertDigestWorkspace(t, localDir)

	var stderr bytes.Buffer
	err := run([]string{"digest", "rebuild", "--window", "monday", "--workspace", "demo"}, strings.NewReader(""), &stderr, &stderr)
	if err == nil {
		t.Fatalf("expected unknown window error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown window") {
		t.Fatalf("expected unknown window in error, got %q", err.Error())
	}
}

func TestDigestUnknownSubcommandErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	var stderr bytes.Buffer
	err := run([]string{"digest", "compute"}, strings.NewReader(""), &stderr, &stderr)
	if err == nil {
		t.Fatalf("expected unknown subcommand error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown digest subcommand") {
		t.Fatalf("expected unknown digest subcommand in error, got %q", err.Error())
	}
}

func TestDigestRebuildCallsRebuilder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	upsertDigestWorkspace(t, localDir)

	fake := &fakeDigestRebuilder{result: digestRebuildResult{Path: localDir + "/digests/yesterday.md", Events: 3}}
	var stdout bytes.Buffer
	withDigestRebuilder(fake, func() {
		if err := run([]string{"digest", "rebuild", "--window", "yesterday", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
			t.Fatalf("run digest rebuild failed: %v\nstdout:\n%s", err, stdout.String())
		}
	})
	if !fake.called {
		t.Fatalf("expected rebuilder to be called")
	}
	if fake.opts.Window != "yesterday" {
		t.Fatalf("expected window=yesterday, got %q", fake.opts.Window)
	}
	if fake.opts.WorkspaceID != "ws_demo" {
		t.Fatalf("expected workspaceId=ws_demo, got %q", fake.opts.WorkspaceID)
	}
	if fake.opts.LocalDir != localDir {
		t.Fatalf("expected localDir=%q, got %q", localDir, fake.opts.LocalDir)
	}
	if !strings.Contains(stdout.String(), "Regenerated ") || !strings.Contains(stdout.String(), "events=3") {
		t.Fatalf("expected success line with event count, got %q", stdout.String())
	}
}

func TestDigestRebuildSurfacesError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	upsertDigestWorkspace(t, localDir)

	fake := &fakeDigestRebuilder{err: errors.New("boom")}
	var stdout bytes.Buffer
	var got error
	withDigestRebuilder(fake, func() {
		got = run([]string{"digest", "rebuild", "--window", "today", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout)
	})
	if got == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(got.Error(), "boom") {
		t.Fatalf("expected error to wrap rebuilder error, got %q", got.Error())
	}
}

func TestDigestRebuildWritesDateStampedDigestAndAlias(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	indexDir := filepath.Join(localDir, "linear", "issues")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatalf("mkdir index dir failed: %v", err)
	}
	indexPayload := `[
  {"identifier":"LIN-1","title":"Integration Tracking","updated":"2026-05-12T10:00:00Z","path":"/linear/issues/LIN-1.json"},
  {"identifier":"LIN-2","title":"Outside Window","updated":"2026-05-11T10:00:00Z","path":"/linear/issues/LIN-2.json"}
]`
	if err := os.WriteFile(filepath.Join(indexDir, "_index.json"), []byte(indexPayload), 0o644); err != nil {
		t.Fatalf("write index failed: %v", err)
	}
	upsertDigestWorkspace(t, localDir)

	var stdout bytes.Buffer
	withDigestRebuilder(localDigestRebuilder{now: func() time.Time {
		return time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	}}, func() {
		if err := run([]string{"digest", "rebuild", "--window", "yesterday", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
			t.Fatalf("run digest rebuild failed: %v\nstdout:\n%s", err, stdout.String())
		}
	})
	for _, rel := range []string{"digests/2026-05-12.md", "digests/yesterday.md"} {
		body, err := os.ReadFile(filepath.Join(localDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s failed: %v", rel, err)
		}
		got := string(body)
		for _, needle := range []string{"date: 2026-05-12", "covers: yesterday", "events: 1", "LIN-1", "/linear/issues/LIN-1.json"} {
			if !strings.Contains(got, needle) {
				t.Fatalf("%s missing %q:\n%s", rel, needle, got)
			}
		}
	}
	if !strings.Contains(stdout.String(), "events=1") {
		t.Fatalf("expected event count in stdout, got %q", stdout.String())
	}
}

func TestDigestRebuildUsesRemoteRootForNonRootMount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	indexDir := filepath.Join(localDir, "pages")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatalf("mkdir index dir failed: %v", err)
	}
	indexPayload := `[
  {"identifier":"page-1","title":"Rooted Page","updated":"2026-05-12T10:00:00Z"}
]`
	if err := os.WriteFile(filepath.Join(indexDir, "_index.json"), []byte(indexPayload), 0o644); err != nil {
		t.Fatalf("write index failed: %v", err)
	}
	writeWritebackListState(t, localDir, syncStateFile{WorkspaceID: "ws_demo", RemoteRoot: "/notion"})
	upsertDigestWorkspace(t, localDir)

	var stdout bytes.Buffer
	withDigestRebuilder(localDigestRebuilder{now: func() time.Time {
		return time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	}}, func() {
		if err := run([]string{"digest", "rebuild", "--window", "yesterday", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
			t.Fatalf("run digest rebuild failed: %v\nstdout:\n%s", err, stdout.String())
		}
	})
	body, err := os.ReadFile(filepath.Join(localDir, "digests", "yesterday.md"))
	if err != nil {
		t.Fatalf("read yesterday digest failed: %v", err)
	}
	got := string(body)
	for _, needle := range []string{"events: 1", "page-1", "/notion/pages/_index.json"} {
		if !strings.Contains(got, needle) {
			t.Fatalf("digest missing %q:\n%s", needle, got)
		}
	}
	if strings.Contains(got, "/pages/_index.json") && !strings.Contains(got, "/notion/pages/_index.json") {
		t.Fatalf("digest used local-relative path instead of remote root:\n%s", got)
	}
}

func TestDigestRebuildReadsRowsIndexesAndNestedDigestNamedDirectories(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	indexDir := filepath.Join(localDir, "notion", "pages")
	nestedDir := filepath.Join(localDir, "notion", "pages", "by-title", "digests")
	for _, dir := range []string{indexDir, nestedDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s failed: %v", dir, err)
		}
	}
	indexPayload := `{"rows":[
  {"id":"page-1","title":"Release Plan","lastEditedAt":"2026-05-12T10:00:00Z","path":"/notion/pages/page-1/content.md"},
  {"id":"page-2","title":"Nested Digest Page","last_edited_at":"2026-05-12T11:00:00Z","path":"/notion/pages/by-title/digests/page-2.json"}
]}`
	if err := os.WriteFile(filepath.Join(indexDir, "_index.json"), []byte(indexPayload), 0o644); err != nil {
		t.Fatalf("write rows index failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "_index.json"), []byte(`[{"id":"page-3","updated":"2026-05-12T12:00:00Z","path":"/notion/pages/by-title/digests/page-3.json"}]`), 0o644); err != nil {
		t.Fatalf("write nested index failed: %v", err)
	}
	upsertDigestWorkspace(t, localDir)

	var stdout bytes.Buffer
	withDigestRebuilder(localDigestRebuilder{now: func() time.Time {
		return time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	}}, func() {
		if err := run([]string{"digest", "rebuild", "--window", "yesterday", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
			t.Fatalf("run digest rebuild failed: %v\nstdout:\n%s", err, stdout.String())
		}
	})
	body, err := os.ReadFile(filepath.Join(localDir, "digests", "yesterday.md"))
	if err != nil {
		t.Fatalf("read yesterday digest failed: %v", err)
	}
	got := string(body)
	for _, needle := range []string{"events: 3", "Release Plan", "Nested Digest Page", "page-3"} {
		if !strings.Contains(got, needle) {
			t.Fatalf("digest missing %q:\n%s", needle, got)
		}
	}
}

func TestDigestWindowsResolveInUTC(t *testing.T) {
	now := time.Date(2026, 5, 18, 1, 2, 3, 0, time.UTC)
	tests := []struct {
		window string
		want   []string
	}{
		{window: "today", want: []string{"2026-05-18"}},
		{window: "yesterday", want: []string{"2026-05-17"}},
		{window: "2026-05-12", want: []string{"2026-05-12"}},
		{window: "this-week", want: []string{"2026-05-18"}},
		{window: "last-week", want: []string{"2026-05-11"}},
	}
	for _, tt := range tests {
		t.Run(tt.window, func(t *testing.T) {
			windows, err := resolveDigestWindows(tt.window, now)
			if err != nil {
				t.Fatalf("resolveDigestWindows failed: %v", err)
			}
			if len(windows) != len(tt.want) {
				t.Fatalf("window count = %d, want %d: %+v", len(windows), len(tt.want), windows)
			}
			for i, want := range tt.want {
				if got := windows[i].Date.Format("2006-01-02"); got != want {
					t.Fatalf("windows[%d] = %s, want %s", i, got, want)
				}
			}
		})
	}
}

func TestDigestRebuildWritesWeeklyDigestFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	indexDir := filepath.Join(localDir, "linear", "issues")
	if err := os.MkdirAll(indexDir, 0o755); err != nil {
		t.Fatalf("mkdir index dir failed: %v", err)
	}
	indexPayload := `[
  {"identifier":"LIN-1","updated":"2026-05-12T10:00:00Z","path":"/linear/issues/LIN-1.json"},
  {"identifier":"LIN-2","updated":"2026-05-17T10:00:00Z","path":"/linear/issues/LIN-2.json"},
  {"identifier":"LIN-3","updated":"2026-05-18T10:00:00Z","path":"/linear/issues/LIN-3.json"}
]`
	if err := os.WriteFile(filepath.Join(indexDir, "_index.json"), []byte(indexPayload), 0o644); err != nil {
		t.Fatalf("write index failed: %v", err)
	}
	upsertDigestWorkspace(t, localDir)

	var stdout bytes.Buffer
	withDigestRebuilder(localDigestRebuilder{now: func() time.Time {
		return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	}}, func() {
		if err := run([]string{"digest", "rebuild", "--window", "last-week", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
			t.Fatalf("run digest rebuild failed: %v\nstdout:\n%s", err, stdout.String())
		}
	})

	body, err := os.ReadFile(filepath.Join(localDir, "digests", "last-week.md"))
	if err != nil {
		t.Fatalf("read last-week digest failed: %v", err)
	}
	got := string(body)
	for _, needle := range []string{"date: 2026-05-11", "covers: last-week", "events: 2", "LIN-1", "LIN-2"} {
		if !strings.Contains(got, needle) {
			t.Fatalf("last-week digest missing %q:\n%s", needle, got)
		}
	}
	if strings.Contains(got, "LIN-3") {
		t.Fatalf("last-week digest included current-week event:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(localDir, "digests", "2026-05-11.md")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("last-week rebuild should write last-week.md, not first daily file; stat err=%v", err)
	}
}

func TestEnsureMirrorLayoutMaterializesActivitySummarySkill(t *testing.T) {
	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(localDir, ".skills", "activity-summary.md"))
	if err != nil {
		t.Fatalf("read activity-summary skill failed: %v", err)
	}
	for _, needle := range []string{"activity-summary", "digests/yesterday.md", "_index.json", "LAYOUT.md"} {
		if !strings.Contains(string(body), needle) {
			t.Fatalf("activity-summary skill missing %q:\n%s", needle, string(body))
		}
	}
}

func upsertDigestWorkspace(t *testing.T, localDir string) {
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
