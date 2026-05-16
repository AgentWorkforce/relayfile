package digest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRenderYesterdayFrontmatter covers F1: RenderYesterday must stamp
// Meta.Date to the closed local day and Meta.Covers to the literal string
// "yesterday", and produce bullets byte-equal (modulo frontmatter) to what
// Run produces for the same event set with Cover: "today".
func TestRenderYesterdayFrontmatter(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	closedDay := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	generatedAt := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	providers := []string{"github", "linear", "notion"}

	w := YesterdayWindow(closedDay, generatedAt, providers, time.UTC)
	path, content, rep, err := RenderYesterday(context.Background(), SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("RenderYesterday: %v", err)
	}
	if path != "digests/yesterday.md" {
		t.Fatalf("path = %q, want digests/yesterday.md", path)
	}
	if rep.Meta.Date != "2026-05-12" {
		t.Fatalf("meta.date = %q, want 2026-05-12", rep.Meta.Date)
	}
	if rep.Meta.Covers != "yesterday" {
		t.Fatalf("meta.covers = %q, want yesterday", rep.Meta.Covers)
	}

	// Acceptance check #2 byte-equality modulo frontmatter: produce the
	// last-today report for the same event set and compare bullet bodies.
	todayWin := Window{
		Date:        closedDay,
		Cover:       "today",
		GeneratedAt: generatedAt,
		Providers:   providers,
		TZ:          time.UTC,
	}
	todayRep, err := Run(context.Background(), SliceSource{Items: events}, todayWin)
	if err != nil {
		t.Fatalf("Run today: %v", err)
	}
	if len(todayRep.Sections) != len(rep.Sections) {
		t.Fatalf("section count drift: today=%d yesterday=%d", len(todayRep.Sections), len(rep.Sections))
	}
	for i := range rep.Sections {
		if rep.Sections[i].Provider != todayRep.Sections[i].Provider {
			t.Fatalf("section %d provider drift: today=%s yesterday=%s",
				i, todayRep.Sections[i].Provider, rep.Sections[i].Provider)
		}
		if len(rep.Sections[i].Bullets) != len(todayRep.Sections[i].Bullets) {
			t.Fatalf("section %d bullets drift", i)
		}
		for j := range rep.Sections[i].Bullets {
			if rep.Sections[i].Bullets[j] != todayRep.Sections[i].Bullets[j] {
				t.Fatalf("section %d bullet %d drift: today=%+v yesterday=%+v",
					i, j, todayRep.Sections[i].Bullets[j], rep.Sections[i].Bullets[j])
			}
		}
	}

	// Content must match the existing yesterday fixture used by TestGoldenYesterday.
	want, err := os.ReadFile("testdata/yesterday-fixture.md")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !bytes.Equal(content, want) {
		t.Fatalf("RenderYesterday output drifted from golden fixture\n--- got ---\n%s\n--- want ---\n%s", content, want)
	}
}

func TestYesterdayFiltersClosedLocalDay(t *testing.T) {
	events := []ChangeEvent{
		{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 11, 23, 59, 59, 0, time.UTC),
			Identifier:    "before",
			Verb:          "updated",
			CanonicalPath: "github/before.json",
		},
		{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
			Identifier:    "inside",
			Verb:          "updated",
			CanonicalPath: "github/inside.json",
		},
		{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
			Identifier:    "after",
			Verb:          "updated",
			CanonicalPath: "github/after.json",
		},
	}

	_, body, rep, err := RenderYesterday(context.Background(), SliceSource{Items: events}, YesterdayWindow(
		time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 13, 0, 5, 0, 0, time.UTC),
		[]string{"github"},
		time.UTC,
	))
	if err != nil {
		t.Fatalf("RenderYesterday: %v", err)
	}
	if rep.Meta.Events != 1 {
		t.Fatalf("events = %d, want 1", rep.Meta.Events)
	}
	if !bytes.Contains(body, []byte("inside")) || bytes.Contains(body, []byte("before")) || bytes.Contains(body, []byte("after")) {
		t.Fatalf("yesterday digest did not filter to the closed day:\n%s", body)
	}
}

// TestWriteYesterdayIdempotentSameDay covers F2 / acceptance check #3:
// repeat invocations with the same event set leave the on-disk file byte-
// equal AND preserve mtime (the equal-bytes short-circuit path).
func TestWriteYesterdayIdempotentSameDay(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	root := t.TempDir()
	w := YesterdayWindow(
		time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		[]string{"github", "linear", "notion"},
		time.UTC,
	)

	first, err := WriteYesterday(context.Background(), root, SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("WriteYesterday first: %v", err)
	}
	if !first.Written {
		t.Fatal("first write reported not written")
	}
	localPath := filepath.Join(root, filepath.FromSlash(first.Path))
	firstBytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read first digest: %v", err)
	}
	firstInfo, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat first digest: %v", err)
	}

	// Backdate the file so mtime equality is a meaningful assertion: if
	// the writer rewrites it, the new mtime will be "now" and != backdated.
	backdated := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(localPath, backdated, backdated); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	firstInfo, err = os.Stat(localPath)
	if err != nil {
		t.Fatalf("re-stat first digest: %v", err)
	}

	second, err := WriteYesterday(context.Background(), root, SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("WriteYesterday second: %v", err)
	}
	if second.Written {
		t.Fatal("second write rewrote byte-equal yesterday digest (mtime would drift)")
	}
	if !second.Skipped {
		t.Fatal("second write should report Skipped=true on byte-equal short-circuit")
	}
	secondBytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read second digest: %v", err)
	}
	if !bytes.Equal(secondBytes, firstBytes) {
		t.Fatalf("yesterday digest changed on no-op rerun")
	}
	secondInfo, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat second digest: %v", err)
	}
	if !secondInfo.ModTime().Equal(firstInfo.ModTime()) {
		t.Fatalf("mtime drifted on no-op rerun: first=%s second=%s",
			firstInfo.ModTime(), secondInfo.ModTime())
	}
}

// TestWriteYesterdayRotatesOnNewDay covers F2 / acceptance check #2:
// when the window advances to a new local day, the writer replaces the file
// atomically and the new body covers the new closed day.
func TestWriteYesterdayRotatesOnNewDay(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	root := t.TempDir()

	first, err := WriteYesterday(context.Background(), root, SliceSource{Items: events},
		YesterdayWindow(
			time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
			[]string{"github", "linear", "notion"},
			time.UTC,
		),
	)
	if err != nil {
		t.Fatalf("WriteYesterday day1: %v", err)
	}
	if !first.Written {
		t.Fatal("day1 write reported not written")
	}

	// Day rolls forward: no events on 2026-05-13 in the fixture, so the
	// rotated yesterday body should be empty (zero events across all
	// configured providers) and the frontmatter date should be 2026-05-13.
	second, err := WriteYesterday(context.Background(), root, SliceSource{Items: nil},
		YesterdayWindow(
			time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC),
			[]string{"github", "linear", "notion"},
			time.UTC,
		),
	)
	if err != nil {
		t.Fatalf("WriteYesterday day2: %v", err)
	}
	if !second.Written {
		t.Fatal("day2 write should have rotated, but Written=false")
	}
	if second.Report.Meta.Date != "2026-05-13" {
		t.Fatalf("rotated date = %q, want 2026-05-13", second.Report.Meta.Date)
	}
	if second.Report.Meta.Events != 0 {
		t.Fatalf("rotated events = %d, want 0", second.Report.Meta.Events)
	}

	localPath := filepath.Join(root, filepath.FromSlash(second.Path))
	body, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read rotated digest: %v", err)
	}
	if !bytes.Contains(body, []byte("date: 2026-05-13")) {
		t.Fatalf("rotated file does not contain new date frontmatter:\n%s", body)
	}
	if !bytes.Contains(body, []byte("covers: yesterday")) {
		t.Fatalf("rotated file lost covers: yesterday:\n%s", body)
	}
}

// TestWriteYesterdayImmutableAgainstLaterDayEvents covers acceptance check
// #3: ingesting events from a later local day and re-running the digest
// pipeline with the SAME closed-day window leaves yesterday.md untouched.
func TestWriteYesterdayImmutableAgainstLaterDayEvents(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	root := t.TempDir()
	w := YesterdayWindow(
		time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		[]string{"github", "linear", "notion"},
		time.UTC,
	)

	first, err := WriteYesterday(context.Background(), root, SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("WriteYesterday first: %v", err)
	}
	localPath := filepath.Join(root, filepath.FromSlash(first.Path))
	firstBytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}

	// Add later-day events — yesterday must not pick them up because the
	// closed-day window's date is still 2026-05-12. Per Run's contract,
	// events with timestamps outside the day still flow through the source
	// (Run does not filter by timestamp), but yesterday's *Meta.Date* and
	// the closed-day immutability contract say once it's written for a
	// given closed day, re-running with the same window+events-of-that-day
	// must be a no-op.
	laterEvents := append([]ChangeEvent(nil), events...)
	laterEvents = append(laterEvents, ChangeEvent{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC),
		Identifier:    "PR-92",
		Verb:          "updated",
		CanonicalPath: "github/repos/AgentWorkforce__workforce/pulls/by-id/92.json",
	})

	// Re-run the closing-day digest with a source that now ALSO contains a
	// later-day (2026-05-13) event. Immutability is pinned by the per-day
	// marker, not by the caller pre-filtering the source: once yesterday.md
	// is closed for 2026-05-12, a re-close for that date must skip the write
	// even if the source is contaminated with out-of-window events.
	second, err := WriteYesterday(context.Background(), root, SliceSource{Items: laterEvents}, w)
	if err != nil {
		t.Fatalf("WriteYesterday second: %v", err)
	}
	if second.Written {
		t.Fatal("rerun with same closed-day set should not rewrite yesterday.md")
	}
	if !second.Skipped {
		t.Fatal("rerun should report Skipped=true")
	}
	secondBytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("yesterday.md mutated by later-day events")
	}
}

// TestCloseLocalDayWritesYesterdayOncePerRollover covers F3: driving the
// fake clock across local midnight in a non-UTC zone invokes CloseLocalDay
// exactly once per rollover and the produced body is byte-equivalent to
// the Run output for the closed local day.
func TestCloseLocalDayWritesYesterdayOncePerRollover(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("America/Los_Angeles tz not available: %v", err)
	}
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	root := t.TempDir()

	// First midnight crossing: now is 2026-05-13 00:05 PT; closed local
	// day is 2026-05-12.
	midnightCrossing := time.Date(2026, 5, 13, 0, 5, 0, 0, la)
	res, err := CloseLocalDay(context.Background(), root, SliceSource{Items: events},
		midnightCrossing, []string{"github", "linear", "notion"}, la)
	if err != nil {
		t.Fatalf("CloseLocalDay: %v", err)
	}
	if !res.Written {
		t.Fatal("first close did not write")
	}
	if res.Report.Meta.Date != "2026-05-12" {
		t.Fatalf("closed-day date = %q, want 2026-05-12", res.Report.Meta.Date)
	}
	if res.Report.Meta.Covers != "yesterday" {
		t.Fatalf("covers = %q, want yesterday", res.Report.Meta.Covers)
	}

	// Second invocation on the same local day: no-op (Skipped).
	res2, err := CloseLocalDay(context.Background(), root, SliceSource{Items: events},
		midnightCrossing.Add(30*time.Minute),
		[]string{"github", "linear", "notion"}, la)
	if err != nil {
		t.Fatalf("CloseLocalDay rerun: %v", err)
	}
	if res2.Written {
		t.Fatal("second close on same closed-day rewrote yesterday")
	}
	if !res2.Skipped {
		t.Fatal("second close should report Skipped=true")
	}

	// Read yesterday.md and assert it is byte-equal to RenderYesterday for
	// the same closed day and event set (acceptance check #2).
	localPath := filepath.Join(root, "digests", "yesterday.md")
	onDisk, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read yesterday.md: %v", err)
	}
	closedDay := time.Date(2026, 5, 12, 0, 0, 0, 0, la)
	_, rendered, _, err := RenderYesterday(context.Background(), SliceSource{Items: events},
		YesterdayWindow(closedDay, midnightCrossing, []string{"github", "linear", "notion"}, la))
	if err != nil {
		t.Fatalf("RenderYesterday: %v", err)
	}
	if !bytes.Equal(onDisk, rendered) {
		t.Fatalf("on-disk yesterday != RenderYesterday output\n--- disk ---\n%s\n--- rendered ---\n%s",
			onDisk, rendered)
	}
}

// TestYesterdayWindowUsesNonUTCZone covers F6 / acceptance check #4:
// when a non-UTC zone is configured, the window date is resolved in that
// zone. With now = 2026-05-13 00:05 PT, the closed local day is
// 2026-05-12 (PT), not 2026-05-13 (UTC).
func TestYesterdayWindowUsesNonUTCZone(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("America/Los_Angeles tz not available: %v", err)
	}
	// Choose an instant where UTC and PT disagree on which calendar day
	// "yesterday" refers to: 2026-05-12 18:00 PT = 2026-05-13 01:00 UTC.
	// In PT, the previous local day is 2026-05-11; in UTC, the previous
	// calendar day is 2026-05-12. Same wall-clock instant, different zone,
	// different yesterday — proving the zone is what anchors the boundary.
	now := time.Date(2026, 5, 12, 18, 0, 0, 0, la)
	closedDayPT := now.In(la).AddDate(0, 0, -1)
	w := YesterdayWindow(closedDayPT, now, []string{"github"}, la)
	if w.TZ != la {
		t.Fatalf("window TZ not preserved")
	}
	rep, err := Run(context.Background(), SliceSource{Items: nil}, w)
	if err != nil {
		t.Fatalf("Run PT: %v", err)
	}
	if rep.Meta.Date != "2026-05-11" {
		t.Fatalf("non-UTC date resolution: got %q, want 2026-05-11 (PT yesterday for 2026-05-12 18:00 PT)", rep.Meta.Date)
	}

	closedDayUTC := now.In(time.UTC).AddDate(0, 0, -1)
	utcWin := YesterdayWindow(closedDayUTC, now, []string{"github"}, time.UTC)
	utcRep, err := Run(context.Background(), SliceSource{Items: nil}, utcWin)
	if err != nil {
		t.Fatalf("Run UTC: %v", err)
	}
	if utcRep.Meta.Date == rep.Meta.Date {
		t.Fatalf("UTC and PT produced the same yesterday-date %q, expected zone divergence", rep.Meta.Date)
	}
	if utcRep.Meta.Date != "2026-05-12" {
		t.Fatalf("UTC yesterday-date = %q, want 2026-05-12", utcRep.Meta.Date)
	}
}

// TestYesterdayTimezoneBoundary covers F6 / acceptance check #4 boundary
// case: an event at 23:30 local on day D lands in day-D yesterday.md after
// midnight; an event at 00:30 local on D+1 does not.
//
// Note: Run does not filter events by timestamp itself — the upstream
// ChangeEventSource is responsible for windowing. This test asserts the
// integration contract by passing a source whose Events() method partitions
// on the window date in the workspace zone, then verifies the produced
// bullets cover the 23:30 event and exclude the 00:30 event.
func TestYesterdayTimezoneBoundary(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("America/Los_Angeles tz not available: %v", err)
	}
	allEvents := []ChangeEvent{
		{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 12, 23, 30, 0, 0, la), // 2026-05-12 23:30 PT
			Identifier:    "PR-late",
			Verb:          "updated",
			CanonicalPath: "github/repos/x/pulls/by-id/late.json",
		},
		{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 13, 0, 30, 0, 0, la), // 2026-05-13 00:30 PT
			Identifier:    "PR-after",
			Verb:          "updated",
			CanonicalPath: "github/repos/x/pulls/by-id/after.json",
		},
	}
	now := time.Date(2026, 5, 13, 1, 0, 0, 0, la)
	closedDay := now.AddDate(0, 0, -1)
	w := YesterdayWindow(closedDay, now, []string{"github"}, la)

	src := windowFilteredSource{items: allEvents, tz: la}
	rep, err := Run(context.Background(), src, w)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Meta.Date != "2026-05-12" {
		t.Fatalf("date = %q, want 2026-05-12", rep.Meta.Date)
	}
	if rep.Meta.Events != 1 {
		t.Fatalf("events = %d, want 1 (only the 23:30 PT event)", rep.Meta.Events)
	}
	var section *DigestSection
	for i := range rep.Sections {
		if rep.Sections[i].Provider == "github" {
			section = &rep.Sections[i]
			break
		}
	}
	if section == nil || len(section.Bullets) != 1 {
		t.Fatalf("expected exactly one github bullet, got %+v", section)
	}
	if section.Bullets[0].Identifier != "PR-late" {
		t.Fatalf("expected PR-late, got %q (boundary event leaked)", section.Bullets[0].Identifier)
	}
}

// windowFilteredSource mimics the production source that partitions events
// by the window's local day. Tests use it to assert the boundary contract
// without depending on the not-yet-present production event store.
type windowFilteredSource struct {
	items []ChangeEvent
	tz    *time.Location
}

func (s windowFilteredSource) Events(w Window) ([]ChangeEvent, error) {
	tz := s.tz
	if tz == nil {
		tz = time.UTC
	}
	dayKey := w.Date.In(tz).Format("2006-01-02")
	out := make([]ChangeEvent, 0, len(s.items))
	for _, ev := range s.items {
		if ev.Timestamp.In(tz).Format("2006-01-02") == dayKey {
			out = append(out, ev)
		}
	}
	return out, nil
}
