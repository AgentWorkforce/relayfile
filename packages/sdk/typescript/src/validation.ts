import { PluginValidationError } from "./errors.js";

const NAME_PATTERN = /^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$/i;
const SEMVERISH_PATTERN =
  /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/;

type UnknownRecord = Record<PropertyKey, unknown>;

export interface ConnectionProviderLike {
  readonly name: string;
  proxy(request: unknown): Promise<unknown> | unknown;
  healthCheck(connectionId: string): Promise<boolean> | boolean;
  handleWebhook?(rawPayload: unknown): Promise<unknown> | unknown;
}

export interface IntegrationAdapterLike {
  readonly name: string;
  readonly version: string;
  readonly provider?: ConnectionProviderLike;
  ingestWebhook(workspaceId: string, event: unknown): Promise<unknown> | unknown;
  computePath(...args: unknown[]): string;
  computeSemantics(...args: unknown[]): unknown;
  sync?(workspaceId: string, options?: unknown): Promise<unknown> | unknown;
  writeBack?(workspaceId: string, path: string, content: string): Promise<void> | void;
  supportedEvents?(): string[];
}

export interface RegistrationFactory {
  (): IntegrationAdapterLike;
  readonly adapterName?: string;
  readonly adapterVersion?: string;
  readonly name?: string;
  readonly version?: string;
}

export interface ValidatedConnectionProvider {
  readonly type: "connection-provider";
  readonly name: string;
  readonly provider: ConnectionProviderLike;
}

export interface ValidatedIntegrationAdapter {
  readonly type: "integration-adapter";
  readonly name: string;
  readonly version: string;
  readonly key: string;
  readonly adapter: IntegrationAdapterLike;
  readonly providerName?: string;
}

export interface ValidatedRegistrationAdapter {
  readonly type: "adapter";
  readonly name: string;
  readonly version: string;
  readonly key: string;
  readonly adapter: IntegrationAdapterLike;
  readonly providerName?: string;
}

export interface ValidatedRegistrationFactory {
  readonly type: "factory";
  readonly name: string;
  readonly version: string;
  readonly key: string;
  readonly factory: RegistrationFactory;
}

export type ValidatedRegistrationInput =
  | ValidatedRegistrationAdapter
  | ValidatedRegistrationFactory;

function describeValue(value: unknown): string {
  if (value === null) {
    return "null";
  }
  if (value === undefined) {
    return "undefined";
  }
  if (Array.isArray(value)) {
    return "array";
  }
  return typeof value;
}

function isRecord(value: unknown): value is UnknownRecord {
  return typeof value === "object" && value !== null;
}

function assertRecord(
  value: unknown,
  pluginType: PluginValidationError["pluginType"],
  field: string,
  label: string
): asserts value is UnknownRecord {
  if (!isRecord(value) && typeof value !== "function") {
    throw new PluginValidationError(
      `${label} must be an object or function, received ${describeValue(value)}.`,
      { field, pluginType }
    );
  }
}

function readField<TValue>(
  value: UnknownRecord,
  key: string
): TValue | undefined {
  return value[key] as TValue | undefined;
}

export function assertStringName(
  value: unknown,
  field: string,
  pluginType: PluginValidationError["pluginType"],
  label: string
): string {
  if (typeof value !== "string") {
    throw new PluginValidationError(`${label} must be a string.`, {
      field,
      pluginType,
      code: "invalid_name",
    });
  }

  const normalized = value.trim();
  if (!normalized) {
    throw new PluginValidationError(`${label} must not be empty.`, {
      field,
      pluginType,
      code: "invalid_name",
    });
  }

  if (!NAME_PATTERN.test(normalized)) {
    throw new PluginValidationError(
      `${label} must match ${NAME_PATTERN.toString()}. Received "${value}".`,
      {
        field,
        pluginType,
        code: "invalid_name",
      }
    );
  }

  return normalized;
}

export function assertSemverishVersion(
  value: unknown,
  field: string,
  pluginType: PluginValidationError["pluginType"],
  label: string
): string {
  if (typeof value !== "string") {
    throw new PluginValidationError(`${label} must be a string.`, {
      field,
      pluginType,
      code: "invalid_version",
    });
  }

  const normalized = value.trim();
  if (!normalized) {
    throw new PluginValidationError(`${label} must not be empty.`, {
      field,
      pluginType,
      code: "invalid_version",
    });
  }

  if (!SEMVERISH_PATTERN.test(normalized)) {
    throw new PluginValidationError(
      `${label} must look like semver (for example "1.2.3" or "1.2.3-beta.1"). Received "${value}".`,
      {
        field,
        pluginType,
        code: "invalid_version",
      }
    );
  }

  return normalized;
}

export function assertRequiredFunction<
  TRecord extends UnknownRecord,
  TKey extends keyof TRecord & string
>(
  value: TRecord,
  key: TKey,
  pluginType: PluginValidationError["pluginType"],
  label: string
): Extract<TRecord[TKey], (...args: unknown[]) => unknown> {
  const candidate = value[key];
  if (typeof candidate !== "function") {
    throw new PluginValidationError(`${label} must define ${key}().`, {
      field: key,
      pluginType,
      code: "missing_function",
    });
  }

  return candidate as Extract<TRecord[TKey], (...args: unknown[]) => unknown>;
}

function assertOptionalFunction(
  value: UnknownRecord,
  key: string,
  pluginType: PluginValidationError["pluginType"],
  label: string
): void {
  const candidate = value[key];
  if (candidate !== undefined && typeof candidate !== "function") {
    throw new PluginValidationError(
      `${label} field ${key} must be a function when provided.`,
      {
        field: key,
        pluginType,
        code: "invalid_function",
      }
    );
  }
}

function makeRegistrationKey(name: string, version: string): string {
  return `${name}@${version}`;
}

function pickFactoryName(factory: RegistrationFactory): string {
  return assertStringName(
    factory.adapterName ?? factory.name,
    factory.adapterName ? "adapterName" : "name",
    "registration",
    "Registration factory name"
  );
}

function pickFactoryVersion(factory: RegistrationFactory): string {
  return assertSemverishVersion(
    factory.adapterVersion ?? factory.version,
    factory.adapterVersion ? "adapterVersion" : "version",
    "registration",
    "Registration factory version"
  );
}

export function validateConnectionProvider(
  provider: unknown
): ValidatedConnectionProvider {
  assertRecord(provider, "connection-provider", "provider", "Connection provider");

  const providerRecord = provider as UnknownRecord;
  const name = assertStringName(
    readField(providerRecord, "name"),
    "name",
    "connection-provider",
    "Connection provider name"
  );

  assertRequiredFunction(
    providerRecord,
    "proxy",
    "connection-provider",
    "Connection provider"
  );
  assertRequiredFunction(
    providerRecord,
    "healthCheck",
    "connection-provider",
    "Connection provider"
  );
  assertOptionalFunction(
    providerRecord,
    "handleWebhook",
    "connection-provider",
    "Connection provider"
  );

  return {
    type: "connection-provider",
    name,
    provider: provider as unknown as ConnectionProviderLike,
  };
}

export function validateIntegrationAdapter(
  adapter: unknown
): ValidatedIntegrationAdapter {
  assertRecord(adapter, "integration-adapter", "adapter", "Integration adapter");

  const adapterRecord = adapter as UnknownRecord;
  const name = assertStringName(
    readField(adapterRecord, "name"),
    "name",
    "integration-adapter",
    "Integration adapter name"
  );
  const version = assertSemverishVersion(
    readField(adapterRecord, "version"),
    "version",
    "integration-adapter",
    "Integration adapter version"
  );

  assertRequiredFunction(
    adapterRecord,
    "ingestWebhook",
    "integration-adapter",
    "Integration adapter"
  );
  assertRequiredFunction(
    adapterRecord,
    "computePath",
    "integration-adapter",
    "Integration adapter"
  );
  assertRequiredFunction(
    adapterRecord,
    "computeSemantics",
    "integration-adapter",
    "Integration adapter"
  );
  assertOptionalFunction(
    adapterRecord,
    "sync",
    "integration-adapter",
    "Integration adapter"
  );
  assertOptionalFunction(
    adapterRecord,
    "writeBack",
    "integration-adapter",
    "Integration adapter"
  );
  assertOptionalFunction(
    adapterRecord,
    "supportedEvents",
    "integration-adapter",
    "Integration adapter"
  );

  const providerCandidate = readField<unknown>(adapterRecord, "provider");
  const providerName = providerCandidate
    ? validateConnectionProvider(providerCandidate).name
    : undefined;

  return {
    type: "integration-adapter",
    name,
    version,
    key: makeRegistrationKey(name, version),
    adapter: adapter as unknown as IntegrationAdapterLike,
    providerName,
  };
}

export function validateRegistrationInput(
  adapterOrFactory: unknown
): ValidatedRegistrationInput {
  if (typeof adapterOrFactory === "function") {
    const factory = adapterOrFactory as RegistrationFactory;
    const name = pickFactoryName(factory);
    const version = pickFactoryVersion(factory);

    return {
      type: "factory",
      name,
      version,
      key: makeRegistrationKey(name, version),
      factory,
    };
  }

  const validated = validateIntegrationAdapter(adapterOrFactory);
  return {
    type: "adapter",
    name: validated.name,
    version: validated.version,
    key: validated.key,
    adapter: validated.adapter,
    providerName: validated.providerName,
  };
}
