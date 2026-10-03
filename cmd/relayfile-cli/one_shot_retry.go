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

	var lastErr error
	attempts := 0
	opts := p.opts
	baseSleep := opts.sleep
	if baseSleep == nil {
		baseSleep = sleepCtx
	}
	opts.sleep = func(ctx context.Context, delay time.Duration) error {
		// politePoll applies jitter after its upper clamp. Keep this command's
		// advertised 60-second ceiling absolute.
		delay = clampDuration(delay, time.Millisecond, oneShotGETMaxDelay)
		if p.stderr != nil {
			fmt.Fprintf(p.stderr, "%s, retrying in %s\n", oneShotRetryLabel(lastErr), delay.Round(time.Millisecond))
		}
		return baseSleep(ctx, delay)
	}

	err := politePoll(ctx, func(ctx context.Context) pollResult {
		attempts++
		lastErr = get(ctx)
		if lastErr == nil || attempts >= oneShotGETMaxAttempts || !isRetryableOneShotGETError(lastErr) {
			return pollResult{done: true}
		}
		var apiErr *apiError
		_ = errors.As(lastErr, &apiErr)
		return pollResult{err: lastErr, httpStatus: apiErr.StatusCode, retryAfter: apiErr.RetryAfter}
	}, opts)
	if err != nil {
		return err
	}
	return lastErr
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
