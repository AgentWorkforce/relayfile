package mountsync

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/digest"
)

// TestDigestPathFilterIsSingleSourced pins the contract that the mountsync
// package treats `digest.IsDigestPath` as the canonical guard for skipping
// digest source-events. The integration rule
// `.claude/rules/relayfile-integration-digests.md` requires the digest
// scheduler to ignore `/digests/*` events so digest writes do not recurse.
// Today, relayfile mountsync does not run a digest scheduler (cloud does);
// this test pins the helper's behavior at the mountsync test surface so
// when the local scheduler lands, it gates on this single source of truth
// rather than re-implementing prefix/suffix checks.
func TestDigestPathFilterIsSingleSourced(t *testing.T) {
	digestPaths := []string{
		"digests/today.md",
		"/digests/today.md",
		"digests/yesterday.md",
		"/digests/yesterday.md",
		"digests/2026-05-12.md",
		"/digests/2026-05-12.md",
	}
	for _, p := range digestPaths {
		if !digest.IsDigestPath(p) {
			t.Fatalf("digest.IsDigestPath(%q) = false, want true (mountsync source-event filter must skip this)", p)
		}
	}
	nonDigestPaths := []string{
		"github/repos/foo/issues/by-id/1.json",
		"notion/pages/by-edited/2026-05-12/page.json",
		".relay/dead-letter/payload.json",
		"digests/2026-02-30.md",   // invalid date stem
		"digests/2026-05-12.json", // wrong extension
		"notion/pages/2026-05-12.md",
	}
	for _, p := range nonDigestPaths {
		if digest.IsDigestPath(p) {
			t.Fatalf("digest.IsDigestPath(%q) = true, want false (mountsync must NOT treat as a digest path)", p)
		}
	}
}

// TestTodayWriteDoesNotRecurse pins the anti-recursion invariant for
// /digests/today.md as a synthetic file event: any future digest scheduler
// in mountsync that consumes filesystem events MUST exclude paths matched by
// digest.IsDigestPath before scheduling another render. The test asserts
// the gating helper rejects today.md (and its date-stamped sibling) so the
// scheduler can be wired with confidence that recursion is impossible at
// the source-event boundary.
func TestTodayWriteDoesNotRecurse(t *testing.T) {
	for _, p := range []string{"digests/today.md", "digests/yesterday.md", "digests/2026-05-15.md"} {
		if !digest.IsDigestPath(p) {
			t.Fatalf("anti-recursion gate broken: %q must match digest.IsDigestPath so the scheduler skips it", p)
		}
	}
}

func TestDigestPathsExcludedFromSourcesButEmitNormalEvents(t *testing.T) {
	root := t.TempDir()
	events, _, _ := startFileWatcher(t, root)

	for _, rel := range []string{
		"digests/today.md",
		"digests/2026-05-12.md",
		"digests/this-week.md",
		"digests/last-week.md",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, filepath.FromSlash(rel))), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte("# digest\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		ev, ok := waitForWatcherEventPath(t, events, rel, 2*time.Second)
		if !ok {
			t.Fatalf("no normal watcher event for %s", rel)
		}
		if ev.path != rel {
			t.Fatalf("watcher normalized path = %q, want %q", ev.path, rel)
		}
		if !digest.IsDigestPath(ev.path) {
			t.Fatalf("%s emitted a normal event but would not be excluded from digest sources", ev.path)
		}
	}
}
