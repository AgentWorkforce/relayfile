import { defineProviderConfigSchema } from "@relayfile/sdk";

export const providerConfigSchema = defineProviderConfigSchema({
  kind: "provider",
  type: "object",
  name: "{{provider}}",
  services: ["{{provider}}"],
  description: "Configuration for the {{provider}} Relayfile provider.",
  version: {
    pluginVersion: "0.1.0",
    schemaVersion: "2026-03-28"
  },
  properties: {
    apiKey: {
      type: "string",
      description: "Credential used for outbound proxy requests."
    },
    baseUrl: {
      type: "string",
      format: "uri",
      default: "https://api.{{provider}}.com",
      description: "Optional API origin override."
    },
    webhookSecret: {
      type: "string",
      description: "Shared secret for verifying incoming webhook signatures."
    }
  },
  required: ["apiKey"],
  additionalProperties: false
} as const);
