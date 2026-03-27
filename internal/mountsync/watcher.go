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
	err := filepath.Walk(fw.localDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if !info.IsDir() {
			return nil
		}
		name := info.Name()
		if name == ".git" || name == ".relay" || name == "node_modules" || name == ".relayfile-mount-state.json" {
			return filepath.SkipDir
		}
		return fw.watcher.Add(path)
	})
	if err != nil {
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

				// If a new directory was created, add it to the watcher and
				// emit synthetic create events for files already inside.
				if event.Op&fsnotify.Create != 0 {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						_ = fw.watcher.Add(event.Name)
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

func (fw *FileWatcher) Close() error {
	return fw.watcher.Close()
}
