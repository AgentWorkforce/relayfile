# E2B ↔ Daytona build and infrastructure provenance

Date: 2026-08-21
PR: `AgentWorkforce/relayfile#435`
Benchmark source parent: `5de6eedacb9a293585858b10ecf089499d4f033a`

## Deployed artifacts

Both agents ran the exact same statically linked Linux/amd64 mount binary,
built with CGO disabled, `-trimpath`, and `-ldflags="-s -w"`.

| Artifact | SHA-256 |
| --- | --- |
| candidate `relayfile-mount` on E2B | `177420bbfc90ea7b878ef2340c1955e9400dce469d047b7e107742e0ec810126` |
| candidate `relayfile-mount` on Daytona | `177420bbfc90ea7b878ef2340c1955e9400dce469d047b7e107742e0ec810126` |
| E2B-hosted `relayfile` service | `e4c4834849c2eef00a26e3a6be647daa7481e329bd9e20871b81438d15c2acf7` |

The candidate binary is built from the product changes carried in the evidence
commit on PR #435. A fresh final build reproduced the deployed candidate SHA
byte-for-byte. The server binary was unchanged during the candidate test.

## Isolated topology

| Role | Provider | Sandbox ID | Local materialization |
| --- | --- | --- | --- |
| Relayfile service | E2B | `ih8xcaiuoon8u1w6ittrw` | n/a |
| agent | E2B | `imhb24eqytdtb3ova0q5y` | `/home/user/optimized/workspace` |
| agent | Daytona | `81d4a121-40db-43b5-b474-5be0784d4898` | `/home/daytona/optimized/workspace` |

The service used a segmented durable filesystem backend with external
writeback disabled and local JWKS validation. The agents communicated only
through HTTPS/WebSocket and had no shared process, volume, or FUSE mount. Each
agent used private mount state outside the public materialized tree. Runtime
tokens, signing material, and expiring provider URLs are not retained.

The controlled core trial used a dedicated disposable Daytona sandbox,
`424f8877-6bd0-49de-8565-5c6a9592954d`, with 4 vCPUs, 4 GiB RAM, and a two-hour
TTL. Two independent mounts, the unchanged candidate binary, and the unchanged
server binary ran on that host. The server had no persistence backend and all
mount/state roots were on `tmpfs`, so the result intentionally excludes
provider-network and durability costs.

## Evidence boundaries

The first candidate warm-up was invalidated because the Daytona read-only probe
still served a superseded workspace root. Subsequent candidate binaries were
also invalidated when repeated watcher tests exposed Linux atomic-rename edge
cases. Those diagnostic samples are retained under `raw/invalid-*` and do not
contribute to `aggregate-summary.json`. After the final Linux test passed 50
times, a replacement warm-up passed and the retained `final-r1` through
`final-r3` files became the complete consecutive release sequence.

The existing owned Daytona agent was reused; no unrelated sandbox was modified.
The two E2B sandboxes were newly created for this benchmark after the original
ephemeral pair reached its TTL. The final E2B pair then reached its own TTL
after all raw evidence was retained on 2026-08-22. The dedicated core Daytona
sandbox was explicitly deleted, and its core-only processes and token state on
the existing Daytona agent were removed. The launch video uses a separately
created demo topology rather than implying the evidence sandboxes are durable.
