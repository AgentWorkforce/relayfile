package mountsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentworkforce/relayfile/internal/digest"
)

// TestSchedulerFiresAtLocalMidnight: the lead plan's gate A boundary case.
// A workspace configured for America/New_York with an event at 23:30 local
// on 2026-05-12 produces a `yesterday.md` whose `date` is 2026-05-12 after
// the scheduler tick fires at 00:05 local on 2026-05-13. UTC clock at that
// instant is 2026-05-13T04:05Z — the test asserts the TZ-local boundary is
// what selects the closed day, not UTC.
func TestSchedulerFiresAtLocalMidnight(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("America/New_York tz not available: %v", err)
	}

	root := t.TempDir()
	events := []digest.ChangeEvent{
		{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 13, 3, 30, 0, 0, time.UTC), // 23:30 EDT on 2026-05-12
			Identifier:    "PR-late",
			Verb:          "updated",
			CanonicalPath: "github/repos/x/pulls/by-id/late.json",
		},
	}

	scheduler := &CloseScheduler{
		MountRoot: root,
		TZ:        ny,
		Providers: []string{"github"},
		Source:    digest.SliceSource{Items: events},
		Now:       func() time.Time { return time.Date(2026, 5, 13, 4, 5, 0, 0, time.UTC) },
	}

	closed, err := scheduler.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(closed) != 1 || closed[0] != "2026-05-12" {
		t.Fatalf("Tick returned %v, want [2026-05-12]", closed)
	}

	body, err := os.ReadFile(filepath.Join(root, "digests", "yesterday.md"))
	if err != nil {
		t.Fatalf("read yesterday.md: %v", err)
	}
	wantFrontmatter := []byte("date: 2026-05-12")
	if !contains(body, wantFrontmatter) {
		t.Fatalf("yesterday.md does not contain %q frontmatter:\n%s", wantFrontmatter, body)
	}
	if !contains(body, []byte("covers: yesterday")) {
		t.Fatalf("yesterday.md missing covers: yesterday:\n%s", body)
	}
	if !contains(body, []byte("PR-late")) {
		t.Fatalf("yesterday.md missing the 23:30-EDT event bullet:\n%s", body)
	}
	dateStamped, err := os.ReadFile(filepath.Join(root, "digests", "2026-05-12.md"))
	if err != nil {
		t.Fatalf("read date-stamped digest: %v", err)
	}
	if !contains(dateStamped, []byte("covers: 2026-05-12")) {
		t.Fatalf("date-stamped digest missing covers date:\n%s", dateStamped)
	}

	// A second tick on the same instant must be a no-op: the marker pins
	// 2026-05-12 and there's no later closed day yet.
	second, err := scheduler.Tick(context.Background())
	if err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second tick should be no-op, got %v", second)
	}
}

// TestSchedulerCatchesUpAcrossOfflineDays: when the daemon was offline for
// several local days, the next tick must close every missing day in
// chronological order. The marker advances to the most recent closed day,
// and `yesterday.md`'s frontmatter reflects that final date.
func TestSchedulerCatchesUpAcrossOfflineDays(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("America/New_York tz not available: %v", err)
	}

	root := t.TempDir()
	// Seed the marker so the scheduler believes the last successful close
	// was 2026-05-09. Three days were missed: 2026-05-10, 2026-05-11,
	// 2026-05-12.
	markerPath := filepath.Join(root, filepath.FromSlash(digest.YesterdayMarkerPath))
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
		t.Fatalf("mkdir marker dir: %v", err)
	}
	if err := os.WriteFile(markerPath, []byte("2026-05-09\n"), 0o644); err != nil {
		t.Fatalf("seed marker: %v", err)
	}

	// One event per missing day so we can verify the chronological close
	// order by reading the eventual yesterday.md (it ends on the final
	// day's content).
	events := []digest.ChangeEvent{
		{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 10, 14, 0, 0, 0, ny),
			Identifier:    "PR-10",
			Verb:          "updated",
			CanonicalPath: "github/repos/x/pulls/by-id/10.json",
		},
		{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 11, 14, 0, 0, 0, ny),
			Identifier:    "PR-11",
			Verb:          "updated",
			CanonicalPath: "github/repos/x/pulls/by-id/11.json",
		},
		{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 12, 14, 0, 0, 0, ny),
			Identifier:    "PR-12",
			Verb:          "updated",
			CanonicalPath: "github/repos/x/pulls/by-id/12.json",
		},
	}

	scheduler := &CloseScheduler{
		MountRoot: root,
		TZ:        ny,
		Providers: []string{"github"},
		// The source intentionally returns all events for every window;
		// the scheduler isn't responsible for partitioning. The test
		// asserts chronological close ordering and the final
		// yesterday.md state, not per-day partitioning (which is a
		// distinct slice).
		Source: digest.SliceSource{Items: events},
		Now:    func() time.Time { return time.Date(2026, 5, 13, 5, 0, 0, 0, time.UTC) }, // 01:00 EDT 2026-05-13
	}

	closed, err := scheduler.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	want := []string{"2026-05-10", "2026-05-11", "2026-05-12"}
	if !equalStrings(closed, want) {
		t.Fatalf("Tick returned %v, want %v (chronological order)", closed, want)
	}

	// Marker now records the most recent closed day.
	markerBytes, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if got := string(markerBytes); got != "2026-05-12\n" {
		t.Fatalf("marker = %q, want 2026-05-12", got)
	}

	body, err := os.ReadFile(filepath.Join(root, "digests", "yesterday.md"))
	if err != nil {
		t.Fatalf("read yesterday.md: %v", err)
	}
	if !contains(body, []byte("date: 2026-05-12")) {
		t.Fatalf("yesterday.md should end on 2026-05-12 after catch-up:\n%s", body)
	}
	for _, date := range want {
		if _, err := os.Stat(filepath.Join(root, "digests", date+".md")); err != nil {
			t.Fatalf("date-stamped catch-up digest %s missing: %v", date, err)
		}
	}

	// A tick immediately after catch-up is a no-op.
	second, err := scheduler.Tick(context.Background())
	if err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second tick should be no-op after catch-up, got %v", second)
	}
}

func TestCoalescer_30s(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	coalescer := &RollingDigestCoalescer{
		Interval: 30 * time.Second,
		Now:      func() time.Time { return now },
	}
	if coalescer.ObserveChange("digests/today.md") {
		t.Fatal("digest writes must not schedule rolling digest regeneration")
	}
	if !coalescer.ObserveChange("github/repos/x/issues/1.json") {
		t.Fatal("provider write should schedule rolling digest regeneration")
	}
	if coalescer.Due() {
		t.Fatal("coalescer should not be due before 30s")
	}
	now = now.Add(29 * time.Second)
	if coalescer.Due() {
		t.Fatal("coalescer should still not be due at 29s")
	}
	now = now.Add(time.Second)
	if !coalescer.Due() {
		t.Fatal("coalescer should be due at 30s")
	}
	coalescer.MarkFlushed()
	if coalescer.Due() {
		t.Fatal("coalescer should not be due after flush")
	}
}

func TestRollingDigestJobsFlushAfterCoalescingWindow(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	markerPath := filepath.Join(root, filepath.FromSlash(digest.YesterdayMarkerPath))
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
		t.Fatalf("mkdir marker dir: %v", err)
	}
	if err := os.WriteFile(markerPath, []byte("2026-05-14\n"), 0o644); err != nil {
		t.Fatalf("seed marker: %v", err)
	}

	source := &countingDigestSource{items: []digest.ChangeEvent{{
		Provider:      "github",
		Timestamp:     now,
		Identifier:    "PR-rolling",
		Verb:          "updated",
		CanonicalPath: "github/repos/x/pulls/by-id/rolling.json",
	}}}
	client := &fakeClient{files: map[string]RemoteFile{}}
	syncer, err := NewSyncer(client, SyncerOptions{
		WorkspaceID:     "ws_rolling",
		LocalRoot:       root,
		DigestSource:    source,
		DigestProviders: []string{"github"},
		DigestTimezone:  "UTC",
		DigestNow:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}

	writeLocal := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		if err := syncer.HandleLocalChange(context.Background(), rel, 0); err != nil {
			t.Fatalf("HandleLocalChange %s: %v", rel, err)
		}
	}

	writeLocal("github/repos/x/pulls/by-id/rolling.json", `{"n":1}`)
	now = now.Add(10 * time.Second)
	writeLocal("github/repos/x/pulls/by-id/rolling-2.json", `{"n":2}`)

	now = now.Add(29 * time.Second)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("pre-due SyncOnce: %v", err)
	}
	for _, rel := range []string{digest.TodayPath, digest.ThisWeekPath} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("%s exists before coalescing interval elapsed (err=%v)", rel, err)
		}
	}

	if syncer.rollingCoalescer.ObserveChange(digest.TodayPath) {
		t.Fatal("digest-path changes must not schedule a rolling flush")
	}

	now = now.Add(time.Second)
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("due SyncOnce: %v", err)
	}
	for _, rel := range []string{digest.TodayPath, digest.ThisWeekPath} {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !contains(body, []byte("PR-rolling")) {
			t.Fatalf("%s missing rolling event:\n%s", rel, body)
		}
	}
	if source.callsByCover[digest.TodayCover] != 1 || source.callsByCover[digest.ThisWeekCover] != 1 {
		t.Fatalf("rolling flush calls = %#v, want one today and one this-week call", source.callsByCover)
	}
	if syncer.rollingCoalescer.Due() {
		t.Fatal("coalescer still due after successful flush")
	}
}

func TestClosingJobs_RunAtLocalMidnight(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("America/New_York tz not available: %v", err)
	}
	root := t.TempDir()
	events := []digest.ChangeEvent{{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 13, 3, 30, 0, 0, time.UTC),
		Identifier:    "PR-syncer",
		Verb:          "updated",
		CanonicalPath: "github/repos/x/pulls/by-id/syncer.json",
	}}
	syncer, err := NewSyncer(&fakeClient{}, SyncerOptions{
		WorkspaceID:     "ws_digest_scheduler",
		LocalRoot:       root,
		DigestSource:    digest.SliceSource{Items: events},
		DigestProviders: []string{"github"},
		DigestTimezone:  ny.String(),
		DigestNow:       func() time.Time { return time.Date(2026, 5, 13, 4, 5, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewSyncer: %v", err)
	}
	if err := syncer.SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	for _, rel := range []string{"digests/yesterday.md", "digests/2026-05-12.md"} {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !contains(body, []byte("PR-syncer")) {
			t.Fatalf("%s missing syncer event:\n%s", rel, body)
		}
	}
}

type countingDigestSource struct {
	items        []digest.ChangeEvent
	callsByCover map[string]int
}

func (s *countingDigestSource) Events(w digest.Window) ([]digest.ChangeEvent, error) {
	if s.callsByCover == nil {
		s.callsByCover = map[string]int{}
	}
	s.callsByCover[w.Cover]++
	return append([]digest.ChangeEvent(nil), s.items...), nil
}

func TestClosingJobs_LastWeekCatchUp(t *testing.T) {
	root := t.TempDir()
	markerPath := filepath.Join(root, filepath.FromSlash(digest.YesterdayMarkerPath))
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
		t.Fatalf("mkdir marker dir: %v", err)
	}
	if err := os.WriteFile(markerPath, []byte("2026-05-09\n"), 0o644); err != nil {
		t.Fatalf("seed marker: %v", err)
	}
	scheduler := &CloseScheduler{
		MountRoot: root,
		TZ:        time.UTC,
		Providers: []string{"github"},
		Source: digest.SliceSource{Items: []digest.ChangeEvent{{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 10, 15, 0, 0, 0, time.UTC),
			Identifier:    "PR-week",
			Verb:          "merged",
			CanonicalPath: "github/repos/x/pulls/by-id/week.json",
		}}},
		Now: func() time.Time { return time.Date(2026, 5, 11, 0, 5, 0, 0, time.UTC) },
	}
	closed, err := scheduler.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if !equalStrings(closed, []string{"2026-05-10"}) {
		t.Fatalf("closed = %v, want [2026-05-10]", closed)
	}
	body, err := os.ReadFile(filepath.Join(root, "digests", "last-week.md"))
	if err != nil {
		t.Fatalf("read last-week.md: %v", err)
	}
	if !contains(body, []byte("covers: last-week")) || !contains(body, []byte("PR-week")) {
		t.Fatalf("last-week.md missing expected content:\n%s", body)
	}
}

func contains(haystack, needle []byte) bool {
	return indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
outer:
	for i := 0; i+len(needle) <= len(haystack); i++ {
		for j := 0; j < len(needle); j++ {
			if haystack[i+j] != needle[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
