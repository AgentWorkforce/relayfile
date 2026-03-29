/**
 * Typed JSON Schema helpers for plugin configuration contracts.
 *
 * The helpers in this module cover the subset of JSON Schema that plugin
 * packages need for machine-readable configuration: descriptions, defaults,
 * examples, required fields, and version metadata.
 */

export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonObject | JsonValue[];

export interface JsonObject {
  [key: string]: JsonValue;
}

export type JsonSchemaTypeName =
  | "string"
  | "number"
  | "integer"
  | "boolean"
  | "null"
  | "object"
  | "array";

export interface JsonSchemaVersionMetadata {
  introducedIn?: string;
  deprecatedIn?: string;
}

export interface JsonSchemaMetadata<TValue = JsonValue> extends JsonSchemaVersionMetadata {
  title?: string;
  description?: string;
  default?: TValue;
  examples?: readonly TValue[];
  const?: TValue;
  enum?: readonly TValue[];
}

export interface JsonSchemaString extends JsonSchemaMetadata<string> {
  type: "string";
  format?: string;
  pattern?: string;
  minLength?: number;
  maxLength?: number;
}

export interface JsonSchemaNumber extends JsonSchemaMetadata<number> {
  type: "number";
  minimum?: number;
  maximum?: number;
  exclusiveMinimum?: number;
  exclusiveMaximum?: number;
  multipleOf?: number;
}

export interface JsonSchemaInteger extends JsonSchemaMetadata<number> {
  type: "integer";
  minimum?: number;
  maximum?: number;
  exclusiveMinimum?: number;
  exclusiveMaximum?: number;
  multipleOf?: number;
}

export interface JsonSchemaBoolean extends JsonSchemaMetadata<boolean> {
  type: "boolean";
}

export interface JsonSchemaNull extends JsonSchemaMetadata<null> {
  type: "null";
}

export type JsonSchemaProperties = Record<string, JsonSchema>;

type RequiredPropertyKeys<
  TProperties extends JsonSchemaProperties,
  TRequired extends readonly (keyof TProperties & string)[] | undefined
> = TRequired extends readonly (keyof TProperties & string)[]
  ? TRequired[number]
  : never;

type OptionalPropertyKeys<
  TProperties extends JsonSchemaProperties,
  TRequired extends readonly (keyof TProperties & string)[] | undefined
> = Exclude<keyof TProperties & string, RequiredPropertyKeys<TProperties, TRequired>>;

type Simplify<TValue> = { [TKey in keyof TValue]: TValue[TKey] } & {};

type InferAdditionalProperties<TAdditional extends JsonSchema | boolean, Depth extends unknown[] = []> =
  TAdditional extends JsonSchema ? Record<string, InferJsonSchema<TAdditional, Depth>>
  : TAdditional extends true ? Record<string, JsonValue>
  : {};

export interface JsonSchemaArray<TItems extends JsonSchema = JsonSchema>
  extends JsonSchemaMetadata<JsonValue[]> {
  type: "array";
  items: TItems;
  minItems?: number;
  maxItems?: number;
}

export interface JsonSchemaObject<
  TProperties extends JsonSchemaProperties = JsonSchemaProperties,
  TRequired extends readonly (keyof TProperties & string)[] | undefined = undefined,
  TAdditional extends JsonSchema | boolean = boolean
> extends JsonSchemaMetadata<JsonObject> {
  type: "object";
  properties?: TProperties;
  required?: TRequired;
  additionalProperties?: TAdditional;
  minProperties?: number;
  maxProperties?: number;
}

export type JsonSchema =
  | JsonSchemaString
  | JsonSchemaNumber
  | JsonSchemaInteger
  | JsonSchemaBoolean
  | JsonSchemaNull
  | JsonSchemaArray<any>
  | JsonSchemaObject<any, any, any>;

export type InferJsonSchema<TSchema extends JsonSchema, Depth extends unknown[] = []> =
  Depth['length'] extends 8 ? JsonValue
  : TSchema extends JsonSchemaString ? string
  : TSchema extends JsonSchemaNumber ? number
  : TSchema extends JsonSchemaInteger ? number
  : TSchema extends JsonSchemaBoolean ? boolean
  : TSchema extends JsonSchemaNull ? null
  : TSchema extends JsonSchemaArray<infer TItems> ? InferJsonSchema<TItems, [...Depth, unknown]>[]
  : TSchema extends JsonSchemaObject<infer TProperties, infer TRequired, infer TAdditional>
    ? Simplify<
        {
          [TKey in RequiredPropertyKeys<TProperties, TRequired>]: InferJsonSchema<TProperties[TKey], [...Depth, unknown]>;
        } & {
          [TKey in OptionalPropertyKeys<TProperties, TRequired>]?: InferJsonSchema<TProperties[TKey], [...Depth, unknown]>;
        } & InferAdditionalProperties<TAdditional, [...Depth, unknown]>
      >
  : never;

export type PluginKind = "adapter" | "provider";

export interface PluginConfigVersionMetadata {
  pluginVersion: string;
  schemaVersion?: string;
}

export interface PluginConfigSchemaBase<
  TKind extends PluginKind,
  TProperties extends JsonSchemaProperties = JsonSchemaProperties,
  TRequired extends readonly (keyof TProperties & string)[] | undefined = undefined,
  TAdditional extends JsonSchema | boolean = false
> extends JsonSchemaObject<TProperties, TRequired, TAdditional> {
  kind: TKind;
  name: string;
  version: PluginConfigVersionMetadata;
}

export interface AdapterConfigSchema<
  TProperties extends JsonSchemaProperties = JsonSchemaProperties,
  TRequired extends readonly (keyof TProperties & string)[] | undefined = undefined,
  TAdditional extends JsonSchema | boolean = false
> extends PluginConfigSchemaBase<"adapter", TProperties, TRequired, TAdditional> {
  provider?: string | readonly string[];
}

export interface ProviderConfigSchema<
  TProperties extends JsonSchemaProperties = JsonSchemaProperties,
  TRequired extends readonly (keyof TProperties & string)[] | undefined = undefined,
  TAdditional extends JsonSchema | boolean = false
> extends PluginConfigSchemaBase<"provider", TProperties, TRequired, TAdditional> {
  services?: string | readonly string[];
}

export class ConfigSchemaDefinitionError extends TypeError {
  constructor(message: string) {
    super(message);
    this.name = "ConfigSchemaDefinitionError";
  }
}

export function defineAdapterConfigSchema<
  const TProperties extends JsonSchemaProperties,
  const TRequired extends readonly (keyof TProperties & string)[] | undefined = undefined,
  const TAdditional extends JsonSchema | boolean = false
>(
  schema: AdapterConfigSchema<TProperties, TRequired, TAdditional>
): AdapterConfigSchema<TProperties, TRequired, TAdditional> {
  validatePluginConfigSchema("adapter", schema);
  return schema;
}

export function defineProviderConfigSchema<
  const TProperties extends JsonSchemaProperties,
  const TRequired extends readonly (keyof TProperties & string)[] | undefined = undefined,
  const TAdditional extends JsonSchema | boolean = false
>(
  schema: ProviderConfigSchema<TProperties, TRequired, TAdditional>
): ProviderConfigSchema<TProperties, TRequired, TAdditional> {
  validatePluginConfigSchema("provider", schema);
  return schema;
}

export function validateAdapterConfigSchema<
  TProperties extends JsonSchemaProperties,
  TRequired extends readonly (keyof TProperties & string)[] | undefined = undefined,
  TAdditional extends JsonSchema | boolean = false
>(
  schema: AdapterConfigSchema<TProperties, TRequired, TAdditional>
): AdapterConfigSchema<TProperties, TRequired, TAdditional> {
  validatePluginConfigSchema("adapter", schema);
  return schema;
}

export function validateProviderConfigSchema<
  TProperties extends JsonSchemaProperties,
  TRequired extends readonly (keyof TProperties & string)[] | undefined = undefined,
  TAdditional extends JsonSchema | boolean = false
>(
  schema: ProviderConfigSchema<TProperties, TRequired, TAdditional>
): ProviderConfigSchema<TProperties, TRequired, TAdditional> {
  validatePluginConfigSchema("provider", schema);
  return schema;
}

export function isJsonObject(value: unknown): value is JsonObject {
  return isPlainObject(value);
}

export function matchesJsonSchemaValue<TSchema extends JsonSchema>(
  schema: TSchema,
  value: JsonValue
): value is InferJsonSchema<TSchema> {
  return matchesJsonSchema(schema, value);
}

export function validateJsonSchemaValue<TSchema extends JsonSchema>(
  schema: TSchema,
  value: JsonValue,
  path = "value"
): InferJsonSchema<TSchema> {
  assertJsonSerializable(value, path);
  assertSchemaCompatibleValue(schema, value, path);
  return value as InferJsonSchema<TSchema>;
}

function validatePluginConfigSchema<
  TProperties extends JsonSchemaProperties,
  TRequired extends readonly (keyof TProperties & string)[] | undefined,
  TAdditional extends JsonSchema | boolean
>(
  expectedKind: PluginKind,
  schema: PluginConfigSchemaBase<PluginKind, TProperties, TRequired, TAdditional>
): void {
  if (schema.kind !== expectedKind) {
    throw new ConfigSchemaDefinitionError(
      `Expected a ${expectedKind} config schema, received ${schema.kind}`
    );
  }

  assertNonEmptyString(schema.name, "schema.name");
  validateVersionMetadata(schema.version, "schema.version");
  validateOptionalString(schema.description, "schema.description");

  if (schema.type !== "object") {
    throw new ConfigSchemaDefinitionError(
      `Plugin config schemas must use type "object"; received "${schema.type}"`
    );
  }

  if (expectedKind === "adapter") {
    validateOptionalStringArray((schema as AdapterConfigSchema).provider, "schema.provider");
  } else {
    validateOptionalStringArray((schema as ProviderConfigSchema).services, "schema.services");
  }

  validateJsonSchema(schema, "schema");
}

function validateVersionMetadata(metadata: PluginConfigVersionMetadata, path: string): void {
  if (!isPlainObject(metadata)) {
    throw new ConfigSchemaDefinitionError(`${path} must be an object`);
  }

  assertNonEmptyString(metadata.pluginVersion, `${path}.pluginVersion`);
  validateOptionalString(metadata.schemaVersion, `${path}.schemaVersion`);
}

function validateJsonSchema(schema: JsonSchema, path: string): void {
  validateOptionalString(schema.title, `${path}.title`);
  validateOptionalString(schema.description, `${path}.description`);
  validateOptionalString(schema.introducedIn, `${path}.introducedIn`);
  validateOptionalString(schema.deprecatedIn, `${path}.deprecatedIn`);

  if ("default" in schema && schema.default !== undefined) {
    assertJsonSerializable(schema.default, `${path}.default`);
    assertSchemaCompatibleValue(schema, schema.default, `${path}.default`);
  }

  if ("const" in schema && schema.const !== undefined) {
    assertJsonSerializable(schema.const, `${path}.const`);
    assertSchemaCompatibleValue(schema, schema.const, `${path}.const`);
  }

  if ("enum" in schema && schema.enum !== undefined) {
    if (!Array.isArray(schema.enum)) {
      throw new ConfigSchemaDefinitionError(`${path}.enum must be an array`);
    }
    for (let index = 0; index < schema.enum.length; index += 1) {
      assertJsonSerializable(schema.enum[index], `${path}.enum[${index}]`);
      assertSchemaCompatibleValue(schema, schema.enum[index], `${path}.enum[${index}]`);
    }
  }

  if ("examples" in schema && schema.examples !== undefined) {
    if (!Array.isArray(schema.examples)) {
      throw new ConfigSchemaDefinitionError(`${path}.examples must be an array`);
    }
    for (let index = 0; index < schema.examples.length; index += 1) {
      assertJsonSerializable(schema.examples[index], `${path}.examples[${index}]`);
      assertSchemaCompatibleValue(schema, schema.examples[index], `${path}.examples[${index}]`);
    }
  }

  switch (schema.type) {
    case "string":
      validateOptionalNumber(schema.minLength, `${path}.minLength`);
      validateOptionalNumber(schema.maxLength, `${path}.maxLength`);
      validateOptionalString(schema.pattern, `${path}.pattern`);
      validateOptionalString(schema.format, `${path}.format`);
      break;
    case "number":
    case "integer":
      validateOptionalNumber(schema.minimum, `${path}.minimum`);
      validateOptionalNumber(schema.maximum, `${path}.maximum`);
      validateOptionalNumber(schema.exclusiveMinimum, `${path}.exclusiveMinimum`);
      validateOptionalNumber(schema.exclusiveMaximum, `${path}.exclusiveMaximum`);
      validateOptionalNumber(schema.multipleOf, `${path}.multipleOf`);
      break;
    case "boolean":
    case "null":
      break;
    case "array":
      validateOptionalNumber(schema.minItems, `${path}.minItems`);
      validateOptionalNumber(schema.maxItems, `${path}.maxItems`);
      validateJsonSchema(schema.items, `${path}.items`);
      break;
    case "object": {
      validateOptionalNumber(schema.minProperties, `${path}.minProperties`);
      validateOptionalNumber(schema.maxProperties, `${path}.maxProperties`);

      if (schema.properties !== undefined && !isPlainObject(schema.properties)) {
        throw new ConfigSchemaDefinitionError(`${path}.properties must be an object`);
      }
      const properties: JsonSchemaProperties = schema.properties ?? {};

      for (const [propertyName, propertySchema] of Object.entries(properties)) {
        validateJsonSchema(propertySchema, `${path}.properties.${propertyName}`);
      }

      const requiredKeys = schema.required;
      if (requiredKeys !== undefined) {
        if (!Array.isArray(requiredKeys)) {
          throw new ConfigSchemaDefinitionError(`${path}.required must be an array`);
        }

        const normalizedRequiredKeys = requiredKeys as readonly string[];
        for (const requiredKey of normalizedRequiredKeys) {
          if (typeof requiredKey !== "string" || requiredKey.length === 0) {
            throw new ConfigSchemaDefinitionError(
              `${path}.required entries must be non-empty strings`
            );
          }

          if (!(requiredKey in properties)) {
            throw new ConfigSchemaDefinitionError(
              `${path}.required contains "${requiredKey}" but no matching property exists`
            );
          }
        }
      }

      if (
        schema.additionalProperties !== undefined &&
        typeof schema.additionalProperties !== "boolean"
      ) {
        validateJsonSchema(schema.additionalProperties, `${path}.additionalProperties`);
      }
      break;
    }
    default:
      assertNever(schema);
  }
}

function assertSchemaCompatibleValue(
  schema: JsonSchema,
  value: JsonValue,
  path: string
): void {
  if (!matchesJsonSchema(schema, value)) {
    throw new ConfigSchemaDefinitionError(
      `${path} is not compatible with schema type "${schema.type}"`
    );
  }
}

function matchesJsonSchema(schema: JsonSchema, value: JsonValue): boolean {
  if ("const" in schema && schema.const !== undefined && !deepEqualJsonValue(value, schema.const)) {
    return false;
  }

  if (
    "enum" in schema &&
    schema.enum !== undefined &&
    !schema.enum.some((candidate) => deepEqualJsonValue(value, candidate))
  ) {
    return false;
  }

  switch (schema.type) {
    case "string":
      return typeof value === "string";
    case "number":
      return typeof value === "number" && Number.isFinite(value);
    case "integer":
      return typeof value === "number" && Number.isInteger(value);
    case "boolean":
      return typeof value === "boolean";
    case "null":
      return value === null;
    case "array":
      return Array.isArray(value) && value.every((item) => matchesJsonSchema(schema.items, item));
    case "object": {
      if (!isPlainObject(value)) {
        return false;
      }

      const properties = schema.properties ?? {};
      const required = schema.required ?? [];
      for (const requiredKey of required) {
        if (!(requiredKey in value)) {
          return false;
        }
      }

      for (const [propertyName, propertyValue] of Object.entries(value)) {
        const propertySchema = properties[propertyName];
        if (propertySchema) {
          if (!matchesJsonSchema(propertySchema, propertyValue)) {
            return false;
          }
          continue;
        }

        if (schema.additionalProperties === false) {
          return false;
        }

        if (
          schema.additionalProperties &&
          typeof schema.additionalProperties !== "boolean" &&
          !matchesJsonSchema(schema.additionalProperties, propertyValue)
        ) {
          return false;
        }
      }

      return true;
    }
    default:
      return assertNever(schema);
  }
}

function deepEqualJsonValue(left: JsonValue, right: JsonValue): boolean {
  if (left === right) {
    return true;
  }

  if (Array.isArray(left) || Array.isArray(right)) {
    if (!Array.isArray(left) || !Array.isArray(right) || left.length !== right.length) {
      return false;
    }
    for (let index = 0; index < left.length; index += 1) {
      if (!deepEqualJsonValue(left[index], right[index])) {
        return false;
      }
    }
    return true;
  }

  if (isPlainObject(left) || isPlainObject(right)) {
    if (!isPlainObject(left) || !isPlainObject(right)) {
      return false;
    }

    const leftKeys = Object.keys(left);
    const rightKeys = Object.keys(right);
    if (leftKeys.length !== rightKeys.length) {
      return false;
    }

    for (const key of leftKeys) {
      if (!(key in right) || !deepEqualJsonValue(left[key], right[key])) {
        return false;
      }
    }
    return true;
  }

  return false;
}

function assertJsonSerializable(value: JsonValue, path: string): void {
  try {
    const serialized = JSON.stringify(value);
    if (serialized === undefined) {
      throw new ConfigSchemaDefinitionError(`${path} is not JSON serializable`);
    }
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    throw new ConfigSchemaDefinitionError(`${path} is not JSON serializable: ${message}`);
  }
}

function validateOptionalString(value: unknown, path: string): void {
  if (value !== undefined && typeof value !== "string") {
    throw new ConfigSchemaDefinitionError(`${path} must be a string`);
  }
}

function validateOptionalNumber(value: unknown, path: string): void {
  if (value !== undefined && (typeof value !== "number" || Number.isNaN(value))) {
    throw new ConfigSchemaDefinitionError(`${path} must be a number`);
  }
}

function validateOptionalStringArray(value: unknown, path: string): void {
  if (value === undefined) {
    return;
  }

  if (typeof value === "string") {
    return;
  }

  if (!Array.isArray(value) || value.some((item) => typeof item !== "string" || item.length === 0)) {
    throw new ConfigSchemaDefinitionError(`${path} must be a string or an array of non-empty strings`);
  }
}

function assertNonEmptyString(value: unknown, path: string): asserts value is string {
  if (typeof value !== "string" || value.trim().length === 0) {
    throw new ConfigSchemaDefinitionError(`${path} must be a non-empty string`);
  }
}

function isPlainObject(value: unknown): value is Record<string, JsonValue> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function assertNever(value: never): never {
  throw new ConfigSchemaDefinitionError(`Unsupported schema node: ${JSON.stringify(value)}`);
}
