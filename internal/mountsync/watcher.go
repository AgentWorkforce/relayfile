package mountsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentworkforce/relayfile/internal/mountscope"
	"github.com/fsnotify/fsnotify"
)

const defaultMaxWatchedDirs = 8192

const defaultLocalChangeBatchWindow = 5 * time.Millisecond

const (
	// DefaultFileChangeSettleDelay protects editors that stream a save through
	// several in-place write/chmod notifications before the bytes are stable.
	DefaultFileChangeSettleDelay = 100 * time.Millisecond
	// DefaultAtomicSaveSettleDelay is intentionally short: a destination-only
	// create/rename/remove notification is already the commit point of an
	// atomic save, so making every agent wait the in-place editor window only
	// adds coordination latency.
	DefaultAtomicSaveSettleDelay = time.Millisecond
	// atomicRenameCorrelationWindow is only a classifier window, not an added
	// debounce. fsnotify reports an atomic temp-file rename as a Rename for the
	// source followed by a Create for the committed destination. A bare Create
	// can also be the start of a streaming write and must use the safe delay.
	atomicRenameCorrelationWindow = 50 * time.Millisecond
)

var (
	ErrWatcherLimitExceeded   = errors.New("mount file watcher directory limit exceeded")
	ErrRecursiveWatcherUnsafe = errors.New("recursive mount file watcher is unsafe on this platform")
)

// LocalChange is one coalesced filesystem-watcher observation. HandleLocalChanges
// deliberately derives write/delete behavior from the current filesystem state;
// Op is retained for diagnostics and compatibility with fsnotify callers.
type LocalChange struct {
	RelativePath string
	Op           fsnotify.Op
}

// LocalChangeBatcher collapses the near-simultaneous per-path callbacks emitted
// by FileWatcher into one ordered batch. The per-file debounce has already let
// editor rename/write/chmod sequences settle; this short cross-file window is
// what turns a multi-file agent save into a single /fs/bulk request.
type LocalChangeBatcher struct {
	mu      sync.Mutex
	window  time.Duration
	maxWait time.Duration
	onBatch func([]LocalChange)
	pending map[string]LocalChange
	timer   *time.Timer
	started time.Time
	lastAdd time.Time
	closed  bool
	wg      sync.WaitGroup
}

func NewLocalChangeBatcher(window time.Duration, onBatch func([]LocalChange)) *LocalChangeBatcher {
	if window <= 0 {
		window = defaultLocalChangeBatchWindow
	}
	return &LocalChangeBatcher{
		window:  window,
		maxWait: 10 * window,
		onBatch: onBatch,
		pending: make(map[string]LocalChange),
	}
}

func (b *LocalChangeBatcher) Add(relativePath string, op fsnotify.Op) {
	if b == nil {
		return
	}
	relativePath = filepath.ToSlash(strings.TrimSpace(relativePath))
	if relativePath == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	change := b.pending[relativePath]
	change.RelativePath = relativePath
	change.Op |= op
	b.pending[relativePath] = change
	now := time.Now()
	b.lastAdd = now
	if b.timer != nil {
		// Flush after one quiet window so callbacks spread across a multi-file
		// save stay in one bulk request. Cap the total wait so sustained churn
		// cannot starve writeback indefinitely.
		delay := b.nextFlushDelayLocked(now)
		// Reset must be unconditional. For an AfterFunc timer, false means the
		// callback has expired or is already running; Reset schedules the next
		// callback in exactly that case. Gating Reset on Stop's result loses the
		// quiet-window extension when Add races the callback.
		if delay <= 0 {
			delay = time.Nanosecond
		}
		b.timer.Reset(delay)
		return
	}
	b.wg.Add(1)
	b.started = now
	b.timer = time.AfterFunc(b.window, b.flush)
}

func (b *LocalChangeBatcher) flush() {
	b.mu.Lock()
	// Reset may race an already-running AfterFunc callback. Recheck the
	// authoritative last-add deadline under the mutex before draining; a stale
	// callback must yield to the newly extended quiet window.
	if b.timer == nil {
		b.mu.Unlock()
		return
	}
	if !b.closed && len(b.pending) > 0 {
		if delay := b.nextFlushDelayLocked(time.Now()); delay > 0 {
			b.timer.Reset(delay)
			b.mu.Unlock()
			return
		}
	}
	b.timer = nil
	b.started = time.Time{}
	b.lastAdd = time.Time{}
	if b.closed || len(b.pending) == 0 {
		b.pending = make(map[string]LocalChange)
		b.mu.Unlock()
		b.wg.Done()
		return
	}
	paths := make([]string, 0, len(b.pending))
	for path := range b.pending {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	changes := make([]LocalChange, 0, len(paths))
	for _, path := range paths {
		changes = append(changes, b.pending[path])
	}
	b.pending = make(map[string]LocalChange)
	onBatch := b.onBatch
	b.mu.Unlock()
	defer b.wg.Done()
	if onBatch != nil {
		onBatch(changes)
	}
}

func (b *LocalChangeBatcher) nextFlushDelayLocked(now time.Time) time.Duration {
	delay := b.window - now.Sub(b.lastAdd)
	if remaining := b.maxWait - now.Sub(b.started); remaining < delay {
		delay = remaining
	}
	return delay
}

func (b *LocalChangeBatcher) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		b.wg.Wait()
		return
	}
	b.closed = true
	if b.timer != nil && b.timer.Stop() {
		b.timer = nil
		b.wg.Done()
	}
	b.pending = nil
	b.mu.Unlock()
	b.wg.Wait()
}

// FileWatcher watches a local directory for changes using OS-level
// notifications. fsnotify uses inotify on Linux and kqueue on macOS. kqueue
// opens a descriptor for every watched file, so production mount loops disable
// this recursive watcher on macOS by default and retain polling reconciliation.
type FileWatcher struct {
	watcher            *fsnotify.Watcher
	localDir           string
	remoteRoot         string
	scopedChild        bool
	onChange           func(relativePath string, op fsnotify.Op)
	maxDirs            int
	watchedDirs        int
	settleDelay        time.Duration
	atomicSettleDelay  time.Duration
	mu                 sync.Mutex
	debounce           map[string]*time.Timer // debounce rapid events per file
	pendingOps         map[string]fsnotify.Op
	debounceVersion    map[string]uint64
	recentRenameDirs   map[string]time.Time
	atomicDestinations map[string]bool
	closed             bool
	healthy            atomic.Bool
	wg                 sync.WaitGroup
}

// FileWatcherTimings controls the two correctness-aware watcher paths. Zero
// values select the production defaults. In-place writes use SettleDelay;
// atomic destination events use AtomicSettleDelay.
type FileWatcherTimings struct {
	SettleDelay       time.Duration
	AtomicSettleDelay time.Duration
}

// recursiveWatcherAllowed reports whether a production Syncer may attach the
// recursive fsnotify watcher. On macOS fsnotify is backed by kqueue, which opens
// one file descriptor for every watched file. A large provider mirror can
// therefore exhaust the process and host file tables even when the number of
// watched directories is capped. Polling reconciliation already scans for
// local writebacks, so fail soft to that bounded path by default.
//
// The opt-in exists for small, controlled mirrors and tests. It is deliberately
// explicit: raising RLIMIT_NOFILE or removing the directory cap is not a safe
// production fix for an unbounded recursive mirror.
func recursiveWatcherAllowed() bool {
	if runtime.GOOS != "darwin" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_FORCE_RECURSIVE_WATCHER"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func NewFileWatcher(localDir string, onChange func(string, fsnotify.Op)) (*FileWatcher, error) {
	return NewFileWatcherForRemoteRoot(localDir, "/", onChange)
}

// NewFileWatcherForRemoteRoot creates a watcher whose basename collision
// guard matches the Syncer's path mapping. A child named after localDir only
// round-trips onto the mount root when the remote root is "/"; beneath a
// non-root mount it is an ordinary descendant.
func NewFileWatcherForRemoteRoot(localDir, remoteRoot string, onChange func(string, fsnotify.Op)) (*FileWatcher, error) {
	return NewFileWatcherForTopology(localDir, remoteRoot, false, onChange)
}

// NewFileWatcherForTopology binds reserved-path handling to the actual local
// topology rather than inferring it from remoteRoot. Exact non-root mounts and
// scoped children can share a remote root while requiring different treatment
// of catalog-only artifacts.
func NewFileWatcherForTopology(localDir, remoteRoot string, scopedChild bool, onChange func(string, fsnotify.Op)) (*FileWatcher, error) {
	return NewFileWatcherForTopologyWithTimings(localDir, remoteRoot, scopedChild, FileWatcherTimings{}, onChange)
}

// NewFileWatcherForTopologyWithTimings is the configurable form used by the
// mount daemon and latency certification harnesses. Existing constructors keep
// the safe defaults above.
func NewFileWatcherForTopologyWithTimings(localDir, remoteRoot string, scopedChild bool, timings FileWatcherTimings, onChange func(string, fsnotify.Op)) (*FileWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	settleDelay := timings.SettleDelay
	if settleDelay <= 0 {
		settleDelay = DefaultFileChangeSettleDelay
	}
	atomicSettleDelay := timings.AtomicSettleDelay
	if atomicSettleDelay <= 0 {
		atomicSettleDelay = DefaultAtomicSaveSettleDelay
	}
	return &FileWatcher{
		watcher:            w,
		localDir:           localDir,
		remoteRoot:         normalizeRemotePath(remoteRoot),
		scopedChild:        scopedChild,
		onChange:           onChange,
		maxDirs:            watcherMaxDirsFromEnv(),
		settleDelay:        settleDelay,
		atomicSettleDelay:  atomicSettleDelay,
		debounce:           make(map[string]*time.Timer),
		pendingOps:         make(map[string]fsnotify.Op),
		debounceVersion:    make(map[string]uint64),
		recentRenameDirs:   make(map[string]time.Time),
		atomicDestinations: make(map[string]bool),
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
	fw.healthy.Store(true)

	// Event loop
	go func() {
		defer fw.healthy.Store(false)
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
						if err := fw.addDirRecursive(event.Name); err != nil {
							return
						}
						fw.emitExistingFileEvents(event.Name)
					}
				}

				// In-place editor writes get a conservative settle window. Atomic
				// destination events are already committed and take the fast path.
				fw.queueChange(rel, event.Op)

			case _, ok := <-fw.watcher.Errors:
				if !ok {
					return
				}
				// Any asynchronous watcher error means event continuity can no
				// longer be guaranteed. Mark this watcher unhealthy by exiting so
				// the mount loop resumes its correctness-first polling reconcile.
				return
			}
		}
	}()

	return nil
}

// Healthy reports whether the watcher event loop is actively preserving a
// contiguous local-change stream. Mount loops must fall back to polling when
// this becomes false, even if the WebSocket remains connected.
func (fw *FileWatcher) Healthy() bool {
	return fw != nil && fw.healthy.Load()
}

func (fw *FileWatcher) shouldSkip(rel string) bool {
	if isMountRuntimeRelativePath(rel) {
		return true
	}
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
	if watcherIgnoredTopLevel(fw.scopedChild, fw.localDir, first) {
		return true
	}
	// Data-loss guard for root mounts: a top-level entry whose name equals the
	// mount directory's own basename is the round-trip-onto-root collision.
	// Non-root mounts map that entry beneath remoteRoot and must retain it.
	if collidesWithMountRootBasename(fw.localDir, fw.remoteRoot, first) {
		return true
	}
	return false
}

// isEphemeralAtomicSaveRelativePath identifies conventional editor/tool
// staging names. The Syncer applies this only while a path is untracked, so a
// deliberately tracked file with one of these names remains writable. This is
// intentionally separate from FileWatcher.shouldSkip: state is required to
// distinguish an atomic-save staging file from legitimate tracked content.
func isEphemeralAtomicSaveRelativePath(relativePath string) bool {
	base := strings.ToLower(filepath.Base(filepath.Clean(relativePath)))
	if base == "" || base == "." {
		return false
	}
	if strings.HasPrefix(base, ".#") ||
		strings.HasPrefix(base, ".goutputstream-") ||
		(strings.HasPrefix(base, ".~lock.") && strings.HasSuffix(base, "#")) {
		return true
	}
	if strings.HasSuffix(base, "___jb_tmp___") ||
		strings.HasSuffix(base, "___jb_old___") ||
		strings.HasSuffix(base, ".swp") ||
		strings.HasSuffix(base, ".swo") ||
		strings.HasSuffix(base, ".swx") ||
		strings.HasSuffix(base, ".tmp") ||
		strings.HasSuffix(base, "~") {
		return true
	}
	for _, marker := range []string{".tmp-", ".writer-tmp-"} {
		if index := strings.LastIndex(base, marker); index > 0 && index+len(marker) < len(base) {
			return true
		}
	}
	return false
}

func collidesWithMountRootBasename(localRoot, remoteRoot, first string) bool {
	return strings.TrimSpace(localRoot) != "" &&
		normalizeRemotePath(remoteRoot) == "/" &&
		first == filepath.Base(filepath.Clean(localRoot))
}

// reservedTopLevel reports whether an entry is bookkeeping at this Syncer's
// topology boundary. Catalog artifacts are reserved for every exact mount;
// under a scoped child the same names are ordinary provider content.
// Mount-runtime sentinels and incidental infrastructure remain reserved at
// every scope so local repository credentials and objects can never become
// sync content.
func reservedTopLevel(scopedChild bool, localRoot, name string) bool {
	if mountscope.IsReservedRuntimeSegment(name) || mountscope.IsInfrastructureTopLevelAt(localRoot, name) {
		return true
	}
	return !scopedChild && mountscope.IsCatalogOwnedTopLevel(name)
}

func (fw *FileWatcher) queueChange(rel string, op fsnotify.Op) {
	fw.mu.Lock()
	if fw.closed {
		fw.mu.Unlock()
		return
	}
	fw.pendingOps[rel] |= op
	now := time.Now()
	for dir, renamedAt := range fw.recentRenameDirs {
		if now.Sub(renamedAt) > atomicRenameCorrelationWindow {
			delete(fw.recentRenameDirs, dir)
		}
	}
	parent := filepath.Dir(filepath.Clean(rel))
	if op&fsnotify.Rename != 0 && isEphemeralAtomicSaveRelativePath(rel) {
		fw.recentRenameDirs[parent] = now
	}
	fw.debounceVersion[rel]++
	version := fw.debounceVersion[rel]
	if t, ok := fw.debounce[rel]; ok {
		if t.Stop() {
			fw.wg.Done()
		}
	}
	delay := fw.settleDelay
	combinedOp := fw.pendingOps[rel]
	atomicDestination := combinedOp&(fsnotify.Rename|fsnotify.Remove) != 0
	if combinedOp&fsnotify.Create != 0 && op&fsnotify.Rename == 0 {
		if renamedAt, ok := fw.recentRenameDirs[parent]; ok && now.Sub(renamedAt) <= atomicRenameCorrelationWindow {
			atomicDestination = true
			fw.atomicDestinations[rel] = true
			delete(fw.recentRenameDirs, parent)
		}
	}
	if fw.atomicDestinations[rel] {
		atomicDestination = true
	}
	if (fw.atomicDestinations[rel] || combinedOp&(fsnotify.Write|fsnotify.Chmod) == 0) &&
		atomicDestination {
		delay = fw.atomicSettleDelay
	}
	fw.wg.Add(1)
	fw.debounce[rel] = time.AfterFunc(delay, func() {
		defer fw.wg.Done()

		fw.mu.Lock()
		if fw.closed {
			if fw.debounceVersion[rel] == version {
				delete(fw.debounce, rel)
				delete(fw.pendingOps, rel)
				delete(fw.debounceVersion, rel)
				delete(fw.atomicDestinations, rel)
			}
			fw.mu.Unlock()
			return
		}
		// A stopped timer may already have entered its callback. Only the
		// newest generation may consume the coalesced operation or delete the
		// replacement timer.
		if fw.debounceVersion[rel] != version {
			fw.mu.Unlock()
			return
		}
		combinedOp := fw.pendingOps[rel]
		delete(fw.debounce, rel)
		delete(fw.pendingOps, rel)
		delete(fw.debounceVersion, rel)
		delete(fw.atomicDestinations, rel)
		fw.mu.Unlock()
		fw.onChange(rel, combinedOp)
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

func watcherIgnoredTopLevel(scopedChild bool, localRoot, name string) bool {
	return reservedTopLevel(scopedChild, localRoot, name) && name != mountscope.DigestsTopLevel
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
		if rel, relErr := filepath.Rel(fw.localDir, path); relErr == nil &&
			rel != "." &&
			isMountRuntimeRelativePath(rel) {
			return filepath.SkipDir
		}
		name := info.Name()
		if fw.isTopLevelReservedDir(path, name) {
			return filepath.SkipDir
		}
		if fw.maxDirs > 0 && fw.watchedDirs >= fw.maxDirs {
			return fmtWatcherLimitExceeded(fw.maxDirs)
		}
		if err := fw.watcher.Add(path); err != nil {
			if isBenignWatcherAddError(err) {
				return nil
			}
			return err
		}
		fw.watchedDirs++
		return nil
	})
}

func watcherMaxDirsFromEnv() int {
	raw := strings.TrimSpace(os.Getenv("RELAYFILE_MOUNT_MAX_WATCH_DIRS"))
	if raw == "" {
		return defaultMaxWatchedDirs
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return defaultMaxWatchedDirs
	}
	if value <= 0 {
		return defaultMaxWatchedDirs
	}
	return value
}

func fmtWatcherLimitExceeded(limit int) error {
	return errors.Join(ErrWatcherLimitExceeded, errors.New("watched directory limit reached: "+strconv.Itoa(limit)))
}

func isBenignWatcherAddError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "already watched") ||
		strings.Contains(message, "already exists") ||
		strings.Contains(message, "file exists")
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
	return watcherIgnoredTopLevel(fw.scopedChild, fw.localDir, name)
}

func (fw *FileWatcher) Close() error {
	fw.healthy.Store(false)
	fw.mu.Lock()
	fw.closed = true
	for rel, timer := range fw.debounce {
		if timer.Stop() {
			fw.wg.Done()
		}
		delete(fw.debounce, rel)
		delete(fw.pendingOps, rel)
		delete(fw.debounceVersion, rel)
		delete(fw.atomicDestinations, rel)
	}
	fw.recentRenameDirs = nil
	fw.atomicDestinations = nil
	fw.mu.Unlock()

	err := fw.watcher.Close()
	fw.wg.Wait()
	return err
}
