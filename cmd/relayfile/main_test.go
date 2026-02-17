package main

import (
	"os"
	"testing"
	"time"
)

func TestIntEnvParsesValue(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_INT", "42")
	got := intEnv("RELAYFILE_TEST_INT", 7)
	if got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}
}

func TestIntEnvFallsBackOnInvalidValue(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_INT_BAD", "not-a-number")
	got := intEnv("RELAYFILE_TEST_INT_BAD", 7)
	if got != 7 {
		t.Fatalf("expected fallback 7, got %d", got)
	}
}

func TestDurationEnvParsesValue(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_DURATION", "150ms")
	got := durationEnv("RELAYFILE_TEST_DURATION", time.Second)
	if got != 150*time.Millisecond {
		t.Fatalf("expected 150ms, got %s", got)
	}
}

func TestDurationEnvFallsBackOnInvalidValue(t *testing.T) {
	t.Setenv("RELAYFILE_TEST_DURATION_BAD", "soon")
	got := durationEnv("RELAYFILE_TEST_DURATION_BAD", 2*time.Second)
	if got != 2*time.Second {
		t.Fatalf("expected fallback 2s, got %s", got)
	}
}

func TestEnvHelpersUseFallbackWhenUnset(t *testing.T) {
	_ = os.Unsetenv("RELAYFILE_TEST_INT_UNSET")
	_ = os.Unsetenv("RELAYFILE_TEST_DURATION_UNSET")

	if got := intEnv("RELAYFILE_TEST_INT_UNSET", 9); got != 9 {
		t.Fatalf("expected fallback 9, got %d", got)
	}
	if got := durationEnv("RELAYFILE_TEST_DURATION_UNSET", 3*time.Second); got != 3*time.Second {
		t.Fatalf("expected fallback 3s, got %s", got)
	}
}

