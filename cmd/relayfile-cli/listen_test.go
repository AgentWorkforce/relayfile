package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMatchListenPath(t *testing.T) {
	cases := []struct {
		glob      string
		eventPath string
		want      bool
	}{
		// Empty glob passes everything.
		{"", "/linear/issues/AR-1.json", true},
		// Provider glob.
		{"/linear/**", "/linear/issues/AR-1.json", true},
		{"/linear/**", "/linear/issues/by-state/backlog/AR-1.json", true},
		{"/linear/**", "/linear", true},
		{"/linear/**", "/github/repos/foo.json", false},
		{"/linear/**", "/digests/today.md", false},
		// Exact single-star glob via path.Match.
		{"/linear/issues/*.json", "/linear/issues/AR-1.json", true},
		{"/linear/issues/*.json", "/linear/issues/by-state/backlog/AR-1.json", false},
		// Double-star catch-all.
		{"**", "/anything/at/all", true},
		{"/**", "/anything/at/all", true},
		// Other providers.
		{"/github/**", "/github/repos/foo.json", true},
		{"/github/**", "/linear/issues/AR-1.json", false},
	}
	for _, c := range cases {
		got := matchListenPath(c.glob, c.eventPath)
		if got != c.want {
			t.Errorf("matchListenPath(%q, %q) = %v, want %v", c.glob, c.eventPath, got, c.want)
		}
	}
}

func TestWsEncodeGlob(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"/linear/**", "/linear/**"},
		{"/linear/issues/*.json", "/linear/issues/*.json"},
		{"/github/**", "/github/**"},
		// Spaces should be encoded.
		{"/foo bar/**", "/foo%20bar/**"},
	}
	for _, c := range cases {
		got := wsEncodeGlob(c.input)
		if got != c.want {
			t.Errorf("wsEncodeGlob(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

func TestListenRunDuplicateSuppressorSuppressesSameContentHashWithinWindow(t *testing.T) {
	suppressor := newListenRunDuplicateSuppressor(2 * time.Second)
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	evt := listenEvent{
		Type:        "file.updated",
		Path:        "/linear/issues/by-state/in-planning/AR-322.json",
		ContentHash: "sha256:a",
	}

	if suppressor.shouldSuppress(evt, now) {
		t.Fatal("first event should not be suppressed")
	}
	if !suppressor.shouldSuppress(evt, now.Add(time.Second)) {
		t.Fatal("duplicate event within window should be suppressed")
	}
	if suppressor.shouldSuppress(evt, now.Add(4*time.Second)) {
		t.Fatal("duplicate event after window should not be suppressed")
	}
}

func TestListenRunDuplicateSuppressorUsesFixedWindow(t *testing.T) {
	suppressor := newListenRunDuplicateSuppressor(2 * time.Second)
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	evt := listenEvent{
		Type:        "file.updated",
		Path:        "/linear/issues/by-state/in-planning/AR-322.json",
		ContentHash: "sha256:a",
	}

	if suppressor.shouldSuppress(evt, now) {
		t.Fatal("first event should not be suppressed")
	}
	if !suppressor.shouldSuppress(evt, now.Add(time.Second)) {
		t.Fatal("duplicate event within window should be suppressed")
	}
	if suppressor.shouldSuppress(evt, now.Add(2500*time.Millisecond)) {
		t.Fatal("duplicate event after original fixed window should not be suppressed")
	}
}

func TestListenRunDuplicateSuppressorSweepsPeriodically(t *testing.T) {
	suppressor := newListenRunDuplicateSuppressor(2 * time.Second)
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)

	for i := 0; i < listenRunDuplicateSweepInterval-1; i++ {
		evt := listenEvent{
			Type:        "file.updated",
			Path:        "/linear/issues/by-state/in-planning/AR-322.json",
			ContentHash: "sha256:" + strconv.Itoa(i),
		}
		if suppressor.shouldSuppress(evt, now) {
			t.Fatalf("first event %d should not be suppressed", i)
		}
	}
	if len(suppressor.seen) != listenRunDuplicateSweepInterval-1 {
		t.Fatalf("expected unswept keys to remain, got %d", len(suppressor.seen))
	}

	evt := listenEvent{
		Type:        "file.updated",
		Path:        "/linear/issues/by-state/in-planning/AR-322.json",
		ContentHash: "sha256:trigger",
	}
	if suppressor.shouldSuppress(evt, now.Add(3*time.Second)) {
		t.Fatal("first trigger event should not be suppressed")
	}
	if len(suppressor.seen) != 1 {
		t.Fatalf("expected periodic sweep to retain only current key, got %d", len(suppressor.seen))
	}
}

func TestListenRunDuplicateSuppressorAllowsDifferentContentHashes(t *testing.T) {
	suppressor := newListenRunDuplicateSuppressor(2 * time.Second)
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	first := listenEvent{
		Type:        "file.updated",
		Path:        "/linear/issues/by-state/in-planning/AR-322.json",
		ContentHash: "sha256:a",
	}
	second := first
	second.ContentHash = "sha256:b"

	if suppressor.shouldSuppress(first, now) {
		t.Fatal("first event should not be suppressed")
	}
	if suppressor.shouldSuppress(second, now.Add(time.Second)) {
		t.Fatal("changed content hash should not be suppressed")
	}
}

func TestListenRunDuplicateSuppressorFallsBackToCorrelationID(t *testing.T) {
	suppressor := newListenRunDuplicateSuppressor(2 * time.Second)
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	evt := listenEvent{
		Type:          "file.updated",
		Path:          "/linear/issues/by-state/in-planning/AR-322.json",
		CorrelationID: "corr_1",
	}

	if suppressor.shouldSuppress(evt, now) {
		t.Fatal("first event should not be suppressed")
	}
	if !suppressor.shouldSuppress(evt, now.Add(time.Second)) {
		t.Fatal("same correlation ID should be suppressed when content hash is absent")
	}

	next := evt
	next.CorrelationID = "corr_2"
	if suppressor.shouldSuppress(next, now.Add(time.Second)) {
		t.Fatal("different correlation ID should not be suppressed")
	}
}

func TestListenRunDuplicateKeyRequiresStableIdentity(t *testing.T) {
	key := listenRunDuplicateKey(listenEvent{
		Type: "file.updated",
		Path: "/linear/issues/by-state/in-planning/AR-322.json",
	})
	if key != "" {
		t.Fatalf("expected no key without content hash or correlation ID, got %q", key)
	}

	key = listenRunDuplicateKey(listenEvent{
		Type:        "file.updated",
		Path:        "/linear/issues/by-state/in-planning/AR-322.json",
		ContentHash: "sha256:a",
	})
	if !strings.Contains(key, "hash:sha256:a") {
		t.Fatalf("expected content hash in duplicate key, got %q", key)
	}
}
