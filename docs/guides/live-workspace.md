# Prove a live Relayfile workspace across machines

The runnable launch demo is [`examples/live-workspace`](../../examples/live-workspace/README.md).
It provisions a fresh Daytona node through `agent-relay fleet spawn --sandbox`,
starts an agent in the Relayfile mount returned by Cloud, and verifies on the
laptop that the agent's uncommitted file and SHA-256 sidecar arrived intact.

```bash
cd examples/live-workspace
npm install
npm run preflight
npm run proof
```

This is a terminal proof, not a localhost dashboard. A run passes only when the
remote mount is live, the nonce matches, and the sandbox and laptop SHA-256
values are identical.

For the founder recording, public self-serve commands, narration, and cleanup
guardrails, use the [launch playbook](../../examples/live-workspace/LAUNCH_PLAYBOOK.md).

The command prints the exact drive-mode attach command after the agent launches.
Replace `--mode drive` with `--mode view` for read-only access, and press
`Ctrl+C` to detach without killing the agent.

For the reviewed multi-provider evidence behind the feature, see
[`docs/evidence/cloud-five-agent-collaboration-20260822`](../evidence/cloud-five-agent-collaboration-20260822/README.md).
