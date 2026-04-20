import { describe, expect, expectTypeOf, it } from "vitest";

import type { ContentIdentity, DedupStore } from "./dedup.js";
import { computeDedupHash } from "./dedup.js";

describe("computeDedupHash coverage", () => {
  it("is deterministic for the same inputs", () => {
    const first = computeDedupHash("ws_acme", "push", "repo:abc:main");
    const second = computeDedupHash("ws_acme", "push", "repo:abc:main");

    expect(second).toBe(first);
  });

  it("differs when workspaceId differs", () => {
    expect(computeDedupHash("ws_acme", "push", "repo:abc:main")).not.toBe(
      computeDedupHash("ws_other", "push", "repo:abc:main"),
    );
  });
});

describe("dedup type contracts", () => {
  it("compile for ContentIdentity and DedupStore consumers", async () => {
    const identity: ContentIdentity = {
      kind: "push",
      key: "repo:abc:main",
    };
    const store: DedupStore = {
      async has(key) {
        if (key !== "dedup-hash") {
          return null;
        }
        return {
          contentIdentity: identity,
          opId: "op_123",
        };
      },
      async put() {
        return undefined;
      },
    };

    expectTypeOf(identity).toMatchTypeOf<ContentIdentity>();
    expectTypeOf(store).toMatchTypeOf<DedupStore>();

    await expect(
      store.put(
        "dedup-hash",
        {
          contentIdentity: identity,
          opId: "op_123",
        },
        300,
      ),
    ).resolves.toBeUndefined();
    await expect(store.has("dedup-hash")).resolves.toEqual({
      contentIdentity: identity,
      opId: "op_123",
    });
  });
});
