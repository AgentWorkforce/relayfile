package mountsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

type watcherEvent struct {
	path string
	op   fsnotify.Op
}

func startFileWatcher(t *testing.T, localDir string) (chan watcherEvent, context.CancelFunc, *FileWatcher) {
	t.Helper()

	events := make(chan watcherEvent, 16)
	onChange := func(relativePath string, op fsnotify.Op) {
		events <- watcherEvent{
			path: filepath.ToSlash(relativePath),
			op:   op,
		}
	}

	watcher, err := NewFileWatcher(localDir, onChange)
	if err != nil {
		t.Fatalf("create file watcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := watcher.Start(ctx); err != nil {
		cancel()
		_ = watcher.Close()
		t.Fatalf("start file watcher: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = watcher.Close()
	})

	return events, cancel, watcher
}

func waitForWatcherEvent(t *testing.T, events <-chan watcherEvent, timeout time.Duration) (watcherEvent, bool) {
	t.Helper()

	select {
	case ev := <-events:
		return ev, true
	case <-time.After(timeout):
		return watcherEvent{}, false
	}
}

func waitForWatcherEventPath(t *testing.T, events <-chan watcherEvent, expected string, timeout time.Duration) (watcherEvent, bool) {
	t.Helper()

	expected = filepath.ToSlash(expected)
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-events:
			if ev.path == expected {
				return ev, true
			}
		case <-deadline:
			return watcherEvent{}, false
		}
	}
}

func assertNoWatcherEvents(t *testing.T, events <-chan watcherEvent, timeout time.Duration) {
	t.Helper()

	select {
	case ev := <-events:
		t.Fatalf("unexpected watcher event: %s %s", ev.path, ev.op.String())
	case <-time.After(timeout):
	}
}

func TestWatcherDetectsFileWrite(t *testing.T) {
	localDir := t.TempDir()
	target := filepath.Join(localDir, "notes.md")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatalf("seed initial file: %v", err)
	}

	events, _, _ := startFileWatcher(t, localDir)

	if err := os.WriteFile(target, []byte("v2"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	ev, ok := waitForWatcherEvent(t, events, time.Second)
	if !ok {
		t.Fatalf("no watcher event within timeout")
	}
	if ev.path != "notes.md" {
		t.Fatalf("unexpected watcher path: %s", ev.path)
	}
	if ev.op&fsnotify.Write == 0 {
		t.Fatalf("expected write op, got %s", ev.op)
	}
}

func TestWatcherDetectsFileCreate(t *testing.T) {
	localDir := t.TempDir()
	target := filepath.Join(localDir, "created.txt")

	events, _, _ := startFileWatcher(t, localDir)

	file, err := os.Create(target)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	ev, ok := waitForWatcherEvent(t, events, time.Second)
	if !ok {
		t.Fatalf("no watcher event within timeout")
	}
	if ev.path != "created.txt" {
		t.Fatalf("unexpected watcher path: %s", ev.path)
	}
	if ev.op&fsnotify.Create == 0 {
		t.Fatalf("expected create op, got %s", ev.op)
	}
}

func TestWatcherDetectsFileDelete(t *testing.T) {
	localDir := t.TempDir()
	target := filepath.Join(localDir, "delete.txt")
	if err := os.WriteFile(target, []byte("remove"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	events, _, _ := startFileWatcher(t, localDir)

	if err := os.Remove(target); err != nil {
		t.Fatalf("delete file: %v", err)
	}

	ev, ok := waitForWatcherEvent(t, events, time.Second)
	if !ok {
		t.Fatalf("no watcher event within timeout")
	}
	if ev.path != "delete.txt" {
		t.Fatalf("unexpected watcher path: %s", ev.path)
	}
	if ev.op&fsnotify.Remove == 0 {
		t.Fatalf("expected remove op, got %s", ev.op)
	}
}

func TestWatcherSkipsGitDir(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	gitConfig := filepath.Join(localDir, ".git", "config")
	if err := os.MkdirAll(filepath.Dir(gitConfig), 0o755); err != nil {
		t.Fatalf("create .git dir: %v", err)
	}
	if err := os.WriteFile(gitConfig, []byte("ignored"), 0o644); err != nil {
		t.Fatalf("write .git/config: %v", err)
	}

	assertNoWatcherEvents(t, events, 300*time.Millisecond)
}

func TestWatcherSkipsNodeModules(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	moduleFile := filepath.Join(localDir, "node_modules", "pkg", "index.js")
	if err := os.MkdirAll(filepath.Dir(moduleFile), 0o755); err != nil {
		t.Fatalf("create node_modules dir: %v", err)
	}
	if err := os.WriteFile(moduleFile, []byte("console.log(1)"), 0o644); err != nil {
		t.Fatalf("write node_modules file: %v", err)
	}

	assertNoWatcherEvents(t, events, 300*time.Millisecond)
}

func TestWatcherSkipsRelayDir(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	relayedFile := filepath.Join(localDir, ".relay", "tokens", "agent.jwt")
	if err := os.MkdirAll(filepath.Dir(relayedFile), 0o755); err != nil {
		t.Fatalf("create .relay dir: %v", err)
	}
	if err := os.WriteFile(relayedFile, []byte("token"), 0o644); err != nil {
		t.Fatalf("write .relay file: %v", err)
	}

	assertNoWatcherEvents(t, events, 300*time.Millisecond)
}

func TestWatcherDebounce(t *testing.T) {
	localDir := t.TempDir()
	target := filepath.Join(localDir, "burst.txt")
	if err := os.WriteFile(target, []byte("start"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	events, _, _ := startFileWatcher(t, localDir)

	for i := 0; i < 5; i++ {
		content := []byte(fmt.Sprintf("v%d", i))
		if err := os.WriteFile(target, content, 0o644); err != nil {
			t.Fatalf("write burst file: %v", err)
		}
		if i < 4 {
			time.Sleep(10 * time.Millisecond)
		}
	}

	ev, ok := waitForWatcherEvent(t, events, time.Second)
	if !ok {
		t.Fatalf("debounced watcher event not received")
	}
	if ev.path != "burst.txt" {
		t.Fatalf("unexpected watcher path: %s", ev.path)
	}
	if ev.op&fsnotify.Write == 0 {
		t.Fatalf("expected write op, got %s", ev.op)
	}

	select {
	case extra := <-events:
		t.Fatalf("expected one debounced event, got extra event: %s %s", extra.path, extra.op.String())
	case <-time.After(250 * time.Millisecond):
	}
}

func TestWatcherNewSubdirectory(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	subdir := filepath.Join(localDir, "docs")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatalf("create subdirectory: %v", err)
	}
	target := filepath.Join(subdir, "new.md")
	if err := os.WriteFile(target, []byte("subdir"), 0o644); err != nil {
		t.Fatalf("write file in new subdirectory: %v", err)
	}

	ev, ok := waitForWatcherEventPath(t, events, filepath.ToSlash("docs/new.md"), time.Second)
	if !ok {
		t.Fatalf("no watcher event for file in new subdirectory")
	}
	if ev.path != "docs/new.md" {
		t.Fatalf("unexpected watcher path: %s", ev.path)
	}
}
