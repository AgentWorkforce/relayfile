import assert from "node:assert/strict";
import test from "node:test";

import { createRelayfileExecutor } from "./relayfile-executor.mjs";

test("materializeWebhook rejects missing paths before writing", async () => {
  const execute = createRelayfileExecutor();

  await assert.rejects(
    () => execute({
      input: {
        operation: {
          op: "materializeWebhook",
          provider: "linear",
          content: { id: "event-1" },
        },
      },
    }),
    /materializeWebhook operation is missing or invalid path/,
  );
});

test("materializeWebhook rejects root paths before writing", async () => {
  const execute = createRelayfileExecutor();

  await assert.rejects(
    () => execute({
      input: {
        operation: {
          op: "materializeWebhook",
          provider: "linear",
          path: "/",
          content: { id: "event-1" },
        },
      },
    }),
    /materializeWebhook operation is missing or invalid path/,
  );
});
