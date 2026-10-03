#!/usr/bin/env npx tsx
import { resolve } from "node:path";
import { appendCaseStepSummary } from "./ci-summary.js";
import { writeEvidence } from "./evidence.js";
import { FakeConformanceAdapter } from "./fake-adapter.js";
import { Harness } from "./harness.js";
import { ADVANCED_CASE_IDS, runScenarios, type AdvancedCaseId } from "./scenarios.js";
import type { CaseResult } from "./types.js";

interface MutantResult {
  id: AdvancedCaseId;
  detected: boolean;
  error: string;
}

async function main(): Promise<void> {
  process.env.E2E_TELEMETRY_DISABLED = "1";
  const conforming = await runFake(undefined, new Set(ADVANCED_CASE_IDS), "fake-conforming");
  const conformingFailures = conforming.harness.cases.filter((result) => result.status !== "passed");
  if (conformingFailures.length > 0 || conforming.harness.cases.length !== ADVANCED_CASE_IDS.length) {
    throw new Error(
      `conforming fake did not pass 16/16: ${conforming.harness.cases.map((result) => `${result.id}=${result.status}:${result.error ?? result.skipReason ?? ""}`).join(", ")}`,
    );
  }
  const summary = conforming.harness.summary();
  await writeEvidence(resolve("artifacts/conformance/fake-conforming/fake-matrix"), summary, conforming.harness.exchanges, conforming.target);
  await appendCaseStepSummary("Fake adapter: conforming implementation (16/16 expected PASS)", conforming.harness.cases);

  const mutants: MutantResult[] = [];
  for (const id of ADVANCED_CASE_IDS) {
    const run = await runFake(id, new Set([id]), `fake-mutant-${id.toLowerCase()}`);
    const result = run.harness.cases.find((candidate) => candidate.id === id);
    const detected = result?.status === "failed";
    mutants.push({ id, detected, error: result?.error ?? result?.skipReason ?? "case was not registered" });
  }
  await appendCaseStepSummary(
    "Fake adapter mutation matrix (each mutant expected DETECTED)",
    mutants.map((result): CaseResult => ({
      id: result.id,
      name: `${result.id} deliberately broken adapter`,
      status: result.detected ? "passed" : "failed",
      durationMs: 0,
      requiredCapabilities: [],
      correlationIds: [],
      ...(result.detected ? { evidenceBasis: "adapter-attested" } : { error: result.error }),
    })),
  );
  for (const result of mutants) {
    console.log(`${result.detected ? "DETECTED" : "MISSED"} ${result.id}${result.error ? ` — ${result.error}` : ""}`);
  }
  const missed = mutants.filter((result) => !result.detected);
  if (missed.length > 0) throw new Error(`fake adapter mutants escaped detection: ${missed.map((item) => item.id).join(", ")}`);
  console.log(`Fake adapter matrix: ${conforming.harness.cases.length}/16 conforming PASS; ${mutants.length}/16 mutants DETECTED`);
}

async function runFake(
  mutant: AdvancedCaseId | undefined,
  includeCaseIds: ReadonlySet<string>,
  seed: string,
): Promise<{ harness: Harness; target: Awaited<ReturnType<FakeConformanceAdapter["start"]>>["target"] }> {
  const adapter = new FakeConformanceAdapter(mutant);
  const running = await adapter.start();
  const harness = new Harness(running.target, "full", seed, undefined, {
    includeCaseIds,
    maxPollTimeoutMs: mutant ? 3_000 : 10_000,
  });
  try {
    await runScenarios(harness);
    return { harness, target: running.target };
  } finally {
    await running.close();
  }
}

main().catch((error) => {
  console.error(error instanceof Error ? error.stack ?? error.message : String(error));
  process.exitCode = 1;
});
