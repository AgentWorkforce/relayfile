import { describe, expect, it } from "vitest";
import { ExampleAdapter, adapterConfigSchema } from "../src/index.js";

describe("adapterConfigSchema", () => {
  it("declares the provider-specific adapter contract", () => {
    expect(adapterConfigSchema.kind).toBe("adapter");
    expect(adapterConfigSchema.name).toBe("{{provider}}");
    expect(adapterConfigSchema.required).toEqual(["apiKey"]);
  });
});

describe("ExampleAdapter", () => {
  it("projects a normalized webhook payload into a Relayfile file", async () => {
    const adapter = new ExampleAdapter();
    const files = await adapter.ingestWebhook({
      provider: "{{provider}}",
      eventType: "updated",
      objectType: "issues",
      objectId: "42",
      payload: {
        title: "Fix sample adapter"
      }
    });

    expect(files).toHaveLength(1);
    expect(files[0]).toMatchObject({
      path: "/{{provider}}/issues/42.json",
      contentType: "application/json"
    });
  });
});
