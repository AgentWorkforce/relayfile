package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testOneShotPolicy(clock *fakeClock, stderr *bytes.Buffer) oneShotGETRetryPolicy {
	return oneShotGETRetryPolicy{
		enabled: true,
		stderr:  stderr,
		opts: politeOpts{
			minInterval:      time.Second,
			maxInterval:      60 * time.Second,
			jitterFraction:   0,
			hardMaxPerSecond: 1,
			now:              clock.Now,
			sleep:            clock.Sleep,
		},
	}
}

func TestOneShotGETRetryPolicyRetriesWorkspaceBusyThenSucceeds(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	var stderr bytes.Buffer
	var attempts int
	err := testOneShotPolicy(clock, &stderr).run(context.Background(), func(context.Context) error {
		attempts++
		if attempts == 1 {
			return &apiError{StatusCode: http.StatusTooManyRequests, Code: "workspace_busy", RetryAfter: 7 * time.Second}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retry returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != 7*time.Second {
		t.Fatalf("sleeps = %v, want [7s]", clock.sleeps)
	}
	if got := stderr.String(); got != "workspace busy, retrying in 7s\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestOneShotGETRetryPolicyClassificationAndExhaustion(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantAttempts int
	}{
		{name: "429", err: &apiError{StatusCode: http.StatusTooManyRequests}, wantAttempts: 3},
		{name: "503", err: &apiError{StatusCode: http.StatusServiceUnavailable}, wantAttempts: 3},
		{name: "other 4xx", err: &apiError{StatusCode: http.StatusNotFound}, wantAttempts: 1},
		{name: "transport", err: errors.New("connection reset"), wantAttempts: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeClock{now: time.Unix(0, 0)}
			var attempts int
			gotErr := testOneShotPolicy(clock, &bytes.Buffer{}).run(context.Background(), func(context.Context) error {
				attempts++
				return tc.err
			})
			if gotErr != tc.err {
				t.Fatalf("error = %v, want original %v", gotErr, tc.err)
			}
			if attempts != tc.wantAttempts {
				t.Fatalf("attempts = %d, want %d", attempts, tc.wantAttempts)
			}
		})
	}
}

func TestOneShotGETRetryPolicyClampsDelayAndCancels(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	ctx, cancel := context.WithCancel(context.Background())
	policy := testOneShotPolicy(clock, &bytes.Buffer{})
	var gotDelay time.Duration
	policy.opts.sleep = func(_ context.Context, delay time.Duration) error {
		gotDelay = delay
		cancel()
		return context.Canceled
	}
	err := policy.run(ctx, func(context.Context) error {
		return &apiError{StatusCode: http.StatusTooManyRequests, RetryAfter: 24 * time.Hour}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if gotDelay != oneShotGETMaxDelay {
		t.Fatalf("delay = %s, want clamp %s", gotDelay, oneShotGETMaxDelay)
	}
}

func TestOneShotWorkspaceReadRetriesWithoutMixingOutput(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"workspace_busy","message":"busy"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":"hello"}`))
	}))
	defer server.Close()

	client, err := newAPIClient(server.URL, testJWTWithWorkspace("ws_demo"))
	if err != nil {
		t.Fatal(err)
	}
	commandClient := &workspaceCommandClient{workspaceID: "ws_demo", client: client, directToken: true}
	clock := &fakeClock{now: time.Unix(0, 0)}
	var stderr bytes.Buffer
	body, _, err := testOneShotPolicy(clock, &stderr).getWorkspaceBytes(context.Background(), commandClient, func(string) string { return "/file" })
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if got := string(body); got != `{"content":"hello"}` {
		t.Fatalf("body = %q", got)
	}
	if strings.Contains(string(body), "retrying") || !strings.Contains(stderr.String(), "retrying in 2s") {
		t.Fatalf("body/stderr mixed: body=%q stderr=%q", body, stderr.String())
	}
}

func TestReadNoRetryMakesSingleRequest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clearRelayfileEnv(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":"workspace_busy","message":"busy"}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	err := run([]string{"read", "ws_demo", "/file", "--server", server.URL, "--token", testJWTWithWorkspace("ws_demo"), "--no-retry"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected read error")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}
