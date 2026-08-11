package main

import (
	"testing"
	"time"
)

func TestParseDurationWithNegativeOne(t *testing.T) {
	for raw, want := range map[string]time.Duration{
		"-1":  -1,
		"24h": 24 * time.Hour,
	} {
		got, err := parseDurationWithNegativeOne(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if got != want {
			t.Fatalf("parse %q = %s, want %s", raw, got, want)
		}
	}
}
