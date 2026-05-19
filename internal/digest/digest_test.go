package digest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadEventsJSONL(t *testing.T, path string) []ChangeEvent {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close %s: %v", path, cerr)
		}
	}()
	var out []ChangeEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev ChangeEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal %s: %v", path, err)
		}
		out = append(out, ev)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

func TestGoldenYesterday(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	w := Window{
		Date:        time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		Cover:       "yesterday",
		GeneratedAt: time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		Providers:   []string{"github", "linear", "notion"},
		TZ:          time.UTC,
	}
	rep, err := Run(context.Background(), SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := Render(rep)
	want, err := os.ReadFile("testdata/yesterday-fixture.md")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestGoldenDateStamped(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	w := DateStampedWindow(
		time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		[]string{"github", "linear", "notion"},
		time.UTC,
	)
	path, got, rep, err := RenderDateStamped(context.Background(), SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("RenderDateStamped: %v", err)
	}
	if path != "digests/2026-05-12.md" {
		t.Fatalf("path = %q, want digests/2026-05-12.md", path)
	}
	if rep.Meta.Date != "2026-05-12" {
		t.Fatalf("date = %q, want 2026-05-12", rep.Meta.Date)
	}
	if rep.Meta.Covers != "2026-05-12" {
		t.Fatalf("covers = %q, want 2026-05-12", rep.Meta.Covers)
	}
	want, err := os.ReadFile("testdata/date-stamped-fixture.md")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestDateStampedFiltersClosedLocalDay(t *testing.T) {
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

	_, body, rep, err := RenderDateStamped(context.Background(), SliceSource{Items: events}, DateStampedWindow(
		time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 13, 0, 5, 0, 0, time.UTC),
		[]string{"github"},
		time.UTC,
	))
	if err != nil {
		t.Fatalf("RenderDateStamped: %v", err)
	}
	if rep.Meta.Events != 1 {
		t.Fatalf("events = %d, want 1", rep.Meta.Events)
	}
	if !bytes.Contains(body, []byte("inside")) || bytes.Contains(body, []byte("before")) || bytes.Contains(body, []byte("after")) {
		t.Fatalf("date-stamped digest did not filter to the closed day:\n%s", body)
	}
}

func TestDateStampedWriterCreatesClosedWindowOnce(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	root := t.TempDir()
	w := DateStampedWindow(
		time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		[]string{"github", "linear", "notion"},
		time.UTC,
	)

	first, err := WriteDateStamped(context.Background(), root, SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("WriteDateStamped first: %v", err)
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

	laterEvents := append([]ChangeEvent(nil), events...)
	laterEvents = append(laterEvents, ChangeEvent{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC),
		Identifier:    "PR-92",
		Verb:          "updated",
		CanonicalPath: "github/repos/AgentWorkforce__workforce/pulls/by-id/92.json",
	})
	second, err := WriteDateStamped(context.Background(), root, SliceSource{Items: laterEvents}, w)
	if err != nil {
		t.Fatalf("WriteDateStamped second: %v", err)
	}
	if second.Written {
		t.Fatal("second write rewrote immutable closed-window digest")
	}
	secondBytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read second digest: %v", err)
	}
	secondInfo, err := os.Stat(localPath)
	if err != nil {
		t.Fatalf("stat second digest: %v", err)
	}
	if !bytes.Equal(secondBytes, firstBytes) {
		t.Fatalf("closed-window digest changed\n--- first ---\n%s\n--- second ---\n%s", firstBytes, secondBytes)
	}
	if !secondInfo.ModTime().Equal(firstInfo.ModTime()) {
		t.Fatalf("mtime changed: first %s second %s", firstInfo.ModTime(), secondInfo.ModTime())
	}
}

// TestDateStampedCloseBoundary drives a fake clock across 2026-05-12 →
// 2026-05-13 midnight and asserts the slice's parent-spec acceptance
// checks #1, #2, #4, #5: pre-close, the date-stamped file does not
// exist; immediately after the boundary, exactly one write of
// digests/2026-05-12.md is observed; the rotation forward (yesterday.md
// renders the closed day) produces the same bullet set as the
// date-stamped file (i.e. body identity with the pre-close today.md
// snapshot, since both digests are produced by the same Run+Render
// path); and a 2026-05-13 event ingested after the boundary leaves the
// closed-window artifact byte-identical.
//
// Note: the Today writer is owned by the sibling `update-today` slice
// and the orchestrating midnight trigger is owned by a future
// scheduler slice. This test exercises the rotation primitives
// (`WriteDateStamped`, `WriteYesterday`) that the trigger will compose,
// using a hand-rolled "today.md" snapshot to stand in for the
// pre-close artifact and asserting the body sections match
// post-rotation.
func TestDateStampedCloseBoundary(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	root := t.TempDir()

	// Phase 1: pre-close clock at 2026-05-12 23:59:59 UTC. Simulate that
	// today.md holds the in-progress digest for 2026-05-12. The
	// date-stamped artifact must not exist yet.
	preCloseClock := time.Date(2026, 5, 12, 23, 59, 59, 0, time.UTC)
	preCloseWindow := Window{
		Date:        time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		Cover:       "today",
		GeneratedAt: preCloseClock,
		Providers:   []string{"github", "linear", "notion"},
		TZ:          time.UTC,
	}
	preCloseRep, err := Run(context.Background(), SliceSource{Items: events}, preCloseWindow)
	if err != nil {
		t.Fatalf("pre-close Run: %v", err)
	}
	todayPath := filepath.Join(root, filepath.FromSlash(TodayPath))
	if err := os.MkdirAll(filepath.Dir(todayPath), 0o755); err != nil {
		t.Fatalf("mkdir digests: %v", err)
	}
	if err := os.WriteFile(todayPath, Render(preCloseRep), 0o644); err != nil {
		t.Fatalf("seed today.md: %v", err)
	}
	dateStampedLocal := filepath.Join(root, filepath.FromSlash("digests/2026-05-12.md"))
	if _, err := os.Stat(dateStampedLocal); !os.IsNotExist(err) {
		t.Fatalf("pre-close: digests/2026-05-12.md should not exist yet, stat err = %v", err)
	}

	// Phase 2: clock crosses 2026-05-13 00:00:01. The midnight trigger
	// composes WriteDateStamped (close the previous day) and
	// WriteYesterday (rotate today.md → yesterday.md).
	postCloseClock := time.Date(2026, 5, 13, 0, 0, 1, 0, time.UTC)
	closedDay := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	dsWindow := DateStampedWindow(closedDay, postCloseClock, []string{"github", "linear", "notion"}, time.UTC)
	dsResult, err := WriteDateStamped(context.Background(), root, SliceSource{Items: events}, dsWindow)
	if err != nil {
		t.Fatalf("WriteDateStamped at boundary: %v", err)
	}
	if !dsResult.Written {
		t.Fatal("WriteDateStamped at boundary reported Written=false on a fresh window")
	}
	if dsResult.Path != "digests/2026-05-12.md" {
		t.Fatalf("WriteDateStamped path = %q, want digests/2026-05-12.md", dsResult.Path)
	}
	dsBytes, err := os.ReadFile(dateStampedLocal)
	if err != nil {
		t.Fatalf("read date-stamped digest: %v", err)
	}
	dsInfo, err := os.Stat(dateStampedLocal)
	if err != nil {
		t.Fatalf("stat date-stamped digest: %v", err)
	}

	yResult, err := CloseLocalDay(context.Background(), root, SliceSource{Items: events}, postCloseClock, []string{"github", "linear", "notion"}, time.UTC)
	if err != nil {
		t.Fatalf("CloseLocalDay at boundary: %v", err)
	}
	if !yResult.Written {
		t.Fatal("CloseLocalDay reported not written; expected a fresh yesterday.md rotation")
	}
	yesterdayLocal := filepath.Join(root, filepath.FromSlash(YesterdayPath))
	yesterdayBytes, err := os.ReadFile(yesterdayLocal)
	if err != nil {
		t.Fatalf("read yesterday.md: %v", err)
	}

	// AC #4: bullets in the date-stamped file match those that appeared
	// in the pre-close today.md, and the rotated yesterday.md uses the
	// same bullet set (different frontmatter only). We compare body
	// sections (everything after the YAML frontmatter terminator).
	preCloseToday, err := os.ReadFile(todayPath)
	if err != nil {
		t.Fatalf("read today.md: %v", err)
	}
	if got, want := bodyAfterFrontmatter(t, dsBytes), bodyAfterFrontmatter(t, preCloseToday); got != want {
		t.Fatalf("date-stamped body diverges from pre-close today.md body\n--- date-stamped ---\n%s\n--- today.md (pre-close) ---\n%s", got, want)
	}
	if got, want := bodyAfterFrontmatter(t, yesterdayBytes), bodyAfterFrontmatter(t, preCloseToday); got != want {
		t.Fatalf("rotated yesterday.md body diverges from pre-close today.md body\n--- yesterday.md ---\n%s\n--- today.md (pre-close) ---\n%s", got, want)
	}

	// AC #5: ingesting a 2026-05-13 event after the boundary leaves the
	// closed-window artifact byte-identical and produces no new write.
	laterEvents := append([]ChangeEvent(nil), events...)
	laterEvents = append(laterEvents, ChangeEvent{
		Provider:      "github",
		Timestamp:     time.Date(2026, 5, 13, 9, 0, 0, 0, time.UTC),
		Identifier:    "PR-92",
		Verb:          "updated",
		CanonicalPath: "github/repos/AgentWorkforce__workforce/pulls/by-id/92.json",
	})
	postIngest, err := WriteDateStamped(context.Background(), root, SliceSource{Items: laterEvents}, dsWindow)
	if err != nil {
		t.Fatalf("WriteDateStamped after 2026-05-13 ingest: %v", err)
	}
	if postIngest.Written {
		t.Fatal("second WriteDateStamped after boundary rewrote the closed-window digest")
	}
	dsBytesAfter, err := os.ReadFile(dateStampedLocal)
	if err != nil {
		t.Fatalf("re-read date-stamped digest: %v", err)
	}
	if !bytes.Equal(dsBytesAfter, dsBytes) {
		t.Fatal("closed-window digest mutated by post-boundary ingest")
	}
	dsInfoAfter, err := os.Stat(dateStampedLocal)
	if err != nil {
		t.Fatalf("stat date-stamped digest after ingest: %v", err)
	}
	if !dsInfoAfter.ModTime().Equal(dsInfo.ModTime()) {
		t.Fatalf("closed-window digest mtime changed: before %s after %s", dsInfo.ModTime(), dsInfoAfter.ModTime())
	}
}

func bodyAfterFrontmatter(t *testing.T, content []byte) string {
	t.Helper()
	s := string(content)
	// Frontmatter is `---\n...---\n`. Find the second `---\n`.
	const sep = "---\n"
	first := strings.Index(s, sep)
	if first != 0 {
		t.Fatalf("content does not start with YAML frontmatter")
	}
	rest := s[len(sep):]
	end := strings.Index(rest, sep)
	if end < 0 {
		t.Fatalf("missing frontmatter terminator")
	}
	return rest[end+len(sep):]
}

// TestDateStampedBodyMatchesYesterdaySnapshot pins parent-spec AC #4:
// the date-stamped renderer and the yesterday renderer produce
// byte-identical body sections for the same fixture and providers
// (frontmatter differs only by `covers` and the date-stamped file
// names the day explicitly). If a future renderer split introduced
// divergent sorting or path canonicalization between the two paths,
// this test would catch it before a clock-driven rotation could.
func TestDateStampedBodyMatchesYesterdaySnapshot(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	closedDay := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	generatedAt := time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC)
	providers := []string{"github", "linear", "notion"}

	_, dsBytes, _, err := RenderDateStamped(
		context.Background(),
		SliceSource{Items: events},
		DateStampedWindow(closedDay, generatedAt, providers, time.UTC),
	)
	if err != nil {
		t.Fatalf("RenderDateStamped: %v", err)
	}
	_, yBytes, _, err := RenderYesterday(
		context.Background(),
		SliceSource{Items: events},
		YesterdayWindow(closedDay, generatedAt, providers, time.UTC),
	)
	if err != nil {
		t.Fatalf("RenderYesterday: %v", err)
	}
	if got, want := bodyAfterFrontmatter(t, dsBytes), bodyAfterFrontmatter(t, yBytes); got != want {
		t.Fatalf("body sections diverged between date-stamped and yesterday renderers\n--- date-stamped ---\n%s\n--- yesterday ---\n%s", got, want)
	}
}

func TestDigestPathTaxonomyIncludesDateStampedFiles(t *testing.T) {
	for _, path := range []string{
		"digests/today.md",
		"/digests/yesterday.md",
		"digests/2026-05-12.md",
		"digests/last-week.md",
		"/digests/last-week.md",
	} {
		if !IsDigestPath(path) {
			t.Fatalf("%q should be recognized as a digest path", path)
		}
	}
	for _, path := range []string{
		"digests/2026-02-30.md",
		"digests/2026-05-12.json",
		"notion/pages/2026-05-12.md",
		"digests/last-weak.md",
		"digests/last-week.txt",
		"digests/last_week.md",
	} {
		if IsDigestPath(path) {
			t.Fatalf("%q should not be recognized as a digest path", path)
		}
	}
}

// TestMaybeCloseDateStampedWindowDefersWithinSameLocalDay pins parent-spec
// acceptance check #1: generating digests during 2026-05-12 local time
// must not yet write digests/2026-05-12.md.
func TestMaybeCloseDateStampedWindowDefersWithinSameLocalDay(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	root := t.TempDir()
	prev := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 12, 23, 59, 0, 0, time.UTC)

	result, err := MaybeCloseDateStampedWindow(
		context.Background(),
		now, prev,
		time.UTC, root,
		SliceSource{Items: events},
		[]string{"github", "linear", "notion"},
		now,
	)
	if err != nil {
		t.Fatalf("MaybeCloseDateStampedWindow: %v", err)
	}
	if result.Written {
		t.Fatalf("date-stamped digest written before local-day boundary crossed")
	}
	if _, err := os.Stat(filepath.Join(root, "digests", "2026-05-12.md")); !os.IsNotExist(err) {
		t.Fatalf("digests/2026-05-12.md should not exist yet: err=%v", err)
	}
}

// TestMaybeCloseDateStampedWindowWritesOnceAtBoundary pins parent-spec
// acceptance check #2: once the local clock crosses 2026-05-13 00:00, a
// regeneration writes digests/2026-05-12.md exactly once. Repeat
// invocation past the boundary is a no-op (O_CREATE|O_EXCL).
func TestMaybeCloseDateStampedWindowWritesOnceAtBoundary(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	root := t.TempDir()
	prev := time.Date(2026, 5, 12, 23, 59, 0, 0, time.UTC)
	now := time.Date(2026, 5, 13, 0, 0, 1, 0, time.UTC)

	first, err := MaybeCloseDateStampedWindow(
		context.Background(),
		now, prev,
		time.UTC, root,
		SliceSource{Items: events},
		[]string{"github", "linear", "notion"},
		now,
	)
	if err != nil {
		t.Fatalf("first MaybeCloseDateStampedWindow: %v", err)
	}
	if !first.Written {
		t.Fatalf("expected date-stamped digest to be written at boundary")
	}
	if first.Path != "digests/2026-05-12.md" {
		t.Fatalf("path = %q, want digests/2026-05-12.md", first.Path)
	}
	localPath := filepath.Join(root, "digests", "2026-05-12.md")
	firstBytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read first digest: %v", err)
	}

	second, err := MaybeCloseDateStampedWindow(
		context.Background(),
		now.Add(time.Minute), prev,
		time.UTC, root,
		SliceSource{Items: events},
		[]string{"github", "linear", "notion"},
		now.Add(time.Minute),
	)
	if err != nil {
		t.Fatalf("second MaybeCloseDateStampedWindow: %v", err)
	}
	if second.Written {
		t.Fatalf("expected second invocation past the boundary to be a no-op")
	}
	secondBytes, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read second digest: %v", err)
	}
	if !bytes.Equal(secondBytes, firstBytes) {
		t.Fatalf("closed-window digest changed under second invocation")
	}
}

// TestIsDigestPathFiltersOutDateStampedSourceEvents pins the
// non-recursion contract from
// .claude/rules/relayfile-integration-digests.md: watcher events for
// digest artifacts must not enter the digest regeneration source-event
// set. In this repo IsDigestPath is the predicate the cloud regen
// pipeline applies; this test demonstrates the filter drops the
// date-stamped path while keeping unrelated provider events.
func TestIsDigestPathFiltersOutDateStampedSourceEvents(t *testing.T) {
	type fsEvent struct{ Path string }
	candidates := []fsEvent{
		{Path: "digests/2026-05-12.md"},
		{Path: "digests/today.md"},
		{Path: "digests/yesterday.md"},
		{Path: "github/repos/AgentWorkforce__workforce/pulls/by-id/92.json"},
		{Path: "notion/pages/2026-05-12.md"},
	}
	var sourceEvents []fsEvent
	for _, ev := range candidates {
		if IsDigestPath(ev.Path) {
			continue
		}
		sourceEvents = append(sourceEvents, ev)
	}
	if len(sourceEvents) != 2 {
		t.Fatalf("expected 2 source events after digest filter, got %d: %+v", len(sourceEvents), sourceEvents)
	}
	for _, ev := range sourceEvents {
		if IsDigestPath(ev.Path) {
			t.Fatalf("digest path %q leaked past the filter into the regen source set", ev.Path)
		}
	}
}

func TestDeterminism(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	w := Window{
		Date:        time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		Cover:       "yesterday",
		GeneratedAt: time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		Providers:   []string{"github", "linear", "notion"},
		TZ:          time.UTC,
	}
	rep1, err := Run(context.Background(), SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("Run #1: %v", err)
	}
	rep2, err := Run(context.Background(), SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("Run #2: %v", err)
	}
	if !bytes.Equal(Render(rep1), Render(rep2)) {
		t.Fatal("two Run calls produced different output")
	}
}

// emptyWindowExpected mirrors the contract: zero events, all configured
// providers rendered with `_no activity_`. Kept inline so the testdata
// directory only contains the files declared in the child lead plan §5.
const emptyWindowExpected = `---
date: 2026-05-12
generated_at: 2026-05-13T00:00:00Z
covers: yesterday
providers: [github, linear, notion]
events: 0
---

# Activity summary for 2026-05-12

## github

_no activity_

## linear

_no activity_

## notion

_no activity_
`

func TestEmptyWindow(t *testing.T) {
	w := Window{
		Date:        time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		Cover:       "yesterday",
		GeneratedAt: time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		Providers:   []string{"github", "linear", "notion"},
		TZ:          time.UTC,
	}
	rep, err := Run(context.Background(), SliceSource{Items: nil}, w)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Meta.Events != 0 {
		t.Fatalf("events: got %d want 0", rep.Meta.Events)
	}
	for _, s := range rep.Sections {
		if len(s.Bullets) != 0 {
			t.Fatalf("provider %s: bullets %d want 0", s.Provider, len(s.Bullets))
		}
	}
	got := Render(rep)
	if string(got) != emptyWindowExpected {
		t.Fatalf("empty mismatch\n--- got ---\n%s\n--- want ---\n%s", got, emptyWindowExpected)
	}
}

func TestEventFromUnlistedProviderStillRenders(t *testing.T) {
	// `Providers` enumerates the adapters connected at digest time. The
	// generator must still render any event that actually arrived in the
	// window, even from a provider not declared in Providers, so the
	// frontmatter `events` count stays consistent with the visible
	// bullets and operators don't see "47 events" with only some of them
	// listed. Upstream filtering of which adapters export `digest()` is
	// the layer that decides participation; once an event reaches Run,
	// it MUST be rendered.
	events := []ChangeEvent{{
		Provider:      "slack",
		Timestamp:     time.Date(2026, 5, 12, 8, 0, 0, 0, time.UTC),
		Identifier:    "C123",
		Verb:          "messaged",
		CanonicalPath: "slack/channels/C123.json",
	}}
	w := Window{
		Date:        time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		Cover:       "yesterday",
		GeneratedAt: time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		Providers:   []string{"github"},
		TZ:          time.UTC,
	}
	rep, err := Run(context.Background(), SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var slackSection *DigestSection
	for i := range rep.Sections {
		if rep.Sections[i].Provider == "slack" {
			slackSection = &rep.Sections[i]
			break
		}
	}
	if slackSection == nil {
		t.Fatal("expected slack section for slack event")
	}
	if len(slackSection.Bullets) != 1 {
		t.Fatalf("slack bullets = %d, want 1", len(slackSection.Bullets))
	}
	var found bool
	for _, p := range rep.Meta.Providers {
		if p == "slack" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("slack missing from frontmatter providers")
	}
	if rep.Meta.Events != len(events) {
		t.Fatalf("meta.events = %d, want %d", rep.Meta.Events, len(events))
	}
}

// TestYesterday_HeaderKeys pins parent-spec gate D: the rendered
// `yesterday.md` frontmatter contains exactly the agreed key set and no
// rejected keys (no `window`, no blockquote-style header). The test parses
// the YAML header naively so a future renderer that adds an unrelated key
// fails this assertion immediately, before any golden-fixture rebaseline.
func TestYesterday_HeaderKeys(t *testing.T) {
	events := loadEventsJSONL(t, "testdata/events.jsonl")
	w := YesterdayWindow(
		time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		[]string{"github", "linear", "notion"},
		time.UTC,
	)
	_, content, _, err := RenderYesterday(context.Background(), SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("RenderYesterday: %v", err)
	}
	keys := frontmatterKeys(t, content)
	want := []string{"date", "generated_at", "covers", "providers", "events"}
	if !equalStringSlice(keys, want) {
		t.Fatalf("yesterday frontmatter keys = %v, want exactly %v (no extra keys, no missing keys, in order)", keys, want)
	}
	for _, rejected := range []string{"window", "title", "summary"} {
		for _, k := range keys {
			if k == rejected {
				t.Fatalf("yesterday frontmatter contains rejected key %q (Work Item A)", rejected)
			}
		}
	}
	if bytes.HasPrefix(content, []byte(">")) {
		t.Fatal("yesterday output starts with blockquote header — must be YAML frontmatter (Work Item A)")
	}
}

// TestYesterday_Determinism_Empty pins gate C for the empty-events path:
// two close-window runs with zero events produce byte-equal output. The
// `_no activity_` rendering must be stable across runs.
func TestYesterday_Determinism_Empty(t *testing.T) {
	w := YesterdayWindow(
		time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		[]string{"github", "linear", "notion"},
		time.UTC,
	)
	rep1, err := RunClosing(context.Background(), SliceSource{Items: nil}, w)
	if err != nil {
		t.Fatalf("Run empty #1: %v", err)
	}
	rep2, err := RunClosing(context.Background(), SliceSource{Items: nil}, w)
	if err != nil {
		t.Fatalf("Run empty #2: %v", err)
	}
	if !bytes.Equal(Render(rep1), Render(rep2)) {
		t.Fatal("empty closing window produced different bytes across runs")
	}
	if rep1.Meta.Events != 0 {
		t.Fatalf("empty: events=%d want 0", rep1.Meta.Events)
	}
}

// TestYesterday_Determinism_Exhaustive pins gate C for the high-volume path:
// two runs over the same oversize event set produce byte-equal output without
// truncating or warning. Self-host relayfile digests are exhaustive; any future
// cloud-side cap belongs outside this shared renderer.
func TestYesterday_Determinism_Exhaustive(t *testing.T) {
	const overCap = DigestCoverageCap + 25
	events := make([]ChangeEvent, 0, overCap)
	for i := 0; i < overCap; i++ {
		events = append(events, ChangeEvent{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 12, 12, 0, i, 0, time.UTC),
			Identifier:    formatNumeric("PR-", i),
			Verb:          "updated",
			CanonicalPath: formatNumeric("github/repos/x/pulls/by-id/", i) + ".json",
		})
	}
	w := YesterdayWindow(
		time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		[]string{"github"},
		time.UTC,
	)
	rep1, err := RunClosing(context.Background(), SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("Run exhaustive #1: %v", err)
	}
	rep2, err := RunClosing(context.Background(), SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("Run exhaustive #2: %v", err)
	}
	if !bytes.Equal(Render(rep1), Render(rep2)) {
		t.Fatal("high-volume closing window produced different bytes across runs")
	}
	if rep1.Meta.Events != overCap {
		t.Fatalf("Meta.Events = %d, want %d (self-host closing digests are exhaustive)", rep1.Meta.Events, overCap)
	}
	assertNoDigestTruncationWarning(t, rep1.Meta.Warnings)
	rendered := Render(rep1)
	if !bytes.Contains(rendered, []byte("PR-524")) {
		t.Fatalf("rendered digest missing final over-cap bullet:\n%s", rendered)
	}
}

// TestCoverage_OverCap pins gate F for self-host relayfile: when more than
// 500 events arrive for a closed window, both yesterday and date-stamped
// digests keep every event and do not emit truncation warnings.
func TestCoverage_OverCap(t *testing.T) {
	const overCap = DigestCoverageCap + 7
	events := make([]ChangeEvent, 0, overCap)
	for i := 0; i < overCap; i++ {
		events = append(events, ChangeEvent{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 12, 12, 0, i, 0, time.UTC),
			Identifier:    formatNumeric("PR-", i),
			Verb:          "updated",
			CanonicalPath: formatNumeric("github/repos/x/pulls/by-id/", i) + ".json",
		})
	}
	w := YesterdayWindow(
		time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		[]string{"github"},
		time.UTC,
	)
	rep, err := RunClosing(context.Background(), SliceSource{Items: events}, w)
	if err != nil {
		t.Fatalf("RunClosing: %v", err)
	}

	if rep.Meta.Events != overCap {
		t.Fatalf("Meta.Events = %d, want %d (self-host closing digests are exhaustive)", rep.Meta.Events, overCap)
	}
	assertNoDigestTruncationWarning(t, rep.Meta.Warnings)
	rendered := Render(rep)
	if bytes.Contains(rendered, []byte(WarningCoverageTruncated)) {
		t.Fatalf("rendered frontmatter unexpectedly contains warning string %q:\n%s", WarningCoverageTruncated, rendered)
	}
	if !bytes.Contains(rendered, []byte("PR-0")) || !bytes.Contains(rendered, []byte("PR-506")) {
		t.Fatalf("rendered yesterday digest missing first or last over-cap bullet:\n%s", rendered)
	}

	dateWindow := DateStampedWindow(
		time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
		[]string{"github"},
		time.UTC,
	)
	_, dateRendered, dateRep, err := RenderDateStamped(context.Background(), SliceSource{Items: events}, dateWindow)
	if err != nil {
		t.Fatalf("RenderDateStamped: %v", err)
	}
	if dateRep.Meta.Events != overCap {
		t.Fatalf("date-stamped Meta.Events = %d, want %d", dateRep.Meta.Events, overCap)
	}
	assertNoDigestTruncationWarning(t, dateRep.Meta.Warnings)
	if !bytes.Contains(dateRendered, []byte("PR-0")) || !bytes.Contains(dateRendered, []byte("PR-506")) {
		t.Fatalf("rendered date-stamped digest missing first or last over-cap bullet:\n%s", dateRendered)
	}

	// The WarningCoverageTruncated constant remains stable for downstream
	// parsers even though this shared renderer does not emit it.
	if WarningCoverageTruncated != "digest.coverage.truncated" {
		t.Fatalf("WarningCoverageTruncated = %q, want stable string digest.coverage.truncated", WarningCoverageTruncated)
	}
}

// TestYesterday_Determinism_OverCap pins gate C over an oversize input set.
// Renamed from the original review-mandated `TestYesterday_Determinism_Warnings`
// because the shared renderer is exhaustive: no truncation warning fires, so
// the `Warnings` suffix was misleading. The current name reflects the actual
// contract (determinism above the historical cap, no truncation).
func TestYesterday_Determinism_OverCap(t *testing.T) {
	TestYesterday_Determinism_Exhaustive(t)
}

// TestYesterday_OverCapExhaustive pins gate F for self-host relayfile.
// Renamed from the original review-mandated `TestYesterday_CoverageCapWarning`
// because the shared renderer is exhaustive: every event over `DigestCoverageCap`
// is still emitted and no `digest.coverage.truncated` warning fires. The
// historical name implied the opposite contract.
func TestYesterday_OverCapExhaustive(t *testing.T) {
	TestCoverage_OverCap(t)
}

func assertNoDigestTruncationWarning(t *testing.T, warnings []string) {
	t.Helper()
	for _, warn := range warnings {
		if strings.Contains(warn, "digest.truncated") || strings.Contains(warn, WarningCoverageTruncated) {
			t.Fatalf("unexpected digest truncation warning: %q", warn)
		}
	}
}

// TestYesterday_TZBoundary pins gate A explicitly: workspace TZ
// America/New_York, event at 2026-05-13T03:30:00Z (== 2026-05-12T23:30 EDT),
// close run at 2026-05-13T04:05:00Z (== 2026-05-13T00:05 EDT). The produced
// yesterday.md must have date=2026-05-12 and contain the late event.
func TestYesterday_TZBoundary(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("America/New_York tz not available: %v", err)
	}
	root := t.TempDir()
	events := []ChangeEvent{
		{
			Provider:      "github",
			Timestamp:     time.Date(2026, 5, 13, 3, 30, 0, 0, time.UTC), // 23:30 EDT 2026-05-12
			Identifier:    "PR-late",
			Verb:          "updated",
			CanonicalPath: "github/repos/x/pulls/by-id/late.json",
		},
	}
	now := time.Date(2026, 5, 13, 4, 5, 0, 0, time.UTC) // 00:05 EDT 2026-05-13
	res, err := CloseLocalDay(context.Background(), root, SliceSource{Items: events}, now, []string{"github"}, ny)
	if err != nil {
		t.Fatalf("CloseLocalDay: %v", err)
	}
	if !res.Written {
		t.Fatal("CloseLocalDay did not write")
	}
	if res.Report.Meta.Date != "2026-05-12" {
		t.Fatalf("date = %q, want 2026-05-12 (workspace-local boundary in America/New_York)", res.Report.Meta.Date)
	}
	if res.Report.Meta.Covers != "yesterday" {
		t.Fatalf("covers = %q, want yesterday", res.Report.Meta.Covers)
	}
	body, err := os.ReadFile(filepath.Join(root, "digests", "yesterday.md"))
	if err != nil {
		t.Fatalf("read yesterday.md: %v", err)
	}
	if !bytes.Contains(body, []byte("PR-late")) {
		t.Fatalf("yesterday.md missing the 23:30-EDT bullet:\n%s", body)
	}
	if !bytes.Contains(body, []byte("date: 2026-05-12")) {
		t.Fatalf("yesterday.md missing date: 2026-05-12 frontmatter:\n%s", body)
	}
}

// frontmatterKeys returns the YAML keys (in order) from a `---\n...\n---\n`
// frontmatter block. Naive parser sufficient for the digest contract where
// keys are always at column 0 and values fit on one line.
func frontmatterKeys(t *testing.T, content []byte) []string {
	t.Helper()
	s := string(content)
	const sep = "---\n"
	if !strings.HasPrefix(s, sep) {
		t.Fatalf("content does not start with YAML frontmatter")
	}
	rest := s[len(sep):]
	end := strings.Index(rest, sep)
	if end < 0 {
		t.Fatalf("missing frontmatter terminator")
	}
	var keys []string
	for _, line := range strings.Split(rest[:end], "\n") {
		if line == "" || strings.HasPrefix(line, " ") || strings.HasPrefix(line, "-") {
			continue
		}
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		keys = append(keys, line[:colon])
	}
	return keys
}

func equalStringSlice(a, b []string) bool {
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

// formatNumeric is a tiny helper for deterministic event IDs without
// pulling fmt into hot paths the tests rerun a lot.
func formatNumeric(prefix string, n int) string {
	digits := ""
	if n == 0 {
		digits = "0"
	}
	for n > 0 {
		digits = string(rune('0'+(n%10))) + digits
		n /= 10
	}
	return prefix + digits
}
