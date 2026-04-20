import { describe, expect, it } from "vitest";

import { computeDedupHash } from "./dedup.js";

describe("computeDedupHash", () => {
  it("returns a stable sha256 hex digest for the identity tuple", () => {
    expect(computeDedupHash("ws_123", "blob", "abc")).toBe(
      "6403ef0791ad402d60057410bad1327c1e960207979140e9eb76e783ab4563ca",
    );
  });

  it("uses tuple boundaries so adjacent segments do not collide", () => {
    expect(computeDedupHash("ab", "c", "d")).not.toBe(
      computeDedupHash("a", "bc", "d"),
    );
  });
});
