import { describe, expect, it } from "vitest";

import { hasOversizedPathSegment, MAX_PATH_SEGMENT_BYTES, writeFile } from "./files.js";
import type { StorageAdapter } from "./storage.js";

describe("hasOversizedPathSegment", () => {
  it("accepts a path whose every segment is at the byte limit", () => {
    const segment = "a".repeat(MAX_PATH_SEGMENT_BYTES - ".json".length);
    expect(hasOversizedPathSegment(`/reddit/subreddits/localllama/posts/${segment}.json`)).toBe(false);
  });

  it("rejects a path with one segment over the byte limit", () => {
    const segment = "a".repeat(MAX_PATH_SEGMENT_BYTES + 1);
    expect(hasOversizedPathSegment(`/reddit/subreddits/localllama/posts/${segment}.json`)).toBe(true);
  });

  it("counts UTF-8 encoded bytes, not JS string length", () => {
    // Each "é" is 1 UTF-16 code unit but 2 UTF-8 bytes.
    const segment = "é".repeat(MAX_PATH_SEGMENT_BYTES); // 200 chars, 400 bytes
    expect(hasOversizedPathSegment(`/x/${segment}`)).toBe(true);
  });

  it("ignores empty segments from a normalized path", () => {
    expect(hasOversizedPathSegment("/reddit/subreddits/localllama.json")).toBe(false);
  });

  it("reproduces the real oversized r/LocalLLaMA post slug that broke the mount", () => {
    const badPath =
      "/reddit/subreddits/localllama/posts/i-just-don-t-get-it-these-big-tech-companies-can-illegally-" +
      "scrape-the-entire-internet-and-gatekeep-their-better-models-behind-higher-prices-so-it-s-natural-" +
      "that-people-look-for-affordable-options-and-there-will-be-providers-who-apparently-distill-models-" +
      "from-them__1uvwn9q.json";
    expect(hasOversizedPathSegment(badPath)).toBe(true);
  });
});

describe("writeFile", () => {
  function storageStub(overrides: Partial<StorageAdapter> = {}): StorageAdapter {
    return {
      getFile: () => null,
      listFiles: () => [],
      putFile: () => {},
      deleteFile: () => {},
      appendEvent: () => {},
      listEvents: () => ({ items: [], nextCursor: null }),
      getRecentEvents: () => [],
      getOperation: () => null,
      putOperation: () => {},
      listOperations: () => ({ items: [], nextCursor: null }),
      nextRevision: () => "rev_1",
      nextOperationId: () => "op_1",
      nextEventId: () => "evt_1",
      enqueueWriteback: () => {},
      getPendingWritebacks: () => [],
      getWorkspaceId: () => "ws_test",
      ...overrides,
    } as StorageAdapter;
  }

  it("rejects a write whose path has an oversized segment before touching storage", () => {
    const segment = "a".repeat(MAX_PATH_SEGMENT_BYTES + 1);
    let storageTouched = false;
    const storage = storageStub({
      getFile: () => {
        storageTouched = true;
        return null;
      },
    });

    const result = writeFile(storage, {
      path: `/reddit/subreddits/localllama/posts/${segment}.json`,
      ifMatch: "*",
      content: "{}",
    });

    expect(result).toEqual({ ok: false, error: "invalid_input" });
    expect(storageTouched).toBe(false);
  });

  it("allows a write with normal-length path segments to proceed past the guard", () => {
    const storage = storageStub();
    const result = writeFile(storage, {
      path: "/reddit/subreddits/localllama/posts/a-normal-title__abc123.json",
      ifMatch: "0",
      content: "{}",
    });

    expect(result.ok).toBe(true);
  });
});
