# Hosted mount one-way latency methodology — 2026-08-07

## Scope

This is the first successful one-way measurement through a hosted Relayfile
deployment. It reuses the LAN baseline harness in
`../mount-latency-20260807/harness/` and preserves every failed or incomplete
attempt. The endpoint under test was `https://api.relayfile.dev`.

The deployed service's build or version could **not** be determined. Its public
health response was HTTP 200 with only a status field, and the response headers
exposed no application build identifier. The receiver CLI was built from source
commit `dadcb88117ef75d683cb6ecebfdd4b26f58e6ffa`; that identifies the local
client, **not** the deployed server artifact.

## Topology

The committed records use only non-identifying aliases:

    sender (host A) -- public HTTPS/WAN --> api.relayfile.dev
                                             |
                                      hosted event service
                                             |
                                   public websocket/WAN
                                             v
                                      receiver (host B)

The sender and receiver were distinct physical hosts. The measured data path
did not use a same-tailnet LAN server. Tailscale was used only for the direct
sender-to-receiver clock-offset exchange; it carried no file write or hosted
mount event.

The receiver mounted the remote `/trials` subtree using the CLI's exact local
layout. A first attempt to mount the full workspace traversed 135,717,443 bytes,
then failed with a hosted Durable Object storage timeout and websocket EOF.
Scoping the mount eliminated unrelated workspace bootstrap cost. The scoped
mount presents `/trials/<run>/...` locally as `<run>/...`; the analyser restores
the declared `trials` prefix for pairing without modifying raw receiver paths.

## Timing model

For each trial:

- `t_send_ns` is read from sender `CLOCK_REALTIME` immediately before the HTTP
  request.
- `t_ack_ns` is read on the sender immediately after the HTTP response or
  error.
- `observed_ns` is read from receiver `CLOCK_REALTIME` when the exact final
  path first becomes visible in the local mirror.
- A multi-file trial completes at the last of its 11 exact final-file
  observations. Atomic-write temporary files remain raw but are ignored.

With interpolated receiver-minus-sender clock offset `o(t)`:

    total = observed_receiver - o(t_send) - t_send
    leg A = t_ack - t_send
    leg B = observed_receiver - o(t_send) - t_ack

No round trip is halved. Leg A is a real WAN request to the hosted service. Leg
B is the hosted acknowledgement-to-receiver observation interval and includes
event delivery plus receiver-side reads and atomic writes.

## Clock offset

`clock-offset.py` uses the NTP four-timestamp calculation over direct TCP:

    delay  = (t3 - t0) - (t2 - t1)
    offset = ((t1 - t0) + (t2 - t3)) / 2

There were 200 exchanges immediately before the measured blocks and 200 after
the last measured block. The least-delay sample anchors each end; offset is
linearly interpolated to each send time. The symmetry uncertainty at an anchor
is ±half its minimum delay. Two endpoint anchors cannot exclude an intervening
clock step or nonlinear slew, so the between-anchor model error remains
unbounded by this dataset and is stated in the results.

## Trial shapes and populations

The exact baseline payload generators were retained:

- `small`: one unique 300-byte file.
- `repo`: 11 unique files totalling 13,992 bytes, matching the baseline's
  realistic repository change-set shape.

Trials use unique run and trial paths and four seconds of spacing. Headline
statistics use one complete population per shape:

- small: `hosted20260807`, n=20 complete of 20 sends;
- repo: `hosted20260807r4`, n=22 complete of 22 sends.

The successful repo population was attempted only after three earlier repo
populations. Those earlier raw sends and watchers are retained:

- r1: 20 sends, 14 complete observations, 6 incomplete at watcher end;
- r2: 20 sends, 12 complete, 7 incomplete, one HTTP 500;
- r3: 25 sends, 15 complete, 10 incomplete before websocket/reconcile stall;
- r4: a freshly bootstrapped scoped mount, 22 sends, 22 complete.

The headline r4 distribution is therefore conditional on a freshly restarted,
successfully completing mount. The incomplete populations are not blended into
its percentile distribution, but they remain part of the reliability record.

## Hosted compatibility adjustment

The first warmup used the baseline `urllib` default user agent and received
Cloudflare HTTP 403 error 1010 before the application handled the write. That
failed record remains in `raw/trials-warmup.jsonl`. The same sender harness was
then given one explicit stable header:

    User-Agent: relayfile-latency-harness/1

No payload, timestamp boundary, trial shape, retry policy, or statistic changed.
The exact modified harness is committed here.

## Receiver-local control

The receiver watcher was also run against 25 local atomic publishes using
`control-local.py`. The control records publish start and end separately, so
visibility occurs somewhere within that interval. Results report lower and
upper detection-delay distributions; no control distribution is subtracted
from the network result.

## Statistics and failure handling

Median and p95 use linear interpolation over sorted complete trials. At n=20,
p95 depends on the two largest observations and is identified as such. HTTP
errors and trials missing any expected final-file observation are counted and
named but excluded from latency percentiles. This exclusion defines a
conditional completed-trial distribution; it is not a claim that failures did
not happen.

Every process wrote append-only JSONL and flushed after each record. Interrupted
watchers intentionally lack a `watcher_finished` record. The assertions gate
both the successful population and the exact counts of earlier failed and
incomplete attempts.

## Reproduction command shape

Secrets and machine identities are represented only as variables:

```sh
python3 harness/clock-offset.py server "$RECEIVER_BIND" "$CLOCK_PORT"
ssh "$SENDER_HOST" python3 clock-offset.py client \
  "$RECEIVER_BIND" "$CLOCK_PORT" 200 clock-offset-pre.jsonl receiver

relayfile mount "$WORKSPACE_ALIAS" "$MOUNT_DIR" \
  --remote-path /trials --local-layout exact \
  --state-dir "$STATE_DIR" --interval 30s --rehome

python3 harness/receiver-watch.py \
  "$MOUNT_DIR" raw/mount-watch.jsonl 420 0.001 receiver-mount-root

python3 harness/sender-trials.py small \
  "$SERVER_URL" "$WORKSPACE_ID" "$TOKEN_FILE" \
  "$RUN_ID" 20 4 raw/trials-small.jsonl sender

python3 harness/sender-trials.py repo \
  "$SERVER_URL" "$WORKSPACE_ID" "$TOKEN_FILE" \
  "$RUN_ID" 22 4 raw/trials-repo.jsonl sender

python3 harness/analyse.py \
  raw/clock-offset-pre.jsonl raw/clock-offset-post.jsonl \
  raw/trials-repo.jsonl raw/mount-watch.jsonl repo "$RUN_ID" trials

python3 harness/assertions.py
```

The actual delegated token, workspace identifier, usernames, hostnames,
connection addresses, and absolute home paths are neither committed nor needed
to interpret the evidence.
