# Post-Auth Mount-Session Guide

This guide explains how sandbox integrators (Daytona, E2B, local runners) attach an already-authorized workspace to a local directory using the `mountWorkspace` and `ensureMountedWorkspace` SDK methods.

The contract backing this guide is `docs/post-auth-mount-session-contract.md` (cloud#498).

---

## Product Flow

The post-auth mount flow requires **two prior steps** before a sandbox can mount a workspace:

1. **Auth** — the caller has a Relayfile Cloud identity (a cloud access token or a workspace-scoped JWT). Anonymous callers cannot mint mount sessions.
2. **Provider connect (optional but recommended)** — if the workspace uses an integration provider (GitHub, Notion, Linear, Slack), that provider should be connected and ready before mounting. `ensureMountedWorkspace` checks this automatically.

Only after these steps does the SDK issue the mount-session request. The resulting mounted workspace handle gives the sandbox access to the workspace's virtual filesystem as ordinary files in a local directory.

```
[Cloud auth] → [Provider connect] → mountWorkspace() → local files on disk
                     ↑
               skipped if verifyProvider: false
               or no provider needed
```

The `mountWorkspace` low-level method skips the provider check and goes straight to the mount-session request. `ensureMountedWorkspace` is the higher-level entry point that gates on provider readiness first.

---

## Why the Sandbox Only Receives a Workspace-Scoped Token

The Cloud route (`POST /api/v1/workspaces/{workspaceId}/relayfile/mount-session`) calls `createWorkspaceJoinAccess` with the caller's credentials. The response carries:

- `relayfileToken` — a workspace-scoped Relayfile JWT, not the caller's Cloud access token.
- `relayfileBaseUrl` — the Relayfile server the launched mount process will talk to.
- `expiresAt` / `suggestedRefreshAt` — expiry information from the join mint.

The Cloud access token (the user's login credential) and any user PII are **never** included in the mount-session response. The sandbox receives only what the mounted process needs: a workspace JWT, the Relayfile server URL, the WebSocket URL, and a Relaycast API key. This limits blast radius if the sandbox environment is compromised.

The scopes on the minted JWT are a subset of the caller's grant. If the caller passes `scopes: ["fs:read"]`, the issued token can only read — even if the caller's own credential allows writes.

---

## Cloud Endpoint Reference

```
POST /api/v1/workspaces/{workspaceId}/relayfile/mount-session
Authorization: Bearer <workspace-JWT or cloud-access-token>
Content-Type: application/json
```

### Request

```ts
type MountSessionRequest = {
  localDir: string;           // required; the sandbox's local mirror path
  remotePath?: string;        // default "/"; subtree to mount
  mode?: "poll" | "fuse";     // default "poll"; see Mode section
  agentName?: string;         // default "relayfile-mount"
  scopes?: string[];          // subset of caller's grant
  provider?: WorkspaceIntegrationProvider; // diagnostic hint only
};
```

`localDir` safety rules (the server validates, does not touch the filesystem):
- Non-empty, ≤ 1024 chars after trim.
- Must not contain NUL bytes, `..` path segments, or be exactly `/`.
- Must not resolve under `/proc`, `/sys`, `/dev`, `/etc/relayfile`.
- Windows drive roots (`C:\`) are rejected.

`remotePath` defaults to `"/"` when absent. Normalized to forward-slash, leading `/`, no trailing slash except root.

### Response

```ts
type MountSessionResponse = {
  workspaceId: string;
  relayfileBaseUrl: string;        // no trailing slash
  relayfileToken: string;          // workspace-scoped JWT
  wsUrl: string;
  remotePath: string;              // normalized
  localDir: string;                // echoed unchanged
  mode: "poll" | "fuse";
  scopes: string[];
  tokenIssuedAt: string | null;
  expiresAt: string | null;        // ISO 8601 from token mint
  suggestedRefreshAt: string | null;
  relaycastApiKey: string;
  relaycastBaseUrl?: string;       // present only when configured
};
```

### Error codes

| Status | Code | Trigger |
|--------|------|---------|
| 400 | `invalid_request` | malformed JSON / wrong shape |
| 400 | `invalid_mode` | `mode` outside `{poll, fuse}` |
| 400 | `invalid_local_dir` | localDir fails safety rules |
| 400 | `invalid_remote_path` | remotePath fails normalization |
| 400 | `invalid_scopes` | scopes invalid or exceed caller's grant |
| 401 | `unauthorized` | no or invalid auth |
| 403 | `forbidden` | auth present but wrong workspace |
| 404 | `workspace_not_found` | unknown workspace ID |
| 500 | `mount_session_failed` | token mint failed |

---

## SDK Usage

### Installation

```bash
npm install @relayfile/sdk
```

### `RelayfileSetup.mountWorkspace`

Use when you already have a `WorkspaceHandle` or a bare `workspaceId`. No provider check is performed — the caller is responsible for knowing the provider is ready.

```ts
import { RelayfileSetup } from "@relayfile/sdk";

const setup = await RelayfileSetup.login();
const workspace = await setup.joinWorkspace("ws_abc123");

// Mount by WorkspaceHandle
const handle = await setup.mountWorkspace({
  workspace,
  localDir: "/sandbox/mnt",
  remotePath: "/notion",
  mode: "poll",               // default; omit for the same result
});

// Or mount by workspaceId (SDK joins internally)
const handle2 = await setup.mountWorkspace({
  workspaceId: "ws_abc123",
  localDir: "/sandbox/mnt",
});

console.log(handle.ready);          // true after first sync
console.log(handle.expiresAt);      // ISO timestamp from token mint
console.log(handle.suggestedRefreshAt);

// Env block for child processes
const env = handle.env();
// Contains: RELAYFILE_BASE_URL, RELAYFILE_TOKEN, RELAYFILE_WORKSPACE,
//           RELAYFILE_REMOTE_PATH, RELAYFILE_LOCAL_DIR, RELAYFILE_MOUNT_MODE,
//           RELAYCAST_API_KEY, RELAY_API_KEY, RELAYCAST_BASE_URL, RELAY_BASE_URL

// Stop when done
await handle.stop();
```

**Input type:**

```ts
interface MountWorkspaceInput {
  workspace?: WorkspaceHandle;   // preferred when caller already has one
  workspaceId?: string;          // alternative; SDK joins internally
  localDir: string;
  remotePath?: string;           // default "/"
  mode?: "poll" | "fuse";        // default "poll"
  background?: boolean;          // default true; false = one-shot probe only
  agentName?: string;
  scopes?: string[];
  signal?: AbortSignal;
  launcher?: MountLauncher;      // for tests; defaults to bundled launcher
  readyTimeoutMs?: number;       // default 60_000
}
```

Exactly one of `workspace` / `workspaceId` must be provided. Passing both or neither throws `MountSessionInputError`.

### `RelayfileSetup.ensureMountedWorkspace`

Use when the workspace has an integration provider that must be connected before mounting. This is the standard entry point for sandbox integrators.

```ts
import { RelayfileSetup, ProviderNotConnectedError, ProviderNotReadyError } from "@relayfile/sdk";

const setup = await RelayfileSetup.login();
const workspace = await setup.joinWorkspace("ws_abc123");

try {
  const handle = await setup.ensureMountedWorkspace({
    workspace,
    localDir: "/sandbox/mnt",
    remotePath: "/notion",
    provider: "notion",
    verifyProvider: true,        // default; checks provider before mounting
    providerReadyTimeoutMs: 30_000,
    scopes: ["fs:read"],
  });
  // provider was connected and ready; handle is live
} catch (err) {
  if (err instanceof ProviderNotConnectedError) {
    console.error(`Provider ${err.provider} is not connected to this workspace.`);
  } else if (err instanceof ProviderNotReadyError) {
    console.error(
      `Provider ${err.provider} connected but not ready. State: ${err.state ?? "unknown"}`
    );
  }
  throw err;
}
```

**Additional input fields (extends `MountWorkspaceInput`):**

```ts
interface EnsureMountedWorkspaceInput extends MountWorkspaceInput {
  provider?: WorkspaceIntegrationProvider; // required when verifyProvider: true
  verifyProvider?: boolean;                // default true
  providerReadyTimeoutMs?: number;         // default 0 (no extra wait)
}
```

### `MountedWorkspaceHandle`

```ts
interface MountedWorkspaceHandle {
  readonly workspaceId: string;
  readonly localDir: string;           // absolute path
  readonly remotePath: string;         // normalized
  readonly mode: "poll" | "fuse";
  readonly ready: boolean;             // mirrors last status() snapshot
  readonly expiresAt: string | null;   // from cloud response; stable
  readonly suggestedRefreshAt: string | null;

  env(): Record<string, string>;
  status(): Promise<MountedWorkspaceStatus>;
  stop(): Promise<void>;
}
```

`status()` reads `${localDir}/.relay/state.json` first; falls back to an HTTP probe when the file is absent or stale. `stop()` is idempotent and never deletes `${localDir}` or its contents.

---

## Mode Mapping (`poll` vs `fuse`)

v1 ships exactly two modes: `"poll"` and `"fuse"`. The name `"stream"` does not exist at any layer — the API returns `400 invalid_mode` and the SDK throws `InvalidMountModeError` before making any HTTP call.

| Mode | Mechanism | Default |
|------|-----------|---------|
| `"poll"` | Background sync loop; optionally accelerated by a WebSocket invalidation channel | **Yes** |
| `"fuse"` | Kernel FUSE mount; gated by build tags; unavailable in the OSS build | No |

**Why `poll` is the default:** The shipping mount loop (`cmd/relayfile-mount/main.go`) implements `poll` unconditionally. The FUSE path is guarded by a build tag and returns `errFuseModeUnavailable` in the OSS build. Defaulting to `fuse` would mean most installations fail at startup. `poll` works everywhere and already uses a WebSocket channel to minimize latency.

**FUSE error handling:** When `mode: "fuse"` is requested and the binary emits `errFuseModeUnavailable`, the launcher throws `MountModeUnavailableError`. It never silently falls back to `poll` — that would hide a configuration mismatch from callers who need FUSE semantics.

---

## Provider Verification Behavior

`ensureMountedWorkspace` checks the provider state before issuing any mount-session request. No token is minted if the check fails.

| `verifyProvider` | Provider connected? | Provider ready? | Result |
|-----------------|---------------------|-----------------|--------|
| `true` (default) | no | — | throws `ProviderNotConnectedError(provider)` |
| `true` | yes | no | waits up to `providerReadyTimeoutMs`; throws `ProviderNotReadyError({ provider, state, initialSyncState })` on timeout |
| `true` | yes | yes | proceeds to mount |
| `false` | — | — | proceeds to mount; no provider claim on handle |

`ProviderNotConnectedError` carries `.provider` and `.code = "provider_not_connected"`.

`ProviderNotReadyError` carries `.provider`, `.state`, `.initialSyncState`, and `.code = "provider_not_ready"`. These fields come directly from the integration status route so callers can surface meaningful diagnostics without an extra round-trip.

When `verifyProvider: true` and no `provider` is supplied, the SDK throws `MountSessionInputError("provider required when verifyProvider=true")` before any network call.

`mountWorkspace` (without "ensure") never checks providers. Use it when you know the provider is ready or when no provider is involved.

---

## Daytona Example

```ts
import { RelayfileSetup, ProviderNotConnectedError } from "@relayfile/sdk";
import Daytona from "@daytonaio/sdk";

const daytona = new Daytona();
const sandbox = await daytona.create();

const setup = await RelayfileSetup.login();
const workspace = await setup.joinWorkspace(process.env.RELAYFILE_WORKSPACE_ID);

// Connect Notion if not already connected
const conn = await workspace.connectIntegration("notion");
if (conn.connectLink) {
  console.log("Open to authorize Notion:", conn.connectLink);
  await workspace.waitForNotion({ timeoutMs: 300_000 });
}

// Mount with provider verification
const handle = await setup.ensureMountedWorkspace({
  workspace,
  localDir: "/home/daytona/relayfile",
  remotePath: "/notion",
  provider: "notion",
  verifyProvider: true,
  scopes: ["fs:read", "fs:write"],
});

console.log("Mounted. expires:", handle.expiresAt);

// Run agent code in sandbox using the env block
await sandbox.process.executeCommand("ls /home/daytona/relayfile", {
  env: handle.env(),
});

await handle.stop();
await daytona.delete(sandbox);
```

---

## E2B Example

```ts
import { RelayfileSetup } from "@relayfile/sdk";
import { Sandbox } from "e2b";

const setup = await RelayfileSetup.login();

// Mount by workspaceId directly — SDK joins internally
const handle = await setup.mountWorkspace({
  workspaceId: process.env.RELAYFILE_WORKSPACE_ID,
  localDir: "/home/user/relayfile",
  remotePath: "/github",
  scopes: ["fs:read"],
});

const sandbox = await Sandbox.create("base", { envs: handle.env() });

const result = await sandbox.commands.run("ls /home/user/relayfile");
console.log(result.stdout);

await handle.stop();
await sandbox.kill();
```

---

## Local / CI Example

```ts
import { RelayfileSetup, MountReadyTimeoutError } from "@relayfile/sdk";
import { execFile } from "node:child_process";
import path from "node:path";

const setup = await RelayfileSetup.login();
const workspace = await setup.joinWorkspace(process.env.RELAYFILE_WORKSPACE_ID);

const localDir = path.resolve("./relayfile-mount");

const handle = await setup.mountWorkspace({
  workspace,
  localDir,
  mode: "poll",
  readyTimeoutMs: 30_000,
}).catch((err) => {
  if (err instanceof MountReadyTimeoutError) {
    console.error("Mount did not become ready within 30s. Check network or workspace state.");
  }
  throw err;
});

// Run agent process with environment block
const env = { ...process.env, ...handle.env() };
execFile("node", ["./agent.js"], { env });

// background: true (default) — stop() when done
process.on("SIGINT", () => handle.stop().then(() => process.exit(0)));
```

**One-shot probe (CI/test):** Pass `background: false` to skip the long-running launcher and get a handle that reflects a single sync probe. `stop()` is a no-op in this mode.

```ts
const handle = await setup.mountWorkspace({
  workspace,
  localDir,
  background: false,   // one-shot; relayfile-mount --once
  readyTimeoutMs: 15_000,
});
console.log("Probe ready:", handle.ready);
// handle.stop() is a no-op
```

---

## Testing with the Mount Harness

Production code uses the bundled launcher (which runs `relayfile-mount`). Tests inject the in-process `startMountHarness` harness from `@relayfile/sdk/mount-harness` to avoid spawning a real process:

```ts
import { RelayfileSetup } from "@relayfile/sdk";
import { startMountHarness } from "@relayfile/sdk/mount-harness";

const harnessLauncher = {
  async start(input) {
    const harness = await startMountHarness({ env: input.env });
    return {
      pid: undefined,
      ready: harness.syncOnce().then(() => {}),
      status: async () => ({ ready: true, mode: "poll", expiresAt: null, suggestedRefreshAt: null }),
      stop: () => harness.stop(),
    };
  },
};

const handle = await setup.mountWorkspace({
  workspace,
  localDir: tmpDir,
  launcher: harnessLauncher,
});
```

The harness honors the same readiness contract as the real launcher: `started` + first `sync.completed` event satisfies the readiness gate.

---

## How This Differs from `WorkspaceHandle.mountEnv()`

`WorkspaceHandle.mountEnv()` is the **lower-level** building block. It returns an env-var record from the handle's existing join token. The caller is responsible for:

- Deciding which token to use (the join token, which may have broad scopes).
- Starting and supervising the `relayfile-mount` process.
- Detecting FUSE unavailability.
- Reading `state.json` or probing HTTP to check readiness.
- Stopping the process on cleanup.

`mountWorkspace` / `ensureMountedWorkspace` wrap all of that:

| Concern | `mountEnv()` | `mountWorkspace()` |
|---------|--------------|--------------------|
| Token source | join token (broad) | fresh mount-session token (caller's requested scopes) |
| Provider verification | none | `ensureMountedWorkspace` checks before minting |
| Process management | caller | SDK launcher (default) or injected `MountLauncher` |
| Readiness gate | caller polls | SDK awaits `launcher.ready` |
| FUSE error | silent | `MountModeUnavailableError` |
| `expiresAt` / `suggestedRefreshAt` | not available | on handle and in `status()` |
| `stop()` | not available | idempotent; drains in-flight syncs; never deletes files |

The env block returned by `MountedWorkspaceHandle.env()` is a superset of `mountEnv()` — it includes the same keys (`RELAYFILE_BASE_URL`, `RELAYFILE_TOKEN`, `RELAYFILE_WORKSPACE`, `RELAYFILE_REMOTE_PATH`, `RELAYFILE_LOCAL_DIR`, `RELAYFILE_MOUNT_MODE`, `RELAYCAST_API_KEY`, `RELAY_API_KEY`, `RELAYCAST_BASE_URL`, `RELAY_BASE_URL`) but the token value comes from the mount-session response, not the join token.

Use `mountEnv()` only when you need fine-grained control over process supervision and are willing to implement all of the above yourself. For all sandbox integrations, prefer `mountWorkspace` or `ensureMountedWorkspace`.

---

## Error Reference

All errors are subclasses of `RelayfileSetupError` and re-exported from `@relayfile/sdk`.

| Class | Code | When thrown |
|-------|------|------------|
| `MountSessionInputError` | `mount_session_input_error` | Both or neither of `workspace`/`workspaceId` supplied; or `verifyProvider: true` without `provider` |
| `InvalidMountModeError` | `invalid_mount_mode` | `mode: "stream"` or any unrecognized mode value |
| `InvalidLocalDirError` | `invalid_local_dir` | `localDir` fails server-side safety rules |
| `InvalidRemotePathError` | `invalid_remote_path` | `remotePath` fails normalization |
| `ProviderNotConnectedError` | `provider_not_connected` | `verifyProvider: true` and provider not connected |
| `ProviderNotReadyError` | `provider_not_ready` | `verifyProvider: true` and provider connected but not ready within `providerReadyTimeoutMs` |
| `MountModeUnavailableError` | `mount_mode_unavailable` | FUSE requested but binary reports `errFuseModeUnavailable` |
| `MountReadyTimeoutError` | `mount_ready_timeout` | Launcher did not reach readiness within `readyTimeoutMs` |
| `CloudAbortError` | `cloud_abort_error` | `signal.abort()` called during mount |
