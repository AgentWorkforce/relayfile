import type { IngestError, IngestResult } from "../adapter.js";
import type { NormalizedWebhook, ProxyResponse } from "../provider.js";

export type NormalizedWebhookFixture = NormalizedWebhook;
export type ProxyResponseFixture = ProxyResponse;
export type IngestResultFixture = IngestResult;

function cloneStringMap(
  value: Record<string, string> | undefined
): Record<string, string> | undefined {
  return value ? { ...value } : undefined;
}

function cloneStringArray(value: string[] | undefined): string[] | undefined {
  return value ? [...value] : undefined;
}

function clonePayload(value: Record<string, unknown>): Record<string, unknown> {
  return { ...value };
}

function cloneNormalizedWebhook(
  fixture: NormalizedWebhookFixture
): NormalizedWebhookFixture {
  return {
    ...fixture,
    payload: clonePayload(fixture.payload),
    relations: cloneStringArray(fixture.relations),
    metadata: cloneStringMap(fixture.metadata)
  };
}

function cloneProxyResponse(fixture: ProxyResponseFixture): ProxyResponseFixture {
  return {
    status: fixture.status,
    headers: cloneStringMap(fixture.headers) ?? {},
    data:
      typeof fixture.data === "object" && fixture.data !== null
        ? { ...(fixture.data as Record<string, unknown>) }
        : fixture.data
  };
}

function cloneIngestError(error: IngestError): IngestError {
  return { ...error };
}

function cloneIngestResult(fixture: IngestResultFixture): IngestResultFixture {
  return {
    filesWritten: fixture.filesWritten,
    filesUpdated: fixture.filesUpdated,
    filesDeleted: fixture.filesDeleted,
    paths: [...fixture.paths],
    errors: fixture.errors.map(cloneIngestError)
  };
}

export function createNormalizedWebhookFixture(
  overrides: Partial<NormalizedWebhookFixture> = {}
): NormalizedWebhookFixture {
  return {
    provider: overrides.provider ?? "github",
    connectionId: overrides.connectionId ?? "conn_test",
    eventType: overrides.eventType ?? "issues.opened",
    objectType: overrides.objectType ?? "issues",
    objectId: overrides.objectId ?? "42",
    payload: clonePayload(
      overrides.payload ?? {
        id: "42",
        title: "Adapter test fixture",
        state: "open"
      }
    ),
    relations: cloneStringArray(overrides.relations),
    metadata: cloneStringMap(overrides.metadata)
  };
}

export function createProxyResponseFixture(
  overrides: Partial<ProxyResponseFixture> = {}
): ProxyResponseFixture {
  return {
    status: overrides.status ?? 200,
    headers: cloneStringMap(overrides.headers) ?? {
      "content-type": "application/json"
    },
    data:
      overrides.data !== undefined
        ? cloneProxyResponse({
            status: 0,
            headers: {},
            data: overrides.data
          }).data
        : { ok: true }
  };
}

export function createIngestResultFixture(
  overrides: Partial<IngestResultFixture> = {}
): IngestResultFixture {
  return {
    filesWritten: overrides.filesWritten ?? 1,
    filesUpdated: overrides.filesUpdated ?? 0,
    filesDeleted: overrides.filesDeleted ?? 0,
    paths: overrides.paths ? [...overrides.paths] : ["/github/issues/42.json"],
    errors: overrides.errors ? overrides.errors.map(cloneIngestError) : []
  };
}

export const normalizedWebhookFixtures = Object.freeze({
  githubIssueOpened: createNormalizedWebhookFixture({
    provider: "github",
    connectionId: "conn_github_test",
    eventType: "issues.opened",
    objectType: "issues",
    objectId: "42",
    payload: {
      id: "42",
      number: 42,
      title: "Fixture issue",
      state: "open"
    },
    relations: ["repo:agent-workforce/relayfile"],
    metadata: {
      installationId: "12345",
      organization: "AgentWorkforce"
    }
  }),
  slackMessagePosted: createNormalizedWebhookFixture({
    provider: "slack",
    connectionId: "conn_slack_test",
    eventType: "message.posted",
    objectType: "messages",
    objectId: "1700000000.100200",
    payload: {
      channel: "C123456",
      ts: "1700000000.100200",
      text: "Fixture message",
      user: "U123456"
    },
    relations: ["channel:C123456"],
    metadata: {
      workspace: "T123456"
    }
  }),
  zendeskTicketUpdated: createNormalizedWebhookFixture({
    provider: "zendesk",
    connectionId: "conn_zendesk_test",
    eventType: "ticket.updated",
    objectType: "tickets",
    objectId: "48291",
    payload: {
      id: 48291,
      priority: "high",
      status: "open",
      subject: "Fixture support ticket"
    },
    relations: ["organization:acme"],
    metadata: {
      subdomain: "acme-support"
    }
  })
}) as Readonly<Record<string, NormalizedWebhookFixture>>;

export const proxyResponseFixtures = Object.freeze({
  ok: createProxyResponseFixture({
    status: 200,
    headers: {
      "content-type": "application/json"
    },
    data: {
      ok: true
    }
  }),
  created: createProxyResponseFixture({
    status: 201,
    headers: {
      "content-type": "application/json",
      location: "/proxy/events/evt_123"
    },
    data: {
      id: "evt_123",
      ok: true
    }
  }),
  notFound: createProxyResponseFixture({
    status: 404,
    headers: {
      "content-type": "application/json"
    },
    data: {
      code: "not_found",
      message: "Resource not found"
    }
  }),
  rateLimited: createProxyResponseFixture({
    status: 429,
    headers: {
      "content-type": "application/json",
      "retry-after": "60"
    },
    data: {
      code: "rate_limited",
      message: "Too many requests"
    }
  })
}) as Readonly<Record<string, ProxyResponseFixture>>;

export const ingestResultFixtures = Object.freeze({
  created: createIngestResultFixture({
    filesWritten: 1,
    filesUpdated: 0,
    filesDeleted: 0,
    paths: ["/github/issues/42.json"],
    errors: []
  }),
  updated: createIngestResultFixture({
    filesWritten: 0,
    filesUpdated: 1,
    filesDeleted: 0,
    paths: ["/zendesk/tickets/48291.json"],
    errors: []
  }),
  partialFailure: createIngestResultFixture({
    filesWritten: 1,
    filesUpdated: 0,
    filesDeleted: 0,
    paths: ["/slack/messages/1700000000.100200.json"],
    errors: [
      {
        path: "/slack/messages/1700000000.100201.json",
        error: "Attachment payload exceeded maximum size."
      }
    ]
  })
}) as Readonly<Record<string, IngestResultFixture>>;

export const defaultNormalizedWebhookFixture = cloneNormalizedWebhook(
  normalizedWebhookFixtures.githubIssueOpened
);
export const defaultProxyResponseFixture = cloneProxyResponse(
  proxyResponseFixtures.ok
);
export const defaultIngestResultFixture = cloneIngestResult(
  ingestResultFixtures.created
);
