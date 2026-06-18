# Relayfile SDK Surface & Cloud Contract (Authoritative)

**Status:** Authoritative reference for the agents-integration example projects.
**SDK version pinned:** `@relayfile/sdk@0.10.1`
**Audience:** Anyone scaffolding a framework example (Vercel AI SDK, OpenAI Agents, Eve, Flue, LangChain).

> **Why this doc exists.** `docs/relayfile-agents-integration-spec.md` is an
> implementation-free proposal and several of its code snippets use APIs that do
> **not** match the shipped SDK. Pin example projects against *this* document, not
> the spec's pseudo-code. Every signature below is verified against SDK source with
> `file:line` citations.

---

## 0. Spec → Reality divergences (read first)

These are the spots where the spec's snippets will silently break if copied verbatim.

| Spec snippet | Reality | Action |
|---|---|---|
| `import { ensureMountedWorkspace } from "@relayfile/sdk"` (top-level fn) | **No such top-level export.** `ensureMountedWorkspace` is a **method on `RelayfileSetup`** (`src/setup.ts:325`). | Use `new RelayfileSetup(...)` → `setup.ensureMountedWorkspace(input)`. |
| `client.readFile(workspaceId, path)` | Correct — positional overload exists (`src/client.ts:1459`), **and** an options-object overload `readFile(input: ReadFileInput)`. | Either form is fine. |
| `client.writeFile({ workspaceId, path, content, contentType })` | Needs **`baseRevision`** (`If-Match` semantics). Use `baseRevision: "*"` for create-or-overwrite. | Always pass `baseRevision`. |
| `workspace.mountEnv()` / `workspace.agentInvite()` | Correct, both exist (`src/setup.ts:898`, `:924`). `agentInvite` is **sync** and does **not** downscope; use `agentInviteScoped()` (async, `:963`) for narrower scopes. | Use scoped variant for least-privilege examples. |
| `client.listTree(workspaceId, { path })` | Correct (`src/client.ts:1444`). | — |
| read cache opt-in | Read cache is **on by default** (TTL 5s, max 500 entries), auto-evicts on remote mutations and on local writes. Pass `readCache: false` to disable. | Examples don't need to enable it; mention it's automatic. |

---

## 1. Bootstrap pattern for examples (canonical)

Two-hop: Cloud control plane mints a short-lived Relayfile data-plane token, then the SDK talks to the data plane.

```
┌─ Cloud control plane ────────────┐      ┌─ Relayfile data plane ──────────┐
│ https://agentrelay.com/cloud     │      │ https://api.relayfile.dev       │
│ POST /api/v1/workspaces/{ws}/    │ ───► │ GET  /v1/workspaces/{rw}/fs/file │
│   relayfile/mount-session        │      │ POST /v1/workspaces/{rw}/fs/bulk │
│ Auth: Bearer <cloud accessToken> │      │ Auth: Bearer <relayfileToken>   │
└──────────────────────────────────┘      └─────────────────────────────────┘
```

**Resolution order each example uses (CI-safe first):**

1. **Env overrides (primary):** `CLOUD_API_URL`, `CLOUD_API_ACCESS_TOKEN`, `CLOUD_WORKSPACE_ID`, and/or a pre-minted `RELAYFILE_BASE_URL` + `RELAYFILE_TOKEN` + `RELAYFILE_WORKSPACE_ID`.
2. **Credential-file fallback:** probe `~/.cloud/credentials.json` (canonical) then the legacy `~/.relayfile/cloud-credentials.json`. **Log which file was chosen** so the operator isn't surprised when a stale file wins.

> ⚠️ **Use the workspace ID that `mount-session` RETURNS** for all `/v1/workspaces/{id}/...`
> calls — never the request-side app-UUID. The app-UUID ↔ `rw_…` shard split was the
> root cause of issue #306. Examples must demonstrate the correct plumbing.

### SDK path (preferred over raw fetch)

```ts
import { RelayfileSetup } from "@relayfile/sdk";

// fromCloudTokens: hand it the Cloud accessToken/refreshToken; it auto-refreshes.
const setup = RelayfileSetup.fromCloudTokens(
  { accessToken, refreshToken, accessTokenExpiresAt },
  { cloudApiUrl: process.env.CLOUD_API_URL ?? "https://agentrelay.com/cloud" },
);

const workspace = await setup.joinWorkspace(process.env.CLOUD_WORKSPACE_ID!);
const client = workspace.client(); // RelayFileClient bound to this workspace, auto-refreshing token

// downstream calls use workspace.workspaceId (the rw_ shard id the cloud handed back)
const tree = await client.listTree(workspace.workspaceId, { path: "/notion", depth: 2 });
```

---

## 2. Cloud contract (from cloud-expert, authoritative)

**Base URLs**
- Cloud control plane: `https://agentrelay.com/cloud` (prod). Override: `CLOUD_API_URL`. Local CLI default: `http://localhost:3000/cloud`.
- Relayfile data plane: `https://api.relayfile.dev` (prod). Override: `RELAYFILE_BASE_URL` / `RELAYFILE_URL`. Staging: `https://staging-api.relayfile.dev`.

**Credentials (canonical)**
- Cloud control-plane creds: **`~/.cloud/credentials.json`** — source of truth `packages/cli/src/credentials.ts` (Cloud repo). Contains `accessToken`, `refreshToken`, `apiUrl`.
- The relayfile CLI's `~/.relayfile/cloud-credentials.json` (`cmd/relayfile-cli/main.go:6870`) is **legacy/stale for Cloud control-plane auth** — probe only as a last resort.

**Token mint endpoint (use this for examples)**
```
POST {CLOUD_API_URL}/api/v1/workspaces/{workspaceId}/relayfile/mount-session
Authorization: Bearer <cloud api access token>
Content-Type: application/json

{ "localDir": "/tmp/relayfile-demo", "remotePath": "/", "mode": "poll",
  "agentName": "integration-verifier",
  "scopes": ["relayfile:fs:read:/notion/**", "relayfile:fs:write:/linear/**"] }
```
Response includes: `workspaceId` (the `rw_…` shard id — use this downstream), `relayfileBaseUrl`, `relayfileToken`, `wsUrl`, `scopes`, expiry fields.
A `delegated-token` route also exists (`POST .../relayfile/delegated-token`) for per-agent scoping; prefer `mount-session` for examples.

**Data-plane auth/header shape**
- Routes: `{RELAYFILE_BASE_URL}/v1/workspaces/{relayfileWorkspaceId}/...`
- `Authorization: Bearer <relayfileToken>`
- `X-Correlation-Id: <stable smoke id>` — **required** on fs routes.
- `Content-Type: application/json` for JSON writes.
- **Do NOT send `X-Workspace-Id`** — Cloud's Relayfile Worker sets it internally when forwarding to the DO; sending it can mis-route.
- Scopes: reads `fs:read` or `relayfile:fs:read:/provider/**`; writes/bulk `fs:write` or `relayfile:fs:write:/provider/**`. Request the narrowest scope each example needs.

**Bulk-write 202=sync contract (after cloud#2274 deploys)**
```
POST {RELAYFILE_BASE_URL}/v1/workspaces/{rw}/fs/bulk
{ "files": [{ "path": "/linear/labels/<id>.json", "content": "...",
              "contentType": "application/json", "encoding": "utf-8" }] }
```
`202` ⇒ files in `results` are immediately readable via `GET /fs/file?path=...`.
cloud#2274 routes `/fs/bulk` through the same provider shard resolver as reads (concurrent per-file shard lookup + aggregated mixed-provider fanout). **Not deployed yet as of this writing** — the live 202=sync smoke against prod waits for cloud-expert's deploy confirmation.

---

## 3. RelayFileClient (`src/client.ts`)

Constructor `(options: RelayFileClientOptions)` — `:1408`.

```ts
interface RelayFileClientOptions {
  baseUrl?: string;                          // default https://api.relayfile.dev
  token: string | (() => string | Promise<string>);
  fetchImpl?: typeof fetch;
  userAgent?: string;
  retry?: { maxRetries?; baseDelayMs?; maxDelayMs?; jitterRatio? };
  changeLog?: { retentionMs?; maxEntries? };
  readCache?: false | { ttlMs?; maxEntries? }; // on by default
}
```

| Method | Signature | Returns | Line |
|---|---|---|---|
| `listTree` | `(workspaceId, options?: { path?; depth?; cursor?; forkId?; correlationId?; signal? })` | `TreeResponse` | 1444 |
| `readFile` | `(workspaceId, path, correlationId?, signal?)` **or** `(input: ReadFileInput)` | `FileReadResponse` | 1459 |
| `queryFiles` | `(workspaceId, options?: { path?; provider?; relation?; permission?; comment?; properties?; cursor?; limit?; ... })` | `FileQueryResponse` | 1503 |
| `writeFile` | `(input: WriteFileInput)` — requires `baseRevision` | `WriteQueuedResponse` | 1530 |
| `bulkWrite` | `(input: { workspaceId; files: BulkWriteFile[]; forkId?; correlationId?; signal? })` | `BulkWriteResponse` | 1555 |
| `deleteFile` | `(input: { workspaceId; path; baseRevision; ... })` | `WriteQueuedResponse` | 1576 |
| `subscribe` | `(globs, onChange, options?)` | `Subscription` | 1644 |
| `connectWebSocket` | `(workspaceId, options?)` | `WebSocketConnection` | 1778 |
| `getToken` | `()` | `Promise<string>` | 1429 |
| `getBaseUrl` | `()` | `string` | 1440 |

`WriteFileInput`: `{ workspaceId, path, baseRevision, content, contentType?, encoding?: "utf-8"|"base64", semantics?, forkId?, contentIdentity?, correlationId?, signal? }` (`src/types.ts:802`). `baseRevision: "*"` = create-or-overwrite.

`BulkWriteFile`: `{ path, contentType?, content, encoding?: "utf-8"|"base64", contentIdentity? }` (`src/types.ts:100`).

---

## 4. RelayfileSetup (`src/setup.ts`)

Constructor `(options?: { cloudApiUrl?; accessToken?; requestTimeoutMs?; retry? })` — `:220`. Default cloud API `https://agentrelay.com/cloud` (`:61`).

| Method | Signature | Returns | Line |
|---|---|---|---|
| `login` (static) | `(options?)` — **`@relayfile/sdk/cli` only**, node-only | `Promise<RelayfileSetup>` | 189 |
| `fromCloudTokens` (static) | `(tokens: RelayfileCloudTokenSet, options?)` | `RelayfileSetup` | 199 |
| `createWorkspace` | `(options?: { name?; permissions?; agentName?; scopes? })` | `Promise<WorkspaceHandle>` | 230 |
| `joinWorkspace` | `(workspaceId, options?: { agentName?; scopes?; permissions? })` | `Promise<WorkspaceHandle>` | 270 |
| `mountWorkspace` | `(input: MountWorkspaceInput)` | `Promise<MountedWorkspaceHandle>` | 291 |
| `ensureMountedWorkspace` | `(input: EnsureMountedWorkspaceInput)` — **method, not top-level export** | `Promise<MountedWorkspaceHandle>` | 325 |
| `getCloudApiUrl` | `()` | `string` | 451 |

`RelayfileCloudTokenSet`: `{ apiUrl?, accessToken, refreshToken, accessTokenExpiresAt, refreshTokenExpiresAt? }`. Auto-refreshes within `refreshWindowMs` (default 60s).

---

## 5. WorkspaceHandle (`src/setup.ts`)

Properties: `info: WorkspaceInfo`, `workspaceId: string`.

| Method | Signature | Returns | Line |
|---|---|---|---|
| `client` | `()` — cached, auto-refreshing token | `RelayFileClient` | 549 |
| `connectIntegration` | `(provider, options?: { connectionId?; allowedIntegrations? })` | `Promise<ConnectIntegrationResult>` | 559 |
| `connectNotion` | `(options?)` | `Promise<ConnectIntegrationResult>` | 607 |
| `waitForConnection` | `(provider, options?: { connectionId?; pollIntervalMs?; timeoutMs?; signal?; onPoll? })` | `Promise<void>` | 616 |
| `isConnected` | `(provider, connectionId)` | `Promise<boolean>` | 682 |
| `listAccessibleResources` | `(provider)` | `Promise<Array<{ id; url; name?; scopes?; avatarUrl? }>>` | 795 |
| `mountEnv` | `(options?: { localDir?; remotePath?; mode?; relaycastBaseUrl? })` | `WorkspaceMountEnv` (Record) | 898 |
| `agentInvite` | `(options?)` — sync, **no** downscope | `AgentWorkspaceInvite` | 924 |
| `agentInviteScoped` | `(options?: { scopes?; agentName?; permissions?; ... })` — mints downscoped JWT | `Promise<AgentWorkspaceInvite>` | 963 |
| `getToken` | `()` | `string` | 885 |
| `refreshToken` | `()` | `Promise<void>` | 1015 |

`ConnectIntegrationResult`: `{ connectLink: string|null, sessionToken: string|null, expiresAt: string|null, alreadyConnected: boolean, connectionId: string }`.

`mountEnv()` returns: `RELAYFILE_BASE_URL`, `RELAYFILE_TOKEN`, `RELAYFILE_WORKSPACE`, `RELAYFILE_REMOTE_PATH`, `RELAYFILE_LOCAL_DIR`, `RELAYFILE_MOUNT_MODE`, `RELAYCAST_API_KEY`, `RELAY_API_KEY`, `RELAYCAST_BASE_URL`, `RELAY_BASE_URL`.

---

## 6. MountedWorkspaceHandle (`src/setup.ts:1081`)

`{ workspaceId, localDir, remotePath, mode: "poll"|"fuse", ready: boolean, expiresAt, suggestedRefreshAt }`
Methods: `env(): Record<string,string>`, `status(): Promise<MountedWorkspaceStatus>`, `stop(): Promise<void>`.

`EnsureMountedWorkspaceInput extends MountWorkspaceInput` with `{ provider?, verifyProvider?, providerReadyTimeoutMs? }`. `MountWorkspaceInput`: `{ workspace? | workspaceId, localDir, remotePath?, mode?: "poll"|"fuse", localLayout?: "exact"|"scoped", syncMode?: "mirror"|"write-only", background?, agentName?, scopes?, signal?, launcher?, readyTimeoutMs? }`.

---

## 7. Supported integration providers (`src/setup-types.ts:4`)

```
github · slack-sage · slack-my-senior-dev · slack-nightcto · notion · linear
```

`WORKSPACE_INTEGRATION_PROVIDERS` is the exact allowed set for `connectIntegration(provider)` and `EnsureMountedWorkspaceInput.provider`.

---

## 8. Existing examples to lift patterns from (`examples/`)

| Dir | Demonstrates | Real SDK calls |
|---|---|---|
| `01-agent-reads-files` | read-only | `listTree(ws, { depth: 2 })`, `readFile(ws, path)`, `queryFiles(ws, { provider })` |
| `02-agent-writes-files` | write + conflict | `writeFile({ ws, path, baseRevision: "*", content, contentType })`, `bulkWrite({ ws, files })`, catches `RevisionConflictError` |
| `03-webhook-to-vfs` | webhook ingest | — |
| `04-realtime-events` | change stream | `subscribe` / `connectWebSocket` |
| `05-relayauth-scoped-agent` | scoped tokens | two clients, different scopes; 403 on out-of-scope read/write |
| `06-writeback-consumer` | writeback handler | — |

Env vars these examples read: `RELAYFILE_TOKEN`, `WORKSPACE_ID` (and scoped variants in 05). The SDK does **not** auto-read `process.env` — the caller passes token + workspaceId explicitly.

---

_Verified against `@relayfile/sdk@0.10.1` source + cloud contract from cloud-expert. Update this doc in the same change if any of these signatures move._
