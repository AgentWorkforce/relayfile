import { describe, expect, it } from "vitest";

import type {
  EnvelopeQueryOptions,
  EnvelopeRow,
  EventRow,
  FileRow,
  OperationRow,
  Paginated,
  PaginationOptions,
  StorageAdapter,
  WritebackItem,
} from "./storage.js";
import {
  MAX_WRITEBACK_ATTEMPTS,
  listWritebacks,
  type WritebackListItem,
} from "./writeback.js";

describe("listWritebacks", () => {
  it("lists pending writebacks deterministically by nextAttemptAt with nulls last", () => {
    const storage = makeStorage([
      operation({
        opId: "op_null",
        path: "/linear/comments/null.json",
        nextAttemptAt: null,
      }),
      operation({
        opId: "op_late",
        path: "/linear/comments/late.json",
        nextAttemptAt: "2026-05-13T12:00:00.000Z",
      }),
      operation({
        opId: "op_early",
        path: "/linear/comments/early.json",
        nextAttemptAt: "2026-05-13T11:00:00.000Z",
      }),
    ]);

    const first = listWritebacks(storage, { state: "pending" }).items;
    const second = listWritebacks(storage, { state: "pending" }).items;

    expect(first.map((item) => item.id)).toEqual([
      "op_early",
      "op_late",
      "op_null",
    ]);
    expect(second).toEqual(first);
    expect(first[0]).toMatchObject({
      workspaceId: "ws_core",
      path: "/linear/comments/early.json",
      state: "pending",
      attemptCount: 0,
      provider: "linear",
    });
  });

  it("sorts before applying the requested page limit", () => {
    const storage = makeStorage(
      [
        operation({
          opId: "op_late",
          nextAttemptAt: "2026-05-13T12:00:00.000Z",
        }),
        operation({
          opId: "op_null",
          nextAttemptAt: null,
        }),
        operation({
          opId: "op_early",
          nextAttemptAt: "2026-05-13T10:00:00.000Z",
        }),
      ],
      { sort: false },
    );

    const first = listWritebacks(storage, { state: "pending", limit: 1 });
    const second = listWritebacks(storage, {
      state: "pending",
      cursor: first.nextCursor ?? undefined,
      limit: 1,
    });

    expect(first.items.map((item) => item.id)).toEqual(["op_early"]);
    expect(first.nextCursor).toBe("op_early");
    expect(second.items.map((item) => item.id)).toEqual(["op_late"]);
    expect(second.nextCursor).toBe("op_late");
  });

  it("lists dead-lettered writebacks with retry metadata", () => {
    const storage = makeStorage([
      operation({
        opId: "op_dead",
        status: "dead_lettered",
        attemptCount: MAX_WRITEBACK_ATTEMPTS,
        lastError: "schema validation failed",
      }),
      operation({ opId: "op_pending", status: "pending" }),
    ]);

    expect(listWritebacks(storage, { state: "dead_lettered" }).items).toEqual([
      expect.objectContaining({
        id: "op_dead",
        state: "dead_lettered",
        attemptCount: MAX_WRITEBACK_ATTEMPTS,
        lastError: "schema validation failed",
      }),
    ]);
  });

  it("lists succeeded and failed writebacks separately", () => {
    const storage = makeStorage([
      operation({ opId: "op_succeeded", status: "succeeded" }),
      operation({ opId: "op_failed", status: "failed", lastError: "timeout" }),
      operation({ opId: "op_pending", status: "pending" }),
    ]);

    expect(listWritebacks(storage, { state: "succeeded" }).items).toEqual([
      expect.objectContaining({ id: "op_succeeded", state: "succeeded" }),
    ]);
    expect(listWritebacks(storage, { state: "failed" }).items).toEqual([
      expect.objectContaining({
        id: "op_failed",
        state: "failed",
        lastError: "timeout",
      }),
    ]);
  });

  it("rejects unknown states", () => {
    const storage = makeStorage([]);

    expect(() =>
      listWritebacks(storage, { state: "dead" as never }),
    ).toThrow(TypeError);
  });

  it("paginates using the returned operation cursor", () => {
    const storage = makeStorage([
      operation({ opId: "op_1", nextAttemptAt: "2026-05-13T10:00:00.000Z" }),
      operation({ opId: "op_2", nextAttemptAt: "2026-05-13T11:00:00.000Z" }),
      operation({ opId: "op_3", nextAttemptAt: "2026-05-13T12:00:00.000Z" }),
      operation({ opId: "op_4", nextAttemptAt: "2026-05-13T13:00:00.000Z" }),
      operation({ opId: "op_5", nextAttemptAt: "2026-05-13T14:00:00.000Z" }),
    ]);

    const first = listWritebacks(storage, { state: "pending", limit: 2 });
    const second = listWritebacks(storage, {
      state: "pending",
      cursor: first.nextCursor ?? undefined,
      limit: 2,
    });

    expect(first.items.map((item) => item.id)).toEqual(["op_1", "op_2"]);
    expect(first.nextCursor).toBe("op_2");
    expect(second.items.map((item) => item.id)).toEqual(["op_3", "op_4"]);
    expect(second.nextCursor).toBe("op_4");
  });

  it("returns an empty page for unknown cursors", () => {
    const storage = makeStorage([operation({ opId: "op_1" })]);

    expect(
      listWritebacks(storage, {
        state: "pending",
        cursor: "op_missing",
      }),
    ).toEqual({ items: [], nextCursor: null });
  });
});

function operation(overrides: Partial<OperationRow>): OperationRow {
  return {
    opId: "op_default",
    path: "/linear/issues/AGE-16__abc/comments/wb.json",
    revision: "rev_1",
    action: "file_upsert",
    provider: "linear",
    status: "pending",
    attemptCount: 0,
    lastError: null,
    nextAttemptAt: null,
    correlationId: "corr_1",
    ...overrides,
  };
}

function makeStorage(
  operations: OperationRow[],
  options: { sort?: boolean } = {},
): StorageAdapter {
  const sortedOperations =
    options.sort === false ? [...operations] : [...operations].sort(compareOperations);
  return {
    getFile: () => null,
    listFiles: () => [],
    putFile: () => undefined,
    deleteFile: () => undefined,
    appendEvent: () => undefined,
    listEvents: () => ({ items: [], nextCursor: null }),
    getRecentEvents: () => [],
    getOperation: (opId) =>
      sortedOperations.find((operationRow) => operationRow.opId === opId) ??
      null,
    putOperation: () => undefined,
    listOperations: (options): Paginated<OperationRow> => {
      const matching = sortedOperations.filter((op) => {
        return !options.status || op.status === options.status;
      });
      const start = options.cursor
        ? matching.findIndex((op) => op.opId === options.cursor) + 1
        : 0;
      const limit = options.limit ?? 100;
      const items = matching.slice(Math.max(0, start), Math.max(0, start) + limit);
      return {
        items,
        nextCursor:
          items.length >= limit ? (items[items.length - 1]?.opId ?? null) : null,
      };
    },
    nextRevision: () => "rev_next",
    nextOperationId: () => "op_next",
    nextEventId: () => "evt_next",
    enqueueWriteback: () => undefined,
    getPendingWritebacks: () => [],
    getWorkspaceId: () => "ws_core",
    listEnvelopes: (_options: EnvelopeQueryOptions): Paginated<EnvelopeRow> => ({
      items: [],
      nextCursor: null,
    }),
  };
}

function compareOperations(left: OperationRow, right: OperationRow): number {
  const leftItem = operationToListItem(left);
  const rightItem = operationToListItem(right);
  if (leftItem.nextAttemptAt && rightItem.nextAttemptAt) {
    const byNextAttempt = leftItem.nextAttemptAt.localeCompare(
      rightItem.nextAttemptAt,
    );
    if (byNextAttempt !== 0) {
      return byNextAttempt;
    }
  } else if (leftItem.nextAttemptAt) {
    return -1;
  } else if (rightItem.nextAttemptAt) {
    return 1;
  }

  return leftItem.id.localeCompare(rightItem.id);
}

function operationToListItem(op: OperationRow): Pick<
  WritebackListItem,
  "id" | "nextAttemptAt"
> {
  return {
    id: op.opId,
    nextAttemptAt: op.nextAttemptAt,
  };
}

void ({} as {
  file?: FileRow;
  event?: EventRow;
  page?: PaginationOptions;
  writeback?: WritebackItem;
});
