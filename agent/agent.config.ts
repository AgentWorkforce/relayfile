import { defineLayeredAgent } from "@agentworkforce/layered-agent";

const stage = process.env.LAYERED_AGENT_STAGE ?? "dev";

export default defineLayeredAgent({
  name: "relayfile",
  repo: "AgentWorkforce/relayfile",
  stage,
  accountId: process.env.LAYERED_AGENT_ACCOUNT_ID,
  arch: "x64",
  instanceType: "m7i.xlarge",
  rootVolumeGiB: 150,
  aptPackages: ["golang-go", "fuse3", "libfuse3-dev", "pkg-config"],
  laneFiles: ["lead-prompt.md", "setup-lane.sh", "load-secrets-lane.sh", "install-skills.py", "nango-skills.lock.json", "nango-persona.md"],
  repos: ["AgentWorkforce/relayfile", "AgentWorkforce/relayfile-cloud", "AgentWorkforce/cloud", "AgentWorkforce/relayfile-adapters"],
  relayDescription: "RelayFile and RelayFile Cloud layered lead",
  names: { platformPrefix: `/relaycast-agent-host/${stage}` },
  workers: { maxConcurrent: 2 },
  secrets: [{ envVar: "NANGO_INTEGRATIONS_KEY_PARAM", param: "nango-production-key", exportAs: "NANGO_SECRET_KEY" }],
});
