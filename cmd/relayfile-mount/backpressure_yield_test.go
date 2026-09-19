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

// Devin and Cursor both flagged this independently on PR #511, and they were
// right: marking a 429 `yielded` is only half the story. finishInitialBootstrap
// treats a yielded first cycle with nothing in progress as a COMPLETED
// bootstrap, because the only other thing that yields — a per-cycle deadline —
// cannot reach that state without a persisted checkpoint. A 429 can: it may
// arrive before the very first saveState. Falling through would exit 0 and hand
// Cloud an empty mirror it believes is fully synced, which is worse than the
// hard failure this PR set out to fix, because it fails silently.
func TestColdBackpressureIsNotReportedAsACompletedBootstrap(t *testing.T) {
	busy := &cycleOutcomeError{
		cause: &mountsync.HTTPError{
			StatusCode: http.StatusTooManyRequests,
			Code:       "workspace_busy",
			Message:    "workspace durable object is busy; retry after the advertised delay",
		},
		yielded:      true,
		backpressure: true,
	}

	if !cycleYielded(busy) {
		t.Fatalf("a 429 must still yield, so it is not a terminal cycle failure")
	}
	if !cycleBackpressure(busy) {
		t.Fatalf("a 429 yield must be distinguishable from a deadline yield")
	}

	// A deadline yield must NOT be mistaken for backpressure: it reaches
	// finishInitialBootstrap only with a checkpoint on disk, where completing is
	// the correct outcome.
	deadline := &cycleOutcomeError{cause: context.DeadlineExceeded, yielded: true}
	if cycleBackpressure(deadline) {
		t.Fatalf("a deadline yield must not be treated as server backpressure")
	}
	if !cycleYielded(deadline) {
		t.Fatalf("a deadline yield must still count as yielded")
	}
}

// The resumable outcome is what makes the cold case safe: it exits
// initialBootstrapIncompleteExitCode so the caller reruns us, rather than
// exit 1 (fatal, the original bug) or exit 0 (silently empty, the regression).
func TestResumableIncompleteExitsRetryableUnderOnce(t *testing.T) {
	resumable := newResumableInitialBootstrapIncompleteError(
		bootstrapResumeState{},
		"initial cycle yielded to server backpressure before any bootstrap progress",
		errors.New("http 429 workspace_busy"),
	)

	if got := mountProcessExitCode(mountConfig{once: true}, resumable); got != initialBootstrapIncompleteExitCode {
		t.Fatalf("cold backpressure must exit retryable, got %d", got)
	}
	terminal := newInitialBootstrapIncompleteError(
		bootstrapResumeState{},
		"initial cycle failed",
		errors.New("http 500"),
	)
	if got := mountProcessExitCode(mountConfig{once: true}, terminal); got != 1 {
		t.Fatalf("a real failure must stay fatal, got %d", got)
	}
}
