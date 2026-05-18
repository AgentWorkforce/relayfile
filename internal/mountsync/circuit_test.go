package mountsync

import (
	"errors"
	"testing"
	"time"
)

// TestCircuitTripsAndCools verifies the open/cooldown/closed transitions.
func TestCircuitTripsAndCools(t *testing.T) {
	t.Setenv("RELAYFILE_CB_WINDOW", "1m")
	t.Setenv("RELAYFILE_CB_THRESHOLD", "3")
	t.Setenv("RELAYFILE_CB_COOLDOWN", "10s")

	c := NewCloudErrorCircuit()
	clock := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	c.SetClock(func() time.Time { return clock })

	if c.IsOpen() {
		t.Fatalf("breaker should start closed")
	}

	// Three failures within the window should trip.
	for i := 0; i < 2; i++ {
		if tripped := c.RecordFailure(); tripped {
			t.Fatalf("breaker tripped too early after %d failures", i+1)
		}
	}
	if !c.RecordFailure() {
		t.Fatalf("expected third failure to trip the breaker")
	}
	if !c.IsOpen() {
		t.Fatalf("breaker should be open after threshold reached")
	}

	// Half-open after cooldown advances.
	clock = clock.Add(11 * time.Second)
	if c.IsOpen() {
		t.Fatalf("breaker should half-open after cooldown")
	}

	// A success during half-open closes it.
	c.RecordSuccess()
	if c.IsOpen() {
		t.Fatalf("breaker should be closed after cooldown + success")
	}

	snap := c.Snapshot()
	if snap.OpenEvents != 1 {
		t.Fatalf("expected OpenEvents=1, got %d", snap.OpenEvents)
	}
}

func TestIsCloudFailureStatus(t *testing.T) {
	cases := map[int]bool{
		200: false,
		404: false,
		408: true,
		429: true,
		500: true,
		503: true,
		504: true,
	}
	for code, want := range cases {
		if got := IsCloudFailureStatus(code); got != want {
			t.Fatalf("status %d: want %v got %v", code, want, got)
		}
	}
}

func TestIsCloudFailureError(t *testing.T) {
	if !IsCloudFailureError(errors.New("connection reset by peer")) {
		t.Fatalf("expected connection reset to be cloud failure")
	}
	if !IsCloudFailureError(errors.New("i/o timeout")) {
		t.Fatalf("expected timeout to be cloud failure")
	}
	if IsCloudFailureError(errors.New("malformed payload")) {
		t.Fatalf("expected malformed payload not to be cloud failure")
	}
	if IsCloudFailureError(nil) {
		t.Fatalf("nil should never be a failure")
	}
}
