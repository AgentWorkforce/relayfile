# Build and Daytona provenance

Date: 2026-08-21  
Base commit: `f89d152502d3bc15161e0673b96cb84f419cd30a`  
Go toolchain: `go1.26.1`  
Workspace: `ws_daytona_realtime_20260821`

## Deployed artifacts

| Artifact | Linux target | SHA-256 |
| --- | --- | --- |
| `cmd/relayfile` | linux/amd64, CGO disabled, trimpath | `bf1ab0cbac31be7e2e57dfd51c108f948498b22fd03cd6711feedbd8ec77ab15` |
| `cmd/relayfile-mount` | linux/amd64, CGO disabled, trimpath | `abac99fe89914049a777d04f2eb699f824cfa2d6d67421107ae09ccf0325c724` |
| product source patch | base commit above | `345be3c883a0594e67b8c5dcca436f94d7ea300485a74a7ce4048d7431927015` |

After the final source/test edit, both Linux binaries were rebuilt with the same
target and flags. Each rebuilt file was byte-identical to the corresponding
binary deployed for all qualifying runs. The complete product diff plus the new
real-time regression test is retained as `source.patch`.

## Daytona topology

All sandboxes were in Daytona region `us`, each with 2 CPU, 2 GiB memory, and
5 GiB disk.

| Role | Name | Sandbox ID |
| --- | --- | --- |
| Relayfile server | `rf-realtime-20260821-server-2` | `f0160628-31c9-4966-8217-f0b130550cd6` |
| Agent A mount | `rf-realtime-20260821-agent-a-2` | `572c94e2-e1ec-454c-a7b8-ad89841398f6` |
| Agent B mount | `rf-realtime-20260821-agent-b-2` | `9383cf19-f4bb-430d-817b-590444cd5775` |

The agents used separate local filesystems. Server traffic crossed the Daytona
HTTPS/WebSocket preview proxy; clock/presence probes crossed separate persistent
HTTPS preview endpoints on the opposite agent. Tokens, the internal HMAC secret,
and signed credentials are deliberately absent from the evidence.

Server settings included external writeback disabled, a private JSON state file,
and a localhost JWKS endpoint. Both mounts used remote root `/`, exact local
layout at `/root/shared-repo`, a 30-second fallback interval with zero jitter,
a 30-second request timeout, WebSockets enabled, and private mount state outside
the materialization.

Before every clean run, the disposable server state and both materializations
were deleted only after their exact benchmark processes and paths were verified.
The server was then reseeded to exactly 1,272 files and 11,321,811 bytes before
both mounts bootstrapped to cursor `evt_2544`. Reset/bootstrap time is excluded
from all latency populations.
