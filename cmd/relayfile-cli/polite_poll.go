package main

import (
	"context"
	"errors"
	"math"
	mathrand "math/rand/v2"
	"time"
)

// Polling discipline for the mount daemon's status / ingress pollers.
//
// On 2026-05-14 a single relayfile mount client entered a tight loop hitting
// /v1/workspaces/.../sync/status at 30+ req/sec for hours. That saturated the
// production AWS Lambda 10-slot concurrency cap on the cloud control-plane and
// 429'd unrelated users. The old waiter loops did `time.Sleep(2 * time.Second)`
// or nothing at all between calls and had no error backoff, no jitter, and no
// hard rate ceiling — so on a fast network they were effectively unbounded.
//
// These constants apply to every polite-poll caller below. Tune them here if a
// future maintainer needs to change the floor / ceiling globally.
const (
	// syncStatusMinPollInterval is the *minimum* gap between two successful
	// polls of the same endpoint. Even if the previous call returned in 50ms,
	// the next call will not fire before this much wall-clock has elapsed.
	syncStatusMinPollInterval = 5 * time.Second

	// syncStatusMaxPollInterval caps the exponential backoff on errors. After
	// several consecutive failures the loop tops out at this gap and stays
	// there until a successful response resets it.
	syncStatusMaxPollInterval = 60 * time.Second

	// syncStatusJitterFraction is the +/- fraction of the next-poll delay
	// that gets randomized per iteration. 0.2 == +/-20%. This avoids the
	// thundering-herd pattern where many daemons that started at the same
	// time (e.g. after a deploy) keep hitting the API in lockstep.
	syncStatusJitterFraction = 0.2

	// syncStatusHardMaxPerSecond is the absolute upper bound on poll rate per
	// (workspace, endpoint). A safety net so that a future code change that
	// accidentally lowers minPollInterval still cannot produce a >1 rps
	// busy-loop. Expressed as requests-per-second.
	syncStatusHardMaxPerSecond = 1
)

// politeOpts controls the timing of a politePoll loop. Defaults match the
// syncStatus* constants above; tests can override them to keep the loop short.
type politeOpts struct {
	minInterval      time.Duration
	maxInterval      time.Duration
	jitterFraction   float64
	hardMaxPerSecond int

	// now / sleep are injectable for tests. Production code leaves them nil
	// and politePoll falls back to the real clock.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

func (o politeOpts) withDefaults() politeOpts {
	if o.minInterval <= 0 {
		o.minInterval = syncStatusMinPollInterval
	}
	if o.maxInterval <= 0 {
		o.maxInterval = syncStatusMaxPollInterval
	}
	if o.maxInterval < o.minInterval {
		o.maxInterval = o.minInterval
	}
	if o.jitterFraction < 0 {
		o.jitterFraction = 0
	}
	if o.jitterFraction > 1 {
		o.jitterFraction = 1
	}
	if o.hardMaxPerSecond <= 0 {
		o.hardMaxPerSecond = syncStatusHardMaxPerSecond
	}
	if o.now == nil {
		o.now = time.Now
	}
	if o.sleep == nil {
		o.sleep = sleepCtx
	}
	return o
}

// pollResult is what a politePoll caller returns from each iteration.
//
//   - done == true: the poll succeeded *and* the caller is satisfied; politePoll
//     returns nil immediately.
//   - err != nil and httpStatus == 0: treat as a network error; back off
//     exponentially up to maxInterval.
//   - err != nil and httpStatus in [400,500): treat the same as 5xx (don't
//     hammer a broken endpoint). Exception: 429 with a Retry-After header is
//     honored exactly (clamped to [minInterval, maxInterval]).
//   - err != nil and httpStatus >= 500: exponential backoff.
//   - err == nil and done == false: keep polling; next call fires after
//     minInterval + jitter.
type pollResult struct {
	done       bool
	err        error
	httpStatus int
	// retryAfter is the server-suggested wait before the next request, taken
	// from a 429 Retry-After header. Zero means "no hint, use the regular
	// backoff schedule".
	retryAfter time.Duration
}

// pollFunc is the per-iteration callback. Returning done=true ends the loop.
type pollFunc func(ctx context.Context) pollResult

// politePoll runs fn in a loop, sleeping between iterations according to opts.
//
// It enforces:
//
//   - A minimum delay between iterations (minInterval). Even instant-returning
//     successful polls do not fire faster than this.
//   - Exponential backoff on error, capped at maxInterval. Backoff resets to
//     minInterval on any successful (err == nil) result.
//   - +/- jitterFraction randomization per iteration.
//   - A hard floor of 1/hardMaxPerSecond between *any* two iterations, even
//     across error/success transitions. Belt-and-suspenders for the case where
//     a future change accidentally lowers minInterval.
//   - context.Context cancellation: the wait between iterations is
//     interruptible. Callers that want bounded poll lifetime should pass a
//     ctx with a deadline.
//
// The function returns whatever ctx.Err() is when the context is cancelled, or
// nil when fn signals done.
func politePoll(ctx context.Context, fn pollFunc, opts politeOpts) error {
	if fn == nil {
		return errors.New("politePoll: fn is nil")
	}
	o := opts.withDefaults()

	hardMin := time.Second / time.Duration(o.hardMaxPerSecond)
	var consecutiveFailures int
	var lastCallAt time.Time

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Hard rate ceiling: enforce a minimum gap between *any* two calls.
		// This is independent of the regular backoff schedule below; it's the
		// safety net that prevents a future bug from re-creating the
		// 30-rps storm.
		if !lastCallAt.IsZero() {
			if gap := o.now().Sub(lastCallAt); gap < hardMin {
				if err := o.sleep(ctx, hardMin-gap); err != nil {
					return err
				}
			}
		}

		lastCallAt = o.now()
		res := fn(ctx)
		if res.done {
			return nil
		}

		var wait time.Duration
		switch {
		case res.err == nil:
			consecutiveFailures = 0
			wait = o.minInterval
		case res.httpStatus == 429 && res.retryAfter > 0:
			// Honor explicit server hint exactly, but clamp into the
			// [min,max] window so a hostile/buggy header can't pin us at
			// either extreme.
			consecutiveFailures++
			wait = clampDuration(res.retryAfter, o.minInterval, o.maxInterval)
		default:
			// Network error, 4xx (non-429), 5xx, or 429 without a
			// Retry-After hint: exponential backoff.
			consecutiveFailures++
			wait = backoffFor(consecutiveFailures, o.minInterval, o.maxInterval)
		}

		wait = applyJitter(wait, o.jitterFraction)
		if wait < hardMin {
			wait = hardMin
		}
		if err := o.sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// backoffFor returns minInterval * 2^(failures-1), clamped to maxInterval.
// failures=1 → minInterval, failures=2 → 2×, failures=3 → 4×, …
func backoffFor(failures int, minInterval, maxInterval time.Duration) time.Duration {
	if failures < 1 {
		failures = 1
	}
	// Use math.Pow on the multiplier so we don't overflow int64 for large
	// failure counts; clamping below means the actual returned value is
	// bounded.
	mult := math.Pow(2, float64(failures-1))
	d := time.Duration(float64(minInterval) * mult)
	if d <= 0 || d > maxInterval {
		return maxInterval
	}
	if d < minInterval {
		return minInterval
	}
	return d
}

// applyJitter returns d randomized by +/- fraction, with a 1ms floor.
func applyJitter(d time.Duration, fraction float64) time.Duration {
	if d <= 0 || fraction <= 0 {
		return d
	}
	if fraction > 1 {
		fraction = 1
	}
	// mathrand.Float64() is in [0, 1); map to [-fraction, +fraction].
	delta := (mathrand.Float64()*2 - 1) * fraction
	out := time.Duration(float64(d) * (1 + delta))
	if out < time.Millisecond {
		return time.Millisecond
	}
	return out
}

func clampDuration(d, lo, hi time.Duration) time.Duration {
	if d < lo {
		return lo
	}
	if d > hi {
		return hi
	}
	return d
}

// sleepCtx blocks for d or until ctx is cancelled, whichever comes first.
// Returns ctx.Err() if the context fires; nil if the timer fires.
//
// This is the seam that lets `relayfile stop` interrupt a poll wait within
// milliseconds instead of having to wait for the full backoff interval.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
