package digest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTodayWindow_UTC(t *testing.T) {
	now := time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC)
	start, end := TodayWindow(now, time.UTC)
	wantStart := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) {
		t.Fatalf("start = %s, want %s", start, wantStart)
	}
	if !end.Equal(wantEnd) {
		t.Fatalf("end = %s, want %s", end, wantEnd)
	}
}

func TestTodayWindow_NonUTC(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skipf("LoadLocation: %v", err)
	}
	// 2026-05-15 01:30 UTC == 2026-05-14 18:30 PDT, so the local-day
	// boundary in PDT is 2026-05-14 00:00 PDT (== 2026-05-14 07:00 UTC).
	now := time.Date(2026, 5, 15, 1, 30, 0, 0, time.UTC)
	start, end := TodayWindow(now, la)
	wantStart := time.Date(2026, 5, 14, 7, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 5, 15, 7, 0, 0, 0, time.UTC)
	if !start.UTC().Equal(wantStart) {
		t.Fatalf("start UTC = %s, want %s", start.UTC(), wantStart)
	}
	if !end.UTC().Equal(wantEnd) {
		t.Fatalf("end UTC = %s, want %s", end.UTC(), wantEnd)
	}
	rep, err := BuildToday(context.Background(), SliceSource{Items: nil}, []string{"github"}, now, la, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildToday: %v", err)
	}
	if rep.Meta.Date != "2026-05-14" {
		t.Fatalf("meta.date = %q, want 2026-05-14", rep.Meta.Date)
	}
}

func TestTodayWindow_DSTBoundaries(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("LoadLocation: %v", err)
	}
	tests := []struct {
		name         string
		now          time.Time
		wantStartUTC time.Time
		wantEndUTC   time.Time
		wantDuration time.Duration
	}{
		{
			name:         "spring-forward",
			now:          time.Date(2026, 3, 8, 12, 0, 0, 0, ny),
			wantStartUTC: time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC),
			wantEndUTC:   time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC),
			wantDuration: 23 * time.Hour,
		},
		{
			name:         "fall-back",
			now:          time.Date(2026, 11, 1, 12, 0, 0, 0, ny),
			wantStartUTC: time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC),
			wantEndUTC:   time.Date(2026, 11, 2, 5, 0, 0, 0, time.UTC),
			wantDuration: 25 * time.Hour,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end := TodayWindow(tt.now, ny)
			if !start.UTC().Equal(tt.wantStartUTC) {
				t.Fatalf("start UTC = %s, want %s", start.UTC(), tt.wantStartUTC)
			}
			if !end.UTC().Equal(tt.wantEndUTC) {
				t.Fatalf("end UTC = %s, want %s", end.UTC(), tt.wantEndUTC)
			}
			if got := end.Sub(start); got != tt.wantDuration {
				t.Fatalf("duration = %s, want %s", got, tt.wantDuration)
			}
			endLocal := end.In(ny)
			if endLocal.Hour() != 0 || endLocal.Minute() != 0 || endLocal.Second() != 0 || endLocal.Nanosecond() != 0 {
				t.Fatalf("end local = %s, want local midnight", endLocal)
			}
		})
	}
}

func TestBuildToday_FiltersOutsideWindow(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	start, end := TodayWindow(now, time.UTC)
	events := []ChangeEvent{
		{Provider: "github", Timestamp: start.Add(-time.Nanosecond), Identifier: "before-start", Verb: "x", CanonicalPath: "github/a"},
		{Provider: "github", Timestamp: start, Identifier: "at-start", Verb: "x", CanonicalPath: "github/b"},
		{Provider: "github", Timestamp: end.Add(-time.Nanosecond), Identifier: "before-end", Verb: "x", CanonicalPath: "github/c"},
		{Provider: "github", Timestamp: end, Identifier: "at-end", Verb: "x", CanonicalPath: "github/d"},
	}
	rep, err := BuildToday(context.Background(), SliceSource{Items: events}, []string{"github"}, now, time.UTC, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildToday: %v", err)
	}
	if rep.Meta.Events != 2 {
		t.Fatalf("events = %d, want 2 (start..end-1ns)", rep.Meta.Events)
	}
	var got []string
	for _, sec := range rep.Sections {
		for _, b := range sec.Bullets {
			got = append(got, b.Identifier)
		}
	}
	if len(got) != 2 || got[0] != "at-start" || got[1] != "before-end" {
		t.Fatalf("bullets = %v, want [at-start before-end]", got)
	}
}

func TestBuildToday_DSTSpringForwardExcludesNextLocalDay(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("LoadLocation: %v", err)
	}
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, ny)
	_, end := TodayWindow(now, ny)
	events := []ChangeEvent{
		{Provider: "github", Timestamp: end.Add(-time.Nanosecond), Identifier: "last-same-day", Verb: "updated", CanonicalPath: "github/same"},
		{Provider: "github", Timestamp: time.Date(2026, 3, 9, 0, 30, 0, 0, ny), Identifier: "next-local-day", Verb: "updated", CanonicalPath: "github/next"},
	}
	rep, err := BuildToday(context.Background(), SliceSource{Items: events}, []string{"github"}, now, ny, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildToday: %v", err)
	}
	if rep.Meta.Events != 1 {
		t.Fatalf("events = %d, want 1", rep.Meta.Events)
	}
	var got []string
	for _, sec := range rep.Sections {
		for _, b := range sec.Bullets {
			got = append(got, b.Identifier)
		}
	}
	if len(got) != 1 || got[0] != "last-same-day" {
		t.Fatalf("bullets = %v, want [last-same-day]", got)
	}
}

func TestBuildToday_DSTFallBackIncludesFullLocalDay(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("LoadLocation: %v", err)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, ny)
	events := []ChangeEvent{
		{Provider: "github", Timestamp: time.Date(2026, 11, 1, 23, 30, 0, 0, ny), Identifier: "late-same-day", Verb: "updated", CanonicalPath: "github/late"},
		{Provider: "github", Timestamp: time.Date(2026, 11, 2, 0, 0, 0, 0, ny), Identifier: "next-local-day", Verb: "updated", CanonicalPath: "github/next"},
	}
	rep, err := BuildToday(context.Background(), SliceSource{Items: events}, []string{"github"}, now, ny, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildToday: %v", err)
	}
	if rep.Meta.Events != 1 {
		t.Fatalf("events = %d, want 1", rep.Meta.Events)
	}
	var got []string
	for _, sec := range rep.Sections {
		for _, b := range sec.Bullets {
			got = append(got, b.Identifier)
		}
	}
	if len(got) != 1 || got[0] != "late-same-day" {
		t.Fatalf("bullets = %v, want [late-same-day]", got)
	}
}

func TestBuildToday_Exhaustive(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	start, _ := TodayWindow(now, time.UTC)
	events := make([]ChangeEvent, 0, 750)
	for i := 0; i < 750; i++ {
		events = append(events, ChangeEvent{
			Provider:      "github",
			Timestamp:     start.Add(time.Duration(i) * time.Millisecond),
			Identifier:    fmt.Sprintf("id-%d", i),
			Verb:          "updated",
			CanonicalPath: fmt.Sprintf("github/x/%d", i),
		})
	}
	rep, err := BuildToday(context.Background(), SliceSource{Items: events}, []string{"github"}, now, time.UTC, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildToday: %v", err)
	}
	if rep.Meta.Events != 750 {
		t.Fatalf("events = %d, want 750 (exhaustive default)", rep.Meta.Events)
	}
	for _, w := range rep.Meta.Warnings {
		if strings.HasPrefix(w, "truncated:") {
			t.Fatalf("unexpected truncation warning: %q", w)
		}
	}
}

func TestBuildToday_TruncationWarning(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	start, _ := TodayWindow(now, time.UTC)
	events := make([]ChangeEvent, 0, 750)
	for i := 0; i < 750; i++ {
		events = append(events, ChangeEvent{
			Provider:      "github",
			Timestamp:     start.Add(time.Duration(i) * time.Millisecond),
			Identifier:    fmt.Sprintf("id-%d", i),
			Verb:          "updated",
			CanonicalPath: fmt.Sprintf("github/x/%d", i),
		})
	}
	rep, err := BuildToday(context.Background(), SliceSource{Items: events}, []string{"github"}, now, time.UTC, BuildOptions{MaxEvents: 500})
	if err != nil {
		t.Fatalf("BuildToday: %v", err)
	}
	if rep.Meta.Events != 500 {
		t.Fatalf("events = %d, want 500", rep.Meta.Events)
	}
	var found bool
	for _, w := range rep.Meta.Warnings {
		if strings.HasPrefix(w, "truncated:") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected truncated: warning, got %v", rep.Meta.Warnings)
	}
}

func TestBuildToday_NoFallbackTZ_Warns(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	rep, err := BuildToday(context.Background(), SliceSource{Items: nil}, []string{"github"}, now, nil, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildToday: %v", err)
	}
	var found bool
	for _, w := range rep.Meta.Warnings {
		if strings.HasPrefix(w, "tz-fallback:") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected tz-fallback warning, got %v", rep.Meta.Warnings)
	}
}

func TestRender_HeaderFieldsMatchContract(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	rep, err := BuildToday(context.Background(), SliceSource{Items: nil}, []string{"github", "linear"}, now, time.UTC, BuildOptions{})
	if err != nil {
		t.Fatalf("BuildToday: %v", err)
	}
	body := string(Render(rep))
	for _, field := range []string{"date:", "generated_at:", "covers:", "providers:", "events:"} {
		if !strings.Contains(body, field) {
			t.Fatalf("header missing %q\n--- body ---\n%s", field, body)
		}
	}
	if strings.Contains(body, "warnings:") {
		t.Fatalf("unexpected warnings header for clean run\n--- body ---\n%s", body)
	}
	withWarn := rep
	withWarn.Meta.Warnings = []string{"truncated: 1 events exceeded MaxEvents=0"}
	bodyWithWarn := string(Render(withWarn))
	if !strings.Contains(bodyWithWarn, "warnings:") {
		t.Fatalf("expected warnings header when set\n--- body ---\n%s", bodyWithWarn)
	}
}

func TestWriteToday_AtomicAndDeterministic(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	events := []ChangeEvent{
		{Provider: "github", Timestamp: now, Identifier: "PR-1", Verb: "updated", CanonicalPath: "github/x/1"},
	}
	first, err := WriteToday(context.Background(), root, SliceSource{Items: events}, []string{"github"}, now, time.UTC, BuildOptions{})
	if err != nil {
		t.Fatalf("WriteToday first: %v", err)
	}
	if !first.Written {
		t.Fatal("first WriteToday reported not written")
	}
	if first.Path != TodayPath {
		t.Fatalf("path = %q, want %q", first.Path, TodayPath)
	}
	localPath := filepath.Join(root, filepath.FromSlash(first.Path))
	firstBytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read first: %v", err)
	}
	if _, err := WriteToday(context.Background(), root, SliceSource{Items: events}, []string{"github"}, now, time.UTC, BuildOptions{}); err != nil {
		t.Fatalf("WriteToday second: %v", err)
	}
	secondBytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read second: %v", err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("rolling rewrite produced different bytes\n--- first ---\n%s\n--- second ---\n%s", firstBytes, secondBytes)
	}
	entries, err := os.ReadDir(filepath.Dir(localPath))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp leftover after atomic rename: %s", e.Name())
		}
	}
}

func TestDayRollover_TodayRendersNewDay(t *testing.T) {
	day1 := time.Date(2026, 5, 15, 23, 30, 0, 0, time.UTC)
	day2 := time.Date(2026, 5, 16, 0, 30, 0, 0, time.UTC)
	root := t.TempDir()

	day1Events := []ChangeEvent{
		{Provider: "github", Timestamp: day1, Identifier: "PR-A", Verb: "updated", CanonicalPath: "github/a"},
	}
	if _, err := WriteToday(context.Background(), root, SliceSource{Items: day1Events}, []string{"github"}, day1, time.UTC, BuildOptions{}); err != nil {
		t.Fatalf("WriteToday day1: %v", err)
	}
	localPath := filepath.Join(root, filepath.FromSlash(TodayPath))
	day1Bytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read day1: %v", err)
	}
	if !strings.Contains(string(day1Bytes), "date: 2026-05-15") {
		t.Fatalf("day1 frontmatter missing date 2026-05-15\n%s", day1Bytes)
	}

	// After local-midnight, the same event stream plus a day2 event must
	// cause the next render to label the file with the new local date.
	day2Events := append([]ChangeEvent(nil), day1Events...)
	day2Events = append(day2Events, ChangeEvent{
		Provider: "github", Timestamp: day2, Identifier: "PR-B", Verb: "updated", CanonicalPath: "github/b",
	})
	if _, err := WriteToday(context.Background(), root, SliceSource{Items: day2Events}, []string{"github"}, day2, time.UTC, BuildOptions{}); err != nil {
		t.Fatalf("WriteToday day2: %v", err)
	}
	day2Bytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read day2: %v", err)
	}
	if !strings.Contains(string(day2Bytes), "date: 2026-05-16") {
		t.Fatalf("day2 frontmatter missing date 2026-05-16\n%s", day2Bytes)
	}
	if strings.Contains(string(day2Bytes), "PR-A") {
		t.Fatalf("day1 event leaked into day2 today.md\n%s", day2Bytes)
	}
}

func TestGoldenToday(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	// Pick a now-instant inside the local-day containing the fixture's
	// event stream so all 11 events fall in the rolling window.
	now := time.Date(2026, 5, 12, 23, 59, 0, 0, time.UTC)
	path, body, rep, err := RenderToday(context.Background(), SliceSource{Items: events}, []string{"github", "linear", "notion"}, now, time.UTC, BuildOptions{})
	if err != nil {
		t.Fatalf("RenderToday: %v", err)
	}
	if path != TodayPath {
		t.Fatalf("path = %q, want %q", path, TodayPath)
	}
	if rep.Meta.Date != "2026-05-12" {
		t.Fatalf("date = %q, want 2026-05-12", rep.Meta.Date)
	}
	if rep.Meta.Covers != TodayCover {
		t.Fatalf("covers = %q, want %q", rep.Meta.Covers, TodayCover)
	}
	if rep.Meta.Events != 11 {
		t.Fatalf("events = %d, want 11", rep.Meta.Events)
	}
	want, err := os.ReadFile("testdata/today-fixture.md")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", body, want)
	}
}
