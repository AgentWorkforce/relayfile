# PR summary

## What changed

- Added bounded retries for transient `429` and `503` responses to the one-shot `tree`, `read`, `export`, and `status` GET commands.
- Reused the CLI's existing polite polling implementation for `Retry-After`, exponential fallback, jitter, rate limiting, and context-aware waits, with three attempts and a 60-second maximum delay.
- Added `--no-retry` to each affected command for callers that require immediate failure.
- Routed retry notices to stderr so file, JSON, and export payloads on stdout remain clean.
- Documented the retry behavior and opt-out flag.

## Tests

- Added deterministic regression coverage for `429 workspace_busy` recovery, `503`, exhaustion, non-retryable errors, delay clamping, cancellation, stdout/stderr separation, and `read --no-retry` request count.
- `go test ./cmd/relayfile-cli -count=1`
- `scripts/check-contract-surface.sh`
- `git diff --check`
