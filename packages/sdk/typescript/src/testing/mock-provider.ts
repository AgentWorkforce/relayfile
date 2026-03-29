import type {
  ConnectionProvider,
  NormalizedWebhook,
  ProxyRequest,
  ProxyResponse
} from "../provider.js";

type MaybePromise<TValue> = TValue | Promise<TValue>;

export interface MockProviderOptions {
  name?: string;
  proxyResponse?: ProxyResponse;
  proxy?: (
    request: ProxyRequest,
    context: MockConnectionProvider
  ) => MaybePromise<ProxyResponse>;
  healthCheckResult?: boolean;
  healthCheck?: (
    connectionId: string,
    context: MockConnectionProvider
  ) => MaybePromise<boolean>;
  webhookResult?: NormalizedWebhook;
  handleWebhook?: (
    rawPayload: unknown,
    context: MockConnectionProvider
  ) => MaybePromise<NormalizedWebhook>;
}

export interface MockConnectionProvider extends ConnectionProvider {
  readonly proxyCalls: ProxyRequest[];
  readonly healthCheckCalls: string[];
  readonly webhookCalls: unknown[];
  readonly results: {
    proxy: ProxyResponse[];
    healthCheck: boolean[];
    webhook: NormalizedWebhook[];
  };
  getLastProxyCall(): ProxyRequest | undefined;
  getLastHealthCheckCall(): string | undefined;
  getLastWebhookCall(): unknown;
  reset(): void;
}

function cloneProxyRequest(request: ProxyRequest): ProxyRequest {
  return {
    ...request,
    headers: request.headers ? { ...request.headers } : undefined,
    query: request.query ? { ...request.query } : undefined
  };
}

function cloneProxyResponse(response: ProxyResponse): ProxyResponse {
  return {
    status: response.status,
    headers: { ...response.headers },
    data: response.data
  };
}

function cloneWebhook(webhook: NormalizedWebhook): NormalizedWebhook {
  return {
    ...webhook,
    payload: { ...webhook.payload },
    relations: webhook.relations ? webhook.relations.slice() : undefined,
    metadata: webhook.metadata ? { ...webhook.metadata } : undefined
  };
}

function defaultWebhookFromPayload(payload: unknown): NormalizedWebhook {
  const record =
    typeof payload === "object" && payload !== null
      ? payload as Record<string, unknown>
      : {};

  return {
    provider:
      typeof record.provider === "string" ? record.provider : "mock-provider",
    connectionId:
      typeof record.connectionId === "string"
        ? record.connectionId
        : "conn_mock",
    eventType:
      typeof record.eventType === "string" ? record.eventType : "updated",
    objectType:
      typeof record.objectType === "string" ? record.objectType : "objects",
    objectId:
      typeof record.objectId === "string"
        ? record.objectId
        : typeof record.id === "string" || typeof record.id === "number"
          ? String(record.id)
          : "1",
    payload: record
  };
}

export function createMockProvider(
  options: MockProviderOptions = {}
): MockConnectionProvider {
  const proxyCalls: ProxyRequest[] = [];
  const healthCheckCalls: string[] = [];
  const webhookCalls: unknown[] = [];
  const proxyResults: ProxyResponse[] = [];
  const healthCheckResults: boolean[] = [];
  const webhookResults: NormalizedWebhook[] = [];

  const provider: MockConnectionProvider = {
    name: options.name ?? "mock-provider",
    proxyCalls,
    healthCheckCalls,
    webhookCalls,
    results: {
      proxy: proxyResults,
      healthCheck: healthCheckResults,
      webhook: webhookResults
    },
    async proxy(request: ProxyRequest): Promise<ProxyResponse> {
      const capturedRequest = cloneProxyRequest(request);
      proxyCalls.push(capturedRequest);

      const response = options.proxy
        ? await options.proxy(capturedRequest, provider)
        : options.proxyResponse ?? {
            status: 200,
            headers: {},
            data: { ok: true }
          };

      const capturedResponse = cloneProxyResponse(response);
      proxyResults.push(capturedResponse);
      return capturedResponse;
    },
    async healthCheck(connectionId: string): Promise<boolean> {
      healthCheckCalls.push(connectionId);

      const result = options.healthCheck
        ? await options.healthCheck(connectionId, provider)
        : options.healthCheckResult ?? true;

      healthCheckResults.push(result);
      return result;
    },
    async handleWebhook(rawPayload: unknown): Promise<NormalizedWebhook> {
      webhookCalls.push(rawPayload);

      const webhook = options.handleWebhook
        ? await options.handleWebhook(rawPayload, provider)
        : options.webhookResult ?? defaultWebhookFromPayload(rawPayload);

      const capturedWebhook = cloneWebhook(webhook);
      webhookResults.push(capturedWebhook);
      return capturedWebhook;
    },
    getLastProxyCall(): ProxyRequest | undefined {
      return proxyCalls.at(-1);
    },
    getLastHealthCheckCall(): string | undefined {
      return healthCheckCalls.at(-1);
    },
    getLastWebhookCall(): unknown {
      return webhookCalls.at(-1);
    },
    reset(): void {
      proxyCalls.length = 0;
      healthCheckCalls.length = 0;
      webhookCalls.length = 0;
      proxyResults.length = 0;
      healthCheckResults.length = 0;
      webhookResults.length = 0;
    }
  };

  return provider;
}
