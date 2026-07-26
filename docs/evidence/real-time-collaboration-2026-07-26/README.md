# Real-time collaboration across two physical machines

Date: 2026-07-26

Base relayfile commit: `ad231edd9515fd13dd0d4a3b853d38455e019744`

Machines: `sf-mac-mini` (`100.101.110.11`) and `finn-mac-mini`
(`100.94.48.37`) over Tailscale

## Result

PASS. Two independent coding agents worked concurrently in separate physical
mounts of one relayfile workspace. They divided a small Go service into backend
and frontend work, edited the same `Status` type while both agents were active,
survived a mount restart during two 20-write edit storms, and wrote adjacent
lines of one shared file across a real network partition. Both mounts and the
server converged without conflict artifacts, silent loss, or corruption.

The run also found a real security bug: a failed WebSocket connection exposed
the workspace bearer token in `relayfile-mount` logs because the Go WebSocket
dial error included its full query string. The accompanying change redacts
`token`, `access_token`, and `api_key` query values. The first repair passed its
unit test but failed a live replay because the CLI logged the returned error
outside `Syncer`; the final repair sanitizes both the formatted Syncer log line
and `webSocketDialError.Error`. A third live outage recorded three
`token=[REDACTED]` markers and zero raw-token occurrences.

## Topology and coordination

- A fresh durable-local relayfile server ran on sf at `:18080`; both machines
  mounted the isolated workspace `ws_rt_collab_20260726`.
- Finn reached that server through a Tailscale SSH local forward on `:18081`.
  Killing only this forward created the partition without affecting unrelated
  processes or workspaces.
- The backend engineer was a fresh Agent Relay fleet worker,
  `rt-collab-backend-finn-20260726`, targeted to a fresh detached fleet node,
  `rt-collab-finn-20260726`, on finn. Fleet invocation:
  `inv_207205120393256960`.
- The frontend engineer was an independent Codex process on sf. Both agents
  received bounded role prompts preserved in this directory.
- Agent Relay 11.2.0's explicit `fleet enable` command failed with an SDK
  capability error on both the installed 11.1.1 CLI and 11.2.0 via npx.
  Enrollment and targeted spawn still worked through the default fleet state.
  This is product feedback, not a relayfile correctness failure.

## Timeline (UTC)

| Time | Event and evidence |
|---|---|
| 18:39:20 | Frontend agent started on sf. |
| 18:39:42 | Backend fleet agent started on finn. |
| 18:39:54 | Frontend wrote the shared `internal/model/status.go` region. |
| 18:40:09 | Backend wrote the same type, adding backend-owned fields. |
| 18:40:17 | Frontend mount observed the merged fields while the agent was still running. |
| 18:40:37 | Frontend committed `6b895ae`. |
| 18:41:41 | Backend committed `5d426db`. |
| 18:42:20–18:43:36 | Concurrent restart probes each performed 20 writes. |
| 18:43:01–18:43:07 | Finn mount was stopped and restarted while sf advanced its sequence from 2 to 12. Both sequences ultimately reached 20. |
| 18:45:30 | Frontend wrote its token to shared `internal/model/partition.go`. |
| 18:45:42–18:46:00 | Tailscale SSH transport was deliberately unavailable; connection refusal and one pending outbox entry were observed. |
| 18:45:46 | Backend wrote its adjacent token while physically disconnected. |
| 18:46:35 | Finn replayed its pending write after reconnect. |
| 18:46:36 | Frontend observed the backend token. |

The frontend and backend commits remain independent local Git histories because
the object under test is relayfile file collaboration, not Git replication.
File content is the convergence oracle.

## Adversarial checks

| Check | Outcome |
|---|---|
| Concurrent, interdependent work | Backend added `BuildSHA` and `APIRevision`; frontend added `Region`, rendered all three fields, and added tests. `go test ./...` passed in both mounts. |
| Same-file/same-type overlap | Both agents edited `internal/model/status.go` while concurrently active. The final type contains all fields and no conflict artifact. |
| Near-concurrent commits | Frontend `6b895ae`; backend `5d426db`. Both retained the converged source. |
| Mount restart mid-storm | Finn mount restarted while both agents were issuing 20-write sequences. Final frontend and backend probes both reached sequence 20. |
| Real network partition | Backend wrote while its only relayfile transport was down. The outbox retained one pending operation and replayed it after reconnection. |
| Silent-loss/corruption oracle | Eleven project files had identical per-file SHA-256 manifests on both machines. Manifest SHA-256: `78d6ec6466524fed964f9137af1947cb3c11b402342347d9f82eb7c52f79e6c3`. |
| Shared partition file | Identical SHA-256 on sf and finn: `19546b7969cd71d21eeb881d0e88e1435930097e04d48a2c62565489cf6a7851`. |
| Conflict artifacts | No entries under `.relay/conflicts` on either mount. |

## Propagation round-trip latency

The first run recorded cross-host wall-clock arrival deltas. PR review correctly
identified that subtracting timestamps from separate machines includes
unbounded clock skew, so those provisional values were discarded.

The measurement was rerun over a fresh isolated relayfile workspace after
review. Each initiating machine atomically wrote a uniquely prefixed ping, the
other physical machine acknowledged it through its own mount, and the initiator
measured ping-to-ack time using only its own monotonic clock. Each sample
therefore includes two relayfile propagations and peer acknowledgment time, but
no cross-machine clock subtraction.

| Initiator | n | Min | Median | Mean | p95 / max |
|---|---:|---:|---:|---:|---:|
| sf | 12 | 232.044 ms | 315.526 ms | 311.452 ms | 372.479 ms |
| finn | 12 | 330.921 ms | 373.230 ms | 374.977 ms | 423.875 ms |

Raw samples are in `sf-initiated-round-trip.csv` and
`finn-initiated-round-trip.csv`. The even-sized medians are the arithmetic mean
of the two central samples.

## Evidence files

- `results.json`: machine-readable outcome and exact identifiers.
- `project-sha256.txt`: the identical final content manifest.
- `mount-chaos-events.log`: curated restart, partition, replay, and redaction
  assertions. Credentials are intentionally absent.
- `*-round-trip.csv`: all 24 monotonic round-trip observations.
- `*-agent-prompt.txt`: exact role and chaos prompts.
- `testdata/realtime-collaboration`: reusable seed project.

## Scope and limitations

The primary same-type writes were 15 seconds apart because the remote fleet
worker needed startup time; they still occurred while both independent agents
were actively executing against the same shared type. The stronger offline
case is unambiguous: the backend write occurred four seconds after transport
loss and was retained locally until replay. Network loss was induced by
dropping the dedicated Tailscale SSH forward rather than disabling the entire
host's Tailscale interface, which isolated this test from unrelated sessions.
