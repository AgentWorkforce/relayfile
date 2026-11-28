import { defineLayeredAgent } from "@agentworkforce/layered-agent";

const stage = process.env.LAYERED_AGENT_STAGE ?? "dev";

export default defineLayeredAgent({
  name: "relayfile",
  repo: "AgentWorkforce/relayfile",
  stage,
  accountId: process.env.LAYERED_AGENT_ACCOUNT_ID,
  arch: "x64",
  instanceType: "t3.medium",
  // The AMI the host already runs: moving the stack onto this pin does not replace it.
  ami: "ami-08cf17e3856f70154",
  rootVolumeGiB: 150,
  aptPackages: ["golang-go", "fuse3", "libfuse3-dev", "pkg-config"],
  laneFiles: ["lead-prompt.md", "setup-lane.sh", "load-secrets-lane.sh", "install-skills.py", "nango-skills.lock.json", "nango-persona.md"],
  repos: ["AgentWorkforce/relayfile", "AgentWorkforce/relayfile-cloud", "AgentWorkforce/cloud", "AgentWorkforce/relayfile-adapters", "AgentWorkforce/relayfile-control"],
  relayDescription: "RelayFile and RelayFile Cloud layered lead",
  // Watch and sweep only the RelayFile repos. AgentWorkforce/cloud stays cloned
  // but off the watch, so cloud's PR and issue traffic doesn't wake this lead
  // (Khaliq, 2026-11-27).
  stewardship: { repos: ["AgentWorkforce/relayfile", "AgentWorkforce/relayfile-cloud", "AgentWorkforce/relayfile-adapters", "AgentWorkforce/relayfile-control"] },
  names: { platformPrefix: `/relaycast-agent-host/${stage}` },
  workers: { maxConcurrent: 2, harness: "codex", model: "gpt-6.2-astra" },
  secrets: [{ envVar: "NANGO_INTEGRATIONS_KEY_PARAM", param: "nango-production-key", exportAs: "NANGO_SECRET_KEY" }],
  // Sessions MCP for its A2A card (Khaliq, 2026-11-13); must match layered-agents infra/agents.ts.
  relaySessions: { tokenParam: `/relaycast-agent-host/${stage}/relay-sessions-token-relayfile` },
  // On Codex until the Anthropic credit is topped up (Khaliq, 2026-11-27:
  // "yes bring relayfile back up on codex"). Its own OpenAI key parameter.
  lead: { harness: "codex", model: "gpt-6.2-astra", openaiKeyParam: `/relaycast-agent-host/${stage}/openai-api-key-relayfile` },
});
