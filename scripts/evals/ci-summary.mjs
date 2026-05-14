#!/usr/bin/env node

import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const RUNS_DIR = path.join(ROOT, ".relayfile", "evals", "runs");

const runDir = findLatestRunDir();
if (!runDir) {
  const summary = "# Relayfile Eval CI Summary\n\nNo Relayfile eval run found; provider evals may have been skipped for an untrusted fork PR.\n";
  console.log(summary);
  if (process.env.GITHUB_STEP_SUMMARY) {
    writeFileSync(process.env.GITHUB_STEP_SUMMARY, summary, { flag: "a" });
  }
  process.exit(0);
}

const resultPath = path.join(runDir, "result.json");
const reviewPath = path.join(runDir, "human-review.md");
const result = readResultJson(resultPath);

const failed = result.tests.filter((test) => test.status === "failed");
const skipped = result.tests.filter((test) => test.status === "skipped");
const needsHuman = result.tests.filter((test) => test.status === "needs-human");
const reviewableNeedsHuman = needsHuman.filter(hasCapturedOutput);
const missingOutputNeedsHuman = needsHuman.filter((test) => !hasCapturedOutput(test));

const lines = [
  "# Relayfile Eval CI Summary",
  "",
  `- Run directory: \`${path.relative(ROOT, runDir)}\``,
  `- Mode: \`${result.mode}\``,
  `- Git SHA: \`${result.git_sha}\``,
  `- Passed: ${result.passed}`,
  `- Needs human review: ${result.needs_human}`,
  `- Reviewable human cases: ${reviewableNeedsHuman.length}`,
  `- Human cases missing Relayfile output: ${missingOutputNeedsHuman.length}`,
  `- Failed: ${result.failed}`,
  `- Skipped: ${result.skipped}`,
  "",
];

appendStatusSection(lines, "Failed", failed);
appendStatusSection(lines, "Skipped", skipped);
appendHumanReviewSection(lines, reviewableNeedsHuman, missingOutputNeedsHuman);

const summary = `${lines.join("\n")}\n`;
console.log(summary);

if (process.env.GITHUB_STEP_SUMMARY) {
  writeFileSync(process.env.GITHUB_STEP_SUMMARY, summary, { flag: "a" });
}

if (failed.length > 0 || skipped.length > 0 || missingOutputNeedsHuman.length > 0) {
  process.exitCode = 1;
}

function appendStatusSection(lines, title, tests) {
  if (tests.length === 0) return;
  lines.push(`## ${title}`, "");
  for (const test of tests) {
    lines.push(`- \`${test.id}\` (${test.suite}/${test.executor})`);
    if (test.error) lines.push(`  - ${test.error}`);
    for (const check of test.checks ?? []) {
      if (check.passed) continue;
      lines.push(`  - FAIL ${check.name}: ${check.message}`);
    }
  }
  lines.push("");
}

function appendHumanReviewSection(lines, reviewableTests, missingOutputTests) {
  if (reviewableTests.length === 0 && missingOutputTests.length === 0) {
    lines.push("## Human Review", "", "No cases require human review.", "");
    return;
  }

  lines.push(
    "## Human Review",
    "",
    `These ${reviewableTests.length} cases passed deterministic checks and include captured Relayfile output for human review.`,
    "",
    `Review worksheet: \`${path.relative(ROOT, reviewPath)}\``,
    "",
  );

  const bySuite = new Map();
  for (const test of reviewableTests) {
    const suite = test.suite ?? "unknown";
    if (!bySuite.has(suite)) bySuite.set(suite, []);
    bySuite.get(suite).push(test);
  }

  for (const [suite, suiteTests] of [...bySuite.entries()].sort(([a], [b]) => a.localeCompare(b))) {
    lines.push(`### ${suite}`, "");
    for (const test of suiteTests) {
      lines.push(`- \`${test.id}\``);
    }
    lines.push("");
  }

  if (missingOutputTests.length > 0) {
    lines.push(
      "### Missing Relayfile Output",
      "",
      "These cases cannot be reviewed because no candidate Relayfile response was captured. Use `Executor: relayfile` or provide `Candidate Output`.",
      "",
    );
    for (const test of missingOutputTests) {
      lines.push(`- \`${test.id}\` (${test.suite}/${test.executor})`);
    }
    lines.push("");
  }
}

function hasCapturedOutput(test) {
  return getCapturedOutput(test).trim().length > 0;
}

function getCapturedOutput(test) {
  return String(
    test.actual?.content
      ?? test.candidate_output
      ?? test.candidateOutput
      ?? test.candidate?.content
      ?? "",
  );
}

function findLatestRunDir() {
  if (!existsSync(RUNS_DIR)) {
    return null;
  }
  const runs = readdirSync(RUNS_DIR)
    .map((dir) => path.join(RUNS_DIR, dir))
    .filter((dir) => existsSync(path.join(dir, "result.json")))
    .flatMap((dir) => {
      const result = safeReadResultJson(path.join(dir, "result.json"));
      return result ? [{ dir, result }] : [];
    })
    .sort((a, b) => String(b.result.timestamp).localeCompare(String(a.result.timestamp)));

  if (runs.length === 0) {
    return null;
  }

  return runs[0].dir;
}

function readResultJson(filePath) {
  const result = safeReadResultJson(filePath);
  if (!result) {
    throw new Error(`Could not parse Relayfile eval result: ${path.relative(ROOT, filePath)}`);
  }
  return result;
}

function safeReadResultJson(filePath) {
  try {
    return JSON.parse(readFileSync(filePath, "utf8"));
  } catch (error) {
    console.warn(`Skipping malformed Relayfile eval result ${path.relative(ROOT, filePath)}: ${error instanceof Error ? error.message : String(error)}`);
    return null;
  }
}
