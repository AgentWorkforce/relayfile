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

// thisWeekFixtureGeneratedAt sits at Wednesday 2026-05-13 00:00 UTC, which is
// inside the same ISO week as the fixture events (Monday 2026-05-11 onward).
var (
	thisWeekFixtureGeneratedAt = time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	thisWeekFixtureMonday      = time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	thisWeekFixtureProviders   = []string{"github", "linear", "notion"}
)

func TestMondayStartUTCAnchorsThisWeek(t *testing.T) {
	cases := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "monday-00:00-is-itself",
			now:  time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
			want: time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "wednesday-rolls-back-to-monday",
			now:  time.Date(2026, 5, 13, 14, 30, 0, 0, time.UTC),
			want: time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		},
		{
			name: "sunday-rolls-back-six-days",
			now:  time.Date(2026, 5, 17, 23, 59, 59, 0, time.UTC),
			want: time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MondayStart(tc.now, time.UTC)
			if !got.Equal(tc.want) {
				t.Fatalf("MondayStart(%s) = %s, want %s (Monday-start anchor)", tc.now, got, tc.want)
			}
		})
	}
}

func TestMondayStartHonorsLocationForThisWeek(t *testing.T) {
	// Use a fixed offset to avoid relying on the host tz database for the
	// DST transition. 2026-05-13 14:30 in UTC-12 is 2026-05-14 02:30 UTC
	// but stays Wednesday locally, so the local Monday boundary is
	// 2026-05-11 00:00 in that zone.
	loc := time.FixedZone("test-12", -12*60*60)
	now := time.Date(2026, 5, 13, 14, 30, 0, 0, loc)
	got := MondayStart(now, loc)
	want := time.Date(2026, 5, 11, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("MondayStart(%s, %s) = %s, want %s", now, loc, got, want)
	}
	if got.Location() != loc {
		t.Fatalf("MondayStart returned location %s, want %s", got.Location(), loc)
	}
}

func TestGoldenThisWeek(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	w := ThisWeekWindow(
		thisWeekFixtureGeneratedAt,
		thisWeekFixtureGeneratedAt,
		thisWeekFixtureProviders,
		time.UTC,
	)
	if !w.Date.Equal(thisWeekFixtureMonday) {
		t.Fatalf("Window.Date = %s, want %s (Monday-start)", w.Date, thisWeekFixtureMonday)
	}
	if w.Cover != ThisWeekCover {
		t.Fatalf("Window.Cover = %q, want %q", w.Cover, ThisWeekCover)
	}
	path, got, rep, err := RenderThisWeek(context.Background(), SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("RenderThisWeek: %v", err)
	}
	if path != ThisWeekPath {
		t.Fatalf("path = %q, want %q", path, ThisWeekPath)
	}
	if rep.Meta.Covers != "this-week" {
		t.Fatalf("meta.covers = %q, want this-week", rep.Meta.Covers)
	}
	if rep.Meta.Date != "2026-05-11" {
		t.Fatalf("meta.date = %q, want 2026-05-11", rep.Meta.Date)
	}
	want, err := os.ReadFile("testdata/this-week-fixture.md")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestThisWeekFrontmatterDateIsMondayStartForMidweekNow(t *testing.T) {
	// now=Wednesday 14:00 local, week start should be that week's Monday.
	now := time.Date(2026, 5, 13, 14, 0, 0, 0, time.UTC)
	w := ThisWeekWindow(now, now, thisWeekFixtureProviders, time.UTC)
	rep, err := Run(context.Background(), SliceSource{Items: nil}, w)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Meta.Date != "2026-05-11" {
		t.Fatalf("meta.date = %q, want 2026-05-11", rep.Meta.Date)
	}
	if rep.Meta.Covers != "this-week" {
		t.Fatalf("meta.covers = %q, want this-week", rep.Meta.Covers)
	}
}

func TestThisWeekFrontmatterDateIsMondayStartForMondayMidnightNow(t *testing.T) {
	now := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	w := ThisWeekWindow(now, now, thisWeekFixtureProviders, time.UTC)
	rep, err := Run(context.Background(), SliceSource{Items: nil}, w)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Meta.Date != "2026-05-11" {
		t.Fatalf("meta.date = %q, want 2026-05-11", rep.Meta.Date)
	}
}

func TestThisWeekWindowBoundaryUTC(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	w := ThisWeekWindow(now, now, thisWeekFixtureProviders, time.UTC)

	sunday := ChangeEvent{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 10, 23, 59, 59, 0, time.UTC),
		Identifier:    "PR-out",
		Verb:          "updated",
		CanonicalPath: "github/repos/o/r/pulls/by-id/1.json",
	}
	monday := ChangeEvent{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
		Identifier:    "PR-monday",
		Verb:          "updated",
		CanonicalPath: "github/repos/o/r/pulls/by-id/2.json",
	}
	wednesday := ChangeEvent{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
		Identifier:    "PR-wed",
		Verb:          "updated",
		CanonicalPath: "github/repos/o/r/pulls/by-id/3.json",
	}

	src := SliceSource{Items: []ChangeEvent{sunday, monday, wednesday}}
	_, content, rep, err := RenderThisWeek(context.Background(), src, w)
	if err != nil {
		t.Fatalf("RenderThisWeek: %v", err)
	}
	if rep.Meta.Events != 2 {
		t.Fatalf("events = %d, want 2", rep.Meta.Events)
	}
	body := string(content)
	if !strings.Contains(body, "PR-monday") {
		t.Fatal("Monday 00:00 event missing from output")
	}
	if !strings.Contains(body, "PR-wed") {
		t.Fatal("Wednesday in-window event missing from output")
	}
	if strings.Contains(body, "PR-out") {
		t.Fatal("Sunday 23:59 event leaked into the window")
	}
}

func TestThisWeekWindowBoundaryNonUTCLocation(t *testing.T) {
	// UTC-12 forces a clear gap between UTC-day-of-week and local-day-of-
	// week. 2026-05-11 11:00 UTC is 2026-05-10 23:00 (Sunday) local; the
	// event must be excluded. 2026-05-11 13:00 UTC is 2026-05-11 01:00
	// (Monday) local; the event must be included.
	loc := time.FixedZone("test-12", -12*60*60)
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, loc)
	w := ThisWeekWindow(now, now, thisWeekFixtureProviders, loc)

	sundayLocal := ChangeEvent{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 11, 11, 0, 0, 0, time.UTC),
		Identifier:    "PR-sun",
		Verb:          "updated",
		CanonicalPath: "github/repos/o/r/pulls/by-id/10.json",
	}
	mondayLocal := ChangeEvent{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 11, 13, 0, 0, 0, time.UTC),
		Identifier:    "PR-mon",
		Verb:          "updated",
		CanonicalPath: "github/repos/o/r/pulls/by-id/11.json",
	}

	src := SliceSource{Items: []ChangeEvent{sundayLocal, mondayLocal}}
	_, content, rep, err := RenderThisWeek(context.Background(), src, w)
	if err != nil {
		t.Fatalf("RenderThisWeek: %v", err)
	}
	if rep.Meta.Events != 1 {
		t.Fatalf("events = %d, want 1 (local Monday only)", rep.Meta.Events)
	}
	body := string(content)
	if strings.Contains(body, "PR-sun") {
		t.Fatal("Sunday-local event leaked into a UTC-12 this-week window")
	}
	if !strings.Contains(body, "PR-mon") {
		t.Fatal("Monday-local event missing from a UTC-12 this-week window")
	}
}

func TestThisWeekRewriteOnNewInWindowEvent(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	w := ThisWeekWindow(now, now, thisWeekFixtureProviders, time.UTC)

	first := []ChangeEvent{{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 12, 9, 0, 0, 0, time.UTC),
		Identifier:    "PR-1",
		Verb:          "updated",
		CanonicalPath: "github/repos/o/r/pulls/by-id/1.json",
	}}
	res1, err := WriteThisWeek(context.Background(), root, SliceSource{Items: first}, w)
	if err != nil {
		t.Fatalf("WriteThisWeek first: %v", err)
	}
	if !res1.Written {
		t.Fatal("first write should report Written=true")
	}
	localPath := filepath.Join(root, filepath.FromSlash(res1.Path))
	got1, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}
	if !strings.Contains(string(got1), "PR-1") {
		t.Fatal("first write missing PR-1 bullet")
	}
	if !strings.Contains(string(got1), "events: 1") {
		t.Fatalf("first write events count wrong, body=%s", string(got1))
	}

	second := append([]ChangeEvent(nil), first...)
	second = append(second, ChangeEvent{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
		Identifier:    "PR-2",
		Verb:          "updated",
		CanonicalPath: "github/repos/o/r/pulls/by-id/2.json",
	})
	res2, err := WriteThisWeek(context.Background(), root, SliceSource{Items: second}, w)
	if err != nil {
		t.Fatalf("WriteThisWeek second: %v", err)
	}
	if !res2.Written {
		t.Fatal("rolling rewrite should report Written=true")
	}
	got2, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	body := string(got2)
	if !strings.Contains(body, "PR-1") || !strings.Contains(body, "PR-2") {
		t.Fatalf("rewrite missing bullets, body=%s", body)
	}
	if !strings.Contains(body, "events: 2") {
		t.Fatalf("rewrite events count wrong, body=%s", body)
	}
}

func TestThisWeekIgnoresEventsBeforeWindow(t *testing.T) {
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	w := ThisWeekWindow(now, now, thisWeekFixtureProviders, time.UTC)
	priorWeek := []ChangeEvent{{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC),
		Identifier:    "PR-old",
		Verb:          "updated",
		CanonicalPath: "github/repos/o/r/pulls/by-id/99.json",
	}}
	_, content, rep, err := RenderThisWeek(context.Background(), SliceSource{Items: priorWeek}, w)
	if err != nil {
		t.Fatalf("RenderThisWeek: %v", err)
	}
	if rep.Meta.Events != 0 {
		t.Fatalf("events = %d, want 0", rep.Meta.Events)
	}
	if strings.Contains(string(content), "PR-old") {
		t.Fatal("prior-week event leaked into this-week digest")
	}
	if !strings.Contains(string(content), "_no activity_") {
		t.Fatalf("empty rendering missing zero-activity body, body=%s", string(content))
	}
}

func TestThisWeekEmptyWindowStillWritesFile(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	w := ThisWeekWindow(now, now, thisWeekFixtureProviders, time.UTC)
	res, err := WriteThisWeek(context.Background(), root, SliceSource{Items: nil}, w)
	if err != nil {
		t.Fatalf("WriteThisWeek: %v", err)
	}
	if !res.Written {
		t.Fatal("empty window must still write the file")
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(res.Path)))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "events: 0") {
		t.Fatalf("empty-window frontmatter missing events: 0, body=%s", string(body))
	}
	if !strings.Contains(string(body), "covers: this-week") {
		t.Fatalf("empty-window frontmatter missing covers: this-week, body=%s", string(body))
	}
	for _, p := range thisWeekFixtureProviders {
		if !strings.Contains(string(body), "## "+p) {
			t.Fatalf("empty-window missing provider section %q, body=%s", p, string(body))
		}
	}
	if strings.Count(string(body), "_no activity_") != len(thisWeekFixtureProviders) {
		t.Fatalf("empty-window expected %d _no activity_ sections, body=%s", len(thisWeekFixtureProviders), string(body))
	}
}

func TestThisWeekPathTaxonomy(t *testing.T) {
	if !IsDigestPath("digests/this-week.md") {
		t.Fatal("digests/this-week.md should be recognized as a digest path")
	}
	if !IsDigestPath("/digests/this-week.md") {
		t.Fatal("leading-slash digests/this-week.md should be recognized as a digest path")
	}
	if IsDigestPath("digests/THIS-WEEK.md") {
		t.Fatal("digest path predicate must be case-sensitive")
	}
	if IsDigestPath("digests/this-week.MD") {
		t.Fatal("digest path predicate must require lowercase .md suffix")
	}
	if IsDigestPath("notion/pages/this-week.md") {
		t.Fatal("non-digests/ path must not be classified as a digest")
	}
}
