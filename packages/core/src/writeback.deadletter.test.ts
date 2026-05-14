import { describe, expect, it } from "vitest";

import {
  normalizeDeadLetterError,
  type DeadLetterError,
} from "./writeback.js";

describe("normalizeDeadLetterError", () => {
  it("round-trips a canonical dead-letter error", () => {
    const input: DeadLetterError = {
      code: "schema_violation",
      message: "body must include event",
      providerStatus: 422,
      providerResponse: { error: "missing event" },
      attempts: 4,
      firstAttemptAt: "2026-05-13T10:00:00.000Z",
      lastAttemptAt: "2026-05-13T10:05:00.000Z",
      opId: "op_dead",
    };

    const normalized = normalizeDeadLetterError(input);

    expect(normalized).toEqual(input);
    expect(Object.isFrozen(normalized)).toBe(true);
  });

  it("rejects unknown code values", () => {
    expect(() =>
      normalizeDeadLetterError({
        ...canonicalInput(),
        code: "provider_unknown",
      }),
    ).toThrow(TypeError);
  });

  it("trims message whitespace", () => {
    expect(
      normalizeDeadLetterError({
        ...canonicalInput(),
        message: "  timed out  ",
      }).message,
    ).toBe("timed out");
  });

  it("rejects missing required fields", () => {
    for (const field of ["firstAttemptAt", "lastAttemptAt", "opId"] as const) {
      const input: Record<string, unknown> = canonicalInput();
      delete input[field];

      expect(() => normalizeDeadLetterError(input)).toThrow(TypeError);
    }
  });

  it("accepts optional provider status and object response", () => {
    expect(
      normalizeDeadLetterError({
        ...canonicalInput(),
        providerStatus: 504,
        providerResponse: { retryable: true },
      }),
    ).toMatchObject({
      providerStatus: 504,
      providerResponse: { retryable: true },
    });
  });

  it("accepts array provider responses matching the canonical schema", () => {
    expect(
      normalizeDeadLetterError({
        ...canonicalInput(),
        providerResponse: [{ error: "bad request" }],
      }).providerResponse,
    ).toEqual([{ error: "bad request" }]);
  });
});

function canonicalInput(): Record<string, unknown> {
  return {
    code: "provider_5xx_exhausted",
    message: "upstream failed",
    attempts: 3,
    firstAttemptAt: "2026-05-13T10:00:00.000Z",
    lastAttemptAt: "2026-05-13T10:05:00.000Z",
    opId: "op_123",
  };
}
