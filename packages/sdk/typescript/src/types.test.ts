import { describe, expect, expectTypeOf, it } from "vitest";
import type {
  DigestHandler,
  DigestSection,
  LayoutManifest,
  SweepWritebackDraftsInput,
  SweepWritebackDraftsResponse,
  AckWritebackDraftDisposition,
  WritebackDeadLetterError,
  WritebackDeadLetterErrorCode,
  WritebackItem,
  WritebackItemDetail
} from "./index.js";

function assertNever(value: never): never {
  throw new Error(`Unexpected value: ${value}`);
}

describe("workspace primitive public types", () => {
  it("accepts enriched writeback list items", () => {
    const item = {
      id: "wb_1",
      workspaceId: "ws_acme",
      path: "/linear/issues/AGE-16__issue_1/comments/wb-1715600000.json",
      revision: "rev_5",
      correlationId: "corr_1",
      state: "pending",
      provider: "linear",
      action: "file_upsert",
      attempts: 1,
      enqueuedAt: "2026-05-13T10:00:00Z",
      lastAttemptAt: "2026-05-13T10:01:00Z"
    } satisfies WritebackItem;

    expectTypeOf(item).toMatchTypeOf<WritebackItem>();
    expect(item.provider).toBe("linear");
  });

  it("accepts dead-lettered writeback items with sidecar errors", () => {
    const error = {
      code: "schema_violation",
      message: "Comment body is required",
      providerStatus: 422,
      providerResponse: { field: "body" },
      attempts: 4,
      firstAttemptAt: "2026-05-13T10:00:00Z",
      lastAttemptAt: "2026-05-13T10:04:00Z",
      opId: "op_01HX"
    } satisfies WritebackDeadLetterError;

    const item = {
      id: "wb_dead",
      workspaceId: "ws_acme",
      path: "/linear/issues/AGE-16__issue_1/comments/wb-1715600000.json",
      revision: "rev_6",
      correlationId: "corr_dead",
      state: "dead_lettered",
      provider: "linear",
      action: "file_upsert",
      attempts: 4,
      lastAttemptAt: "2026-05-13T10:04:00Z",
      error
    } satisfies WritebackItemDetail;

    expectTypeOf(item).toMatchTypeOf<WritebackItem>();
    expect(item.error.code).toBe("schema_violation");
  });

  it("accepts writeback list rows emitted by the CLI", () => {
    const item = {
      id: "op_01HX",
      workspaceId: "ws_acme",
      path: "/linear/issues/AGE-16__issue_1/comments/wb-1715600000.json",
      revision: "2026-05-13T10:04:00Z",
      correlationId: "op_01HX",
      state: "dead",
      provider: "linear",
      ts: "2026-05-13T10:04:00Z",
      code: "schema_violation",
      message: "Comment body is required",
      providerStatus: 422,
      providerResponse: { field: "body" },
      attempts: 4,
      firstAttemptAt: "2026-05-13T10:00:00Z",
      lastAttemptAt: "2026-05-13T10:04:00Z"
    } satisfies WritebackItem;

    expectTypeOf(item).toMatchTypeOf<WritebackItem>();
    expect(item.state).toBe("dead");
  });

  it("exports writeback ack and sweep public types", () => {
    const draft = {
      action: "renamed",
      from: "/slack/channels/C/messages/messages 0e89a031-65f0-480e-a823-ab1d94b324ea.json",
      to: "/slack/channels/C/messages/1780018871.351819.json"
    } satisfies AckWritebackDraftDisposition;
    const input = {
      workspaceId: "ws_acme",
      pathPrefix: "/slack/channels/C",
      patterns: ["wb-*.json"],
      apply: false
    } satisfies SweepWritebackDraftsInput;
    const response = {
      dryRun: true,
      scanned: 1,
      removed: [{ path: draft.from, reason: "space-uuid-draft" }],
      skipped: []
    } satisfies SweepWritebackDraftsResponse;

    expectTypeOf(input).toMatchTypeOf<SweepWritebackDraftsInput>();
    expectTypeOf(response).toMatchTypeOf<SweepWritebackDraftsResponse>();
    expect(response.removed[0]?.path).toBe(draft.from);
  });

  it("accepts digest handlers that return null or a section", async () => {
    const noActivity: DigestHandler = async () => null;
    const withActivity: DigestHandler = async (ctx) => ({
      provider: ctx.provider,
      bullets: [
        {
          text: "AGE-16 moved to in-review",
          canonicalPath: "/linear/issues/AGE-16__issue_1.json"
        }
      ]
    });

    expect(await noActivity(makeDigestContext())).toBeNull();
    expectTypeOf(await withActivity(makeDigestContext())).toMatchTypeOf<DigestSection | null>();
  });

  it("accepts layout manifests with schema references", () => {
    const manifest = {
      provider: "linear",
      materialization: "lazy",
      resources: [
        {
          name: "issues",
          canonicalFilename: "<identifier>__<uuid>.json",
          writebackActions: ["comments"],
          writebackSchemas: [
            {
              provider: "linear",
              resource: "comments",
              path: "/linear/issues/comments/.schema.json"
            }
          ],
          aliases: [{ segment: "by-title", description: "Issue title aliases" }]
        }
      ]
    } satisfies LayoutManifest;

    expectTypeOf(manifest).toMatchTypeOf<LayoutManifest>();
    expect(manifest.resources[0]?.aliases?.[0]?.segment).toBe("by-title");
  });

  it("covers every dead-letter error code", () => {
    const labels = [
      labelDeadLetterError("schema_violation"),
      labelDeadLetterError("provider_4xx"),
      labelDeadLetterError("provider_5xx_exhausted"),
      labelDeadLetterError("timeout")
    ];

    expect(labels).toEqual(["schema", "provider-4xx", "provider-5xx", "timeout"]);
  });
});

function makeDigestContext(): Parameters<DigestHandler>[0] {
  return {
    provider: "linear",
    window: {
      from: "2026-05-12T00:00:00Z",
      to: "2026-05-13T00:00:00Z"
    },
    async changeEvents() {
      return [];
    }
  };
}

function labelDeadLetterError(code: WritebackDeadLetterErrorCode): string {
  switch (code) {
    case "schema_violation":
      return "schema";
    case "provider_4xx":
      return "provider-4xx";
    case "provider_5xx_exhausted":
      return "provider-5xx";
    case "timeout":
      return "timeout";
    default:
      return assertNever(code);
  }
}
