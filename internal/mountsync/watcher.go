package mountsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FileWatcher watches a local directory for changes using OS-level
// notifications (inotify on Linux, FSEvents on macOS).
type FileWatcher struct {
	watcher  *fsnotify.Watcher
	localDir string
	onChange func(relativePath string, op fsnotify.Op)
	mu       sync.Mutex
	debounce map[string]*time.Timer // debounce rapid events per file
	closed   bool
	wg       sync.WaitGroup
}

func NewFileWatcher(localDir string, onChange func(string, fsnotify.Op)) (*FileWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &FileWatcher{
		watcher:  w,
		localDir: localDir,
		onChange: onChange,
		debounce: make(map[string]*time.Timer),
	}, nil
}

// Start begins watching. Recursively adds all subdirectories.
// Skips internal runtime trees such as .git, .relay, .skills, and node_modules.
// Digest files are deliberately watched: they must emit normal mount events,
// while HandleLocalChange still refuses to write them back upstream.
func (fw *FileWatcher) Start(ctx context.Context) error {
	// Walk localDir, add all dirs to watcher (fsnotify watches dirs, not files)
	if err := fw.addDirRecursive(fw.localDir); err != nil {
		return err
	}

	// Event loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-fw.watcher.Events:
				if !ok {
					return
				}
				// Skip events for ignored dirs/files
				rel, err := filepath.Rel(fw.localDir, event.Name)
				if err != nil {
					continue
				}
				if fw.shouldSkip(rel) {
					continue
				}

				// If a new directory was created, recursively add it AND every
				// subdirectory underneath to the watcher, then emit synthetic
				// create events for files already inside. The recursive add is
				// load-bearing: when a sync-down creates a nested tree (e.g.
				// `notion/pages/<page>/blocks/`) in one operation, fsnotify
				// only delivers an event for the topmost new directory. Adding
				// only `event.Name` would leave the inner subdirs unwatched
				// and any subsequent edits to files inside them silent.
				if event.Op&fsnotify.Create != 0 {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						_ = fw.addDirRecursive(event.Name)
						fw.emitExistingFileEvents(event.Name)
					}
				}

				// Debounce: wait 100ms for rapid events on same file to settle
				// (editors often do write + chmod + rename in quick succession).
				fw.queueChange(rel, event.Op)

			case _, ok := <-fw.watcher.Errors:
				if !ok {
					return
				}
				// Log but don't crash on watcher errors
			}
		}
	}()

	return nil
}

func (fw *FileWatcher) shouldSkip(rel string) bool {
	parts := strings.SplitN(rel, string(os.PathSeparator), 2)
	first := parts[0]
	// Match the state file itself and only its writeFileAtomic temp
	// variants (e.g. ".relayfile-mount-state.json.tmp-12345"). A broader
	// HasPrefix would silently swallow legitimate sibling files like
	// ".relayfile-mount-state.json.backup" — those should sync normally.
	if first == ".relayfile-mount-state.json" ||
		strings.HasPrefix(first, ".relayfile-mount-state.json.tmp-") {
		return true
	}
	if watcherIgnoredTopLevel(first) {
		return true
	}
	// Data-loss guard: a top-level entry whose name equals the mount
	// directory's own basename is the round-trip-onto-root collision.
	// Never sync it.
	if fw.localDir != "" && first == filepath.Base(filepath.Clean(fw.localDir)) {
		return true
	}
	return false
}

// reservedTopLevel reports whether a top-level entry name is internal
// bookkeeping that must never participate in sync. Centralized so the
// watcher and scanLocalFiles stay in agreement. This list applies to
// top-level entries, including files such as _PERMISSIONS.md; addDirRecursive
// is directory-only and skips a different sentinel for the mount state file.
func reservedTopLevel(name string) bool {
	return name == ".git" || name == ".relay" || name == ".skills" ||
		name == "digests" || name == "node_modules" ||
		name == "_PERMISSIONS.md"
}

func (fw *FileWatcher) queueChange(rel string, op fsnotify.Op) {
	fw.mu.Lock()
	if fw.closed {
		fw.mu.Unlock()
		return
	}
	if t, ok := fw.debounce[rel]; ok {
		if t.Stop() {
			fw.wg.Done()
		}
	}
	fw.wg.Add(1)
	fw.debounce[rel] = time.AfterFunc(100*time.Millisecond, func() {
		defer fw.wg.Done()

		fw.mu.Lock()
		if fw.closed {
			delete(fw.debounce, rel)
			fw.mu.Unlock()
			return
		}
		delete(fw.debounce, rel)
		fw.mu.Unlock()
		fw.onChange(rel, op)
	})
	fw.mu.Unlock()
}

func (fw *FileWatcher) emitExistingFileEvents(base string) {
	_ = filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(fw.localDir, path)
		if relErr != nil || fw.shouldSkip(rel) {
			return nil
		}
		fw.queueChange(rel, fsnotify.Create)
		return nil
	})
}

func watcherIgnoredTopLevel(name string) bool {
	return reservedTopLevel(name) && name != "digests"
}

// addDirRecursive walks `base` and adds every directory underneath it to the
// fsnotify watcher, skipping top-level internal runtime trees. Used both at startup (to seed the watcher with the
// existing tree) and at runtime (when a sync-down creates a new nested
// directory structure that we need to start watching).
func (fw *FileWatcher) addDirRecursive(base string) error {
	return filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors; transient FS issues shouldn't kill the walk
		}
		if !info.IsDir() {
			return nil
		}
		name := info.Name()
		if fw.isTopLevelReservedDir(path, name) {
			return filepath.SkipDir
		}
		// Best-effort add. fsnotify returns an error for already-watched dirs
		// on macOS/FSEvents in some cases; we ignore it because re-adding is
		// a no-op semantically.
		_ = fw.watcher.Add(path)
		return nil
	})
}

func (fw *FileWatcher) isTopLevelReservedDir(path, name string) bool {
	rel, err := filepath.Rel(fw.localDir, path)
	if err != nil || rel == "." {
		return false
	}
	first := strings.SplitN(rel, string(os.PathSeparator), 2)[0]
	if first != name {
		return false
	}
	return watcherIgnoredTopLevel(name)
}

func (fw *FileWatcher) Close() error {
	fw.mu.Lock()
	fw.closed = true
	for rel, timer := range fw.debounce {
		if timer.Stop() {
			fw.wg.Done()
		}
		delete(fw.debounce, rel)
	}
	fw.mu.Unlock()

	err := fw.watcher.Close()
	fw.wg.Wait()
	return err
}
