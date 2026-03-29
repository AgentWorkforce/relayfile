import { ExampleAdapter } from "../src/index.js";

async function main(): Promise<void> {
  const adapter = new ExampleAdapter();
  const files = await adapter.ingestWebhook({
    provider: "{{provider}}",
    eventType: "created",
    objectType: "tickets",
    objectId: "demo-1",
    payload: {
      title: "Sample Relayfile adapter workflow"
    }
  });

  console.log(JSON.stringify(files, null, 2));
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
