package main

import (
	"bytes"
	"context"
	"errors"
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

func TestDigestRebuildStubIsNotWired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)

	localDir := t.TempDir()
	if err := ensureMirrorLayout(localDir); err != nil {
		t.Fatalf("ensureMirrorLayout failed: %v", err)
	}
	upsertDigestWorkspace(t, localDir)

	var stdout bytes.Buffer
	err := run([]string{"digest", "rebuild", "--window", "today", "--workspace", "demo"}, strings.NewReader(""), &stdout, &stdout)
	if err == nil {
		t.Fatalf("expected stub error, got nil")
	}
	if !strings.Contains(err.Error(), "not yet wired") {
		t.Fatalf("expected not-yet-wired error from stub, got %q", err.Error())
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
