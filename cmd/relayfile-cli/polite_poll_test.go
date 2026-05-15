package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a minimal injectable clock for the polite-poll tests. It lets
// us assert on the sleeps politePoll issues without actually waiting wall
// clock seconds. The test driver advances `now` whenever it observes a sleep
// call so the rate-limit math inside politePoll sees a consistent timeline.
type fakeClock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if d > 0 {
		c.sleeps = append(c.sleeps, d)
		c.now = c.now.Add(d)
	}
	return nil
}

func newTestOpts(c *fakeClock) politeOpts {
	return politeOpts{
		minInterval:      5 * time.Second,
		maxInterval:      60 * time.Second,
		jitterFraction:   0, // disable jitter so assertions are exact
		hardMaxPerSecond: 1,
		now:              c.Now,
		sleep:            c.Sleep,
	}
}

func TestPolitePoll_SuccessfulPollsSpacedByMinInterval(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	var calls int32
	err := politePoll(context.Background(), func(ctx context.Context) pollResult {
		n := atomic.AddInt32(&calls, 1)
		if n >= 4 {
			return pollResult{done: true}
		}
		return pollResult{}
	}, newTestOpts(clock))
	if err != nil {
		t.Fatalf("politePoll returned err: %v", err)
	}
	if calls != 4 {
		t.Fatalf("expected 4 calls, got %d", calls)
	}
	// 3 sleeps between the 4 polls. Each should be exactly minInterval since
	// jitter is disabled.
	if len(clock.sleeps) != 3 {
		t.Fatalf("expected 3 sleeps, got %d: %v", len(clock.sleeps), clock.sleeps)
	}
	for i, s := range clock.sleeps {
		if s != 5*time.Second {
			t.Errorf("sleep[%d] = %s, want 5s", i, s)
		}
	}
}

func TestPolitePoll_ExponentialBackoffOnErrors(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	netErr := errors.New("connection refused")
	var calls int32
	err := politePoll(context.Background(), func(ctx context.Context) pollResult {
		n := atomic.AddInt32(&calls, 1)
		if n >= 6 {
			return pollResult{done: true}
		}
		return pollResult{err: netErr}
	}, newTestOpts(clock))
	if err != nil {
		t.Fatalf("politePoll returned err: %v", err)
	}
	// 5 backoff sleeps before the 6th call succeeds. Expected sequence:
	// 5s, 10s, 20s, 40s, 60s (capped at maxInterval).
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 60 * time.Second}
	if len(clock.sleeps) != len(want) {
		t.Fatalf("expected %d sleeps, got %d: %v", len(want), len(clock.sleeps), clock.sleeps)
	}
	for i, w := range want {
		if clock.sleeps[i] != w {
			t.Errorf("sleep[%d] = %s, want %s", i, clock.sleeps[i], w)
		}
	}
}

func TestPolitePoll_BackoffResetsAfterSuccess(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	netErr := errors.New("boom")
	var calls int32
	err := politePoll(context.Background(), func(ctx context.Context) pollResult {
		n := atomic.AddInt32(&calls, 1)
		switch n {
		case 1, 2: // two failures: 5s, 10s
			return pollResult{err: netErr}
		case 3: // success: backoff resets, next gap is 5s
			return pollResult{}
		case 4: // success again, gap should still be 5s
			return pollResult{}
		default:
			return pollResult{done: true}
		}
	}, newTestOpts(clock))
	if err != nil {
		t.Fatalf("politePoll returned err: %v", err)
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 5 * time.Second, 5 * time.Second}
	if len(clock.sleeps) != len(want) {
		t.Fatalf("expected %d sleeps, got %d: %v", len(want), len(clock.sleeps), clock.sleeps)
	}
	for i, w := range want {
		if clock.sleeps[i] != w {
			t.Errorf("sleep[%d] = %s, want %s", i, clock.sleeps[i], w)
		}
	}
}

func TestPolitePoll_429WithRetryAfterIsRespected(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	var calls int32
	err := politePoll(context.Background(), func(ctx context.Context) pollResult {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return pollResult{
				err:        errors.New("rate limited"),
				httpStatus: 429,
				retryAfter: 30 * time.Second,
			}
		}
		return pollResult{done: true}
	}, newTestOpts(clock))
	if err != nil {
		t.Fatalf("politePoll returned err: %v", err)
	}
	if len(clock.sleeps) != 1 {
		t.Fatalf("expected 1 sleep, got %d: %v", len(clock.sleeps), clock.sleeps)
	}
	if clock.sleeps[0] != 30*time.Second {
		t.Errorf("sleep[0] = %s, want 30s (Retry-After)", clock.sleeps[0])
	}
}

func TestPolitePoll_429RetryAfterClampedToMaxInterval(t *testing.T) {
	// A hostile server returning Retry-After: 999999 should not pin us at
	// hours-long sleeps — politePoll clamps to maxInterval.
	clock := &fakeClock{now: time.Unix(0, 0)}
	var calls int32
	err := politePoll(context.Background(), func(ctx context.Context) pollResult {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return pollResult{
				err:        errors.New("rate limited"),
				httpStatus: 429,
				retryAfter: 1 * time.Hour,
			}
		}
		return pollResult{done: true}
	}, newTestOpts(clock))
	if err != nil {
		t.Fatalf("politePoll returned err: %v", err)
	}
	if len(clock.sleeps) != 1 {
		t.Fatalf("expected 1 sleep, got %d", len(clock.sleeps))
	}
	if clock.sleeps[0] != 60*time.Second {
		t.Errorf("sleep[0] = %s, want 60s (clamped to maxInterval)", clock.sleeps[0])
	}
}

func TestPolitePoll_429WithoutRetryAfterFallsBackToExponential(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	var calls int32
	err := politePoll(context.Background(), func(ctx context.Context) pollResult {
		n := atomic.AddInt32(&calls, 1)
		if n <= 2 {
			return pollResult{err: errors.New("rate limited"), httpStatus: 429}
		}
		return pollResult{done: true}
	}, newTestOpts(clock))
	if err != nil {
		t.Fatalf("politePoll returned err: %v", err)
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second}
	if len(clock.sleeps) != len(want) {
		t.Fatalf("expected %d sleeps, got %v", len(want), clock.sleeps)
	}
	for i, w := range want {
		if clock.sleeps[i] != w {
			t.Errorf("sleep[%d] = %s, want %s", i, clock.sleeps[i], w)
		}
	}
}

func TestPolitePoll_4xxNon429BacksOffExponentially(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	var calls int32
	err := politePoll(context.Background(), func(ctx context.Context) pollResult {
		n := atomic.AddInt32(&calls, 1)
		if n <= 3 {
			return pollResult{err: errors.New("bad request"), httpStatus: 400}
		}
		return pollResult{done: true}
	}, newTestOpts(clock))
	if err != nil {
		t.Fatalf("politePoll returned err: %v", err)
	}
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}
	if len(clock.sleeps) != len(want) {
		t.Fatalf("expected %d sleeps, got %v", len(want), clock.sleeps)
	}
	for i, w := range want {
		if clock.sleeps[i] != w {
			t.Errorf("sleep[%d] = %s, want %s", i, clock.sleeps[i], w)
		}
	}
}

func TestPolitePoll_ContextCancellationInterruptsWait(t *testing.T) {
	// Use the real clock here so we can prove that cancelling the context
	// returns quickly (well under minInterval). This is what makes
	// `relayfile stop` responsive.
	ctx, cancel := context.WithCancel(context.Background())
	var calls int32
	start := time.Now()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	err := politePoll(ctx, func(ctx context.Context) pollResult {
		atomic.AddInt32(&calls, 1)
		return pollResult{}
	}, politeOpts{
		minInterval:      5 * time.Second,
		maxInterval:      60 * time.Second,
		jitterFraction:   0,
		hardMaxPerSecond: 1,
	})
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed > 1*time.Second {
		t.Errorf("politePoll took %s to honour cancellation; expected <1s", elapsed)
	}
	if calls < 1 {
		t.Errorf("expected at least one poll call before cancel, got %d", calls)
	}
}

func TestPolitePoll_HardMaxRateEnforced(t *testing.T) {
	// Set minInterval to 0 to simulate a future bug that disables the
	// regular gap. The hardMaxPerSecond floor should still kick in and
	// prevent a busy loop.
	clock := &fakeClock{now: time.Unix(0, 0)}
	var calls int32
	opts := politeOpts{
		minInterval:      0,
		maxInterval:      60 * time.Second,
		jitterFraction:   0,
		hardMaxPerSecond: 1,
		now:              clock.Now,
		sleep:            clock.Sleep,
	}
	err := politePoll(context.Background(), func(ctx context.Context) pollResult {
		n := atomic.AddInt32(&calls, 1)
		if n >= 3 {
			return pollResult{done: true}
		}
		return pollResult{}
	}, opts)
	if err != nil {
		t.Fatalf("politePoll returned err: %v", err)
	}
	// With minInterval=0, normal between-poll sleep is 0. But the
	// hardMaxPerSecond=1 floor (= 1 second between any two calls) still
	// requires a sleep before each subsequent call. Either the
	// post-call sleep or the pre-call hard-rate sleep will satisfy this,
	// so we just assert the total elapsed time on the fake clock is at
	// least 2 seconds across 3 calls.
	elapsed := clock.now.Sub(time.Unix(0, 0))
	if elapsed < 2*time.Second {
		t.Errorf("hard max rate not enforced: %d calls in %s of fake-clock time", calls, elapsed)
	}
}

func TestPolitePoll_HardMaxAcrossSuccessIterations(t *testing.T) {
	// Even if politePoll's regular minInterval sleep is somehow zero
	// (jitter happened to round to ~0, future bug, etc.), no two calls
	// should be made within 1/hardMaxPerSecond. Verify by checking
	// fake-clock timestamps when fn returns instantly.
	clock := &fakeClock{now: time.Unix(0, 0)}
	var callTimes []time.Time
	opts := politeOpts{
		minInterval:      100 * time.Millisecond, // tiny on purpose
		maxInterval:      60 * time.Second,
		jitterFraction:   0,
		hardMaxPerSecond: 1, // 1s hard floor
		now:              clock.Now,
		sleep:            clock.Sleep,
	}
	err := politePoll(context.Background(), func(ctx context.Context) pollResult {
		callTimes = append(callTimes, clock.Now())
		if len(callTimes) >= 4 {
			return pollResult{done: true}
		}
		return pollResult{}
	}, opts)
	if err != nil {
		t.Fatalf("politePoll returned err: %v", err)
	}
	for i := 1; i < len(callTimes); i++ {
		gap := callTimes[i].Sub(callTimes[i-1])
		if gap < time.Second {
			t.Errorf("call[%d] fired %s after call[%d]; hard-max requires >= 1s", i, gap, i-1)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"  ", 0},
		{"5", 5 * time.Second},
		{"0", 0},
		{"-3", 0},
		{"not-a-number", 0},
	}
	for _, tc := range cases {
		if got := parseRetryAfter(tc.in); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestBackoffFor(t *testing.T) {
	min := 5 * time.Second
	max := 60 * time.Second
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 5 * time.Second}, // clamped to >=1
		{1, 5 * time.Second},
		{2, 10 * time.Second},
		{3, 20 * time.Second},
		{4, 40 * time.Second},
		{5, 60 * time.Second}, // capped
		{50, 60 * time.Second},
	}
	for _, tc := range cases {
		if got := backoffFor(tc.failures, min, max); got != tc.want {
			t.Errorf("backoffFor(%d) = %s, want %s", tc.failures, got, tc.want)
		}
	}
}
