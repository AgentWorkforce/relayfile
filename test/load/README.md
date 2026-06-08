# Load probes

## Tier-2 CF-DO scoped writeback

`tier2-cfdo-scoped-writeback.mjs` is the credential-gated second leg of the
team-spawn relayfile load proof. It runs the real `relayfile-mount` binary in
poll mode against a deployed Relayfile API backed by Cloudflare Durable Objects.

This probe is **not** part of default CI and must not be treated as passing when
credentials are absent. Without explicit `RELAYFILE_TIER2_RUN=1` and a
provisioned disposable CF-DO workspace, it emits a skipped evidence record and
exits with code `77`.

Self-test the guardrails without any external service:

```bash
npm run test:load:tier2-scoped-writeback:self-test
```

Run against a provisioned CF-DO workspace:

```bash
RELAYFILE_TIER2_RUN=1 \
RELAYFILE_TIER2_RELAYFILE_URL=https://dev-<stage>-api.relayfile.dev \
RELAYFILE_TIER2_RELAYAUTH_URL=https://dev-<stage>-api.relayauth.dev \
RELAYFILE_TIER2_WORKSPACE_ID=<workspace-id> \
RELAYFILE_TIER2_WORKSPACE_TOKEN=<workspace-token> \
npm run test:load:tier2-scoped-writeback
```

Alternatively, provide `RELAYFILE_TIER2_RELAYAUTH_API_KEY=<relayauth-api-key>`
instead of `RELAYFILE_TIER2_WORKSPACE_TOKEN`; the harness mints a temporary
workspace token before minting scoped path tokens. When a workspace token is
provided directly, it must be able to mint RelayAuth path tokens. The harness
uses a run-scoped data-plane path token to seed and verify Relayfile contents.
Direct Relayfile and RelayAuth API calls are bounded by
`RELAYFILE_TIER2_API_TIMEOUT_MS`, defaulting to 45000 ms.

The generated JSON evidence records:

- per-member narrow path-scoped write-token scopes;
- real `relayfile-mount` bootstrap and writeback process outcomes;
- in-scope edits that reached the remote workspace;
- out-of-scope sentinels that did not reach the remote workspace;
- observed status codes, `Retry-After` values, and mount-log pathology flags.

The out-of-scope sentinel proves the harness's local-root bound: the mounted
local root stays inside the member's assigned path, so static read-context files
outside that root are not scanned or written. Daemon-side token-scope filtering
is complementary and is covered by the Tier-1 syncer fixture plus this harness's
mint-time path-token validator.

The mount state file is placed outside each member's mounted local root so
harness bookkeeping never becomes part of the scoped writeback surface.

The acceptance bar is the absence of the #1602 failure mode under N concurrent
scoped writebacks: no 500/object-reset/context-deadline. Graceful backpressure
as `429 workspace_busy` with `Retry-After` is acceptable at the effective
admission cap. The harness records observed caps; it does not bake a magic
inflight number into the result.
