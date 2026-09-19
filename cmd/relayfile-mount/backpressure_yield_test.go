package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/agentworkforce/relayfile/internal/mountsync"
)

// A busy workspace answers 429 workspace_busy with an advertised Retry-After.
// Classifying that as a cycle FAILURE made the first cycle of an initial
// bootstrap fatal, so the run died roughly 35s into a 210s budget having synced
// zero files — surfacing to Cloud as BootstrapFailedError. That accounted for 50
// of 62 proactive mount-bootstrap failures over three days in production. The
// retry budget already existed; the cycle only had to yield so the ticker could
// use it.
func TestBackpressureIsAYieldNotACycleFailure(t *testing.T) {
	busy := &mountsync.HTTPError{
		StatusCode: http.StatusTooManyRequests,
		Code:       "workspace_busy",
		Message:    "workspace durable object is busy; retry after the advertised delay",
	}

	if !isBackpressureError(busy) {
		t.Fatalf("429 workspace_busy must be recognised as backpressure")
	}
	if !cycleYielded(&cycleOutcomeError{cause: busy, yielded: true}) {
		t.Fatalf("a backpressure cycle must report as yielded so the bootstrap resumes")
	}
	// Wrapped the way the syncer actually returns it.
	if !isBackpressureError(fmt.Errorf("mount sync cycle: %w", busy)) {
		t.Fatalf("backpressure must be detected through error wrapping")
	}
}

// Deliberately narrow. A 5xx is the server being BROKEN, not busy, and a
// context deadline is handled by its own pre-existing branch. Treating either
// as backpressure would let a genuinely unhealthy backend look like a queue and
// spin the bootstrap for its whole budget instead of failing loudly.
func TestOnlyTooManyRequestsCountsAsBackpressure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"500", &mountsync.HTTPError{StatusCode: http.StatusInternalServerError, Message: "boom"}},
		{"503", &mountsync.HTTPError{StatusCode: http.StatusServiceUnavailable, Message: "down"}},
		{"403", &mountsync.HTTPError{StatusCode: http.StatusForbidden, Message: "nope"}},
		{"deadline", context.DeadlineExceeded},
		{"plain", errors.New("something else")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if isBackpressureError(tc.err) {
				t.Fatalf("%v must not be treated as server backpressure", tc.err)
			}
		})
	}
}
