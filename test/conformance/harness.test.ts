import assert from "node:assert/strict";
import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import Ajv2020 from "ajv/dist/2020.js";
import { parse } from "yaml";
import { loadTarget } from "./config.js";
import { redact, redactEnvironmentText, writeEvidence } from "./evidence.js";
import { Harness } from "./harness.js";
import { validateOpenApiResponse } from "./openapi-validator.js";
import type { EvidenceSummary, ResolvedTarget } from "./types.js";

test("go-local target descriptor is valid", async () => {
  const loaded = await loadTarget("go-local", {});
  assert.equal(loaded.target.id, "go-local");
  assert.equal(loaded.target.runtime.kind, "go-oracle");
  assert(loaded.target.capabilities.has("durable-restart"));
});

test("unconfigured remote target fails loudly", async () => {
  await assert.rejects(() => loadTarget("cloudflare-hosted", {}), /unconfigured/u);
});

test("checked-in target descriptors conform to the target schema", async () => {
  const schema = JSON.parse(await readFile("test/conformance/target.schema.json", "utf8"));
  const validate = new Ajv2020({ allErrors: true, strict: false, validateFormats: false }).compile(schema);
  const files = (await readdir("test/conformance/targets")).filter((name) => name.endsWith(".json"));
  for (const file of files) {
    const descriptor = JSON.parse(await readFile(join("test/conformance/targets", file), "utf8"));
    assert(validate(descriptor), `${file}: ${JSON.stringify(validate.errors)}`);
  }
});

test("hosted Cloudflare target resolves for public-edge coverage without private controls", async () => {
  const loaded = await loadTarget("cloudflare-hosted", remoteEnvironment());
  assert.equal(loaded.target.runtime.kind, "cloudflare-do");
  assert.equal(loaded.target.control, undefined);
});

test("Terse target fails closed when its actor shared secret is unset", async () => {
  await assert.rejects(() => loadTarget("terse", remoteEnvironment()), /must fail closed/u);
});

test("unknown capabilities are rejected", async () => {
  const dir = await mkdtemp(join(tmpdir(), "relayfile-target-test-"));
  const path = join(dir, "bad.json");
  await writeFile(
    path,
    JSON.stringify({
      schemaVersion: 1,
      id: "bad",
      runtime: { kind: "go-oracle", version: "test" },
      relayfileSha: "test",
      baseUrl: "http://127.0.0.1:1",
      workspaces: { primary: "a", secondary: "b" },
      auth: { strategy: "local-rs256" },
      capabilities: ["not-real"],
    }),
  );
  await assert.rejects(() => loadTarget(path, {}), /unknown capabilities/u);
  await rm(dir, { recursive: true, force: true });
});

test("redaction removes credentials from nested request evidence", () => {
  const target = fakeTarget();
  target.tokens.primary = "token-primary-secret";
  target.control = { baseUrl: "http://control", token: "rk_live_secret", operations: new Set() };
  const safe = redact(
    {
      headers: { Authorization: "Bearer token-primary-secret", Cookie: "session=secret" },
      nested: { api_key: "rk_live_secret", text: "prefix token-primary-secret suffix" },
    },
    target,
  );
  const serialized = JSON.stringify(safe);
  assert(!serialized.includes("token-primary-secret"));
  assert(!serialized.includes("rk_live_secret"));
  assert(!serialized.includes("session=secret"));
  assert(serialized.includes("[REDACTED]"));
});

test("console error redaction removes configured environment secrets", () => {
  const safe = redactEnvironmentText("adapter failed with top-secret-token", {
    RELAYFILE_CONFORMANCE_CONTROL_TOKEN: "top-secret-token",
  });
  assert(!safe.includes("top-secret-token"));
  assert(safe.includes("[REDACTED]"));
});

test("missing capability skips in core and fails in full", async () => {
  const core = new Harness(fakeTarget(), "core", "seed");
  await core.case("RF-TEST-001", "capability", ["clock-control"], async () => {});
  assert.equal(core.cases[0]?.status, "skipped");

  const full = new Harness(fakeTarget(), "full", "seed");
  await full.case("RF-TEST-001", "capability", ["clock-control"], async () => {});
  assert.equal(full.cases[0]?.status, "failed");
});

test("evidence emits JSON, JSONL, and JUnit without secrets", async () => {
  const dir = await mkdtemp(join(tmpdir(), "relayfile-evidence-test-"));
  const target = fakeTarget();
  target.tokens.primary = "token-primary-secret";
  const summary: EvidenceSummary = {
    schemaVersion: 1,
    seed: "seed",
    profile: "core",
    target: {
      id: target.id,
      baseUrl: target.baseUrl,
      runtime: target.runtime,
      relayfileSha: target.relayfileSha,
      capabilities: [],
    },
    startedAt: "2026-01-01T00:00:00.000Z",
    finishedAt: "2026-01-01T00:00:01.000Z",
    counts: { passed: 1, failed: 0, skipped: 0 },
    cases: [
      {
        id: "RF-TEST-001",
        name: "evidence",
        status: "passed",
        durationMs: 1,
        requiredCapabilities: [],
        correlationIds: ["seed-0001"],
      },
    ],
  };
  await writeEvidence(
    dir,
    summary,
    [
      {
        correlationId: "seed-0001",
        method: "GET",
        url: "http://example.test",
        startedAt: summary.startedAt,
        durationMs: 1,
        request: { headers: { Authorization: "Bearer token-primary-secret" } },
        response: { status: 200, headers: {}, body: { token: "token-primary-secret" } },
      },
    ],
    target,
  );
  const all = `${await readFile(join(dir, "summary.json"), "utf8")}\n${await readFile(
    join(dir, "requests.jsonl"),
    "utf8",
  )}\n${await readFile(join(dir, "junit.xml"), "utf8")}`;
  assert(!all.includes("token-primary-secret"));
  assert(all.includes("RF-TEST-001"));
  await rm(dir, { recursive: true, force: true });
});

test("OpenAPI validator accepts the concrete conflict envelope", async () => {
  await validateOpenApiResponse(
    "PUT",
    "/v1/workspaces/primary/fs/file?path=%2Ftest.md",
    409,
    {
      code: "revision_conflict",
      message: "revision conflict",
      correlationId: "seed-0001",
      expectedRevision: "rev_1",
      currentRevision: "rev_2",
      currentContentPreview: "current",
    },
    "application/json",
  );
});

test("conflict schemas retain every required ErrorResponse field", async () => {
  const document = parse(await readFile("openapi/relayfile-v1.openapi.yaml", "utf8")) as {
    components: { schemas: Record<string, { required?: string[] }> };
  };
  const schemas = document.components.schemas;
  const baseRequired = schemas.ErrorResponse?.required ?? [];
  for (const name of ["ConflictErrorResponse", "ForkCommitConflictResponse"]) {
    const required = new Set(schemas[name]?.required ?? []);
    for (const field of baseRequired) assert(required.has(field), `${name} stopped requiring ${field}`);
  }
});

function fakeTarget(): ResolvedTarget {
  return {
    id: "fake",
    runtime: { kind: "go-oracle", version: "test" },
    relayfileSha: "test",
    baseUrl: "http://example.test",
    workspaces: { primary: "primary", secondary: "secondary" },
    tokens: {},
    capabilities: new Set(),
    sourcePath: "fake.json",
  };
}

function remoteEnvironment(): NodeJS.ProcessEnv {
  return {
    RELAYFILE_BASE_URL: "https://relayfile.example.test",
    RELAYFILE_WORKSPACE_PRIMARY: "primary",
    RELAYFILE_WORKSPACE_SECONDARY: "secondary",
    RELAYFILE_TOKEN_PRIMARY: "primary-token",
    RELAYFILE_TOKEN_SECONDARY: "secondary-token",
    RELAYFILE_TOKEN_PATH_SCOPED: "path-token",
    RELAYFILE_TOKEN_READ_ONLY: "read-only-token",
    RELAYFILE_RUNTIME_VERSION: "runtime-sha",
    RELAYFILE_SHA: "relayfile-sha",
    RELAYFILE_CONFORMANCE_CONTROL_URL: "https://control.example.test",
    RELAYFILE_CONFORMANCE_CONTROL_TOKEN: "control-token",
  };
}
