# Poll storms

## Background: the 2026-05-14 incident

On 2026-05-14 a single relayfile mount client (`/Users/hassanwari/relayfile-mount`)
entered a tight polling loop hitting
`/v1/workspaces/{id}/sync/status` at **30+ requests per second**, sustained
for hours. That saturated the production AWS Lambda 10-slot concurrency cap
on the cloud control plane and caused unrelated users to receive `429 Too
Many Requests` on their own status calls. Root cause: the mount daemon's
file-watcher callback called `writeSnapshot()` on every fsnotify event, and
`writeSnapshot()` made a synchronous `GET /sync/status`. A noisy editor
(rapid save / autosave / log file tailing inside the mount tree) generates
dozens of events per second. There was no minimum interval, no jitter, no
backoff on errors, and no hard rate ceiling — so on a fast network the
client polled as fast as the cloud could respond.

## The new polling discipline

All client-side polling of `/sync/status`, `/sync/ingress`, and the cloud
integration `/integrations/.../status` endpoints now goes through the
`politePoll` helper (`cmd/relayfile-cli/polite_poll.go`).

The discipline:

| Knob                          | Default | What it does                                                                 |
|-------------------------------|---------|------------------------------------------------------------------------------|
| `syncStatusMinPollInterval`   | 5 s     | Minimum gap between two successful polls of the same endpoint.               |
| `syncStatusMaxPollInterval`   | 60 s    | Cap on the exponential backoff applied after consecutive failures.           |
| `syncStatusJitterFraction`    | 0.2     | ±20 % randomization per iteration to break thundering-herd alignment.        |
| `syncStatusHardMaxPerSecond`  | 1       | Hard rate ceiling per (workspace, endpoint). Belt-and-suspenders safety net. |

Error handling:

- **Network error or 5xx**: exponential backoff starting at `minInterval`,
  doubling each consecutive failure, capped at `maxInterval`.
- **4xx (except 429)**: same exponential backoff. We deliberately do not
  hammer a broken endpoint.
- **429 with `Retry-After` header**: honored exactly, clamped into
  `[minInterval, maxInterval]` (defends against a hostile / buggy
  `Retry-After: 99999`).
- **429 without `Retry-After`**: falls through to exponential backoff.
- **Any successful response** resets the failure counter and the next gap
  is back to `minInterval`.

`writeSnapshot()` inside the long-running mount daemon (`runMountSync`)
caches the last `syncStatusResponse` and only re-fetches when
`syncStatusMinPollInterval` has elapsed since the last fetch. File-watcher
callbacks now reuse the cached snapshot instead of issuing a fresh GET per
event.

Context cancellation is honored: `relayfile stop` interrupts a poll wait
within milliseconds rather than having to wait for the full backoff
interval.

## Verifying the daemon is not storming

If a user reports unusual cloud-side rate limiting, or you suspect a
regression, check the daemon's poll rate against the cloud access log.
On the daemon side, the log file lives at the path printed in the start
banner (typically `~/.relayfile/<workspace>/mount.log`). Look for
`/sync/status` references in the access log of the cloud worker:

```bash
# In the cloud repo, on a deployed stage:
aws logs filter-log-events \
  --log-group-name /aws/lambda/agentworkforce-cloud-prod-WebHandler \
  --start-time $(date -u -v-15M +%s)000 \
  --filter-pattern '"GET /v1/workspaces/" "sync/status"' \
  --max-items 100 \
  --region us-east-1 | jq '.events | length'
```

If a single workspace ID shows more than ~12 status requests per minute
(one every 5 s, plus jitter, plus a little slack for retries on transient
errors), the daemon is misbehaving. Capture its log and the trace.

To eyeball poll rate on the client side, run with verbose logging and grep
for the status-snapshot log lines around each fetch:

```bash
RELAYFILE_LOG_LEVEL=debug relayfile mount <workspace>
# In another terminal:
tail -f ~/.relayfile/<workspace>/mount.log | grep -E 'sync/status|writeSnapshot'
```

## When to expect backoff vs base interval

- **Steady state, no errors**: one poll every 5 s ±20 % jitter (range
  roughly 4–6 s) for every endpoint that uses `politePoll`.
- **Server returning 5xx / network blip**: backoff doubles per failure
  (5 s → 10 s → 20 s → 40 s → 60 s capped). One success snaps it back to 5 s.
- **Server returning 429 Retry-After: N**: the next poll waits ~N seconds
  (clamped to [5 s, 60 s]) regardless of prior failures.
- **Local file watcher firing rapidly**: `writeSnapshot()` may be invoked
  hundreds of times per second, but no more than one `/sync/status` GET
  fires per `syncStatusMinPollInterval`.

## Related server-side work

A complementary server-side rate-limit on `/v1/workspaces/.../sync/status`
is being added in the cloud repo (in flight; the in-progress PR will be
linked here once it merges). The two fixes are independent — even without
the server-side limit, this client change alone closes the storm. The
server-side work adds defense in depth for any client (e.g. an older
relayfile binary, or a third-party tool) that doesn't yet observe this
discipline.

## Future work

- The same polite-poll discipline should be applied to any new long-lived
  polling loop. Search for `for {` loops that contain `client.getJSON` or
  `client.do` calls and route them through `politePoll`.
- Consider emitting client-side metrics (poll rate, backoff state) so a
  future incident can be diagnosed from telemetry rather than from CloudWatch
  log forensics.
