import { describe, expect, expectTypeOf, it } from "vitest"

import {
  ConfigSchemaDefinitionError,
  defineAdapterConfigSchema,
  defineProviderConfigSchema,
  isJsonObject,
  matchesJsonSchemaValue,
  validateAdapterConfigSchema,
  validateJsonSchemaValue,
  validateProviderConfigSchema,
  type InferJsonSchema
} from "./config-schema.js"
import * as sdk from "./index.js"

function expectDefinitionError(factory: () => unknown, message: string) {
  expect(factory).toThrowError(
    expect.objectContaining<Partial<ConfigSchemaDefinitionError>>({
      name: "ConfigSchemaDefinitionError",
      message
    })
  )
}

describe("config schema helpers", () => {
  it("declares adapter and provider schemas", () => {
    const adapterSchema = defineAdapterConfigSchema({
      kind: "adapter",
      type: "object",
      name: "github",
      provider: ["github", "github-enterprise"],
      version: {
        pluginVersion: "1.2.3",
        schemaVersion: "2026-03-28"
      },
      properties: {
        token: {
          type: "string",
          description: "Access token"
        },
        retries: {
          type: "integer",
          default: 3,
          minimum: 0
        },
        advanced: {
          type: "object",
          properties: {
            dryRun: {
              type: "boolean",
              default: false
            },
            labels: {
              type: "array",
              items: { type: "string" }
            }
          },
          required: ["dryRun"],
          additionalProperties: false
        }
      },
      required: ["token"],
      additionalProperties: false
    } as const)

    const providerSchema = defineProviderConfigSchema({
      kind: "provider",
      type: "object",
      name: "nango",
      services: ["github", "slack"],
      version: {
        pluginVersion: "2.0.0"
      },
      properties: {
        baseUrl: {
          type: "string",
          format: "uri"
        },
        timeoutMs: {
          type: "integer",
          default: 30_000,
          minimum: 0
        }
      },
      required: ["baseUrl"],
      additionalProperties: false
    } as const)

    expectTypeOf<InferJsonSchema<typeof adapterSchema>>().toEqualTypeOf<{
      token: string
      retries?: number
      advanced?: {
        dryRun: boolean
        labels?: string[]
      }
    }>()
    expectTypeOf<InferJsonSchema<typeof providerSchema>>().toEqualTypeOf<{
      baseUrl: string
      timeoutMs?: number
    }>()

    const adapterConfig = {
      token: "ghp_test",
      retries: 2,
      advanced: {
        dryRun: true,
        labels: ["repo", "issues"]
      }
    }
    const providerConfig = {
      baseUrl: "https://api.nango.dev",
      timeoutMs: 15_000
    }

    expect(matchesJsonSchemaValue(adapterSchema, adapterConfig)).toBe(true)
    expect(matchesJsonSchemaValue(providerSchema, providerConfig)).toBe(true)
    expect(validateJsonSchemaValue(adapterSchema, adapterConfig)).toEqual(adapterConfig)
    expect(validateJsonSchemaValue(providerSchema, providerConfig)).toEqual(providerConfig)
  })

  it("preserves required fields and defaults", () => {
    const adapterSchema = validateAdapterConfigSchema(
      defineAdapterConfigSchema({
        kind: "adapter",
        type: "object",
        name: "salesforce",
        provider: "salesforce",
        version: {
          pluginVersion: "3.4.5"
        },
        properties: {
          clientId: {
            type: "string"
          },
          clientSecret: {
            type: "string"
          },
          retries: {
            type: "integer",
            default: 5
          }
        },
        required: ["clientId", "clientSecret"],
        additionalProperties: false
      } as const)
    )
    const providerSchema = validateProviderConfigSchema(
      defineProviderConfigSchema({
        kind: "provider",
        type: "object",
        name: "hubspot",
        services: "crm",
        version: {
          pluginVersion: "1.0.0",
          schemaVersion: "2026-03-28"
        },
        properties: {
          baseUrl: {
            type: "string",
            default: "https://api.hubapi.com"
          },
          syncWindowMinutes: {
            type: "integer",
            default: 60
          }
        },
        required: ["baseUrl"],
        additionalProperties: false
      } as const)
    )

    expect(validateAdapterConfigSchema(adapterSchema)).toBe(adapterSchema)
    expect(validateProviderConfigSchema(providerSchema)).toBe(providerSchema)

    expect(adapterSchema.required).toEqual(["clientId", "clientSecret"])
    expect(adapterSchema.properties.retries.default).toBe(5)
    expect(providerSchema.required).toEqual(["baseUrl"])
    expect(providerSchema.properties.baseUrl.default).toBe("https://api.hubapi.com")
    expect(providerSchema.properties.syncWindowMinutes.default).toBe(60)

    expect(
      matchesJsonSchemaValue(adapterSchema, {
        clientId: "client",
        retries: 1
      })
    ).toBe(false)
    expect(
      matchesJsonSchemaValue(providerSchema, {
        syncWindowMinutes: 30
      })
    ).toBe(false)
  })

  it("fails clearly for invalid schema definitions", () => {
    expectDefinitionError(
      () =>
        defineAdapterConfigSchema({
          kind: "adapter",
          type: "object",
          name: "github",
          version: {
            pluginVersion: "1.2.3"
          },
          properties: {
            token: { type: "string" }
          },
          required: ["missing"],
          additionalProperties: false
        } as const),
      'schema.required contains "missing" but no matching property exists'
    )

    expectDefinitionError(
      () =>
        defineProviderConfigSchema({
          kind: "provider",
          type: "array",
          name: "nango",
          version: {
            pluginVersion: "1.2.3"
          },
          properties: {},
          items: { type: "string" }
        } as never),
      'Plugin config schemas must use type "object"; received "array"'
    )

    expectDefinitionError(
      () =>
        validateAdapterConfigSchema({
          kind: "adapter",
          type: "object",
          name: "github",
          provider: "github",
          version: {
            pluginVersion: "1.2.3"
          },
          properties: {
            retries: {
              type: "integer",
              default: "three"
            }
          },
          additionalProperties: false
        } as never),
      'schema.properties.retries.default is not compatible with schema type "integer"'
    )

    expectDefinitionError(
      () =>
        validateProviderConfigSchema({
          kind: "provider",
          type: "object",
          name: "hubspot",
          services: ["crm", ""],
          version: {
            pluginVersion: "1.2.3"
          },
          properties: {},
          additionalProperties: false
        } as never),
      "schema.services must be a string or an array of non-empty strings"
    )
  })

  it("exports config schema helpers from src/index.ts", () => {
    expect(sdk.ConfigSchemaDefinitionError).toBe(ConfigSchemaDefinitionError)
    expect(sdk.defineAdapterConfigSchema).toBe(defineAdapterConfigSchema)
    expect(sdk.defineProviderConfigSchema).toBe(defineProviderConfigSchema)
    expect(sdk.isJsonObject).toBe(isJsonObject)
    expect(sdk.matchesJsonSchemaValue).toBe(matchesJsonSchemaValue)
    expect(sdk.validateAdapterConfigSchema).toBe(validateAdapterConfigSchema)
    expect(sdk.validateJsonSchemaValue).toBe(validateJsonSchemaValue)
    expect(sdk.validateProviderConfigSchema).toBe(validateProviderConfigSchema)
  })

  it("keeps schema metadata JSON serializable", () => {
    const schema = defineAdapterConfigSchema({
      kind: "adapter",
      type: "object",
      name: "github",
      title: "GitHub Adapter Config",
      description: "Configuration contract for the GitHub adapter",
      introducedIn: "1.0.0",
      deprecatedIn: "2.0.0",
      version: {
        pluginVersion: "1.2.3",
        schemaVersion: "2026-03-28"
      },
      properties: {
        region: {
          type: "string",
          title: "Region",
          description: "Regional API host",
          default: "us",
          enum: ["us", "eu"],
          examples: ["us"],
          introducedIn: "1.0.0"
        },
        rollout: {
          type: "object",
          description: "Feature rollout settings",
          properties: {
            enabled: {
              type: "boolean",
              default: true
            },
            channels: {
              type: "array",
              default: ["stable"],
              items: {
                type: "string",
                examples: ["stable"]
              }
            }
          },
          required: ["enabled"],
          additionalProperties: false
        }
      },
      required: ["region"],
      additionalProperties: false
    } as const)

    const serialized = JSON.stringify(schema)
    const parsed = JSON.parse(serialized) as unknown

    expect(isJsonObject(parsed)).toBe(true)
    expect(parsed).toMatchObject({
      kind: "adapter",
      name: "github",
      title: "GitHub Adapter Config",
      description: "Configuration contract for the GitHub adapter",
      introducedIn: "1.0.0",
      deprecatedIn: "2.0.0",
      version: {
        pluginVersion: "1.2.3",
        schemaVersion: "2026-03-28"
      },
      properties: {
        region: {
          type: "string",
          default: "us",
          enum: ["us", "eu"],
          examples: ["us"]
        },
        rollout: {
          type: "object",
          properties: {
            enabled: {
              type: "boolean",
              default: true
            },
            channels: {
              type: "array",
              default: ["stable"],
              items: {
                type: "string",
                examples: ["stable"]
              }
            }
          }
        }
      }
    })
    expect(JSON.stringify(parsed)).toBe(serialized)
  })
})
