# Relayfile feature verification procedures

This document is routed by `manifest.yaml`; it intentionally does not repeat a second list of feature IDs. Run from the repository root. Every shell procedure starts in an isolated fixture and records one terminal result with `vf_result`.

Outcome rules are strict:

- `PASS`: every listed positive and negative assertion ran and matched.
- `FAIL`: a command ran but an assertion, cleanup, or safety fence failed.
- `SKIP`: the requested environment or credential was absent. A skip is never included in the pass count.
- `MANUAL`: the check is inherently interactive/destructive or only an operator can confirm the external effect. Manual is never included in the pass count.

Never run a hosted/provider procedure against a non-disposable workspace. Preserve command output in `.workflow-artifacts/verify-features/`; redact tokens and HMAC secrets.

## cli-discovery-and-lifecycle

### Prerequisites

Go 1.22+, Node 22+, `bash`, and no live credentials. Supervisor install/uninstall and default interactive setup are manual-only.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start cli
trap vf_cleanup EXIT
go build -o "$VF_CASE_ROOT/relayfile" ./cmd/relayfile-cli
export RELAYFILE_STATE_FILE="$VF_CASE_ROOT/state/state.json"
export RELAYFILE_SOCK="$VF_CASE_ROOT/state/control.sock"
```

### Commands

```bash
"$VF_CASE_ROOT/relayfile" help >"$VF_CASE_ROOT/help.txt"
"$VF_CASE_ROOT/relayfile" --help >"$VF_CASE_ROOT/help-flag.txt"
"$VF_CASE_ROOT/relayfile" version >"$VF_CASE_ROOT/version.txt"
"$VF_CASE_ROOT/relayfile" writeback sweep-drafts --help >"$VF_CASE_ROOT/sweep-help.txt"
"$VF_CASE_ROOT/relayfile" listen --help >"$VF_CASE_ROOT/listen-help.txt"
```

### Positive assertions

```bash
cmp "$VF_CASE_ROOT/help.txt" "$VF_CASE_ROOT/help-flag.txt"
grep -Eq '[0-9]+\.[0-9]+\.[0-9]+' "$VF_CASE_ROOT/version.txt"
grep -q 'sweep-drafts' "$VF_CASE_ROOT/sweep-help.txt"
grep -q 'listen' "$VF_CASE_ROOT/listen-help.txt"
```

### Negative assertions

```bash
if "$VF_CASE_ROOT/relayfile" definitely-not-a-command >"$VF_CASE_ROOT/bad.out" 2>&1; then exit 1; fi
grep -Eqi 'unknown|usage' "$VF_CASE_ROOT/bad.out"
```

### Cleanup

The EXIT trap calls `vf_cleanup`, which requires the fixture marker and a `relayfile-feature-*` basename before recursive removal. No home directory or supervisor state is touched.

### Automation limits

Report setup, supervisor mutation, observer browser launch, and destructive stop/restart against a real daemon as `MANUAL` unless a disposable service fixture is supplied. The raw Go version fallback may differ from release-injected builds; report the observed value rather than rewriting it.

### Reporting

```bash
vf_result PASS cli-discovery-and-lifecycle 'help, version, hidden leaves, and unknown-command negative matched'
```

## workspace-auth-and-scoped-invites

### Prerequisites

Tier 1 negatives require Go. Hosted preflight requires `RELAYFILE_LIVE=1`, a disposable `RELAYFILE_CLOUD_API_URL`, token, and workspace. The full create/join/invite/mount/delete sequence remains manual until it has a receipt-bearing disposable harness.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start workspace-auth
trap vf_cleanup EXIT
go build -o "$VF_CASE_ROOT/relayfile" ./cmd/relayfile-cli
export RELAYFILE_STATE_FILE="$VF_CASE_ROOT/state/state.json"
```

### Commands

```bash
"$VF_CASE_ROOT/relayfile" help >"$VF_CASE_ROOT/help.txt"
go test ./cmd/relayfile-cli -run 'TestWorkspace(RequiresSubcommandMentionsJoin|UseSetsDefaultWorkspace|JoinStoresDelegatedRelayfileCredentials|DeleteRemovesCatalogEntry|ListIncludes|Current)' -count=1 | tee "$VF_CASE_ROOT/workspace-tests.log"
go test ./internal/httpapi -run 'Test(AuthRequired|ScopeAndWorkspaceClaimsEnforced)' -count=1 | tee "$VF_CASE_ROOT/auth-tests.log"
if [[ "${RELAYFILE_LIVE:-0}" == 1 ]]; then
  : "${RELAYFILE_CLOUD_API_URL:?}" "${RELAYFILE_CLOUD_TOKEN:?}"
  "$VF_CASE_ROOT/relayfile" workspace status "${RELAYFILE_WORKSPACE_ID:?}" >"$VF_CASE_ROOT/live-status.json"
fi
```

### Positive assertions

```bash
grep -q 'relayfile workspace' "$VF_CASE_ROOT/help.txt"
grep -q '^ok' "$VF_CASE_ROOT/workspace-tests.log"
grep -q '^ok' "$VF_CASE_ROOT/auth-tests.log"
if [[ "${RELAYFILE_LIVE:-0}" == 1 ]]; then grep -q "${RELAYFILE_WORKSPACE_ID}" "$VF_CASE_ROOT/live-status.json"; fi
```

### Negative assertions

```bash
if "$VF_CASE_ROOT/relayfile" workspace join >"$VF_CASE_ROOT/join.out" 2>&1; then exit 1; fi
if "$VF_CASE_ROOT/relayfile" workspace use >"$VF_CASE_ROOT/use.out" 2>&1; then exit 1; fi
grep -Eqi 'usage.*workspace join' "$VF_CASE_ROOT/join.out"
grep -Eqi 'usage.*workspace use' "$VF_CASE_ROOT/use.out"
```

### Cleanup

The deterministic block creates no remote workspace. For the manual hosted sequence, remove only the disposable workspace created for that run, then let the marker-checked EXIT trap remove local state. If remote deletion was not confirmed, report `FAIL`, retain its ID in evidence, and do not call cleanup complete.

### Automation limits

Without live inputs, local negatives may pass but hosted create/join/invite/mount-session assertions are `SKIP`. Login needing a browser and workspace delete remain `MANUAL` unless the disposable fixture explicitly authorizes them.

### Reporting

```bash
if [[ "${RELAYFILE_LIVE:-0}" == 1 ]]; then vf_result MANUAL workspace-auth-and-scoped-invites 'hosted status preflight passed; create, scoped invite, mount session, and confirmed workspace cleanup still require operator evidence'; else vf_result SKIP workspace-auth-and-scoped-invites 'hosted workspace credentials absent; local negatives only'; fi
```

## integration-catalog-connect-and-bindings

### Prerequisites

Go, Node, a Unix-socket-capable host, and optionally `RELAYFILE_LIVE=1` with a disposable provider connection. Provider mutation requires explicit live opt-in.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start integrations
trap vf_cleanup EXIT
go build -o "$VF_CASE_ROOT/relayfile" ./cmd/relayfile-cli
export RELAYFILE_SOCK="$VF_CASE_ROOT/state/control.sock"
```

### Commands

```bash
"$VF_CASE_ROOT/relayfile" integration available --json >"$VF_CASE_ROOT/catalog.json"
"$VF_CASE_ROOT/relayfile" integration search github --json >"$VF_CASE_ROOT/search.json"
"$VF_CASE_ROOT/relayfile" integration resolve-path github owner/repo >"$VF_CASE_ROOT/path.out"
npm run test --workspace=@relayfile/client 2>&1 | tee "$VF_CASE_ROOT/client-test.log"
```

### Positive assertions

```bash
grep -q 'github' "$VF_CASE_ROOT/catalog.json"
grep -q 'github' "$VF_CASE_ROOT/search.json"
grep -q '/github/' "$VF_CASE_ROOT/path.out"
grep -Eqi 'pass|passed' "$VF_CASE_ROOT/client-test.log"
```

### Negative assertions

```bash
if "$VF_CASE_ROOT/relayfile" integration connect unknown-provider >"$VF_CASE_ROOT/unknown.out" 2>&1; then exit 1; fi
"$VF_CASE_ROOT/relayfile" integration disconnect github >"$VF_CASE_ROOT/fence.out" 2>&1
grep -Eqi 'unknown|--yes|confirm' "$VF_CASE_ROOT/unknown.out" "$VF_CASE_ROOT/fence.out"
grep -Eqi 'aborted|declined' "$VF_CASE_ROOT/fence.out"
```

### Cleanup

Unbind only paths created in the disposable workspace, disconnect only a connection created by this run, confirm absence through `integration list`, then run marker-checked local cleanup.

### Automation limits

Fallback catalog and canonical path helpers are Tier 1. OAuth, browser consent, Nango/Composio receipt, and real unbind/disconnect are `SKIP` without live inputs and `MANUAL` when provider UI confirmation is required.

### Reporting

```bash
vf_result PASS integration-catalog-connect-and-bindings 'fallback catalog, path mapping, client contract, and destructive negatives passed'
```

## filesystem-crud-query-export

### Prerequisites

Go, Node 22, and a free loopback port. The conformance harness creates a local RS256/JWKS fixture and an isolated workspace in memory.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start filesystem
trap vf_cleanup EXIT
VF_PORT="$(vf_free_port)"
export VF_PORT
```

### Commands

```bash
env -u RELAYFILE_BASE_URL \
  RELAYFILE_PORT="$VF_PORT" \
  RELAYFILE_DEV_MODE=1 \
  RELAYFILE_CONFORMANCE_BINARY="$VF_CASE_ROOT/server" \
  npx tsx scripts/conformance.ts --ci | tee "$VF_CASE_ROOT/conformance.log"
```

### Positive assertions

```bash
grep -Fq 'Read file returns content and metadata' "$VF_CASE_ROOT/conformance.log"
grep -Fq 'Tree lists files and directories' "$VF_CASE_ROOT/conformance.log"
grep -Fq 'Query by property (author=agent-beta)' "$VF_CASE_ROOT/conformance.log"
grep -Fq 'JSON export returns all files with metadata' "$VF_CASE_ROOT/conformance.log"
```

### Negative assertions

```bash
grep -Fq 'Stale revision returns 409 with conflict details' "$VF_CASE_ROOT/conformance.log"
grep -Fq 'If-Match: 0 fails for existing file' "$VF_CASE_ROOT/conformance.log"
grep -Fq 'ACL marker denies access to unauthorized agent' "$VF_CASE_ROOT/conformance.log"
test ! -e "$VF_CASE_ROOT/server"
```

### Cleanup

The conformance harness deletes its fixture file, stops its recorded child, closes the JWKS server, and unlinks the temp binary before returning. The EXIT trap then removes only the marker-checked fixture.

### Automation limits

GitHub fetch-import requires a disposable reachable tarball and is `SKIP` locally; uploaded archive traversal and size bounds remain deterministic.

### Reporting

```bash
vf_result PASS filesystem-crud-query-export 'authenticated CRUD, tree, query, export, stale revision, create-only, and ACL negatives passed'
```

## acl-revision-and-fork-safety

### Prerequisites

Go tests; hosted scope positives require two separately scoped disposable tokens.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start acl-forks
trap vf_cleanup EXIT
```

### Commands

```bash
go test ./internal/httpapi ./internal/relayfile -run 'Test.*(ACL|IfMatch|Fork|Revision|Conflict)' -count=1 | tee "$VF_CASE_ROOT/tests.log"
```

### Positive assertions

```bash
grep -q '^ok' "$VF_CASE_ROOT/tests.log"
```

### Negative assertions

The selected tests must observe a stale `If-Match` conflict and deny a token outside its path scope; a test filter that reports “no tests to run” is a `FAIL`.

```bash
grep -Eqi 'fork|acl|revision|conflict' internal/httpapi/*_test.go internal/relayfile/*_test.go
! grep -q 'no tests to run' "$VF_CASE_ROOT/tests.log"
```

### Cleanup

Go tests use in-memory/temp stores. Remove the marker-checked fixture; for hosted follow-up, discard the fork and confirm both tokens can no longer read its overlay.

### Automation limits

Hosted JWKS and two-token scope proofs are `SKIP` without disposable credentials. Never weaken token scopes to make a fixture pass.

### Reporting

```bash
vf_result PASS acl-revision-and-fork-safety 'ACL, stale revision, conflict, and fork lifecycle tests passed'
```

## events-websocket-and-recovery

### Prerequisites

Go and Node; no external receiver is required for local event feed and WebSocket tests.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start events
trap vf_cleanup EXIT
```

### Commands

```bash
go test ./internal/httpapi ./internal/relayfile -run 'Test.*(Event|WebSocket|Cursor)' -count=1 | tee "$VF_CASE_ROOT/go.log"
npm run test --workspace=packages/sdk/typescript -- --run src/sync.test.ts | tee "$VF_CASE_ROOT/sdk.log"
```

### Positive assertions

```bash
grep -q '^ok' "$VF_CASE_ROOT/go.log"
grep -Eqi 'pass|passed' "$VF_CASE_ROOT/sdk.log"
```

### Negative assertions

Verify the tests include stale/invalid cursor or reconnect coverage and fail if only a live WebSocket happy path ran.

```bash
rg -q 'reconnect|cursor|fallback' packages/sdk/typescript/src/sync.test.ts internal/httpapi/*_test.go
```

### Cleanup

Close every WebSocket/subscription in the test finally block and remove the marker-checked fixture. A leaked listener or process is `FAIL`.

### Automation limits

Network partition timing on a hosted workspace is Tier 5 and `SKIP` without opt-in. Local polling fallback is deterministic.

### Reporting

```bash
vf_result PASS events-websocket-and-recovery 'event cursor, websocket, reconnect, and fallback assertions passed'
```

## provider-ingest-events-and-digests

### Prerequisites

Go. Live provider sequences require a disposable object and credentials for each claimed backend.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start ingest-digests
trap vf_cleanup EXIT
```

### Commands

```bash
go test ./internal/relayfile ./internal/digest -run 'Test.*(Ingest|Digest|Envelope|Terminal|Delete)' -count=1 | tee "$VF_CASE_ROOT/tests.log"
```

### Positive assertions

The fixture must assert both the provider file event and changed `/digests/today.md` plus `/digests/yesterday.md` artifacts.

```bash
grep -q '^ok' "$VF_CASE_ROOT/tests.log"
rg -q 'digests/(today|yesterday)\.md' internal/relayfile internal/digest
```

### Negative assertions

Assert digest writes do not recurse and a terminal status update remains a file update; deletion is accepted only for an actual upstream delete.

```bash
rg -q 'isDigest|digest.*path|/digests/' internal/relayfile internal/digest
rg -q 'closed|merged|archived|completed|canceled|resolved' internal/relayfile .claude/rules/relayfile-integration-digests.md
```

### Cleanup

Remove only provider objects labeled with the run marker; confirm the corresponding real delete event, then remove the local fixture. Leave terminal-state objects intact until the explicit delete phase.

### Automation limits

Generic ingest tests are deterministic. Nango and Composio create/update/terminal/delete sequences are separate Tier 5 checks and `SKIP` without credentials; a JSON-only Nango dry run is not a live pass.

### Reporting

```bash
vf_result PASS provider-ingest-events-and-digests 'generic ingest event, digest artifact, non-recursion, and terminal-state rules passed'
```

## writeback-retry-dead-letter-and-receipts

### Prerequisites

Go and Node. A provider receipt assertion needs an explicitly disposable external record.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start writeback
trap vf_cleanup EXIT
```

### Commands

```bash
go test ./internal/relayfile ./internal/httpapi -run 'Test.*(Writeback|Operation|Replay|DeadLetter|Draft|StaleRunning)' -count=1 | tee "$VF_CASE_ROOT/go.log"
npm run test --workspace=packages/sdk/typescript -- --run src/writeback-consumer.test.ts | tee "$VF_CASE_ROOT/sdk.log"
```

### Positive assertions

```bash
grep -q '^ok' "$VF_CASE_ROOT/go.log"
grep -Eqi 'pass|passed' "$VF_CASE_ROOT/sdk.log"
```

### Negative assertions

The fixture must reject an invalid acknowledgement/receipt, bound retries, and retain a dead-lettered operation for replay.

```bash
rg -q 'invalid.*receipt|dead.?letter|attempt' internal/relayfile/*_test.go packages/sdk/typescript/src/*test.ts
```

### Cleanup

Acknowledge or delete only fixture operations after recording evidence. Confirm no pending item with the run correlation ID remains; remove the marker-checked fixture.

### Automation limits

Mock receipts prove local state transitions. A real provider-specific receipt is Tier 5 and `SKIP` without live credentials. `writeback skip-stuck` and destructive provider delete are `MANUAL` unless the fixture explicitly creates the stuck cursor/object.

### Reporting

```bash
vf_result PASS writeback-retry-dead-letter-and-receipts 'queue, retry, replay, dead-letter, invalid receipt, and draft assertions passed'
```

## mirror-mount-bootstrap-outbox-and-fences

### Prerequisites

Go, Node, loopback ports, and filesystem watchers. No FUSE permission is needed.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start mount
trap vf_cleanup EXIT
export RELAYFILE_LOCAL_DIR="$VF_CASE_ROOT/mount"
export RELAYFILE_MOUNT_STATE_DIR="$VF_CASE_ROOT/state"
mkdir -p "$RELAYFILE_LOCAL_DIR"
```

### Commands

```bash
go test ./internal/mountsync -count=1 | tee "$VF_CASE_ROOT/mountsync.log"
VF_PORT="$(vf_free_port)"
RELAYFILE_PORT="$VF_PORT" npx tsx scripts/e2e.ts --ci | tee "$VF_CASE_ROOT/e2e.log"
```

### Positive assertions

```bash
grep -q '^ok' "$VF_CASE_ROOT/mountsync.log"
grep -Eqi 'pass|passed' "$VF_CASE_ROOT/e2e.log"
```

### Negative assertions

Confirm tests cover snapshot delete-ratio blocking, clobber acknowledgement, unsafe rehome/PID reuse, bootstrap stall/resume, tombstones, and untracked lazy push refusal.

```bash
rg -q 'delete.*ratio|clobber|rehome|PID|tombstone|stall|untracked' internal/mountsync cmd/relayfile-cli/*_test.go
```

### Cleanup

Stop only the PID recorded under `$VF_CASE_ROOT/state`, verify it belongs to the built mount binary, wait for exit, then remove the marker-checked fixture. Never signal a PID from an unverified state file.

### Automation limits

The local two-mount flow is deterministic. E2E's absent outbound receiver must be recorded `SKIP` rather than included in its pass count. OS supervisor and long-running token renewal remain `MANUAL` or bounded dedicated tests.

### Reporting

```bash
vf_result PASS mirror-mount-bootstrap-outbox-and-fences 'mountsync, two-mount flow, bootstrap, outbox, and destructive fences passed'
```

## fuse-virtual-artifacts-and-invalidation

### Prerequisites

Linux/macOS with FUSE installed, explicit mount permission, `RELAYFILE_LIVE_FUSE=1`, an existing disposable Relayfile workspace, and `RELAYFILE_BASE_URL`, `RELAYFILE_TOKEN`, and `RELAYFILE_WORKSPACE` set for that fixture.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start fuse
trap vf_cleanup EXIT
mkdir -p "$VF_CASE_ROOT/mount"
```

### Commands

```bash
if [[ "${RELAYFILE_LIVE_FUSE:-0}" != 1 ]]; then vf_result SKIP fuse-virtual-artifacts-and-invalidation 'FUSE opt-in absent'; exit 0; fi
go test ./internal/mountfuse -count=1 | tee "$VF_CASE_ROOT/unit.log"
VF_MOUNT_BIN="${RELAYFILE_MOUNT_BIN:-relayfile-mount}"
"$VF_MOUNT_BIN" --mode fuse --local-dir "$VF_CASE_ROOT/mount" --state-dir "$VF_CASE_ROOT/state" >"$VF_CASE_ROOT/fuse.log" 2>&1 &
VF_FUSE_PID=$!
printf '%s\n' "$VF_FUSE_PID" >"$VF_CASE_ROOT/state/fuse.pid"
for _ in {1..100}; do
  if mount | grep -Fq -- "$VF_CASE_ROOT/mount"; then break; fi
  kill -0 "$VF_FUSE_PID" 2>/dev/null
  sleep 0.1
done
mount | grep -Fq -- "$VF_CASE_ROOT/mount"
```

### Positive assertions

Read a normal file and each generated layout/schema/digest artifact, mutate the remote fixture, and assert the cached read invalidates to the new revision.

```bash
grep -q '^ok' "$VF_CASE_ROOT/unit.log"
test -r "$VF_CASE_ROOT/mount/LAYOUT.md"
```

### Negative assertions

```bash
if printf x >"$VF_CASE_ROOT/mount/LAYOUT.md" 2>"$VF_CASE_ROOT/readonly.err"; then exit 1; fi
grep -Eqi 'read.?only|operation not permitted' "$VF_CASE_ROOT/readonly.err"
```

### Cleanup

Verify `$VF_CASE_ROOT/state/fuse.pid` still names the child started by the command block. On Linux run `fusermount3 -u "$VF_CASE_ROOT/mount"` (or `fusermount -u` when that is the installed implementation); on macOS run `umount "$VF_CASE_ROOT/mount"`. Wait for `$VF_FUSE_PID`, confirm the exact mountpoint is absent from the platform mount table, then remove the marker-checked fixture. Failure to unmount or reap the recorded child is `FAIL`, not `SKIP`; automation must never signal an unverified PID.

### Automation limits

Unsupported host, missing FUSE package, or missing opt-in is `SKIP`. Interactive privilege escalation is `MANUAL`; automation must not invoke sudo.

### Reporting

```bash
vf_result PASS fuse-virtual-artifacts-and-invalidation 'FUSE read, invalidation, virtual artifact, readonly negative, and unmount passed'
```

## typescript-exports-and-client-flow

### Prerequisites

Node 22 and root dependencies installed with `npm ci`.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start ts-sdk
trap vf_cleanup EXIT
```

### Commands

```bash
npm run build --workspace=packages/core
npm run build --workspace=packages/sdk/typescript
npm run test --workspace=packages/sdk/typescript | tee "$VF_CASE_ROOT/test.log"
(cd packages/sdk/typescript && npx vitest run src/client.test.ts -t 'durable resource subscriptions' --reporter=verbose) | tee "$VF_CASE_ROOT/durable-subscriptions.log"
node scripts/check-sdk-parity.mjs | tee "$VF_CASE_ROOT/parity.log"
```

### Positive assertions

```bash
grep -Eqi 'pass|passed' "$VF_CASE_ROOT/test.log"
grep -q 'creates, lists, claims, accepts, and cancels through owner-scoped routes' "$VF_CASE_ROOT/durable-subscriptions.log"
grep -Eqi 'pass|ok' "$VF_CASE_ROOT/parity.log"
node -e "import('@relayfile/sdk').then(m=>{if(!m.RelayFileClient)process.exit(1)})"
```

### Negative assertions

Call the local client with a stale revision and an unreachable loopback URL; assert typed conflict/network failure instead of a successful empty result.

```bash
rg -q 'RevisionConflictError|RelayFileApiError' packages/sdk/typescript/src/client.test.ts packages/sdk/typescript/src/errors.ts
grep -q 'surfaces capability 404s and validates identifiers before a request' "$VF_CASE_ROOT/durable-subscriptions.log"
```

### Cleanup

Close subscriptions and clients in test finally blocks; remove the marker-checked fixture. Build output is project-native and may remain for later gates.

### Automation limits

Retained changes, hosted webhooks, setup, mount-session methods, and a live durable-subscription service are `SKIP` without hosted inputs. The durable-subscription unit flow proves exact routes, owner stripping, claim-token handling, and validation but does not claim that a hosted deployment implements the OpenAPI capability. Export presence alone does not pass a hosted behavior check.

### Reporting

```bash
vf_result PASS typescript-exports-and-client-flow 'build, parity, root import, durable subscription lifecycle, client positives, and typed negatives passed'
```

## python-exports-parity-and-client-flow

### Prerequisites

Python 3.11+, `uv`, and locked extras synchronized.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start py-sdk
trap vf_cleanup EXIT
```

### Commands

```bash
( cd packages/sdk/python && uv sync --all-extras --locked )
( cd packages/sdk/python && uv run --locked python -m pytest tests ) | tee "$VF_CASE_ROOT/pytest.log"
node scripts/check-sdk-parity.mjs | tee "$VF_CASE_ROOT/parity.log"
( cd packages/sdk/python && uv run --locked python -c 'import relayfile; assert len(relayfile.__all__) == len(set(relayfile.__all__))' )
```

### Positive assertions

```bash
grep -Eqi 'passed' "$VF_CASE_ROOT/pytest.log"
grep -Eqi 'pass|ok' "$VF_CASE_ROOT/parity.log"
```

### Negative assertions

The client tests must assert typed errors for invalid state, oversize payload, queue full, and revision conflict.

```bash
rg -q 'InvalidStateError|PayloadTooLargeError|QueueFullError|RevisionConflictError' packages/sdk/python/tests
```

### Cleanup

Close async clients/subscriptions and remove the marker-checked fixture. `.venv` is ignored project state and is not recursively deleted by this procedure.

### Automation limits

Hosted setup and `on_write` network recovery are `SKIP` without a disposable workspace. Sync and async local mock flows remain deterministic.

### Reporting

```bash
vf_result PASS python-exports-parity-and-client-flow 'locked pytest, parity, unique __all__, and typed error assertions passed'
```

## package-exports-control-plane-and-local-mount

### Prerequisites

Node 22, Go, and Unix sockets for local control-plane tests.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start packages
trap vf_cleanup EXIT
export RELAYFILE_SOCK="$VF_CASE_ROOT/state/control.sock"
```

### Commands

```bash
npm run build --workspace=packages/core
npm run build --workspace=@relayfile/client
npm run build --workspace=packages/local-mount
npm run test --workspace=@relayfile/client | tee "$VF_CASE_ROOT/client.log"
npm run test --workspace=packages/local-mount | tee "$VF_CASE_ROOT/local.log"
```

### Positive assertions

```bash
grep -Eqi 'pass|passed' "$VF_CASE_ROOT/client.log" "$VF_CASE_ROOT/local.log"
node -e "Promise.all([import('@relayfile/core'),import('@relayfile/client'),import('@relayfile/local-mount')]).then(x=>{if(x.some(v=>!v))process.exit(1)})"
```

### Negative assertions

Assert control-plane API version mismatch fails and local mount readonly/exclude rules reject writes or population outside configured roots.

```bash
rg -q 'apiVersion|version.*mismatch' packages/client/src/*test.ts
rg -q 'readonly|exclude|outside|travers' packages/local-mount/src/*test.ts
```

### Cleanup

Stop the exact temp control-plane socket owner, run local-mount final sync/teardown, verify no symlink escapes the fixture, then remove the marker-checked fixture.

### Automation limits

Platform copy/symlink differences must be reported separately. A skipped optional symlink test is `SKIP`, not package PASS, unless all required platform assertions ran.

### Reporting

```bash
vf_result PASS package-exports-control-plane-and-local-mount 'package imports, control-plane tests, and local mount safety passed'
```

## agent-framework-tools-events-and-forwarder

### Prerequisites

Node 22. Hosted/Relaycast forwarding needs disposable tokens and `RELAYFILE_LIVE=1`.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start agents
trap vf_cleanup EXIT
```

### Commands

```bash
npm run build --workspace=packages/agents
npx vitest run --reporter=verbose packages/agents/src/forward.test.ts packages/agents/src/tools/read-roots.test.ts | tee "$VF_CASE_ROOT/test.log"
node -e "Promise.all([import('@relayfile/agents'),import('@relayfile/agents/vercel'),import('@relayfile/agents/openai'),import('@relayfile/agents/langchain')]).then(x=>{if(x.length!==4)process.exit(1)})"
```

### Positive assertions

```bash
grep -Eqi 'pass|passed' "$VF_CASE_ROOT/test.log"
```

### Negative assertions

Invoke each framework read tool with an obvious out-of-prefix path and assert rejection; the current implementation is a lexical prefix filter and must not be described as canonical path traversal protection. Simulate forwarding exclusions with the focused test fixture.

```bash
grep -Fq 'rejects an out-of-prefix read' "$VF_CASE_ROOT/test.log"
grep -Fq 'skips delete operations and adapter skip views' "$VF_CASE_ROOT/test.log"
```

### Cleanup

Close agent subscriptions and delete only messages/files bearing the fixture correlation ID. Remove the marker-checked local fixture.

### Automation limits

Framework mocks are deterministic. Hosted credential refresh and Relaycast forwarding are `SKIP` without opt-in; sending to a real channel is `MANUAL` unless a disposable destination is supplied.

### Reporting

```bash
vf_result PASS agent-framework-tools-events-and-forwarder 'exports, framework tools, path fences, and event recovery passed'
```

## server-backends-auth-and-ingress

### Prerequisites

Go; PostgreSQL requires a disposable `RELAYFILE_TEST_POSTGRES_DSN`. Never point tests at production DSNs.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start server
trap vf_cleanup EXIT
export RELAYFILE_DATA_DIR="$VF_CASE_ROOT/data"
export RELAYFILE_STATE_FILE="$VF_CASE_ROOT/state/state.json"
```

### Commands

```bash
go test ./cmd/relayfile ./internal/httpapi ./internal/relayfile -count=1 | tee "$VF_CASE_ROOT/local.log"
if [[ -n "${RELAYFILE_TEST_POSTGRES_DSN:-}" ]]; then go test ./internal/relayfile -run Postgres -count=1 | tee "$VF_CASE_ROOT/postgres.log"; fi
```

### Positive assertions

```bash
grep -q '^ok' "$VF_CASE_ROOT/local.log"
if [[ -n "${RELAYFILE_TEST_POSTGRES_DSN:-}" ]]; then grep -q '^ok' "$VF_CASE_ROOT/postgres.log"; fi
```

### Negative assertions

Assert invalid bearer signature/scope, bad HMAC, stale timestamp, replayed delivery, oversize body, and rate-limit rejection. Also assert zero `StoreOptions` leaves delete-storm and stale-running protections disabled so the catalog does not misstate production policy.

```bash
rg -q 'HMAC|replay|skew|rate|body.*limit|scope' internal/httpapi/*_test.go
rg -q 'defaults.*disabled|Disabled unless|Threshold.*0' internal/relayfile/delete_storm_test.go internal/relayfile/stale_running_test.go
```

### Cleanup

Drop only the disposable PostgreSQL schema/database named for the run if one was created, close queues/workers, and remove the marker-checked local fixture.

### Automation limits

Memory/file backends and auth negatives are deterministic. PostgreSQL is `SKIP` without the explicit test DSN. Dormant server safety options are truthful library-only PASS, not production-enabled PASS.

### Reporting

```bash
if [[ -n "${RELAYFILE_TEST_POSTGRES_DSN:-}" ]]; then vf_result PASS server-backends-auth-and-ingress 'memory, file, PostgreSQL, auth, ingress, and truthful dormant fences passed'; else vf_result SKIP server-backends-auth-and-ingress 'PostgreSQL absent; deterministic memory/file/auth assertions passed'; fi
```

## cloud-observer-status-and-alerts

### Prerequisites

Local Go/Node for dashboard/admin tests; hosted observer/change/webhook checks need `RELAYFILE_LIVE=1` and disposable credentials.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start observability
trap vf_cleanup EXIT
```

### Commands

```bash
go test ./internal/httpapi -run 'Test.*(Dashboard|Admin|Ingress|Sync|Backend)' -count=1 | tee "$VF_CASE_ROOT/go.log"
npm run test --workspace=@relayfile/file-observer | tee "$VF_CASE_ROOT/ui.log"
```

### Positive assertions

```bash
grep -q '^ok' "$VF_CASE_ROOT/go.log"
grep -Eqi 'pass|passed' "$VF_CASE_ROOT/ui.log"
```

### Negative assertions

Assert admin endpoints deny missing admin scope and alert pagination/truncation stays bounded. A local 404 for hosted retained-change or outbound-webhook routes is an expected `SKIP`, never a positive assertion.

```bash
rg -q 'admin:read|alertsTruncated|nextCursor' internal/httpapi/*_test.go openapi/relayfile-v1.openapi.yaml
```

### Cleanup

Stop the local observer/server fixture and delete only a hosted webhook subscription created by this run. Confirm deletion before removing local evidence.

### Automation limits

Local dashboard/admin/UI is deterministic. Hosted observer, retained changes, public receiver delivery, DLQ, and replay are `SKIP` without live inputs. Browser visual confirmation is `MANUAL`.

### Reporting

```bash
vf_result PASS cloud-observer-status-and-alerts 'local dashboard, admin status, alerts, scope negative, and observer tests passed; hosted surfaces reported separately'
```

## packed-artifacts-and-release-smoke

### Prerequisites

Node 22, Go, Python/uv, build toolchains, and a temp directory with enough space. Publishing credentials are neither required nor permitted.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start packed
trap vf_cleanup EXIT
mkdir -p "$VF_CASE_ROOT/packs" "$VF_CASE_ROOT/node-consumer" "$VF_CASE_ROOT/python-consumer"
```

### Commands

```bash
node scripts/packed-feature-e2e.mjs | tee "$VF_CASE_ROOT/packed.log"
scripts/check-contract-surface.sh | tee "$VF_CASE_ROOT/contract.log"
```

### Positive assertions

The packed E2E installs tarballs and a wheel into clean temp consumers, imports every declared export subpath, resolves the platform binary, and verifies CLI version plus Python root imports.

```bash
grep -Fq 'PACKED_FEATURE_E2E_PASS npm_tarballs=7 python_wheel=1 declared_imports=15' "$VF_CASE_ROOT/packed.log"
grep -Fq 'contract check passed' "$VF_CASE_ROOT/contract.log"
git diff --exit-code -- packages/client/src/generated/control-plane.ts
```

### Negative assertions

The packed E2E asserts an undeclared SDK subpath import fails and verifies that the built packages contain the expected declared entrypoints.

```bash
grep -Fq 'undeclared_negative=1' "$VF_CASE_ROOT/packed.log"
! rg -q 'npm publish|git tag|gh release create|twine upload' "$VF_CASE_ROOT/packed.log"
```

### Cleanup

The packed E2E removes its own prefix-checked temp consumer in a `finally` block. The procedure trap removes only its marker-checked evidence fixture. Do not run `npm publish`, create tags/releases, mutate package versions, or delete project build output.

### Automation limits

Pack/install/import is Tier 4 deterministic. Cross-OS native execution is matrix-dependent. Any publish, tag, GitHub Release, PyPI upload, or provenance confirmation is `MANUAL` and excluded from automated PASS.

### Reporting

```bash
vf_result PASS packed-artifacts-and-release-smoke 'clean pack/install/import/version/local-flow and negative package assertions passed; publish remains MANUAL'
```

## personas-evals-and-guardian

### Prerequisites

Node 22. Live Slack is optional and only enabled by explicit `SLACK_CHANNEL`; provider evals require `OPENROUTER_API_KEY` and live opt-in.

### Isolated setup

```bash
source scripts/feature-runbook-lib.sh
vf_start automation
trap vf_cleanup EXIT
```

### Commands

```bash
node scripts/validate-feature-catalog.mjs | tee "$VF_CASE_ROOT/catalog.log"
npx tsx --test .agentworkforce/agents/relayfile-feature-guardian/manifest-contract.test.ts .agentworkforce/agents/relayfile-feature-guardian/agent.test.ts | tee "$VF_CASE_ROOT/tests.log"
npm run evals:compile
: >"$VF_CASE_ROOT/evals.log"
for VF_EVAL_SUITE in concurrency permissions provider-readiness slack-events vfs-contracts writeback; do
  RELAYFILE_EVAL_RUNS_DIR="$VF_CASE_ROOT/eval-runs" \
    node scripts/evals/run-relayfile-evals.mjs --mode offline --suite "$VF_EVAL_SUITE" | tee -a "$VF_CASE_ROOT/evals.log"
done
```

### Positive assertions

```bash
grep -q 'FEATURE_CATALOG_PASS' "$VF_CASE_ROOT/catalog.log"
grep -Eqi 'pass' "$VF_CASE_ROOT/tests.log"
test "$(grep -c 'failed=0 skipped=0' "$VF_CASE_ROOT/evals.log")" -eq 6
```

### Negative assertions

Tests must reject malformed/oversized guardian state, wrong clone path/scope, non-404 bootstrap responses, unsafe manifest shrink, CAS ambiguity, and receiptless Slack responses. Validator mutation tests must reject all A3 drift classes.

```bash
rg -q 'oversized|404|CAS|receipt|shrink|scope|path escape|stale summary' .agentworkforce/agents/relayfile-feature-guardian/*.test.ts
```

### Cleanup

The deterministic tests use temp/in-memory state and mock Slack; eval run artifacts are redirected under `$VF_CASE_ROOT/eval-runs` and removed by the marker-checked trap. For live opt-in, delete the fixture Slack message only if policy permits, record its external receipt, and never grant read access to the Slack mount.

### Automation limits

The six selected offline suites are deterministic. The `initial-integrations-e2e` suite is a live/manual provider workflow, and the broader `integrations` suite currently contains human-review coverage plus a pre-existing unsupported `jqFilter` case; neither is included in deterministic PASS. Provider evals are `SKIP` without credentials. Real Slack delivery is `SKIP` without input and must use idempotent post-then-checkpoint semantics when enabled.

### Reporting

```bash
vf_result PASS personas-evals-and-guardian 'catalog, omission contract, guardian failure matrix, and six deterministic offline eval suites passed; live/human suites and optional delivery reported separately'
```
