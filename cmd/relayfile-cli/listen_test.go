package main

import "testing"

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
