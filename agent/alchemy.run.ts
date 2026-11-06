/**
 * layered-relayfile's host. Deployed only by
 * .github/workflows/agent-deploy.yml (layered-agents' reusable workflow),
 * never by hand without Khaliq. The first deploy creates the host: a full
 * deploy, from a manual dispatch with allow_restart.
 *
 * A fresh host: it creates its own role and instance profile
 * (layered-agent-relayfile-<stage>), which may read only the kit's two
 * shared parameters, and its own session and telemetry log groups. The role
 * carries the permissions boundary layered-agent-host-boundary-relayfile-<stage>,
 * which layered-agents' human-deployed platform owns: this deploy can never give
 * the host more than that, whatever agent.config.ts says. Its
 * network ids come from SSM under /relaycast-agent-host/<stage>/network/, the
 * existing agent-hosts VPC that relaycast-cloud's agent-host stack publishes.
 *
 * State: the shared "layered-agents" store (alchemy-state-layered-agents-<account>-<region>-an),
 * prefix agents/relayfile.
 */
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import * as Alchemy from "alchemy";
import * as AWS from "@alchemy.run/aws";
import * as Effect from "effect/Effect";
import { agentStatePrefix, guardProblems, layeredAgentHost, layeredAgentsState, readNetwork } from "@agentworkforce/layered-agent";
import agent from "./agent.config.ts";

const pin = process.env.LAYERED_AGENT_PIN_TEMPLATE;
const templateOverride = pin ? JSON.stringify(JSON.parse(readFileSync(pin, "utf8"))) : undefined;

export default Alchemy.Stack(
  "layered-agent-relayfile",
  { providers: AWS.providers(), state: layeredAgentsState(agentStatePrefix(agent.input.name)) },
  Effect.gen(function* () {
    const stage = yield* Alchemy.Stage;
    const env = yield* AWS.AWSEnvironment.current;
    const problems = guardProblems({ stage, accountId: env.accountId, region: env.region }, agent);
    if (problems.length > 0) throw new Error(`relayfile agent: not deployable; nothing was changed:\n  - ${problems.join("\n  - ")}`);
    const network = yield* Effect.promise(() => readNetwork(agent.names.platformPrefix, agent.region));
    return yield* layeredAgentHost(agent, {
      accountId: env.accountId,
      laneDir: fileURLToPath(new URL(".", import.meta.url)),
      network,
      templateOverride,
    });
  }),
);
