import { describe, expect, it } from "vitest";

import { AdapterRegistry } from "./registry.js";
import { DuplicateAdapterNameError, PluginValidationError } from "./errors.js";
import {
  type ConnectionProviderLike,
  type IntegrationAdapterLike,
  assertSemverishVersion,
  assertStringName,
  validateConnectionProvider,
  validateIntegrationAdapter,
  validateRegistrationInput,
} from "./validation.js";
import * as sdk from "./index.js";

function createProvider(
  overrides: Partial<ConnectionProviderLike> = {}
): ConnectionProviderLike {
  return {
    name: "github",
    proxy: async () => ({ status: 200 }),
    healthCheck: async () => true,
    handleWebhook: async (payload) => payload,
    ...overrides,
  };
}

function createAdapter(
  overrides: Partial<IntegrationAdapterLike> = {}
): IntegrationAdapterLike {
  return {
    name: "github",
    version: "1.2.3",
    provider: createProvider(),
    ingestWebhook: async () => ({ ok: true }),
    computePath: (objectType = "issues", objectId = "1") =>
      `/github/${String(objectType)}/${String(objectId)}.json`,
    computeSemantics: () => ({ properties: { provider: "github" } }),
    sync: async () => ({ ok: true }),
    writeBack: async () => undefined,
    supportedEvents: () => ["created"],
    ...overrides,
  };
}

describe("plugin validation", () => {
  it("accepts a valid connection provider and integration adapter", () => {
    const provider = createProvider();
    const adapter = createAdapter({ provider });

    const validatedProvider = validateConnectionProvider(provider);
    const validatedAdapter = validateIntegrationAdapter(adapter);

    expect(validatedProvider).toMatchObject({
      type: "connection-provider",
      name: "github",
      provider,
    });
    expect(validatedAdapter).toMatchObject({
      type: "integration-adapter",
      name: "github",
      version: "1.2.3",
      key: "github@1.2.3",
      adapter,
      providerName: "github",
    });
  });

  it("fails with clear errors when provider methods are missing", () => {
    expect(() =>
      validateConnectionProvider({
        name: "github",
        healthCheck: async () => true,
      })
    ).toThrowError(
      expect.objectContaining<Partial<PluginValidationError>>({
        name: "PluginValidationError",
        field: "proxy",
        code: "missing_function",
        pluginType: "connection-provider",
        message: "Connection provider must define proxy().",
      })
    );

    expect(() =>
      validateConnectionProvider({
        name: "github",
        proxy: async () => ({ status: 200 }),
      })
    ).toThrowError(
      expect.objectContaining<Partial<PluginValidationError>>({
        field: "healthCheck",
        code: "missing_function",
        message: "Connection provider must define healthCheck().",
      })
    );
  });

  it("fails with clear errors when adapter methods are missing", () => {
    expect(() =>
      validateIntegrationAdapter({
        name: "github",
        version: "1.2.3",
        computePath: () => "/github/issues/1.json",
        computeSemantics: () => ({ properties: {} }),
      })
    ).toThrowError(
      expect.objectContaining<Partial<PluginValidationError>>({
        field: "ingestWebhook",
        code: "missing_function",
        pluginType: "integration-adapter",
        message: "Integration adapter must define ingestWebhook().",
      })
    );

    expect(() =>
      validateIntegrationAdapter({
        name: "github",
        version: "1.2.3",
        ingestWebhook: async () => ({ ok: true }),
        computeSemantics: () => ({ properties: {} }),
      })
    ).toThrowError(
      expect.objectContaining<Partial<PluginValidationError>>({
        field: "computePath",
        code: "missing_function",
        message: "Integration adapter must define computePath().",
      })
    );

    expect(() =>
      validateIntegrationAdapter({
        name: "github",
        version: "1.2.3",
        ingestWebhook: async () => ({ ok: true }),
        computePath: () => "/github/issues/1.json",
      })
    ).toThrowError(
      expect.objectContaining<Partial<PluginValidationError>>({
        field: "computeSemantics",
        code: "missing_function",
        message: "Integration adapter must define computeSemantics().",
      })
    );
  });

  it("rejects empty names and invalid versions", () => {
    expect(() =>
      validateConnectionProvider({
        name: "   ",
        proxy: async () => ({ status: 200 }),
        healthCheck: async () => true,
      })
    ).toThrowError(
      expect.objectContaining<Partial<PluginValidationError>>({
        field: "name",
        code: "invalid_name",
        message: "Connection provider name must not be empty.",
      })
    );

    expect(() =>
      validateIntegrationAdapter({
        name: "github",
        version: "1.2",
        ingestWebhook: async () => ({ ok: true }),
        computePath: () => "/github/issues/1.json",
        computeSemantics: () => ({ properties: {} }),
      })
    ).toThrowError(
      expect.objectContaining<Partial<PluginValidationError>>({
        field: "version",
        code: "invalid_version",
        message: expect.stringContaining(
          "Integration adapter version must look like semver"
        ),
      })
    );

    expect(() =>
      assertStringName("", "name", "registration", "Registration factory name")
    ).toThrowError(
      expect.objectContaining<Partial<PluginValidationError>>({
        field: "name",
        code: "invalid_name",
        message: "Registration factory name must not be empty.",
      })
    );

    expect(() =>
      assertSemverishVersion(
        "release",
        "version",
        "registration",
        "Registration factory version"
      )
    ).toThrowError(
      expect.objectContaining<Partial<PluginValidationError>>({
        field: "version",
        code: "invalid_version",
        message: expect.stringContaining(
          "Registration factory version must look like semver"
        ),
      })
    );
  });

  it("validates registration inputs and catches malformed factories", () => {
    const adapter = createAdapter();
    const factory = Object.assign(() => adapter, {
      adapterName: "github",
      adapterVersion: "1.2.3",
    });

    expect(validateRegistrationInput(adapter)).toMatchObject({
      type: "adapter",
      name: "github",
      version: "1.2.3",
      key: "github@1.2.3",
      providerName: "github",
    });

    expect(validateRegistrationInput(factory)).toMatchObject({
      type: "factory",
      name: "github",
      version: "1.2.3",
      key: "github@1.2.3",
      factory,
    });

    const malformedFactory = Object.assign(() => adapter, {
      adapterName: "   ",
      adapterVersion: "1.2",
    });

    expect(() => validateRegistrationInput(malformedFactory)).toThrowError(
      expect.objectContaining<Partial<PluginValidationError>>({
        field: "adapterName",
        code: "invalid_name",
        pluginType: "registration",
        message: "Registration factory name must not be empty.",
      })
    );
  });

  it("catches duplicate plugin registration", () => {
    const registry = new AdapterRegistry<IntegrationAdapterLike>();
    const adapter = createAdapter();

    expect(registry.registerAdapter(adapter)).toBe(adapter);
    expect(() => registry.registerAdapter(createAdapter())).toThrowError(
      expect.objectContaining<Partial<DuplicateAdapterNameError>>({
        name: "DuplicateAdapterNameError",
        code: "duplicate_adapter_name",
        adapterName: "github",
        message: "Adapter already registered: github",
      })
    );
  });

  it("keeps public validation exports usable from src/index.ts", () => {
    const provider = createProvider({ name: "slack" });
    const adapter = createAdapter({
      name: "slack",
      version: "2.0.0",
      provider,
    });
    const factory = Object.assign(() => adapter, {
      adapterName: "slack",
      adapterVersion: "2.0.0",
    });

    expect(sdk.validateConnectionProvider(provider)).toMatchObject({
      type: "connection-provider",
      name: "slack",
    });
    expect(sdk.validateIntegrationAdapter(adapter)).toMatchObject({
      type: "integration-adapter",
      key: "slack@2.0.0",
      providerName: "slack",
    });
    expect(sdk.validateRegistrationInput(factory)).toMatchObject({
      type: "factory",
      key: "slack@2.0.0",
    });
    expect(sdk.assertStringName("slack", "name", "registration", "Name")).toBe(
      "slack"
    );
    expect(
      sdk.assertSemverishVersion(
        "2.0.0",
        "version",
        "registration",
        "Version"
      )
    ).toBe("2.0.0");

    const registry = new sdk.AdapterRegistry<IntegrationAdapterLike>();
    registry.registerAdapter(adapter);
    expect(registry.getAdapter("slack")).toBe(adapter);
    expect(sdk.DuplicateAdapterNameError).toBe(DuplicateAdapterNameError);
    expect(sdk.PluginValidationError).toBe(PluginValidationError);
  });
});
