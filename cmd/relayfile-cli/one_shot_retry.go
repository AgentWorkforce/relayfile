package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	oneShotGETMaxAttempts = 3
	oneShotGETMinDelay    = time.Second
	oneShotGETMaxDelay    = 60 * time.Second
	oneShotGETJitter      = 0.2
)

type oneShotGETRetryPolicy struct {
	enabled bool
	stderr  io.Writer
	opts    politeOpts
	jitter  func(time.Duration, float64) time.Duration
}

func defaultOneShotGETRetryPolicy(enabled bool, stderr io.Writer) oneShotGETRetryPolicy {
	return oneShotGETRetryPolicy{
		enabled: enabled,
		stderr:  stderr,
		opts: politeOpts{
			minInterval:      oneShotGETMinDelay,
			maxInterval:      oneShotGETMaxDelay,
			jitterFraction:   oneShotGETJitter,
			hardMaxPerSecond: 1,
		},
	}
}

func (p oneShotGETRetryPolicy) run(ctx context.Context, get func(context.Context) error) error {
	if !p.enabled {
		return get(ctx)
	}

	opts := p.opts.withDefaults()
	jitter := p.jitter
	if jitter == nil {
		jitter = applyJitter
	}
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := get(ctx)
		if err == nil || attempt >= oneShotGETMaxAttempts || !isRetryableOneShotGETError(err) {
			return err
		}

		var apiErr *apiError
		_ = errors.As(err, &apiErr)
		baseDelay := backoffFor(attempt, opts.minInterval, opts.maxInterval)
		if apiErr.RetryAfter > 0 {
			baseDelay = clampDuration(apiErr.RetryAfter, opts.minInterval, opts.maxInterval)
		}
		delay := jitter(baseDelay, opts.jitterFraction)
		// Retry-After is a lower bound. Symmetric jitter must never make us
		// retry before the server's advertised delay has elapsed.
		if apiErr.RetryAfter > 0 && delay < baseDelay {
			delay = baseDelay
		}
		delay = clampDuration(delay, time.Millisecond, opts.maxInterval)
		if p.stderr != nil {
			fmt.Fprintf(p.stderr, "%s, retrying in %s\n", oneShotRetryLabel(err), delay.Round(time.Millisecond))
		}
		if err := opts.sleep(ctx, delay); err != nil {
			return err
		}
	}
}

func isRetryableOneShotGETError(err error) bool {
	var apiErr *apiError
	return errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusTooManyRequests || apiErr.StatusCode == http.StatusServiceUnavailable)
}

func oneShotRetryLabel(err error) string {
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests && apiErr.Code == "workspace_busy" {
		return "workspace busy"
	}
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
		return "rate limited"
	}
	return "service unavailable"
}

func (p oneShotGETRetryPolicy) getWorkspaceBytes(ctx context.Context, client *workspaceCommandClient, pathForWorkspace func(string) string) ([]byte, string, error) {
	var body []byte
	var contentType string
	err := p.run(ctx, func(ctx context.Context) error {
		var err error
		body, contentType, err = client.getWorkspaceBytes(ctx, pathForWorkspace)
		return err
	})
	return body, contentType, err
}

func (p oneShotGETRetryPolicy) getWorkspaceJSON(ctx context.Context, client *workspaceCommandClient, pathForWorkspace func(string) string, out any) error {
	return p.run(ctx, func(ctx context.Context) error {
		return client.getWorkspaceJSON(ctx, pathForWorkspace, out)
	})
}
