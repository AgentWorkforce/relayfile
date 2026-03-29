import type { IngestResult, IntegrationAdapter } from "./adapter.js";
import type { RelayFileClient } from "./client.js";
import { DuplicateAdapterNameError, MissingAdapterError } from "./errors.js";
import {
  createPluginEventEmitter,
  type PluginEventListener,
  type PluginEventMap
} from "./plugin-events.js";
import type {
  AdapterLoaderDiagnostic,
  DiscoverInstalledAdaptersResult,
  LoadAdapterModuleFailure,
  LoadAdapterModuleResult,
  LoadAdapterModuleSuccess
} from "./plugin-loader.js";
import type { NormalizedWebhook } from "./provider.js";

type AdapterProviderMetadata = {
  provider?: {
    name?: string;
  };
};

type CreateDiscoveredAdapter<TAdapter extends IntegrationAdapter> = (
  result: LoadAdapterModuleSuccess,
  context: {
    client?: RelayFileClient;
    registry?: AdapterRegistry<TAdapter>;
  }
) => TAdapter | Promise<TAdapter>;

type DiscoverInstalledAdaptersFn = (
  cwd?: string
) => Promise<DiscoverInstalledAdaptersResult>;

type LoadAdapterModuleFn = (
  packageName: string
) => Promise<LoadAdapterModuleResult>;

function isLoadAdapterModuleSuccess(
  result: LoadAdapterModuleResult
): result is LoadAdapterModuleSuccess {
  return result.ok;
}

export type AdapterRegistryEventMap = PluginEventMap<NormalizedWebhook, IngestResult>;

export interface RegisterDiscoveredAdaptersOptions<
  TAdapter extends IntegrationAdapter = IntegrationAdapter
> {
  cwd?: string;
  registry?: AdapterRegistry<TAdapter>;
  createAdapter: CreateDiscoveredAdapter<TAdapter>;
  discoverInstalledAdapters?: DiscoverInstalledAdaptersFn;
  loadAdapterModule?: LoadAdapterModuleFn;
}

export interface RegisterDiscoveredAdaptersResult<
  TAdapter extends IntegrationAdapter = IntegrationAdapter
> {
  registry: AdapterRegistry<TAdapter>;
  discovery: DiscoverInstalledAdaptersResult;
  loaded: LoadAdapterModuleSuccess[];
  failures: LoadAdapterModuleFailure[];
  registered: TAdapter[];
  diagnostics: AdapterLoaderDiagnostic[];
}

export interface BootstrapRelayFileClientAdaptersOptions<
  TAdapter extends IntegrationAdapter = IntegrationAdapter
> {
  client: RelayFileClient;
  cwd?: string;
  createAdapter: CreateDiscoveredAdapter<TAdapter>;
  discoverInstalledAdapters?: DiscoverInstalledAdaptersFn;
  loadAdapterModule?: LoadAdapterModuleFn;
}

export interface BootstrapRelayFileClientAdaptersResult<
  TAdapter extends IntegrationAdapter = IntegrationAdapter
> {
  client: RelayFileClient;
  discovery: DiscoverInstalledAdaptersResult;
  loaded: LoadAdapterModuleSuccess[];
  failures: LoadAdapterModuleFailure[];
  registered: TAdapter[];
  diagnostics: AdapterLoaderDiagnostic[];
}

async function resolveAdapterLoader(
  discoverInstalledAdapters?: DiscoverInstalledAdaptersFn,
  loadAdapterModule?: LoadAdapterModuleFn
): Promise<{
  discoverInstalledAdapters: DiscoverInstalledAdaptersFn;
  loadAdapterModule: LoadAdapterModuleFn;
}> {
  if (discoverInstalledAdapters && loadAdapterModule) {
    return {
      discoverInstalledAdapters,
      loadAdapterModule
    };
  }

  const loader = await import("./plugin-loader.js");

  return {
    discoverInstalledAdapters:
      discoverInstalledAdapters ?? loader.discoverInstalledAdapters,
    loadAdapterModule: loadAdapterModule ?? loader.loadAdapterModule
  };
}

async function registerDiscoveredAdaptersWithRegistrar<
  TAdapter extends IntegrationAdapter
>(
  options: {
    cwd?: string;
    createAdapter: CreateDiscoveredAdapter<TAdapter>;
    discoverInstalledAdapters?: DiscoverInstalledAdaptersFn;
    loadAdapterModule?: LoadAdapterModuleFn;
  },
  context: {
    client?: RelayFileClient;
    registry?: AdapterRegistry<TAdapter>;
  },
  registerAdapter: (adapter: TAdapter) => TAdapter
): Promise<{
  discovery: DiscoverInstalledAdaptersResult;
  loaded: LoadAdapterModuleSuccess[];
  failures: LoadAdapterModuleFailure[];
  registered: TAdapter[];
  diagnostics: AdapterLoaderDiagnostic[];
}> {
  const loader = await resolveAdapterLoader(
    options.discoverInstalledAdapters,
    options.loadAdapterModule
  );
  const discovery = await loader.discoverInstalledAdapters(options.cwd);
  const diagnostics = [...discovery.diagnostics];
  const loaded: LoadAdapterModuleSuccess[] = [];
  const failures: LoadAdapterModuleFailure[] = [];
  const registered: TAdapter[] = [];

  for (const descriptor of discovery.adapters) {
    const result = await loader.loadAdapterModule(descriptor.packageName);
    diagnostics.push(...result.diagnostics);

    if (isLoadAdapterModuleSuccess(result)) {
      loaded.push(result);
      const adapter = await options.createAdapter(result, context);
      registered.push(registerAdapter(adapter));
      continue;
    }

    failures.push(result);
  }

  return {
    discovery,
    loaded,
    failures,
    registered,
    diagnostics
  };
}

export class AdapterRegistry<TAdapter extends IntegrationAdapter = IntegrationAdapter> {
  private readonly adapters = new Map<string, TAdapter>();
  private readonly events = createPluginEventEmitter<AdapterRegistryEventMap>();

  on<TEventName extends keyof AdapterRegistryEventMap>(
    eventName: TEventName,
    listener: PluginEventListener<AdapterRegistryEventMap, TEventName>
  ): () => void {
    return this.events.on(eventName, listener);
  }

  off<TEventName extends keyof AdapterRegistryEventMap>(
    eventName: TEventName,
    listener: PluginEventListener<AdapterRegistryEventMap, TEventName>
  ): void {
    this.events.off(eventName, listener);
  }

  clearListeners(eventName?: keyof AdapterRegistryEventMap): void {
    this.events.clear(eventName);
  }

  listenerCount<TEventName extends keyof AdapterRegistryEventMap>(eventName: TEventName): number {
    return this.events.listenerCount(eventName);
  }

  registerAdapter<TRegisteredAdapter extends TAdapter>(
    adapter: TRegisteredAdapter
  ): TRegisteredAdapter {
    if (this.adapters.has(adapter.name)) {
      throw new DuplicateAdapterNameError(adapter.name);
    }

    this.adapters.set(adapter.name, adapter);
    this.events.emit("registered", {
      adapterName: adapter.name,
      version: adapter.version,
      providerName: this.getProviderName(adapter),
      supportedEvents: adapter.supportedEvents?.()
    });
    return adapter;
  }

  registerAdapters<TRegisteredAdapter extends TAdapter>(
    adapters: Iterable<TRegisteredAdapter>
  ): TRegisteredAdapter[] {
    const registered: TRegisteredAdapter[] = [];

    for (const adapter of adapters) {
      registered.push(this.registerAdapter(adapter));
    }

    return registered;
  }

  unregisterAdapter(name: string): boolean {
    return this.adapters.delete(name);
  }

  getAdapter(name: string): TAdapter | undefined {
    return this.adapters.get(name);
  }

  async routeWebhook(
    workspaceId: string,
    event: NormalizedWebhook
  ): Promise<IngestResult> {
    const adapter = this.getAdapter(event.provider);

    if (!adapter) {
      const error = new MissingAdapterError(event.provider);
      this.events.emit("error", { workspaceId, event, error });
      throw error;
    }

    try {
      const result = await adapter.ingestWebhook(workspaceId, event);
      this.events.emit("ingested", {
        adapterName: adapter.name,
        workspaceId,
        event,
        result
      });
      return result;
    } catch (error) {
      const normalizedError =
        error instanceof Error ? error : new Error(String(error));
      this.events.emit("error", {
        adapterName: adapter.name,
        workspaceId,
        event,
        error: normalizedError
      });
      throw normalizedError;
    }
  }

  private getProviderName(adapter: TAdapter): string {
    const providerName = (adapter as TAdapter & AdapterProviderMetadata).provider?.name;
    return typeof providerName === "string" && providerName.length > 0
      ? providerName
      : adapter.name;
  }
}

export async function registerDiscoveredAdapters<
  TAdapter extends IntegrationAdapter = IntegrationAdapter
>(
  options: RegisterDiscoveredAdaptersOptions<TAdapter>
): Promise<RegisterDiscoveredAdaptersResult<TAdapter>> {
  const registry = options.registry ?? new AdapterRegistry<TAdapter>();
  const result = await registerDiscoveredAdaptersWithRegistrar(
    options,
    { registry },
    (adapter) => registry.registerAdapter(adapter)
  );

  return {
    registry,
    ...result
  };
}

export async function bootstrapRelayFileClientAdapters<
  TAdapter extends IntegrationAdapter = IntegrationAdapter
>(
  options: BootstrapRelayFileClientAdaptersOptions<TAdapter>
): Promise<BootstrapRelayFileClientAdaptersResult<TAdapter>> {
  const result = await registerDiscoveredAdaptersWithRegistrar(
    options,
    { client: options.client },
    (adapter) => options.client.registerAdapter(adapter)
  );

  return {
    client: options.client,
    ...result
  };
}
