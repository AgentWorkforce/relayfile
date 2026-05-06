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
// Skips .git, .relay, node_modules.
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
	return first == ".git" || first == ".relay" || first == "node_modules" ||
		first == ".relayfile-mount-state.json" || first == "_PERMISSIONS.md"
}

func (fw *FileWatcher) queueChange(rel string, op fsnotify.Op) {
	fw.mu.Lock()
	if t, ok := fw.debounce[rel]; ok {
		t.Stop()
	}
	fw.debounce[rel] = time.AfterFunc(100*time.Millisecond, func() {
		fw.onChange(rel, op)
		fw.mu.Lock()
		delete(fw.debounce, rel)
		fw.mu.Unlock()
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

// addDirRecursive walks `base` and adds every directory underneath it to the
// fsnotify watcher, skipping `.git`, `.relay`, `node_modules`, and the
// mount-state file. Used both at startup (to seed the watcher with the
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
		if name == ".git" || name == ".relay" || name == "node_modules" || name == ".relayfile-mount-state.json" {
			return filepath.SkipDir
		}
		// Best-effort add. fsnotify returns an error for already-watched dirs
		// on macOS/FSEvents in some cases; we ignore it because re-adding is
		// a no-op semantically.
		_ = fw.watcher.Add(path)
		return nil
	})
}

func (fw *FileWatcher) Close() error {
	return fw.watcher.Close()
}
