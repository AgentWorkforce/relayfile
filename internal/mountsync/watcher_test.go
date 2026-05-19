package mountsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/digest"
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

// TestWatcherEmitsYesterdayDigestFileNoRecursion covers the lead-plan F7
// requirement for the yesterday slice: a write to `digests/yesterday.md`
// must produce a filesystem event for mounted observers (gate E) AND the
// shared digest-source classifier `digest.IsDigestPath` must flag the path
// so the regen pipeline's recursion guard drops it. This pairs the
// existing `TestDateStampedDigestDoesNotReEnterRegeneration` (date-stamped
// sibling) with the same proof for the rolling-yesterday artifact.
func TestWatcherEmitsYesterdayDigestFileNoRecursion(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	digestFile := filepath.Join(localDir, "digests", "yesterday.md")
	if err := os.MkdirAll(filepath.Dir(digestFile), 0o755); err != nil {
		t.Fatalf("create digest dir: %v", err)
	}
	if err := os.WriteFile(digestFile, []byte("# digest yesterday\n"), 0o644); err != nil {
		t.Fatalf("write digest file: %v", err)
	}

	ev, ok := waitForWatcherEventPath(t, events, "digests/yesterday.md", time.Second)
	if !ok {
		t.Fatalf("no watcher event for digests/yesterday.md within timeout")
	}
	if ev.op&fsnotify.Create == 0 && ev.op&fsnotify.Write == 0 {
		t.Fatalf("expected create or write op, got %s", ev.op)
	}

	if !digest.IsDigestPath("digests/yesterday.md") {
		t.Fatal("digest.IsDigestPath(\"digests/yesterday.md\") = false; recursion-guard predicate must cover yesterday.md")
	}

	// Sanity: a non-digest write next to it still classifies as a regen
	// trigger, so the predicate isn't accidentally over-broad.
	if digest.IsDigestPath("notion/pages/by-title/foo.json") {
		t.Fatal("digest.IsDigestPath classified a non-digest path as a digest artifact")
	}
}

// TestCloseLocalDayInternalMarkerDoesNotReEnterRegeneration exercises the
// actual yesterday close-write path, not just a synthetic write to
// digests/yesterday.md. The public artifact must be visible to watchers and
// classified by the digest recursion guard; internal marker/temp files must
// stay under .relay so they are skipped before they can become source events.
func TestCloseLocalDayInternalMarkerDoesNotReEnterRegeneration(t *testing.T) {
	localDir := t.TempDir()
	if strings.HasPrefix(filepath.ToSlash(digest.YesterdayMarkerPath), "digests/") {
		t.Fatalf("YesterdayMarkerPath = %q, want watcher-skipped .relay path, not digests/.state", digest.YesterdayMarkerPath)
	}
	legacyMarker := "digests/.state/yesterday.lock"
	if digest.YesterdayMarkerPath == legacyMarker {
		t.Fatalf("YesterdayMarkerPath regressed to legacy watched path %q", legacyMarker)
	}
	if err := os.MkdirAll(filepath.Join(localDir, "digests"), 0o755); err != nil {
		t.Fatalf("create digest dir: %v", err)
	}
	events, _, _ := startFileWatcher(t, localDir)

	now := time.Date(2026, 5, 13, 0, 5, 0, 0, time.UTC)
	_, err := digest.CloseLocalDay(
		context.Background(),
		localDir,
		digest.SliceSource{Items: []digest.ChangeEvent{{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
			Identifier:    "PR-1",
			Verb:          "updated",
			CanonicalPath: "github/repos/x/pulls/by-id/1.json",
		}}},
		now,
		[]string{"github"},
		time.UTC,
	)
	if err != nil {
		t.Fatalf("CloseLocalDay: %v", err)
	}
	if _, err := os.Stat(filepath.Join(localDir, filepath.FromSlash(digest.YesterdayMarkerPath))); err != nil {
		t.Fatalf("marker was not written under %s: %v", digest.YesterdayMarkerPath, err)
	}

	observed := map[string]fsnotify.Op{}
	deadline := time.After(2 * time.Second)
collect:
	for {
		select {
		case ev := <-events:
			observed[ev.path] = observed[ev.path] | ev.op
		case <-deadline:
			break collect
		}
	}

	if _, ok := observed["digests/yesterday.md"]; !ok {
		t.Fatalf("expected watcher event for digests/yesterday.md, observed=%v", observed)
	}
	for path := range observed {
		switch path {
		case digest.YesterdayMarkerPath:
			t.Fatalf("internal marker %q was emitted by watcher; it must stay under skipped .relay state", path)
		case legacyMarker:
			t.Fatalf("legacy marker %q was emitted by watcher; this path would bypass the digest recursion guard", path)
		}
		if strings.HasPrefix(path, "digests/") && !digest.IsDigestPath(path) {
			t.Fatalf("watched digest-tree path %q is not classified by digest.IsDigestPath; regeneration would re-enter", path)
		}
	}
}

// TestWatcherEmitsLastWeekDigestFileNoRecursion covers acceptance check #5
// for the `update-last-week` slice: a write to `digests/last-week.md` must
// produce a normal filesystem event (so mount observers + hosted listeners
// see the new file) AND the shared digest-source classifier
// `digest.IsDigestPath` must flag the path so the regen pipeline's
// recursion guard drops it, per
// `.claude/rules/relayfile-integration-digests.md`. Mirrors the
// yesterday-slice sibling above.
func TestWatcherEmitsLastWeekDigestFileNoRecursion(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	digestFile := filepath.Join(localDir, "digests", "last-week.md")
	if err := os.MkdirAll(filepath.Dir(digestFile), 0o755); err != nil {
		t.Fatalf("create digest dir: %v", err)
	}
	if err := os.WriteFile(digestFile, []byte("# digest last-week\n"), 0o644); err != nil {
		t.Fatalf("write digest file: %v", err)
	}

	ev, ok := waitForWatcherEventPath(t, events, "digests/last-week.md", time.Second)
	if !ok {
		t.Fatalf("no watcher event for digests/last-week.md within timeout")
	}
	if ev.op&fsnotify.Create == 0 && ev.op&fsnotify.Write == 0 {
		t.Fatalf("expected create or write op, got %s", ev.op)
	}

	if !digest.IsDigestPath("digests/last-week.md") {
		t.Fatal("digest.IsDigestPath(\"digests/last-week.md\") = false; recursion-guard predicate must cover last-week.md")
	}
	// Near-miss spellings must NOT be classified as digest paths — a
	// regression here would silently drop legitimate provider writes from
	// regeneration.
	for _, near := range []string{
		"digests/last-weak.md",
		"digests/last-week.txt",
		"digests/last_week.md",
	} {
		if digest.IsDigestPath(near) {
			t.Fatalf("digest.IsDigestPath(%q) = true; near-miss must not be treated as a digest artifact", near)
		}
	}
}

// TestWriteLastWeekDoesNotExposeWatchedTempFiles exercises the real
// temp+rename writer. The final digest must be emitted for mounted observers,
// but any temp files must stay under watcher-skipped internal state so the
// rolling coalescer cannot schedule regeneration from last-week's own write.
func TestWriteLastWeekDoesNotExposeWatchedTempFiles(t *testing.T) {
	localDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localDir, "digests"), 0o755); err != nil {
		t.Fatalf("create digest dir: %v", err)
	}
	events, _, _ := startFileWatcher(t, localDir)

	window := digest.LastWeekWindow(
		time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 11, 0, 5, 0, 0, time.UTC),
		[]string{"github"},
		time.UTC,
	)
	result, err := digest.WriteLastWeek(
		context.Background(),
		localDir,
		digest.SliceSource{Items: []digest.ChangeEvent{{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC),
			Identifier:    "PR-last-week",
			Verb:          "merged",
			CanonicalPath: "github/repos/x/pulls/by-id/last-week.json",
		}}},
		window,
	)
	if err != nil {
		t.Fatalf("WriteLastWeek: %v", err)
	}
	if !result.Written || result.Path != digest.LastWeekPath {
		t.Fatalf("WriteLastWeek result = %#v, want written %s", result, digest.LastWeekPath)
	}

	observed := map[string]fsnotify.Op{}
	deadline := time.After(2 * time.Second)
collect:
	for {
		select {
		case ev := <-events:
			observed[ev.path] = observed[ev.path] | ev.op
		case <-deadline:
			break collect
		}
	}

	if _, ok := observed[digest.LastWeekPath]; !ok {
		t.Fatalf("expected watcher event for %s, observed=%v", digest.LastWeekPath, observed)
	}
	coalescer := &RollingDigestCoalescer{
		Interval: 30 * time.Second,
		Now:      func() time.Time { return time.Date(2026, 5, 11, 0, 5, 0, 0, time.UTC) },
	}
	for path := range observed {
		if strings.HasPrefix(path, ".relay/") {
			t.Fatalf("internal digest temp path %q was emitted by watcher", path)
		}
		if strings.HasPrefix(path, "digests/") {
			if !digest.IsDigestPath(path) {
				t.Fatalf("watched digest-tree path %q is not classified by digest.IsDigestPath; regeneration would re-enter", path)
			}
			if coalescer.ObserveChange(path) {
				t.Fatalf("rolling coalescer scheduled a flush from digest writer path %q", path)
			}
		}
	}
}

func TestWatcherEmitsDateStampedDigestFiles(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	digestFile := filepath.Join(localDir, "digests", "2026-05-12.md")
	if err := os.MkdirAll(filepath.Dir(digestFile), 0o755); err != nil {
		t.Fatalf("create digest dir: %v", err)
	}
	if err := os.WriteFile(digestFile, []byte("# digest\n"), 0o644); err != nil {
		t.Fatalf("write digest file: %v", err)
	}

	ev, ok := waitForWatcherEventPath(t, events, "digests/2026-05-12.md", time.Second)
	if !ok {
		t.Fatalf("no watcher event for date-stamped digest within timeout")
	}
	if ev.op&fsnotify.Create == 0 && ev.op&fsnotify.Write == 0 {
		t.Fatalf("expected create or write op, got %s", ev.op)
	}
}

// TestDateStampedDigestDoesNotReEnterRegeneration pins the parent-spec
// no-recursion contract called out in
// `.claude/rules/relayfile-integration-digests.md` and CLAUDE.md's Digest
// Runtime Contract: a write to `/digests/<YYYY-MM-DD>.md` must produce a
// normal filesystem event (so mounts see the new file) but must NOT be
// re-ingested as a digest-source event — otherwise digest regeneration
// would loop on its own output. The digest-source classifier is
// `digest.IsDigestPath`. This test exercises both halves: the watcher
// emits an event for the digest path, the classifier flags that path so
// the regen pipeline drops it, and a sibling non-digest write under the
// same workspace still classifies as a regen-trigger.
func TestDateStampedDigestDoesNotReEnterRegeneration(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	digestFile := filepath.Join(localDir, "digests", "2026-05-12.md")
	if err := os.MkdirAll(filepath.Dir(digestFile), 0o755); err != nil {
		t.Fatalf("create digest dir: %v", err)
	}
	if err := os.WriteFile(digestFile, []byte("# digest\n"), 0o644); err != nil {
		t.Fatalf("write digest file: %v", err)
	}

	otherFile := filepath.Join(localDir, "notion", "pages", "demo.json")
	if err := os.MkdirAll(filepath.Dir(otherFile), 0o755); err != nil {
		t.Fatalf("create notion dir: %v", err)
	}
	if err := os.WriteFile(otherFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write non-digest file: %v", err)
	}

	// Drain events for up to a second, recording each observed path. Both
	// the digest and non-digest writes must produce watcher events.
	observed := map[string]fsnotify.Op{}
	deadline := time.After(2 * time.Second)
collect:
	for {
		select {
		case ev := <-events:
			observed[ev.path] = observed[ev.path] | ev.op
			if _, sawDigest := observed["digests/2026-05-12.md"]; sawDigest {
				if _, sawOther := observed["notion/pages/demo.json"]; sawOther {
					break collect
				}
			}
		case <-deadline:
			break collect
		}
	}

	if _, ok := observed["digests/2026-05-12.md"]; !ok {
		t.Fatalf("expected watcher event for digests/2026-05-12.md, observed=%v", observed)
	}
	if _, ok := observed["notion/pages/demo.json"]; !ok {
		t.Fatalf("expected watcher event for non-digest path, observed=%v", observed)
	}

	// The digest-source classifier (digest.IsDigestPath) is the gate the
	// regeneration pipeline uses to drop digest writes. Exercise it with
	// the exact paths the watcher just produced. If a future refactor
	// removes or weakens this classifier, this assertion catches the tight
	// write→ingest→write loop that would result.
	for path := range observed {
		isDigest := digest.IsDigestPath(path)
		switch path {
		case "digests/2026-05-12.md":
			if !isDigest {
				t.Fatalf("digest.IsDigestPath(%q) = false; regeneration would re-enter on the date-stamped digest", path)
			}
		case "notion/pages/demo.json":
			if isDigest {
				t.Fatalf("digest.IsDigestPath(%q) = true; non-digest writes would be silently dropped", path)
			}
		}
	}
}

// TestThisWeekDigestDoesNotReEnterRegeneration mirrors the date-stamped
// digest no-recursion test for the rolling weekly artifact
// `/digests/this-week.md`. The watcher must emit a normal fs event for the
// rewrite so mount observers see the change, and `digest.IsDigestPath` must
// classify the path as a digest so the regen pipeline drops it instead of
// looping on its own output.
func TestThisWeekDigestDoesNotReEnterRegeneration(t *testing.T) {
	localDir := t.TempDir()
	events, _, _ := startFileWatcher(t, localDir)

	digestFile := filepath.Join(localDir, "digests", "this-week.md")
	if err := os.MkdirAll(filepath.Dir(digestFile), 0o755); err != nil {
		t.Fatalf("create digest dir: %v", err)
	}
	if err := os.WriteFile(digestFile, []byte("# this-week\n"), 0o644); err != nil {
		t.Fatalf("write digest file: %v", err)
	}

	otherFile := filepath.Join(localDir, "linear", "issues", "demo.json")
	if err := os.MkdirAll(filepath.Dir(otherFile), 0o755); err != nil {
		t.Fatalf("create linear dir: %v", err)
	}
	if err := os.WriteFile(otherFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write non-digest file: %v", err)
	}

	observed := map[string]fsnotify.Op{}
	deadline := time.After(2 * time.Second)
collect:
	for {
		select {
		case ev := <-events:
			observed[ev.path] = observed[ev.path] | ev.op
			if _, sawDigest := observed["digests/this-week.md"]; sawDigest {
				if _, sawOther := observed["linear/issues/demo.json"]; sawOther {
					break collect
				}
			}
		case <-deadline:
			break collect
		}
	}

	if _, ok := observed["digests/this-week.md"]; !ok {
		t.Fatalf("expected watcher event for digests/this-week.md, observed=%v", observed)
	}
	if _, ok := observed["linear/issues/demo.json"]; !ok {
		t.Fatalf("expected watcher event for non-digest path, observed=%v", observed)
	}
	for path := range observed {
		isDigest := digest.IsDigestPath(path)
		switch path {
		case "digests/this-week.md":
			if !isDigest {
				t.Fatalf("digest.IsDigestPath(%q) = false; regeneration would re-enter on the rolling weekly digest", path)
			}
		case "linear/issues/demo.json":
			if isDigest {
				t.Fatalf("digest.IsDigestPath(%q) = true; non-digest writes would be silently dropped", path)
			}
		}
	}
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
