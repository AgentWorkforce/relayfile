import { describe, expect, it } from "vitest";
import { genericPresent, genericReplyPathFor, IntegrationAdapter, type AdapterWebhook, type IngestResult } from "./integration-adapter.js";
import type { RelayFileClient } from "./client.js";
import type { ConnectionProvider } from "./connection.js";
import type { FileSemantics } from "./types.js";

class TestAdapter extends IntegrationAdapter {
  readonly name = "linear";
  readonly version = "1.0.0";

  constructor() {
    super({} as RelayFileClient, {} as ConnectionProvider);
  }

  async ingestWebhook(_workspaceId: string, _event: AdapterWebhook): Promise<IngestResult> {
    return { filesWritten: 0, filesUpdated: 0, filesDeleted: 0, paths: [], errors: [] };
  }

  computePath(objectType: string, objectId: string): string {
    return `/linear/${objectType}/${objectId}.json`;
  }

  computeSemantics(objectType: string, objectId: string, payload: Record<string, unknown>): FileSemantics {
    return {
      properties: {
        title: String(payload.title ?? objectId),
        "provider.status": String(payload.status ?? "open"),
        objectType
      },
      relations: ["/linear/users/u1.json"],
      comments: ["ready"]
    };
  }
}

describe("relay binding defaults", () => {
  it("builds a semantics-aware truncated presenter view", () => {
    const adapter = new TestAdapter();
    const view = genericPresent({
      workspaceId: "ws_1",
      path: "/linear/issues/ENG-1.json",
      operation: "update",
      revision: "rev_1",
      previousRevision: null,
      timestamp: "2026-06-27T00:00:00.000Z",
      source: "sync",
      actor: { type: "user", id: "alice" },
      value: {
        title: "A".repeat(800),
        status: "triage"
      }
    }, adapter);

    expect(view.author).toBe("alice");
    expect(view.text.length).toBeLessThanOrEqual(500);
    expect(view.text).toContain("update /linear/issues/ENG-1.json");
    expect(view.text).toContain("status: triage");
    expect(view.text).toContain("1 relation");
  });

  it("falls back to the provider segment as author", () => {
    const view = genericPresent({
      workspaceId: "ws_1",
      path: "/github/issues/1.json",
      operation: "create",
      revision: "rev_1",
      previousRevision: null,
      timestamp: "2026-06-27T00:00:00.000Z",
      source: "sync"
    }, new TestAdapter());

    expect(view.author).toBe("github");
  });

  it("returns the conventional replies draft path", () => {
    expect(genericReplyPathFor("/linear/issues/ENG-1.json")).toBe("/linear/issues/ENG-1.json/replies/draft.json");
    expect(genericReplyPathFor("linear/issues/ENG-1.json")).toBeNull();
  });
});
