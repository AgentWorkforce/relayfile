import { defineAdapterConfigSchema } from "@relayfile/sdk";

export const adapterConfigSchema = defineAdapterConfigSchema({
  kind: "adapter",
  type: "object",
  name: "{{provider}}",
  provider: "{{provider}}",
  description: "Configuration for the {{provider}} Relayfile adapter.",
  version: {
    pluginVersion: "0.1.0",
    schemaVersion: "2026-03-28"
  },
  properties: {
    apiKey: {
      type: "string",
      description: "Credential used to fetch provider data or validate webhook state."
    },
    baseUrl: {
      type: "string",
      format: "uri",
      default: "https://api.{{provider}}.com",
      description: "Optional override for non-production environments."
    },
    projects: {
      type: "array",
      items: {
        type: "string"
      },
      description: "Optional subset of provider projects or resources to sync."
    }
  },
  required: ["apiKey"],
  additionalProperties: false
} as const);
