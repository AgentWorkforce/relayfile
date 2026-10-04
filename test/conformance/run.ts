#!/usr/bin/env npx tsx
import { resolve } from "node:path";
import { assertFullRunSafety, loadTarget } from "./config.js";
import { appendEvidenceStepSummary } from "./ci-summary.js";
import { redact, redactEnvironmentText, writeEvidence } from "./evidence.js";
import { Harness } from "./harness.js";
import { LocalGoTarget } from "./local-go.js";
import { runScenarios } from "./scenarios.js";
import type { Profile } from "./types.js";

interface Options {
  target: string;
  profile: Profile;
  seed?: string;
  outDir?: string;
}

async function main(): Promise<void> {
  process.env.E2E_TELEMETRY_DISABLED = "1";
  const options = parseOptions(process.argv.slice(2));
  const loaded = await loadTarget(options.target);
  const seed = options.seed ?? defaultSeed(loaded.file.auth.strategy === "local-rs256");
  let target = loaded.target;
  const local = loaded.file.auth.strategy === "local-rs256" ? new LocalGoTarget() : undefined;

  try {
    if (local) target = await local.start(target);
    assertFullRunSafety(target, options.profile, process.env, local !== undefined);
    const harness = new Harness(
      target,
      options.profile,
      seed,
      local ? () => local.restart(target.baseUrl) : undefined,
    );
    if (options.profile === "full" && target.control?.operations.has("reset")) {
      await harness.case("RF-SETUP-001", "controlled target reset succeeds", [], async () => {
        await harness.control("reset");
      });
    }
    if (!harness.cases.some((result) => result.id === "RF-SETUP-001" && result.status === "failed")) {
      await runScenarios(harness);
    }
    const summary = harness.summary();
    const outDir = resolve(options.outDir ?? `artifacts/conformance/${target.id}/${seed}`);
    await writeEvidence(outDir, summary, harness.exchanges, target);
    await appendEvidenceStepSummary(redact(summary, target));
    printSummary(redact(summary, target), outDir);
    if (summary.counts.failed > 0) process.exitCode = 1;
  } finally {
    await local?.close();
  }
}

function parseOptions(args: string[]): Options {
  if (args.includes("--help") || args.includes("-h")) {
    console.log(`Relayfile backend-neutral conformance harness

Usage:
  npx tsx test/conformance/run.ts --target go-local [--profile core] [--seed rfce-v1]
  npx tsx test/conformance/run.ts --target cloudflare-hosted --profile full --seed "$CI_RUN_ID"
  npx tsx test/conformance/run.ts --target terse --profile full --seed "$CI_RUN_ID"

Options:
  --target <name|path>   Target descriptor under test/conformance/targets or a JSON path
  --profile core|full   core permits explicit capability skips; full turns every skip into failure
  --seed <value>        Deterministic namespace seed; defaults to rfce-v1 locally and a run-unique remote seed
  --out-dir <path>      Evidence directory (default artifacts/conformance/<target>/<seed>)
`);
    process.exit(0);
  }

  validateArguments(args);

  const target = option(args, "--target") ?? "go-local";
  const profile = (option(args, "--profile") ?? "core") as Profile;
  if (profile !== "core" && profile !== "full") throw new Error(`invalid --profile ${profile}`);
  const seed = option(args, "--seed") ?? process.env.RELAYFILE_CONFORMANCE_SEED;
  if (seed) validateSeed(seed);
  return { target, profile, ...(seed ? { seed } : {}), ...(option(args, "--out-dir") ? { outDir: option(args, "--out-dir") } : {}) };
}

function defaultSeed(local: boolean): string {
  if (local) return "rfce-v1";
  const runId = process.env.GITHUB_RUN_ID?.trim();
  const attempt = process.env.GITHUB_RUN_ATTEMPT?.trim() || "1";
  const seed = runId ? `rfce-${runId}-${attempt}` : `rfce-${Date.now()}-${process.pid}`;
  validateSeed(seed);
  return seed;
}

function validateSeed(seed: string): void {
  if (!/^[A-Za-z0-9._-]{1,80}$/u.test(seed) || seed === "." || seed === "..") {
    throw new Error("--seed must match [A-Za-z0-9._-]{1,80} and cannot be . or ..");
  }
}

function validateArguments(args: string[]): void {
  const allowed = new Set(["--target", "--profile", "--seed", "--out-dir"]);
  for (let index = 0; index < args.length; index++) {
    const arg = args[index]!;
    if (!arg.startsWith("--")) throw new Error(`unexpected positional argument ${arg}`);
    const [name, inline] = arg.split("=", 2);
    if (!allowed.has(name!)) throw new Error(`unknown option ${name}`);
    if (inline !== undefined) {
      if (!inline) throw new Error(`${name} requires a value`);
      continue;
    }
    const value = args[++index];
    if (!value || value.startsWith("--")) throw new Error(`${name} requires a value`);
  }
}

function option(args: string[], name: string): string | undefined {
  const inline = args.find((arg) => arg.startsWith(`${name}=`));
  if (inline) return inline.slice(name.length + 1);
  const index = args.indexOf(name);
  if (index < 0) return undefined;
  const value = args[index + 1];
  if (!value || value.startsWith("--")) throw new Error(`${name} requires a value`);
  return value;
}

function printSummary(summary: ReturnType<Harness["summary"]>, outDir: string): void {
  for (const result of summary.cases) {
    const marker = result.status === "passed" ? "PASS" : result.status === "failed" ? "FAIL" : "SKIP";
    const detail = result.error ?? result.skipReason;
    console.log(`${marker} ${result.id} ${result.name}${detail ? ` — ${detail}` : ""}`);
  }
  console.log(
    `Conformance ${summary.target.id}: ${summary.counts.passed} passed, ${summary.counts.failed} failed, ${summary.counts.skipped} skipped`,
  );
  console.log(`Evidence: ${outDir}`);
}

main().catch((error) => {
  console.error(redactEnvironmentText(error instanceof Error ? error.message : String(error)));
  process.exitCode = 2;
});
