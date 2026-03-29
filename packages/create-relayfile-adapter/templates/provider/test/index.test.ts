import { describe, expect, it } from "vitest";
import { ExampleProvider, providerConfigSchema } from "../src/index.js";

describe("providerConfigSchema", () => {
  it("declares the provider config contract", () => {
    expect(providerConfigSchema.kind).toBe("provider");
    expect(providerConfigSchema.name).toBe("{{provider}}");
    expect(providerConfigSchema.required).toEqual(["apiKey"]);
  });
});

describe("ExampleProvider", () => {
  it("reports healthy connections for non-empty ids", async () => {
    const provider = new ExampleProvider();

    await expect(provider.healthCheck("conn_test")).resolves.toBe(true);
  });

  it("returns a placeholder proxy response", async () => {
    const provider = new ExampleProvider();
    const response = await provider.proxy({
      method: "GET",
      path: "/tickets"
    });

    expect(response).toMatchObject({
      ok: true,
      provider: "{{projectName}}"
    });
  });
});
