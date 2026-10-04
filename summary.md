# PR summary

## What changed

- Accept bounded provider-namespaced `upstream:v1:...` event cursors in mountsync checkpoint validation while retaining support for `0` and `evt_<n>` cursors.
- Keep the self-hosted checkpoint verifier, CLI lifecycle validation, and all three OpenAPI checkpoint cursor schemas aligned with the backend-neutral contract.
- Add regression coverage for legacy and upstream cursor forms, multi-event `:N` suffixes, the 400-character payload boundary, malformed/control/whitespace inputs, mixed `evt_`/`upstream:` feeds, and destination checkpoint verification with an upstream cursor.

## Verification

- `go test ./internal/mountsync ./internal/relayfile ./cmd/relayfile-cli`
- `scripts/check-contract-surface.sh`
- `git diff --check`

## Rollout note

This compatibility change must be released before AgentWorkforce/relayfile-cloud#293 begins emitting canonical upstream event IDs. If that ordering cannot be guaranteed, #293 should gate the new IDs behind a rollout flag until compatible Relayfile clients are deployed.
