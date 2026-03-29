export { providerConfigSchema } from "./config-schema.js";

export interface ProviderProxyRequest {
  method: string;
  path: string;
  headers?: Record<string, string>;
  body?: unknown;
  query?: Record<string, string>;
}

export interface RelayfileProvider {
  readonly name: string;
  healthCheck(connectionId: string): Promise<boolean>;
  proxy<TResponse = unknown>(request: ProviderProxyRequest): Promise<TResponse>;
}

export class ExampleProvider implements RelayfileProvider {
  readonly name = "{{projectName}}";

  async healthCheck(connectionId: string): Promise<boolean> {
    return connectionId.length > 0;
  }

  async proxy<TResponse = unknown>(_request: ProviderProxyRequest): Promise<TResponse> {
    return {
      ok: true,
      provider: this.name
    } as TResponse;
  }
}
