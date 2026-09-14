# Repository seed transport

The mount batches ordinary `/v1/workspaces/{workspaceId}/fs/bulk` JSON writes
at 8 MiB of serialized content. Production Relayfile Cloud retains its
10 MiB plus 64 KiB framing guard; aggregate repository size is not a request limit.

When a single file exceeds that batch budget, the mount checks `/health` for
`file-stream-v1`. The Cloud extension uses the existing `PUT /fs/file?path=...`
route with raw bytes and these headers:

- `Content-Type: application/octet-stream`
- `Content-Length`: required exact decoded file size, at most 64 MiB by default;
  absent or malformed lengths return 411 before the body is read
- `X-Relayfile-Encoding`: `utf-8` or `base64` (storage/read representation;
  the uploaded body always contains raw bytes)
- `X-Relayfile-Content-Type`: original media type
- `X-Relayfile-Mode`: octal permissions, for example `755`
- `X-Relayfile-Content-Identity`: optional JSON durable command identity
- `If-Match`: original revision/create-only precondition

Symlinks stay in bounded JSON batches. The Worker forwards the raw stream
without cloning or buffering it. The Durable Object counts and hashes bytes
while writing R2, checks the revision again after upload, and records the same
filesystem event and digest refresh as ordinary writes. Explicit deployment
write caps remain authoritative. This does not raise the JSON write limit.

Older standalone Go servers do not advertise this Cloud extension. Large
single-file writes retain their existing bounded 96 MiB JSON compatibility
path; old Cloud servers may reject them until the streaming server is deployed.
No file is truncated or omitted to fit a request.

Rollout order: deploy reviewed Relayfile Cloud changes, verify
`file-stream-v1`, release the reviewed Go mount client, then update the Cloud
mount pin and rebuild/promote its Daytona snapshot through the established
release pipeline. Production proof should run only after both sides are live.

Local acceptance uses `local/repository-seed.test.ts` in Relayfile Cloud with
`RELAYFILE_MOUNT_BIN` pointing to the candidate Go executable. It exercises
`--push-local-once` against actual Workers/DO/R2 and independently hashes the
stored 64 MiB binary. This is local validation, not production proof.
