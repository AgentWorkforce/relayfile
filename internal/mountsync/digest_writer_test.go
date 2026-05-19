package mountsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/agentworkforce/relayfile/internal/digest"
)

// TestYesterdayCloseEmitsFSEventNoRecursion exercises gate E for the
// yesterday slice: invoking the close-write path through digest.CloseLocalDay
// while a FileWatcher is running must:
//
//  1. emit a Modify/Create filesystem event for digests/yesterday.md so
//     mounted observers see the rotation, and
//  2. classify that path as a digest artifact via digest.IsDigestPath, so
//     the regen pipeline drops it instead of looping on its own output.
func TestYesterdayCloseEmitsFSEventNoRecursion(t *testing.T) {
	root := t.TempDir()
	events, _, _ := startFileWatcher(t, root)

	events_in := []digest.ChangeEvent{
		{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 12, 14, 0, 0, 0, time.UTC),
			Identifier:    "PR-1",
			Verb:          "merged",
			CanonicalPath: "github/repos/x/pulls/by-id/1.json",
		},
	}
	now := time.Date(2026, 5, 13, 0, 5, 0, 0, time.UTC)

	res, err := digest.CloseLocalDay(context.Background(), root, digest.SliceSource{Items: events_in},
		now, []string{"github"}, time.UTC)
	if err != nil {
		t.Fatalf("CloseLocalDay: %v", err)
	}
	if !res.Written {
		t.Fatal("CloseLocalDay reported not written; expected a fresh yesterday.md")
	}

	ev, ok := waitForWatcherEventPath(t, events, "digests/yesterday.md", 2*time.Second)
	if !ok {
		t.Fatal("no watcher event for digests/yesterday.md after close write")
	}
	if ev.op&fsnotify.Create == 0 && ev.op&fsnotify.Write == 0 {
		t.Fatalf("expected create or write op, got %s", ev.op)
	}

	if !digest.IsDigestPath("digests/yesterday.md") {
		t.Fatal("digest.IsDigestPath(\"digests/yesterday.md\") = false; recursion guard would not drop the close write")
	}
}

// TestYesterdayCloseImmutable exercises gate B: after the close write
// completes for a given local day, a subsequent ingest event for a later
// day MUST leave yesterday.md byte-equal. The proof is SHA-256 before /
// after the spurious ingest, with the marker mechanism in
// digest.WriteYesterday short-circuiting the rewrite.
func TestYesterdayCloseImmutable(t *testing.T) {
	root := t.TempDir()
	closedDay := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	generatedAt := time.Date(2026, 5, 13, 0, 5, 0, 0, time.UTC)

	closedEvents := []digest.ChangeEvent{
		{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 12, 14, 0, 0, 0, time.UTC),
			Identifier:    "PR-1",
			Verb:          "merged",
			CanonicalPath: "github/repos/x/pulls/by-id/1.json",
		},
	}

	first, err := digest.CloseLocalDayFor(context.Background(), root, digest.SliceSource{Items: closedEvents},
		closedDay, generatedAt, []string{"github"}, time.UTC)
	if err != nil {
		t.Fatalf("CloseLocalDayFor first: %v", err)
	}
	if !first.Written {
		t.Fatal("first CloseLocalDayFor did not report Written=true")
	}

	yesterdayPath := filepath.Join(root, "digests", "yesterday.md")
	beforeBytes, err := os.ReadFile(yesterdayPath)
	if err != nil {
		t.Fatalf("read yesterday.md: %v", err)
	}
	beforeSum := sha256.Sum256(beforeBytes)
	beforeHex := hex.EncodeToString(beforeSum[:])

	// Now a later-day event arrives. A naive re-run with the same closed
	// day window and the *new* event set must not mutate yesterday.md.
	laterEvents := append([]digest.ChangeEvent(nil), closedEvents...)
	laterEvents = append(laterEvents, digest.ChangeEvent{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 13, 15, 0, 0, 0, time.UTC),
		Identifier:    "PR-2",
		Verb:          "updated",
		CanonicalPath: "github/repos/x/pulls/by-id/2.json",
	})

	second, err := digest.CloseLocalDayFor(context.Background(), root, digest.SliceSource{Items: laterEvents},
		closedDay, generatedAt.Add(time.Hour), []string{"github"}, time.UTC)
	if err != nil {
		t.Fatalf("CloseLocalDayFor second: %v", err)
	}
	if second.Written {
		t.Fatal("second close mutated yesterday.md (Written=true) — marker did not gate it")
	}
	if !second.Skipped {
		t.Fatal("second close should report Skipped=true after marker short-circuit")
	}

	afterBytes, err := os.ReadFile(yesterdayPath)
	if err != nil {
		t.Fatalf("re-read yesterday.md: %v", err)
	}
	afterSum := sha256.Sum256(afterBytes)
	afterHex := hex.EncodeToString(afterSum[:])

	if beforeHex != afterHex {
		t.Fatalf("yesterday.md SHA-256 changed after spurious ingest: before=%s after=%s", beforeHex, afterHex)
	}
}
