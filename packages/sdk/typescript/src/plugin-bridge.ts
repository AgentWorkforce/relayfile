import {
  loadAdapterModule,
  type AdapterLoaderDiagnostic,
  type AdapterLoaderDiagnosticCode,
  type AdapterLoaderDiagnosticSeverity,
  type AdapterModuleExport,
  type LoadAdapterModuleFailure,
  type LoadAdapterModuleSuccess,
  type LoadedAdapterMetadata,
} from "./plugin-loader.js";
import {
  validateIntegrationAdapter,
  type IntegrationAdapterLike,
  type ValidatedIntegrationAdapter,
} from "./validation.js";

export interface AdapterRegistrar<TAdapter extends IntegrationAdapterLike = IntegrationAdapterLike> {
  registerAdapter(adapter: TAdapter): TAdapter;
}

export type PluginBridgeStage = "instantiate" | "validate" | "register";

export type PluginBridgeDiagnosticCode = Extract<
  AdapterLoaderDiagnosticCode,
  | "adapter_instantiation_failed"
  | "adapter_validation_failed"
  | "adapter_registration_failed"
>;

export interface InstantiateLoadedAdapterSuccess<
  TAdapter extends IntegrationAdapterLike = IntegrationAdapterLike,
  TExport extends AdapterModuleExport = AdapterModuleExport
> {
  ok: true;
  packageName: string;
  diagnostics: AdapterLoaderDiagnostic[];
  metadata: LoadedAdapterMetadata<TExport>;
  adapter: TAdapter;
  validated: Omit<ValidatedIntegrationAdapter, "adapter"> & { adapter: TAdapter };
}

export interface PluginBridgeFailure<
  TExport extends AdapterModuleExport = AdapterModuleExport
> {
  ok: false;
  packageName: string;
  stage: PluginBridgeStage;
  diagnostics: AdapterLoaderDiagnostic[];
  error: Error;
  metadata?: LoadedAdapterMetadata<TExport>;
}

export type InstantiateLoadedAdapterResult<
  TAdapter extends IntegrationAdapterLike = IntegrationAdapterLike,
  TExport extends AdapterModuleExport = AdapterModuleExport
> = InstantiateLoadedAdapterSuccess<TAdapter, TExport> | PluginBridgeFailure<TExport>;

export interface RegisterLoadedAdapterSuccess<
  TAdapter extends IntegrationAdapterLike = IntegrationAdapterLike,
  TExport extends AdapterModuleExport = AdapterModuleExport,
  TRegistry extends AdapterRegistrar<TAdapter> = AdapterRegistrar<TAdapter>
> extends InstantiateLoadedAdapterSuccess<TAdapter, TExport> {
  registry: TRegistry;
  registered: TAdapter;
}

export type RegisterLoadedAdapterResult<
  TAdapter extends IntegrationAdapterLike = IntegrationAdapterLike,
  TExport extends AdapterModuleExport = AdapterModuleExport,
  TRegistry extends AdapterRegistrar<TAdapter> = AdapterRegistrar<TAdapter>
> = RegisterLoadedAdapterSuccess<TAdapter, TExport, TRegistry> | PluginBridgeFailure<TExport>;

export type LoadAndRegisterAdapterResult<
  TAdapter extends IntegrationAdapterLike = IntegrationAdapterLike,
  TRegistry extends AdapterRegistrar<TAdapter> = AdapterRegistrar<TAdapter>
> = LoadAdapterModuleFailure | RegisterLoadedAdapterResult<TAdapter, AdapterModuleExport, TRegistry>;

type LoadedAdapterInput<TExport extends AdapterModuleExport = AdapterModuleExport> =
  | LoadedAdapterMetadata<TExport>
  | LoadAdapterModuleSuccess<Record<string, unknown>, TExport>;

function resolveMetadata<TExport extends AdapterModuleExport>(
  input: LoadedAdapterInput<TExport>
): { diagnostics: AdapterLoaderDiagnostic[]; metadata: LoadedAdapterMetadata<TExport> } {
  if ("metadata" in input) {
    return {
      diagnostics: [...input.diagnostics],
      metadata: input.metadata,
    };
  }

  return {
    diagnostics: [],
    metadata: input,
  };
}

function normalizeError(error: unknown): Error {
  return error instanceof Error ? error : new Error(String(error));
}

function createBridgeDiagnostic(
  severity: AdapterLoaderDiagnosticSeverity,
  code: PluginBridgeDiagnosticCode,
  message: string,
  metadata: LoadedAdapterMetadata,
  error?: unknown
): AdapterLoaderDiagnostic {
  const normalizedError = error === undefined ? undefined : normalizeError(error);

  return {
    severity,
    code,
    message,
    packageName: metadata.packageName,
    path: metadata.entrypoint,
    detail: normalizedError?.message,
  };
}

function instantiateAdapterExport<TAdapter extends IntegrationAdapterLike, TExport extends AdapterModuleExport>(
  metadata: LoadedAdapterMetadata<TExport>,
  args: unknown[]
): TAdapter {
  if (metadata.exportKind === "class") {
    const AdapterClass = metadata.exportValue as new (...constructorArgs: unknown[]) => TAdapter;
    return new AdapterClass(...args);
  }

  const adapterFactory = metadata.exportValue as (...factoryArgs: unknown[]) => TAdapter;
  return adapterFactory(...args);
}

export function instantiateLoadedAdapter<
  TAdapter extends IntegrationAdapterLike = IntegrationAdapterLike,
  TExport extends AdapterModuleExport = AdapterModuleExport
>(
  input: LoadedAdapterInput<TExport>,
  ...args: unknown[]
): InstantiateLoadedAdapterResult<TAdapter, TExport> {
  const { diagnostics, metadata } = resolveMetadata(input);

  let adapter: TAdapter;
  try {
    adapter = instantiateAdapterExport<TAdapter, TExport>(metadata, args);
  } catch (error) {
    const normalizedError = normalizeError(error);
    diagnostics.push(
      createBridgeDiagnostic(
        "error",
        "adapter_instantiation_failed",
        `Failed to instantiate adapter export ${metadata.exportName} from ${metadata.packageName}.`,
        metadata,
        normalizedError
      )
    );
    return {
      ok: false,
      packageName: metadata.packageName,
      stage: "instantiate",
      diagnostics,
      error: normalizedError,
      metadata,
    };
  }

  try {
    const validated = validateIntegrationAdapter(adapter);
    return {
      ok: true,
      packageName: metadata.packageName,
      diagnostics,
      metadata,
      adapter: validated.adapter as TAdapter,
      validated: {
        ...validated,
        adapter: validated.adapter as TAdapter,
      },
    };
  } catch (error) {
    const normalizedError = normalizeError(error);
    diagnostics.push(
      createBridgeDiagnostic(
        "error",
        "adapter_validation_failed",
        `Loaded adapter export ${metadata.exportName} from ${metadata.packageName} did not satisfy the SDK adapter contract.`,
        metadata,
        normalizedError
      )
    );
    return {
      ok: false,
      packageName: metadata.packageName,
      stage: "validate",
      diagnostics,
      error: normalizedError,
      metadata,
    };
  }
}

export function registerLoadedAdapter<
  TAdapter extends IntegrationAdapterLike = IntegrationAdapterLike,
  TExport extends AdapterModuleExport = AdapterModuleExport,
  TRegistry extends AdapterRegistrar<TAdapter> = AdapterRegistrar<TAdapter>
>(
  registry: TRegistry,
  input: LoadedAdapterInput<TExport>,
  ...args: unknown[]
): RegisterLoadedAdapterResult<TAdapter, TExport, TRegistry> {
  const instantiated = instantiateLoadedAdapter<TAdapter, TExport>(input, ...args);
  if (!instantiated.ok) {
    return instantiated as PluginBridgeFailure<TExport>;
  }

  try {
    const registered = registry.registerAdapter(instantiated.adapter);
    return {
      ...instantiated,
      registry,
      registered,
    };
  } catch (error) {
    const normalizedError = normalizeError(error);
    const diagnostics = [...instantiated.diagnostics];
    diagnostics.push(
      createBridgeDiagnostic(
        "error",
        "adapter_registration_failed",
        `Failed to register adapter ${instantiated.validated.name} from ${instantiated.packageName}.`,
        instantiated.metadata,
        normalizedError
      )
    );
    return {
      ok: false,
      packageName: instantiated.packageName,
      stage: "register",
      diagnostics,
      error: normalizedError,
      metadata: instantiated.metadata,
    };
  }
}

export async function loadAndRegisterAdapter<
  TAdapter extends IntegrationAdapterLike = IntegrationAdapterLike,
  TRegistry extends AdapterRegistrar<TAdapter> = AdapterRegistrar<TAdapter>
>(
  registry: TRegistry,
  packageName: string,
  ...args: unknown[]
): Promise<LoadAndRegisterAdapterResult<TAdapter, TRegistry>> {
  const loaded = await loadAdapterModule(packageName);
  if (!loaded.ok) {
    return loaded as LoadAdapterModuleFailure;
  }

  return registerLoadedAdapter<TAdapter, AdapterModuleExport, TRegistry>(
    registry,
    loaded,
    ...args
  );
}
