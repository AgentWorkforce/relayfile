/**
 * Vercel AI SDK agent that REACTS to live Linear file events via Relayfile.
 *
 * The killer multi-agent / proactive pattern:
 *   Linear webhook  →  Cloud sync  →  /linear/issues/X.json revision++  →  rf.onEvent fires
 *                                                                              ↓
 *                                                              agent reads + reacts
 *
 * No polling, no MCP request/response loop. The agent is event-driven by the
 * provider's own webhooks, ingested through Relayfile.
 *
 * Run (long-lived):
 *   ANTHROPIC_API_KEY=... CLOUD_WORKSPACE_ID=<app-uuid> npm run dev
 *
 * Then edit a Linear issue (label / status / title) — your agent will react in
 * seconds, no polling.
 */

import { anthropic } from "@ai-sdk/anthropic";
import { generateText } from "ai";

import { connect, tools, type FilesystemEvent } from "@relayfile/agents";

const rf = await connect({
  scopes: [
    "relayfile:fs:read:/linear/**",
    "relayfile:fs:write:/linear/**",
  ],
});

console.log(`Listening on workspace ${rf.workspaceId}.`);
console.log("Edit a Linear issue or label to trigger the agent. Ctrl-C to stop.\n");

async function react(event: FilesystemEvent) {
  console.log(`\n📥 event: ${event.type} @ ${event.path} rev=${event.revision}`);

  let context = "";
  try {
    const file = await rf.read(event.path);
    context = file.content.slice(0, 4000);
  } catch (err) {
    console.log(`  (file not yet readable: ${(err as Error).message})`);
    return;
  }
  const path = event.path;

  const result = await generateText({
    model: anthropic("claude-sonnet-4-6"),
    tools: tools.vercel(rf, { readPaths: ["/linear"] }),
    maxSteps: 4,
    system:
      "You are a Linear triage agent. When something changes in /linear, decide if " +
      "any action is needed (e.g. summarise, route, flag). Be concise — one short " +
      "paragraph max. Use the relayfile_* tools to read related context if needed.",
    prompt:
      `A Linear file just changed at ${path}. Content excerpt:\n\n${context}\n\n` +
      "Briefly: what is this and does it need attention?",
  });

  console.log(`🤖 ${result.text}\n`);
}

rf.onEvent(["/linear/**"], react);

// Keep the process alive until killed.
await new Promise(() => {});
