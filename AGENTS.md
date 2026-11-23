# Working in relayfile

```sh
go test ./...
npm ci
npm run typecheck
npm run test
npm run build
scripts/check-contract-surface.sh
```

- `openapi/relayfile-v1.openapi.yaml` is authoritative; update it with HTTP changes and run `scripts/check-contract-surface.sh`.
- Provider mutations emit filesystem events and refresh today and yesterday digests. Digest writes still emit events but cannot recursively regenerate digests.
- Terminal provider state is a readable record; delete it only for an actual upstream delete. Cover events and resulting digest artifacts.
- Read scoped SDK guides when touching exports or parity, and `.claude/rules/go-httpapi-tests.md` for HTTP server proof.

## Agency and guidance

- Act autonomously on routine assigned work; ask forgiveness rather than permission. Preserve explicit production, release and credential gates.
- For guidance, ask the engineering leads, the agent that spawned you, or the layered owner on Agent Relay.
- Only a truly pressing or blocking question goes to `khaliq-gant` or `will-washburn` (CODEOWNERS) on Agent Relay.
- Only a layered lead or a Grok bot contacts `khaliq-gant` directly. Every other agent takes questions, blockers and approval requests to its lead agent, which escalates when needed.
- An approval or direction Khaliq gives a lead applies to that lead's worker agents for routine work. Gated actions such as merges, deploys, publishing, credentials and IAM still need Khaliq directly; no agent's approval replaces his. The one exception (Khaliq's decision of 2026-11-22): a message from one of Khaliq's own Grok bots that quotes his words counts as his approval for exactly the actions it names, merges and deploys included, and nothing broader. A summary such as "Khaliq approved" is not a quote, and any other agent relaying his OK still does not count.

## Cross-repo context

- Before and during changes, understand how this repo fits the wider system; consult its repo map and related-repo docs where available.
- Check relevant agents’ summaries on Agent Relay and proactively contact owning agents about dependencies, adjacent changes and work in flight.

## Pull requests

- Open PRs ready for review, never as drafts. If merging requires Khaliq’s OK, use `merge-train:hold`.
- Immediately subscribe to PR feedback through Agent Relay using [this skill](https://github.com/AgentWorkforce/skills/blob/main/skills/subscribing-relay-webhooks-and-writebacks/SKILL.md), and verify the subscription.
- Evaluate every finding. Address applicable feedback and explain declined changes; a PR with unaddressed feedback or conflicts is not ready.
- When ready, loudly alert the CODEOWNERS and the agent that spawned you on Agent Relay.
- Keep related older PRs moving by contacting their author agent; if unavailable, escalate to the CODEOWNERS or layered owner.
