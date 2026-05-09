# Post-Auth Mount Session Self-Review

Date: 2026-05-09

## Verdict

Changes requested.

## Findings

1. High: the published SDK contract and guide still describe a `ProviderNotReadyError(..., lastState)` surface that the implementation does not export.

   The implementation exposes `ProviderNotReadyError.state` and `ProviderNotReadyError.initialSyncState`, not `lastState` ([packages/sdk/typescript/src/setup-errors.ts](/Users/khaliqgant/Projects/AgentWorkforce/.workflow-worktrees/post-auth-mount-session/relayfile/packages/sdk/typescript/src/setup-errors.ts:187)). But the contract still says timeout should throw `ProviderNotReadyError(provider, lastState)` ([docs/post-auth-mount-session-contract.md](/Users/khaliqgant/Projects/AgentWorkforce/.workflow-worktrees/post-auth-mount-session/relayfile/docs/post-auth-mount-session-contract.md:389)), and the guide still tells consumers to read `err.lastState` and documents `.lastState` as the public field ([docs/guides/post-auth-mount-session.md](/Users/khaliqgant/Projects/AgentWorkforce/.workflow-worktrees/post-auth-mount-session/relayfile/docs/guides/post-auth-mount-session.md:196), [docs/guides/post-auth-mount-session.md](/Users/khaliqgant/Projects/AgentWorkforce/.workflow-worktrees/post-auth-mount-session/relayfile/docs/guides/post-auth-mount-session.md:252)).

   Impact: the user-facing SDK contract in this slice is internally inconsistent. Consumers following the guide or contract will reach for a property that does not exist on the shipped error class.

   Fix: either rename the implementation to match the documented `lastState` surface, or update the contract, guide prose, and examples to the actual exported shape (`state` and `initialSyncState`).

2. Medium: the cloud mount-session route tests still do not cover the full acceptance matrix that the contract requires.

   The contract’s required route coverage includes scoped-success, anonymous-workspace + valid Relayfile JWT, invalid/expired Relayfile JWT, unknown workspace, malformed `remotePath`, exhaustive invalid `localDir` cases, and `relaycastBaseUrl` omission when unset ([docs/post-auth-mount-session-contract.md](/Users/khaliqgant/Projects/AgentWorkforce/.workflow-worktrees/post-auth-mount-session/relayfile/docs/post-auth-mount-session-contract.md:480)). The current suite covers the happy path, workspace API token, same-workspace Relayfile JWT, missing auth, wrong-workspace JWT, missing `localDir`, invalid `mode`, response shaping, and `invalid_scopes` only ([tests/relayfile-mount-session-route.test.ts](/Users/khaliqgant/Projects/AgentWorkforce/.workflow-worktrees/post-auth-mount-session/cloud/tests/relayfile-mount-session-route.test.ts:255)).

   Impact: the main auth and validation edges described as required acceptance cases are still unverified in the cloud worktree.

   Fix: add the missing §8.1 route cases before merge.

## Verification

Focused verification passed:

- `relayfile/packages/sdk/typescript`: `npm test -- --run src/setup.test.ts src/onWrite.test.ts src/mount-launcher.test.ts`
- `relayfile/packages/sdk/typescript`: `npm run typecheck`
- `cloud`: `node ./node_modules/vitest/vitest.mjs run tests/relayfile-mount-session-route.test.ts packages/web/app/api/v1/linear/query/route.test.ts`
- `cloud`: `npm run typecheck --silent`

The previous Cloud/SDK mount-session 400-shape mismatch is fixed: the SDK now accepts both `httpBody.code` and `httpBody.error`, and the regression is covered in `setup.test.ts`.

SELF_REVIEW_CHANGES_REQUESTED: align the documented ProviderNotReadyError surface with the exported SDK API, and add the missing mount-session route coverage required by contract §8.1.
