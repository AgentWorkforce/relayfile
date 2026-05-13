import { describe, expect, it } from "vitest";

import type {
  EnvelopeRow,
  EventRow,
  FileRow,
  OperationRow,
  Paginated,
  PaginationOptions,
  StorageAdapter,
  WritebackItem,
} from "./storage.js";
import { resolveWritebackSchemaRef } from "./writeback.js";

describe("resolveWritebackSchemaRef", () => {
  it("derives the schema id from a writeback path", () => {
    expect(
      resolveWritebackSchemaRef(
        makeStorage(),
        "/linear/issues/AGE-16__abc/comments/wb-1715000000.json",
      ),
    ).toEqual({
      provider: "linear",
      resource: "comments",
      schemaId: "linear/comments.schema.json",
    });
  });

  it("returns null for paths without a resource segment", () => {
    expect(resolveWritebackSchemaRef(makeStorage(), "/linear/wb.json")).toBeNull();
  });

  it("uses a registered schema id when storage provides one", () => {
    const storage = makeStorage({
      getWritebackSchemaId: (provider, resource) =>
        `${provider}/${resource}.v2.schema.json`,
    });

    expect(
      resolveWritebackSchemaRef(
        storage,
        "/linear/issues/AGE-16__abc/comments/wb.json",
      )?.schemaId,
    ).toBe("linear/comments.v2.schema.json");
  });

  it("returns null when storage reports no registered schema", () => {
    const storage = makeStorage({
      getWritebackSchemaId: () => null,
    });

    expect(
      resolveWritebackSchemaRef(
        storage,
        "/linear/issues/AGE-16__abc/comments/wb.json",
      ),
    ).toBeNull();
  });
});

function makeStorage(overrides: Partial<StorageAdapter> = {}): StorageAdapter {
  return {
    getFile: () => null,
    listFiles: () => [],
    putFile: () => undefined,
    deleteFile: () => undefined,
    appendEvent: () => undefined,
    listEvents: () => ({ items: [], nextCursor: null }),
    getRecentEvents: () => [],
    getOperation: () => null,
    putOperation: () => undefined,
    listOperations: (): Paginated<OperationRow> => ({
      items: [],
      nextCursor: null,
    }),
    nextRevision: () => "rev_next",
    nextOperationId: () => "op_next",
    nextEventId: () => "evt_next",
    enqueueWriteback: () => undefined,
    getPendingWritebacks: () => [],
    getWorkspaceId: () => "ws_core",
    ...overrides,
  };
}

void ({} as {
  envelope?: EnvelopeRow;
  event?: EventRow;
  file?: FileRow;
  page?: PaginationOptions;
  writeback?: WritebackItem;
});
