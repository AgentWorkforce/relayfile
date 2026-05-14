import { describe, expect, it, vi } from "vitest";
import type { RelayFileClient } from "./client.js";
import type { ConnectionProvider } from "./connection.js";
import type { WritebackItem } from "./types.js";
import { WritebackConsumer } from "./writeback-consumer.js";

describe("WritebackConsumer", () => {
  it("passes enriched writeback items through to handlers unchanged", async () => {
    const item: WritebackItem = {
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
      error: {
        code: "schema_violation",
        message: "Comment body is required",
        providerStatus: 422,
        providerResponse: { field: "body" },
        attempts: 4,
        firstAttemptAt: "2026-05-13T10:00:00Z",
        lastAttemptAt: "2026-05-13T10:04:00Z",
        opId: "op_01HX"
      }
    };
    const execute = vi.fn().mockResolvedValue(undefined);
    const client = {
      listPendingWritebacks: vi.fn().mockResolvedValue([item]),
      ackWriteback: vi.fn().mockResolvedValue({
        status: "acknowledged",
        id: "wb_dead",
        success: true
      })
    } as unknown as RelayFileClient;

    const consumer = new WritebackConsumer({
      client,
      workspaceId: "ws_acme",
      handlers: [{ canHandle: () => true, execute }],
      provider: {} as ConnectionProvider,
      pollIntervalMs: 0
    });

    await consumer.pollOnce();

    expect(execute).toHaveBeenCalledWith(item, expect.any(Object));
    expect(execute.mock.calls[0]?.[0]).toBe(item);
    expect(client.ackWriteback).toHaveBeenCalledWith({
      workspaceId: "ws_acme",
      itemId: "wb_dead",
      success: true,
      correlationId: "corr_dead",
      signal: undefined
    });
  });
});
