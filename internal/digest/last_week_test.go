package digest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// weekFixtureWindow returns the canonical LastWeekWindow used by the golden
// + closing-window tests: ISO-week Monday 2026-05-04 through Sunday
// 2026-05-10, generated_at 2026-05-11T00:00:00Z, three providers.
func weekFixtureWindow() Window {
	return LastWeekWindow(
		time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		[]string{"github", "linear", "notion"},
		time.UTC,
	)
}

func TestGoldenLastWeek(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/last-week-events.jsonl")
	w := weekFixtureWindow()

	path, got, rep, err := RenderLastWeek(context.Background(), SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("RenderLastWeek: %v", err)
	}
	if path != LastWeekPath {
		t.Fatalf("path = %q, want %q", path, LastWeekPath)
	}
	if rep.Meta.Covers != LastWeekCover {
		t.Fatalf("covers = %q, want %q", rep.Meta.Covers, LastWeekCover)
	}
	if rep.Meta.Date != "2026-05-04" {
		t.Fatalf("date = %q, want 2026-05-04 (ISO Monday week start)", rep.Meta.Date)
	}
	if rep.Meta.Events != len(events) {
		t.Fatalf("events = %d, want %d", rep.Meta.Events, len(events))
	}

	want, err := os.ReadFile("testdata/last-week-fixture.md")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestEmptyLastWeek(t *testing.T) {
	w := weekFixtureWindow()
	_, got, rep, err := RenderLastWeek(context.Background(), SliceSource{Items: nil}, w)
	if err != nil {
		t.Fatalf("RenderLastWeek: %v", err)
	}
	if rep.Meta.Events != 0 {
		t.Fatalf("events = %d, want 0", rep.Meta.Events)
	}
	for _, sec := range rep.Sections {
		if len(sec.Bullets) != 0 {
			t.Fatalf("provider %s: bullets %d, want 0", sec.Provider, len(sec.Bullets))
		}
	}
	expected := `---
date: 2026-05-04
generated_at: 2026-05-11T00:00:00Z
covers: last-week
providers: [github, linear, notion]
events: 0
---

# Activity summary for 2026-05-04

## github

_no activity_

## linear

_no activity_

## notion

_no activity_
`
	if string(got) != expected {
		t.Fatalf("empty mismatch\n--- got ---\n%s\n--- want ---\n%s", got, expected)
	}
}

func TestLastWeekClosingWindowTriggerOnSundayToMonday(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/last-week-events.jsonl")
	root := t.TempDir()
	tz := time.UTC

	// Crossing from Sunday 2026-05-10 into Monday 2026-05-11 must fire the
	// write exactly once with date = the week's Monday (2026-05-04).
	prev := time.Date(2026, 5, 10, 23, 50, 0, 0, tz)
	now := time.Date(2026, 5, 11, 0, 10, 0, 0, tz)
	generated := time.Date(2026, 5, 11, 0, 0, 0, 0, tz)
	result, err := MaybeCloseLastWeekWindow(
		context.Background(),
		now, prev,
		tz, root,
		SliceSource{Items: events},
		[]string{"github", "linear", "notion"},
		generated,
	)
	if err != nil {
		t.Fatalf("MaybeCloseLastWeekWindow: %v", err)
	}
	if !result.Written {
		t.Fatal("crossing Sun→Mon must write last-week.md")
	}
	if result.Report.Meta.Date != "2026-05-04" {
		t.Fatalf("date = %q, want 2026-05-04", result.Report.Meta.Date)
	}

	// Calling again the same Monday is a no-op — the file already exists
	// with the matching `date:` frontmatter.
	again, err := MaybeCloseLastWeekWindow(
		context.Background(),
		now, prev,
		tz, root,
		SliceSource{Items: events},
		[]string{"github", "linear", "notion"},
		generated,
	)
	if err != nil {
		t.Fatalf("second MaybeCloseLastWeekWindow: %v", err)
	}
	if again.Written {
		t.Fatal("second call must be a no-op for the same closed week")
	}
}

func TestLastWeekClosingWindowSkipsMidweekDays(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/last-week-events.jsonl")
	root := t.TempDir()
	tz := time.UTC

	// Crossing from Wednesday into Thursday must NOT write last-week.md —
	// the week is still open.
	prev := time.Date(2026, 5, 6, 23, 50, 0, 0, tz)
	now := time.Date(2026, 5, 7, 0, 10, 0, 0, tz)
	generated := now
	result, err := MaybeCloseLastWeekWindow(
		context.Background(),
		now, prev,
		tz, root,
		SliceSource{Items: events},
		[]string{"github", "linear", "notion"},
		generated,
	)
	if err != nil {
		t.Fatalf("MaybeCloseLastWeekWindow: %v", err)
	}
	if result.Written {
		t.Fatal("mid-week boundary must not produce a last-week.md")
	}
	if _, err := os.Stat(filepath.Join(root, "digests", "last-week.md")); !os.IsNotExist(err) {
		t.Fatalf("digests/last-week.md must not exist on mid-week boundaries: %v", err)
	}
}

func TestLastWeekImmutabilityWithinSameWeek(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/last-week-events.jsonl")
	root := t.TempDir()
	w := weekFixtureWindow()

	first, err := WriteLastWeek(context.Background(), root, SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("WriteLastWeek first: %v", err)
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

	// Append a week-N+1 event and re-run regeneration before the next
	// week-boundary. The closed-week file must not change.
	laterEvents := append([]ChangeEvent(nil), events...)
	laterEvents = append(laterEvents, ChangeEvent{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC),
		Identifier:    "PR-200",
		Verb:          "opened",
		CanonicalPath: "github/repos/AgentWorkforce__workforce/pulls/by-id/200.json",
	})
	second, err := WriteLastWeek(context.Background(), root, SliceSource{Items: laterEvents}, w)
	if err != nil {
		t.Fatalf("WriteLastWeek second: %v", err)
	}
	if second.Written {
		t.Fatal("re-running for the same closed week must be a no-op")
	}
	secondBytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read second digest: %v", err)
	}
	if !bytes.Equal(secondBytes, firstBytes) {
		t.Fatalf("closed-week digest changed\n--- first ---\n%s\n--- second ---\n%s", firstBytes, secondBytes)
	}
	secondInfo, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat second digest: %v", err)
	}
	if !secondInfo.ModTime().Equal(firstInfo.ModTime()) {
		t.Fatalf("mtime changed: first %s second %s", firstInfo.ModTime(), secondInfo.ModTime())
	}
}

func TestLastWeekReplacedAtNextWeekBoundary(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/last-week-events.jsonl")
	root := t.TempDir()

	// Week N: 2026-05-04 — 2026-05-10.
	wN := weekFixtureWindow()
	if _, err := WriteLastWeek(context.Background(), root, SliceSource{Items: events}, wN); err != nil {
		t.Fatalf("WriteLastWeek week N: %v", err)
	}
	localPath := filepath.Join(root, "digests", "last-week.md")
	weekNBytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read week N digest: %v", err)
	}
	if !strings.Contains(string(weekNBytes), "date: 2026-05-04\n") {
		t.Fatalf("expected week-N date in digest, got: %s", weekNBytes)
	}

	// Week N+1: 2026-05-11 — 2026-05-17. Single event on Monday.
	weekNPlus1Events := []ChangeEvent{{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 11, 10, 0, 0, 0, time.UTC),
		Identifier:    "PR-300",
		Verb:          "opened",
		CanonicalPath: "github/repos/AgentWorkforce__workforce/pulls/by-id/300.json",
	}}
	wNPlus1 := LastWeekWindow(
		time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC),
		[]string{"github", "linear", "notion"},
		time.UTC,
	)
	result, err := WriteLastWeek(context.Background(), root, SliceSource{Items: weekNPlus1Events}, wNPlus1)
	if err != nil {
		t.Fatalf("WriteLastWeek week N+1: %v", err)
	}
	if !result.Written {
		t.Fatal("week N+1 must replace the previous file")
	}
	if !result.Replaced {
		t.Fatal("week N+1 must report Replaced=true")
	}
	weekNPlus1Bytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read week N+1 digest: %v", err)
	}
	if strings.Contains(string(weekNPlus1Bytes), "2026-05-04") {
		t.Fatalf("week N+1 digest still references week-N date:\n%s", weekNPlus1Bytes)
	}
	if !strings.Contains(string(weekNPlus1Bytes), "date: 2026-05-11\n") {
		t.Fatalf("week N+1 digest missing week-N+1 date:\n%s", weekNPlus1Bytes)
	}
	if !strings.Contains(string(weekNPlus1Bytes), "PR-300 opened") {
		t.Fatalf("week N+1 digest missing the week's event:\n%s", weekNPlus1Bytes)
	}
	if strings.Contains(string(weekNPlus1Bytes), "PR-100 opened") {
		t.Fatalf("week N+1 digest leaked week-N event:\n%s", weekNPlus1Bytes)
	}
}

func TestLastWeekStartIsISOMonday(t *testing.T) {
	tz := time.UTC
	cases := []struct {
		in   time.Time
		want string // YYYY-MM-DD
	}{
		// 2026-05-04 is a Monday.
		{time.Date(2026, 5, 4, 12, 0, 0, 0, tz), "2026-05-04"},
		// Same week: Wednesday.
		{time.Date(2026, 5, 6, 0, 0, 0, 0, tz), "2026-05-04"},
		// Same week: Sunday (last day of ISO week).
		{time.Date(2026, 5, 10, 23, 59, 59, 0, tz), "2026-05-04"},
		// Next-week Monday rolls forward.
		{time.Date(2026, 5, 11, 0, 0, 0, 0, tz), "2026-05-11"},
	}
	for _, tc := range cases {
		got := LastWeekStart(tc.in, tz).Format("2006-01-02")
		if got != tc.want {
			t.Errorf("LastWeekStart(%s) = %s, want %s", tc.in.Format(time.RFC3339), got, tc.want)
		}
	}
}

func TestIsLastWeekClosingDayOnlyFiresOnSunday(t *testing.T) {
	tz := time.UTC
	cases := []struct {
		in   time.Time
		want bool
	}{
		{time.Date(2026, 5, 4, 12, 0, 0, 0, tz), false},  // Monday
		{time.Date(2026, 5, 9, 12, 0, 0, 0, tz), false},  // Saturday
		{time.Date(2026, 5, 10, 0, 0, 0, 0, tz), true},   // Sunday
		{time.Date(2026, 5, 10, 23, 59, 0, 0, tz), true}, // Sunday late
		{time.Date(2026, 5, 11, 0, 1, 0, 0, tz), false},  // Monday
	}
	for _, tc := range cases {
		got := IsLastWeekClosingDay(tc.in, tz)
		if got != tc.want {
			t.Errorf("IsLastWeekClosingDay(%s)=%v, want %v", tc.in.Format(time.RFC3339), got, tc.want)
		}
	}
}

func TestLastWeekFiltersOutEventsOutsideTheClosedWeek(t *testing.T) {
	// One event the Sunday before the window and one the Monday after must
	// both be excluded; events at the inclusive Monday 00:00 start and just
	// before the next Monday 00:00 must be included.
	tz := time.UTC
	events := []ChangeEvent{
		{Provider: "github", Timestamp: time.Date(2026, 5, 3, 23, 59, 0, 0, tz), Identifier: "PR-pre", Verb: "u", CanonicalPath: "github/x.json"},
		{Provider: "github", Timestamp: time.Date(2026, 5, 4, 0, 0, 0, 0, tz), Identifier: "PR-start", Verb: "u", CanonicalPath: "github/x.json"},
		{Provider: "github", Timestamp: time.Date(2026, 5, 10, 23, 59, 59, 0, tz), Identifier: "PR-end", Verb: "u", CanonicalPath: "github/x.json"},
		{Provider: "github", Timestamp: time.Date(2026, 5, 11, 0, 0, 0, 0, tz), Identifier: "PR-post", Verb: "u", CanonicalPath: "github/x.json"},
	}
	w := weekFixtureWindow()
	_, _, rep, err := RenderLastWeek(context.Background(), SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("RenderLastWeek: %v", err)
	}
	if rep.Meta.Events != 2 {
		t.Fatalf("events = %d, want 2 (only PR-start and PR-end fall inside the closed week)", rep.Meta.Events)
	}
	ids := map[string]bool{}
	for _, sec := range rep.Sections {
		for _, b := range sec.Bullets {
			ids[b.Identifier] = true
		}
	}
	for _, want := range []string{"PR-start", "PR-end"} {
		if !ids[want] {
			t.Errorf("missing %s in rendered bullets", want)
		}
	}
	for _, banned := range []string{"PR-pre", "PR-post"} {
		if ids[banned] {
			t.Errorf("event %s leaked across the week boundary", banned)
		}
	}
}
