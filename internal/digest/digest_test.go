package digest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
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
	defer f.Close()
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
