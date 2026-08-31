# Relayfile launch playbook

## The claim

> An agent writes an uncommitted file in a fresh cloud machine. The same bytes
> appear on your laptop through Relayfile, without Git, curl, or a host path.

The demo earns that claim with a run-specific nonce and two independent
SHA-256 calculations. Do not lead with a dashboard. Lead with the machine
boundary, the ordinary filesystem write, and the verified local read.

If someone asks “why not Git, rsync, S3, or an API?”, the answer is not that
moving bytes was impossible before Relayfile. The difference is that those
approaches make the agent or human operate the transfer. Relayfile makes the
workspace itself available on both machines, so the product interaction remains
“open and save a file.”

Today that means live uncommitted work can move between laptops and sandboxes.
It opens the door to durable agents, cross-machine handoff, shared human/agent
review, and eventually reattach or teleport—but keep the Agent37 claims labeled
as fast-follow until its acceptance flow is literally green.

## Founder demo

Use two terminals. Record both if possible.

One-time setup:

```bash
cd examples/live-workspace
npm install
npm run setup
npm run preflight
```

Terminal A starts the proof:

```bash
npm run proof
```

As soon as it prints `Attach:`, paste that command into Terminal B. Detach with
`Ctrl+C`; the agent keeps running. Let Terminal A finish. Stop on the `PASS`
receipt and show these lines:

- remote hostname and mount path
- the local `relayfile read` command
- matching SHA-256
- `Git commits: 0`
- the measured write-to-read observation

For repeat takes, reuse the node and mount path printed by the first run instead
of provisioning another sandbox:

```bash
npm run proof -- --node <node-name> --mount-path <absolute-mount-path>
```

If that node is not placement-ready, attach to the already-running proof agent
or release stale agents before retrying. Do not create a sequence of fresh
sandboxes just to get another recording take.

Suggested narration:

1. “This is a real Daytona machine, not localhost.”
2. “The agent is writing with normal filesystem calls. No Git and no transfer API.”
3. “Relayfile makes that workspace present on both machines.”
4. “The laptop verifies the exact bytes against the sandbox's SHA-256.”
5. “Now I can attach to the same running agent from another terminal.”

## Public self-serve path

Once this example is merged to the public default branch, a user can run:

```bash
git clone https://github.com/AgentWorkforce/relayfile.git
cd relayfile/examples/live-workspace
npm install
npm run setup
npm run proof
```

The proof fails closed. A user gets `PASS` only after a real mount, remote
filesystem write, local read, nonce match, and SHA-256 match.

## Cleanup and launch limits

The proof leaves the agent running so attach remains demonstrable. Release the
agent with the exact command printed by the run.

Agent Relay 11.8.7 does not yet expose successful Cloud sandbox deletion from
the fleet CLI. Until that lands, ask users to delete the sandbox from Cloud
fleet controls after the demo and position this as a controlled public beta,
not an unlimited sandbox loop.

Do not claim a universal millisecond SLA from one run. It is accurate to say
that Relayfile verified the exact bytes across machines and to quote the
observation printed by that specific run.
