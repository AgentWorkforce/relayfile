export { adapterConfigSchema } from "./config-schema.js";

export interface RelayfileWebhookEvent {
  provider: string;
  eventType: string;
  objectType: string;
  objectId: string;
  payload: unknown;
}

export interface RelayfileFileProjection {
  path: string;
  content: string;
  contentType: string;
  metadata?: Record<string, string>;
}

export interface RelayfileAdapter {
  readonly name: string;
  ingestWebhook(event: RelayfileWebhookEvent): Promise<RelayfileFileProjection[]>;
}

export class ExampleAdapter implements RelayfileAdapter {
  readonly name = "{{projectName}}";

  async ingestWebhook(event: RelayfileWebhookEvent): Promise<RelayfileFileProjection[]> {
    return [
      {
        path: `/${event.provider}/${event.objectType}/${event.objectId}.json`,
        content: JSON.stringify(
          {
            provider: event.provider,
            eventType: event.eventType,
            objectType: event.objectType,
            objectId: event.objectId,
            payload: event.payload
          },
          null,
          2
        ),
        contentType: "application/json",
        metadata: {
          provider: event.provider,
          template: "adapter"
        }
      }
    ];
  }
}
