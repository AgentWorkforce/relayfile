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

func TestWatcherCloseCancelsPendingDebounce(t *testing.T) {
	localDir := t.TempDir()
	events := make(chan watcherEvent, 1)
	watcher, err := NewFileWatcher(localDir, func(relativePath string, op fsnotify.Op) {
		events <- watcherEvent{path: filepath.ToSlash(relativePath), op: op}
	})
	if err != nil {
		t.Fatalf("create file watcher: %v", err)
	}

	watcher.queueChange("notes.md", fsnotify.Write)
	if err := watcher.Close(); err != nil {
		t.Fatalf("close watcher: %v", err)
	}
	assertNoWatcherEvents(t, events, 150*time.Millisecond)
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

// TestWatcherSkipsMountStateTempFiles pins the bug where writeFileAtomic
// produced temp files like ".relayfile-mount-state.json.tmp-12345" which
// the watcher used to NOT recognize as ours (it only matched the exact
// state-file name). The watcher would queue an upload for the temp file,
// race the rename, fail with ENOENT, and bubble that error out of
// saveState — blocking websocket-event apply and writeback queueing.
func TestWatcherSkipsMountStateTempFiles(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	// Same temp pattern writeFileAtomic produces for the state file.
	tmp := filepath.Join(localDir, ".relayfile-mount-state.json.tmp-12345")
	if err := os.WriteFile(tmp, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write temp state file: %v", err)
	}
	// Also exercise the state file itself.
	state := filepath.Join(localDir, ".relayfile-mount-state.json")
	if err := os.WriteFile(state, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	assertNoWatcherEvents(t, events, 300*time.Millisecond)
}

// TestWatcherDoesNotSkipMountStateSiblings pins that the narrow skip
// matches only the state file and its ".tmp-" variants — siblings that
// merely share the prefix (e.g. a user-created
// ".relayfile-mount-state.json.backup") still produce events and sync
// like any other mount file.
func TestWatcherDoesNotSkipMountStateSiblings(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	sibling := filepath.Join(localDir, ".relayfile-mount-state.json.backup")
	if err := os.WriteFile(sibling, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write sibling file: %v", err)
	}

	expected := filepath.ToSlash(".relayfile-mount-state.json.backup")
	if _, ok := waitForWatcherEventPath(t, events, expected, time.Second); !ok {
		t.Fatalf("expected an event for the .backup sibling; the skip is too broad")
	}
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

// TestWatcherNewNestedSubdirectoryTree pins the regression that motivated
// the recursive add: when a sync-down (or any caller) creates a deeply
// nested tree of new directories in one operation, fsnotify only delivers
// a single Create event for the topmost new directory. If we add only
// `event.Name` to the watcher, files written several levels deep in the
// new tree never produce a Write event because the inner directories were
// never subscribed to.
//
// Concrete production case: the Notion adapter mirrors a page like
//
//	/notion/pages/demos--<hex>/blocks/<id>.json
//
// in one MkdirAll-style operation. The bug surfaced as "agent edits the
// file in their local mount but no writeback queues."
func TestWatcherNewNestedSubdirectoryTree(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	// Create three levels of new directories in one MkdirAll call. Mirrors
	// what the sync-down does when a previously-unseen page lands.
	deepDir := filepath.Join(localDir, "notion", "pages", "demo--abc")
	if err := os.MkdirAll(deepDir, 0o755); err != nil {
		t.Fatalf("create nested subdirectories: %v", err)
	}
	// Write a file at the bottom of the new tree. Pre-fix, this file's
	// directory was never added to fsnotify, so no Write event fired.
	target := filepath.Join(deepDir, "content.md")
	if err := os.WriteFile(target, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file in nested subdirectory: %v", err)
	}

	expected := filepath.ToSlash("notion/pages/demo--abc/content.md")
	if _, ok := waitForWatcherEventPath(t, events, expected, 2*time.Second); !ok {
		t.Fatalf("no watcher event for file in deeply nested new subdirectory tree")
	}
}

// TestWatcherEditInNestedSubdirAfterSyncDown is the closest reproduction
// of the production failure mode: a mount-daemon is running, a sync-down
// creates a new nested tree (file already populated by the sync), and
// then a user/agent edits the file. The edit must produce a Write event.
func TestWatcherEditInNestedSubdirAfterSyncDown(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	// Sync-down phase: nested tree appears, populated.
	deepDir := filepath.Join(localDir, "notion", "pages", "demo--abc")
	if err := os.MkdirAll(deepDir, 0o755); err != nil {
		t.Fatalf("create nested subdirectories: %v", err)
	}
	target := filepath.Join(deepDir, "content.md")
	if err := os.WriteFile(target, []byte("v1"), 0o644); err != nil {
		t.Fatalf("seed file in nested subdirectory: %v", err)
	}
	// Drain any initial Create-side events so we can isolate the next Write.
	expected := filepath.ToSlash("notion/pages/demo--abc/content.md")
	_, _ = waitForWatcherEventPath(t, events, expected, 2*time.Second)

	// Edit phase: user/agent overwrites the file.
	if err := os.WriteFile(target, []byte("v2 — edited"), 0o644); err != nil {
		t.Fatalf("edit file in nested subdirectory: %v", err)
	}

	// Must observe at least one event for the edited path. fsnotify can
	// deliver this as Write or Create depending on platform/editor; either
	// is fine — the point is that the watcher saw it.
	if _, ok := waitForWatcherEventPath(t, events, expected, 2*time.Second); !ok {
		t.Fatalf("no watcher event for edit in nested subdirectory created at runtime")
	}
}

func TestWatcherDoesNotSkipNestedReservedNameDirectories(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	nestedDir := filepath.Join(localDir, "notion", "pages", "by-title", "digests")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("create nested reserved-name directory: %v", err)
	}
	target := filepath.Join(nestedDir, "page.json")
	if err := os.WriteFile(target, []byte(`{"id":"page"}`), 0o644); err != nil {
		t.Fatalf("write file in nested reserved-name directory: %v", err)
	}

	expected := filepath.ToSlash("notion/pages/by-title/digests/page.json")
	if _, ok := waitForWatcherEventPath(t, events, expected, 2*time.Second); !ok {
		t.Fatalf("no watcher event for nested reserved-name directory path")
	}
}
