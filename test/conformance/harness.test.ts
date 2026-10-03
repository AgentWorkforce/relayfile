import assert from "node:assert/strict";
import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import Ajv2020 from "ajv/dist/2020.js";
import addFormats from "ajv-formats";
import { parse } from "yaml";
import { renderCaseCheckAnnotation } from "./ci-summary.js";
import { assertFullRunSafety, loadTarget } from "./config.js";
import { redact, redactEnvironmentText, writeEvidence } from "./evidence.js";
import { Harness, RequestTransportError } from "./harness.js";
import { validateOpenApiResponse } from "./openapi-validator.js";
import { CONFORMANCE_CASE_IDS, runScenarios } from "./scenarios.js";
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
  const ajv = new Ajv2020({ allErrors: true, strict: false });
  addFormats(ajv);
  const validate = ajv.compile(schema);
  const files = (await readdir("test/conformance/targets")).filter((name) => name.endsWith(".json"));
  for (const file of files) {
    const descriptor = JSON.parse(await readFile(join("test/conformance/targets", file), "utf8"));
    assert(validate(descriptor), `${file}: ${JSON.stringify(validate.errors)}`);
  }
});

test("target schema rejects unusable environment auth and malformed URLs", async () => {
  const schema = JSON.parse(await readFile("test/conformance/target.schema.json", "utf8"));
  const ajv = new Ajv2020({ allErrors: true, strict: false });
  addFormats(ajv);
  const validate = ajv.compile(schema);
  const descriptor = JSON.parse(await readFile("test/conformance/targets/cloudflare-hosted.json", "utf8"));
  descriptor.baseUrl = "not a URL";
  delete descriptor.baseUrlEnv;
  delete descriptor.auth.primaryTokenEnv;
  assert.equal(validate(descriptor), false);
  assert(validate.errors?.some((error) => error.instancePath === "/baseUrl"));
  assert(validate.errors?.some((error) => error.params.missingProperty === "primaryTokenEnv"));
});

test("hosted Cloudflare target resolves for public-edge coverage without private controls", async () => {
  const loaded = await loadTarget("cloudflare-hosted", remoteEnvironment());
  assert.equal(loaded.target.runtime.kind, "cloudflare-do");
  assert.equal(loaded.target.control, undefined);
});

test("remote full profiles require explicit disposable-workspace confirmation", async () => {
  const loaded = await loadTarget("cloudflare-controlled", remoteEnvironment());
  assert.throws(() => assertFullRunSafety(loaded.target, "full", {}), /RELAYFILE_CONFORMANCE_DISPOSABLE=1/u);
  assert.doesNotThrow(() =>
    assertFullRunSafety(loaded.target, "full", { RELAYFILE_CONFORMANCE_DISPOSABLE: "1" }),
  );
  assert.doesNotThrow(() => assertFullRunSafety(loaded.target, "core", {}));
});

test("only the locally spawned Go oracle bypasses remote full-profile safety", async () => {
  const loaded = await loadTarget("go-local", {});
  assert.throws(() => assertFullRunSafety(loaded.target, "full", {}), /reset control operation/u);
  assert.doesNotThrow(() => assertFullRunSafety(loaded.target, "full", {}, true));
});

test("Terse target fails closed when its actor shared secret is unset", async () => {
  await assert.rejects(() => loadTarget("terse", remoteEnvironment()), /must fail closed/u);
});

test("all target descriptors register the same stable case IDs", async () => {
  const expected = [...CONFORMANCE_CASE_IDS].sort();
  for (const name of ["go-local", "cloudflare-hosted", "cloudflare-controlled", "terse"]) {
    const env = name === "go-local" ? {} : remoteEnvironment({ terse: name === "terse" });
    const { target } = await loadTarget(name, env);
    const harness = new Harness(target, "full", "registration-audit", undefined, { registerOnly: true });
    await runScenarios(harness);
    assert.deepEqual(harness.cases.map((result) => result.id).sort(), expected, `${name} registered a different case set`);
    const runtimeAuth = harness.cases.find((result) => result.id === "RF-AUTH-005");
    assert(runtimeAuth, `${name} omitted RF-AUTH-005`);
    if (target.runtime.kind !== "terse-durable-actors") {
      assert.equal(runtimeAuth.status, "skipped");
      assert.equal(runtimeAuth.skipKind, "not-applicable");
      assert.equal(runtimeAuth.skipReason, `not applicable to ${target.runtime.kind}`);
    }
  }
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

test("unknown runtimes, malformed URLs, and unauthenticated controls are rejected", async () => {
  const dir = await mkdtemp(join(tmpdir(), "relayfile-target-security-test-"));
  const base = {
    schemaVersion: 1,
    id: "bad",
    runtime: { kind: "go-oracle", version: "test" },
    relayfileSha: "test",
    baseUrl: "http://127.0.0.1:1",
    workspaces: { primary: "a", secondary: "b" },
    auth: { strategy: "local-rs256" },
    capabilities: [],
  };
  try {
    const runtimePath = join(dir, "runtime.json");
    await writeFile(runtimePath, JSON.stringify({ ...base, runtime: { kind: "terse-durable-actor", version: "test" } }));
    await assert.rejects(() => loadTarget(runtimePath, {}), /unsupported runtime.kind/u);

    const urlPath = join(dir, "url.json");
    await writeFile(urlPath, JSON.stringify({ ...base, baseUrl: "not a URL" }));
    await assert.rejects(() => loadTarget(urlPath, {}), /invalid base URL/u);

    const controlPath = join(dir, "control.json");
    await writeFile(controlPath, JSON.stringify({
      ...base,
      control: { baseUrl: "http://127.0.0.1:2", tokenEnv: "CONTROL_TOKEN", operations: [] },
    }));
    await assert.rejects(() => loadTarget(controlPath, {}), /CONTROL_TOKEN is required/u);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
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

test("CI annotations expose escaped per-case results through the check-run API", () => {
  const annotation = renderCaseCheckAnnotation("Fake 100% matrix", [
    {
      id: "RF-TEST-001",
      name: "passes",
      status: "passed",
      durationMs: 1,
      requiredCapabilities: [],
      correlationIds: [],
      evidenceBasis: "adapter-attested",
    },
    {
      id: "RF-TEST-002",
      name: "skips",
      status: "skipped",
      durationMs: 1,
      requiredCapabilities: [],
      correlationIds: [],
      skipReason: "not applicable\r\nto fake",
    },
  ]);
  assert(annotation.startsWith("::notice title=Fake 100%25 matrix::"));
  assert(annotation.includes("RF-TEST-001 PASSED [adapter-attested]"));
  assert(annotation.includes("%0ARF-TEST-002 SKIPPED"));
  assert(annotation.includes("not applicable%0D%0Ato fake"));
});

test("control failures preserve non-JSON response evidence", async () => {
  const target = fakeTarget();
  target.control = {
    baseUrl: "http://control.example.test",
    token: "control-secret-token",
    operations: new Set(["reset"]),
  };
  const harness = new Harness(target, "full", "seed");
  const previousFetch = globalThis.fetch;
  globalThis.fetch = (async () => new Response("adapter unavailable", { status: 503 })) as typeof fetch;
  try {
    await assert.rejects(() => harness.control("reset"), /failed with status 503/u);
  } finally {
    globalThis.fetch = previousFetch;
  }
  assert.equal(harness.exchanges.length, 1);
  assert.equal(harness.exchanges[0]?.response.status, 503);
  assert.equal(harness.exchanges[0]?.response.body, "adapter unavailable");
});

test("response body transport failures preserve status and headers in evidence", async () => {
  const harness = new Harness(fakeTarget(), "core", "seed");
  const previousFetch = globalThis.fetch;
  globalThis.fetch = (async () => new Response(
    new ReadableStream({
      start(controller) { controller.error(new Error("body interrupted")); },
    }),
    { status: 200, headers: { "Content-Type": "application/json", "X-Test": "present" } },
  )) as typeof fetch;
  try {
    await assert.rejects(
      () => harness.request("GET", "/v1/workspaces/primary/fs/file?path=%2Ftest.md", { token: false }),
      (error: unknown) => error instanceof RequestTransportError && error.phase === "response-body",
    );
  } finally {
    globalThis.fetch = previousFetch;
  }
  assert.equal(harness.exchanges.length, 1);
  assert.equal(harness.exchanges[0]?.response.status, 200);
  assert.equal(harness.exchanges[0]?.response.headers["x-test"], "present");
  assert.deepEqual(harness.exchanges[0]?.response.body, { transportError: "Error", phase: "response-body" });
});

test("poll bounds a stalled read", async () => {
  const harness = new Harness(fakeTarget(), "core", "seed");
  await assert.rejects(
    () => harness.poll("stalled read", () => new Promise<never>(() => {}), () => false, 25),
    /did not converge within 25ms/u,
  );
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

test("JUnit evidence strips XML 1.0-forbidden control characters", async () => {
  const dir = await mkdtemp(join(tmpdir(), "relayfile-junit-test-"));
  const target = fakeTarget();
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
    counts: { passed: 0, failed: 1, skipped: 0 },
    cases: [{
      id: "RF-TEST-XML",
      name: "invalid control",
      status: "failed",
      durationMs: 1,
      requiredCapabilities: [],
      correlationIds: [],
      error: "bad\u0000message",
    }],
  };
  await writeEvidence(dir, summary, [], target);
  const junit = await readFile(join(dir, "junit.xml"), "utf8");
  assert(!junit.includes("\u0000"));
  assert(junit.includes("badmessage"));
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

test("OpenAPI validator rejects undeclared lookalike media types", async () => {
  await assert.rejects(
    () => validateOpenApiResponse(
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
      "application/jsonp",
    ),
    /OpenAPI declares/u,
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

function remoteEnvironment(options: { terse?: boolean } = {}): NodeJS.ProcessEnv {
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
    ...(options.terse ? {
      RELAYFILE_TERSE_SHARED_SECRET: "runtime-shared-secret",
      RELAYFILE_TERSE_RUNTIME_URL: "https://terse.example.test",
      RELAYFILE_TERSE_ADMIN_KEY: "terse-admin-key",
      RELAYFILE_TERSE_PROJECT_ID: "project",
      RELAYFILE_TERSE_ACTOR_NAME: "RelayfileWorkspace",
      RELAYFILE_TERSE_ACTOR_ID: "primary",
    } : {}),
  };
}
