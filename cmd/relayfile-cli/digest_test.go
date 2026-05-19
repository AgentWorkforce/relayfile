package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	upsertDigestWorkspace(t, localDir, "")

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

func TestDigestRebuildCallsInjectedRebuilder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	upsertDigestWorkspace(t, localDir, "Europe/Oslo")

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
	if fake.opts.Window != "yesterday" || fake.opts.WorkspaceID != "ws_demo" || fake.opts.LocalDir != localDir {
		t.Fatalf("unexpected opts: %+v", fake.opts)
	}
	if fake.opts.Timezone != "Europe/Oslo" {
		t.Fatalf("expected configured timezone in opts, got %q", fake.opts.Timezone)
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
	upsertDigestWorkspace(t, localDir, "")

	fake := &fakeDigestRebuilder{err: errors.New("boom")}
	var stdout bytes.Buffer
	var got error
	withDigestRebuilder(fake, func() {
		got = run([]string{"digest", "rebuild", "--window", "today", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout)
	})
	if got == nil || !strings.Contains(got.Error(), "boom") {
		t.Fatalf("expected boom error, got %v", got)
	}
}

func TestDigestRebuildRealWindows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_TZ", "UTC")
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	withDigestNow(now, func() {
		localDir := t.TempDir()
		seedDigestMirror(t, localDir, now)
		upsertDigestWorkspace(t, localDir, "UTC")

		for _, window := range []string{"today", "yesterday", "this-week", "last-week", "2026-05-15", "2026-05-12"} {
			var stdout bytes.Buffer
			if err := run([]string{"digest", "rebuild", "--window", window, "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
				t.Fatalf("digest rebuild %s failed: %v", window, err)
			}
			if !strings.Contains(stdout.String(), "Regenerated ") {
				t.Fatalf("digest rebuild %s missing success output: %q", window, stdout.String())
			}
		}

		for _, rel := range []string{
			"digests/today.md",
			"digests/yesterday.md",
			"digests/this-week.md",
			"digests/last-week.md",
			"digests/2026-05-15.md",
			"digests/2026-05-12.md",
		} {
			if _, err := os.Stat(filepath.Join(localDir, filepath.FromSlash(rel))); err != nil {
				t.Fatalf("expected %s to be written: %v", rel, err)
			}
		}
	})
}

func TestDigestRebuildYesterdayWritesDateStampedSibling(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_TZ", "UTC")
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	withDigestNow(now, func() {
		localDir := t.TempDir()
		seedDigestMirror(t, localDir, now)
		upsertDigestWorkspace(t, localDir, "UTC")

		var stdout bytes.Buffer
		if err := run([]string{"digest", "rebuild", "--window", "yesterday", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
			t.Fatalf("digest rebuild yesterday failed: %v", err)
		}
		for _, rel := range []string{"digests/yesterday.md", "digests/2026-05-14.md"} {
			if _, err := os.Stat(filepath.Join(localDir, filepath.FromSlash(rel))); err != nil {
				t.Fatalf("expected %s to be written by isolated yesterday rebuild: %v", rel, err)
			}
		}
		body, err := os.ReadFile(filepath.Join(localDir, "digests", "2026-05-14.md"))
		if err != nil {
			t.Fatalf("read date-stamped sibling: %v", err)
		}
		if !bytes.Contains(body, []byte("linear/issues/2.md")) || bytes.Contains(body, []byte("github/issues/1.md")) {
			t.Fatalf("date-stamped sibling did not cover only the closed local day:\n%s", body)
		}
	})
}

func TestDigestRebuildPastDateWithNoEvents(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_TZ", "UTC")
	withDigestNow(time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC), func() {
		localDir := t.TempDir()
		if err := ensureMirrorLayout(localDir); err != nil {
			t.Fatalf("ensureMirrorLayout failed: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(localDir, "github"), 0o755); err != nil {
			t.Fatalf("mkdir provider: %v", err)
		}
		upsertDigestWorkspace(t, localDir, "UTC")

		var stdout bytes.Buffer
		if err := run([]string{"digest", "rebuild", "--window", "2026-05-12", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
			t.Fatalf("digest rebuild no-events date failed: %v", err)
		}
		body, err := os.ReadFile(filepath.Join(localDir, "digests", "2026-05-12.md"))
		if err != nil {
			t.Fatalf("read date digest: %v", err)
		}
		if !bytes.Contains(body, []byte("events: 0")) || !bytes.Contains(body, []byte("_no activity_")) {
			t.Fatalf("no-event digest missing expected body:\n%s", body)
		}
	})
}

func TestDigestRebuildPastDateExcludesNewerFiles(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_TZ", "UTC")
	withDigestNow(time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC), func() {
		localDir := t.TempDir()
		if err := ensureMirrorLayout(localDir); err != nil {
			t.Fatalf("ensureMirrorLayout failed: %v", err)
		}
		files := map[string]time.Time{
			"github/issues/old.md": time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
			"github/issues/new.md": time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC),
		}
		for rel, mod := range files {
			path := filepath.Join(localDir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", rel, err)
			}
			if err := os.WriteFile(path, []byte(rel), 0o644); err != nil {
				t.Fatalf("write %s: %v", rel, err)
			}
			if err := os.Chtimes(path, mod, mod); err != nil {
				t.Fatalf("chtimes %s: %v", rel, err)
			}
		}
		upsertDigestWorkspace(t, localDir, "UTC")

		var stdout bytes.Buffer
		if err := run([]string{"digest", "rebuild", "--window", "2026-05-12", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout); err != nil {
			t.Fatalf("digest rebuild past date failed: %v", err)
		}
		body, err := os.ReadFile(filepath.Join(localDir, "digests", "2026-05-12.md"))
		if err != nil {
			t.Fatalf("read rebuilt digest: %v", err)
		}
		if !bytes.Contains(body, []byte("github/issues/old.md")) || bytes.Contains(body, []byte("github/issues/new.md")) {
			t.Fatalf("past date rebuild included files outside the requested day:\n%s", body)
		}
	})
}

func TestDigestRebuildDateErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_TZ", "UTC")
	withDigestNow(time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC), func() {
		localDir := t.TempDir()
		if err := ensureMirrorLayout(localDir); err != nil {
			t.Fatalf("ensureMirrorLayout failed: %v", err)
		}
		upsertDigestWorkspace(t, localDir, "UTC")

		var stdout bytes.Buffer
		err := run([]string{"digest", "rebuild", "--window", "2026-05-16", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout)
		if err == nil || !strings.Contains(err.Error(), "future") {
			t.Fatalf("expected future date error, got %v", err)
		}
		err = run([]string{"digest", "rebuild", "--window", "2026-02-30", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout)
		if err == nil || !strings.Contains(err.Error(), "unknown window") {
			t.Fatalf("expected malformed date error, got %v", err)
		}
	})
}

func TestDigestRebuildJSONIncludesWarningsWhenPresent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	upsertDigestWorkspace(t, localDir, "UTC")

	fake := &fakeDigestRebuilder{result: digestRebuildResult{
		Path:     filepath.Join(localDir, "digests", "today.md"),
		Events:   1,
		Warnings: []string{"example warning"},
	}}
	var stdout bytes.Buffer
	withDigestRebuilder(fake, func() {
		if err := run([]string{"digest", "rebuild", "--window", "today", "--workspace", "demo", "--json"}, strings.NewReader(""), &stdout, &stdout); err != nil {
			t.Fatalf("digest rebuild json failed: %v", err)
		}
	})
	var got digestRebuildResult
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json output invalid: %v\n%s", err, stdout.String())
	}
	if len(got.Warnings) != 1 || got.Warnings[0] != "example warning" {
		t.Fatalf("warnings missing from json result: %+v", got)
	}
}

func TestDigestRebuildJSONAndForceReplace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	t.Setenv("RELAYFILE_TZ", "UTC")
	withDigestNow(time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC), func() {
		localDir := t.TempDir()
		seedDigestMirror(t, localDir, time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC))
		oldPath := filepath.Join(localDir, "github", "issues", "old.md")
		if err := os.MkdirAll(filepath.Dir(oldPath), 0o755); err != nil {
			t.Fatalf("mkdir old issue: %v", err)
		}
		if err := os.WriteFile(oldPath, []byte("old"), 0o644); err != nil {
			t.Fatalf("write old issue: %v", err)
		}
		oldTime := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
		if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
			t.Fatalf("chtimes old issue: %v", err)
		}
		upsertDigestWorkspace(t, localDir, "UTC")

		target := filepath.Join(localDir, "digests", "2026-05-12.md")
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatalf("mkdir digest dir: %v", err)
		}
		if err := os.WriteFile(target, []byte("stale"), 0o644); err != nil {
			t.Fatalf("write stale digest: %v", err)
		}

		var stdout bytes.Buffer
		if err := run([]string{"digest", "rebuild", "--window", "2026-05-12", "--workspace", "demo", "--json"}, strings.NewReader(""), &stdout, &stdout); err != nil {
			t.Fatalf("digest rebuild json failed: %v", err)
		}
		var got digestRebuildResult
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("json output invalid: %v\n%s", err, stdout.String())
		}
		if got.WorkspaceID != "ws_demo" || got.Window != "2026-05-12" || got.Events == 0 {
			t.Fatalf("unexpected json result: %+v", got)
		}
		if got.Warnings != nil {
			t.Fatalf("expected warnings to be omitted for clean rebuild, got %+v", got.Warnings)
		}
		body, err := os.ReadFile(target)
		if err != nil {
			t.Fatalf("read rebuilt digest: %v", err)
		}
		if bytes.Equal(body, []byte("stale")) || !bytes.Contains(body, []byte("github/issues/old.md")) {
			t.Fatalf("force rebuild did not replace stale digest:\n%s", body)
		}
	})
}

func seedDigestMirror(t *testing.T, localDir string, now time.Time) {
	t.Helper()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	files := map[string]time.Time{
		"github/issues/1.md": now,
		"linear/issues/2.md": now.AddDate(0, 0, -1),
	}
	for rel, mod := range files {
		path := filepath.Join(localDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(rel), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("chtimes %s: %v", rel, err)
		}
	}
}

func withDigestNow(now time.Time, fn func()) {
	prev := digestNow
	digestNow = func() time.Time { return now }
	defer func() { digestNow = prev }()
	fn()
}

func upsertDigestWorkspace(t *testing.T, localDir, timezone string) {
	t.Helper()
	if _, err := upsertWorkspaceDetails(workspaceRecord{
		Name:       "demo",
		ID:         "ws_demo",
		LocalDir:   localDir,
		Timezone:   timezone,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsertWorkspaceDetails failed: %v", err)
	}
}
