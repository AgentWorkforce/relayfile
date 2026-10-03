#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

import {
  assertHumanEvalExpected,
  createDefaultHumanEvalExecutors,
  createHumanEvalRunRecord,
  createSkippedEvalError,
  defaultRedactActual,
  humanEvalNeedsReview,
  loadDotenv,
  loadHumanEvalCasesFromSuitesDir,
  matchesHumanEvalFilters,
  printHumanEvalRunSummary,
  validateHumanEvalCase,
  writeHumanEvalRunArtifacts,
} from "@agent-assistant/telemetry/evals";

import { assertRelayfileExpected } from "./relayfile-checks.mjs";
import { createRelayfileExecutor } from "./relayfile-executor.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const SUITES_DIR = path.join(ROOT, "evals", "suites");
const RUNS_DIR = process.env.RELAYFILE_EVAL_RUNS_DIR
  ? path.resolve(ROOT, process.env.RELAYFILE_EVAL_RUNS_DIR)
  : path.join(ROOT, ".relayfile", "evals", "runs");
const DEFAULT_OPENROUTER_MODEL = "openai/gpt-oss-120b:free";
const OPENROUTER_CHAT_COMPLETIONS_ENDPOINT = "https://openrouter.ai/api/v1/chat/completions";

loadDotenv(path.join(ROOT, ".env"));

const args = parseArgs(process.argv.slice(2));
const providerMode = args.provider || args.mode === "provider" || process.env.RELAYFILE_EVAL_PROVIDER === "1" || process.env.HUMAN_EVAL_PROVIDER === "1";
const mode = args.mode ?? (providerMode ? "provider" : "offline");
const allCases = loadHumanEvalCasesFromSuitesDir(SUITES_DIR, { rootDir: ROOT });
const selectedCases = allCases.filter((testCase) => matchesHumanEvalFilters(testCase, {
  suite: args.suite,
  caseId: args.caseId,
  tags: args.tags,
}));

if (args.list) {
  listCases(selectedCases);
  process.exit(0);
}

if (selectedCases.length === 0) {
  console.log("No eval cases selected.");
  console.log("Add human-authored markdown cases under evals/suites/*/cases.md and run npm run evals:compile.");
  console.log("Try: npm run evals:list");
  process.exit(0);
}

const run = createRunRecord({ selectedCases, mode });
const defaultExecutors = createDefaultHumanEvalExecutors(ROOT);
const relayfileExecutor = createRelayfileExecutor({
  workspace: process.env.RELAYFILE_WORKSPACE,
  mountPath: process.env.RELAYFILE_MOUNT,
  token: process.env.RELAYFILE_TOKEN,
});
const executors = {
  ...defaultExecutors,
  relayfile: relayfileExecutor,
};

for (const testCase of selectedCases) {
  const trials = readPositiveInt(args.trials ?? testCase.trials, 1);
  for (let trialIndex = 0; trialIndex < trials; trialIndex += 1) {
    const startedAt = Date.now();
    const executorName = args.executor ?? testCase.executor ?? "relayfile";
    const trial = {
      id: testCase.id,
      suite: testCase.suite,
      kind: testCase.kind ?? "capability",
      executor: executorName,
      trial: trialIndex + 1,
      tags: testCase.tags ?? [],
      status: "failed",
      duration_ms: 0,
      checks: [],
      input: testCase.input,
      expected: testCase.expected,
    };

    try {
      validateHumanEvalCase(testCase);
      const executor = executors[executorName];
      if (!args.reviewOnly && executor === undefined) {
        throw new Error(`Unknown executor "${executorName}" for ${testCase.id}`);
      }

      const actual = args.reviewOnly
        ? { status: "manual_review_required", content: "", toolCalls: [] }
        : await executor(testCase, { providerMode, rootDir: ROOT });
      const checks = args.reviewOnly
        ? []
        : [...assertHumanEvalExpected(testCase, actual), ...assertRelayfileExpected(testCase, actual)];
      const deterministicPassed = args.reviewOnly || checks.every((check) => check.passed);
      let needsHuman = humanEvalNeedsReview(testCase);
      if (deterministicPassed && needsHuman && providerMode) {
        checks.push(await reviewWithOpenRouter(testCase, actual, checks));
        needsHuman = !checks.every((check) => check.passed);
      }

      run.tests.push({
        ...trial,
        status: deterministicPassed ? (needsHuman ? "needs-human" : "passed") : "failed",
        actual: redactRelayfileActual(actual),
        checks,
        duration_ms: Date.now() - startedAt,
      });
    } catch (error) {
      run.tests.push({
        ...trial,
        status: isSkippedError(error) ? "skipped" : "failed",
        error: error instanceof Error ? error.message : String(error),
        duration_ms: Date.now() - startedAt,
      });
    } finally {
      writeHumanEvalRunArtifacts(run);
    }
  }
}

writeHumanEvalRunArtifacts(run, { final: true });
printHumanEvalRunSummary(run, { productName: "Relayfile Evals", rootDir: ROOT });
process.exitCode = run.tests.some((test) => (
  test.status === "failed" || (shouldFailOnSkipped(args) && test.status === "skipped")
)) ? 1 : 0;

function parseArgs(argv) {
  const parsed = { tags: new Set() };
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === "--provider") parsed.provider = true;
    else if (arg === "--review-only") parsed.reviewOnly = true;
    else if (arg === "--list") parsed.list = true;
    else if (arg === "--suite") parsed.suite = readOptionValue(argv, ++index, "--suite");
    else if (arg === "--case") parsed.caseId = readOptionValue(argv, ++index, "--case");
    else if (arg === "--executor") parsed.executor = readOptionValue(argv, ++index, "--executor");
    else if (arg === "--tag") parsed.tags.add(readOptionValue(argv, ++index, "--tag"));
    else if (arg === "--trials") parsed.trials = Number(readOptionValue(argv, ++index, "--trials"));
    else if (arg === "--mode") parsed.mode = readOptionValue(argv, ++index, "--mode");
    else if (arg === "--fail-on-skipped") parsed.failOnSkipped = true;
    else if (arg === "--help" || arg === "-h") {
      printHelp();
      process.exit(0);
    } else {
      throw new Error(`Unknown argument: ${arg}`);
    }
  }
  return parsed;
}

function readOptionValue(argv, index, option) {
  const value = argv[index];
  if (value === undefined || value.startsWith("--")) throw new Error(`${option} requires a value`);
  return value;
}

function printHelp() {
  console.log(`Usage: node scripts/evals/run-relayfile-evals.mjs [options]

Options:
  --list             List selected cases without running them.
  --suite NAME       Run one suite.
  --case ID          Run one case id.
  --tag TAG          Require a tag. Can be repeated.
  --trials N         Override trial count for every case.
  --executor NAME    Override selected cases to run with this executor.
  --mode MODE        Run mode label, usually offline or provider.
  --provider         Alias for --mode provider; also reviews human cases with OpenRouter.
  --fail-on-skipped  Treat skipped cases as a non-zero exit condition.
  --review-only      Do not execute cases; create human review worksheets.
`);
}

async function reviewWithOpenRouter(testCase, actual, checks) {
  const apiKey = process.env.OPENROUTER_API_KEY;
  if (!apiKey) {
    throw createSkippedEvalError("openrouter review skipped; OPENROUTER_API_KEY is missing");
  }

  const model = process.env.RELAYFILE_EVAL_OPENROUTER_MODEL
    ?? process.env.HUMAN_EVAL_OPENROUTER_MODEL
    ?? DEFAULT_OPENROUTER_MODEL;
  const timeoutMs = readPositiveInt(process.env.RELAYFILE_EVAL_OPENROUTER_TIMEOUT_MS, 120_000);
  const maxTokens = readPositiveInt(process.env.RELAYFILE_EVAL_OPENROUTER_MAX_TOKENS, 700);
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);

  try {
    const response = await fetch(OPENROUTER_CHAT_COMPLETIONS_ENDPOINT, {
      method: "POST",
      signal: controller.signal,
      headers: {
        authorization: `Bearer ${apiKey}`,
        "content-type": "application/json",
        "http-referer": process.env.GITHUB_SERVER_URL
          ? `${process.env.GITHUB_SERVER_URL}/${process.env.GITHUB_REPOSITORY ?? ""}`
          : "https://github.com/AgentWorkforce/relayfile",
        "x-title": "Relayfile Evals",
      },
      body: JSON.stringify({
        model,
        temperature: 0,
        max_tokens: maxTokens,
        messages: [
          {
            role: "system",
            content: [
              "You are a strict evaluator for relayfile human-review evals.",
              "Return only JSON with keys pass:boolean and reason:string.",
              "Mark pass true only when the actual output satisfies every Must and violates no Must Not.",
            ].join(" "),
          },
          {
            role: "user",
            content: buildReviewPrompt(testCase, actual, checks),
          },
        ],
      }),
    });

    const payload = await response.json().catch(() => ({}));
    if (!response.ok) {
      const detail = typeof payload?.error?.message === "string" ? payload.error.message : JSON.stringify(payload);
      throw new Error(`OpenRouter review failed: ${response.status} ${detail}`);
    }

    const content = contentFromOpenRouterChoice(payload?.choices?.[0]);
    const verdict = parseOpenRouterVerdict(content);
    return {
      name: "openrouterReview",
      passed: verdict.pass === true,
      message: `model=${model}; ${verdict.reason || "no reason returned"}`,
    };
  } catch (error) {
    if (error instanceof Error && error.name === "AbortError") {
      throw new Error(`OpenRouter review timed out after ${timeoutMs}ms`);
    }
    throw error;
  } finally {
    clearTimeout(timeout);
  }
}

function buildReviewPrompt(testCase, actual, checks) {
  return JSON.stringify({
    id: testCase.id,
    suite: testCase.suite,
    message: testCase.input?.message,
    must: testCase.expected?.must ?? [],
    mustNot: testCase.expected?.mustNot ?? [],
    deterministicChecks: checks.map((check) => ({
      name: check.name,
      passed: check.passed,
      message: check.message,
    })),
    actual: redactRelayfileActual(actual),
  }, null, 2);
}

function contentFromOpenRouterChoice(choice) {
  const message = choice?.message;
  const direct = typeof message?.content === "string" ? message.content.trim() : "";
  if (direct) return direct;

  const contentParts = Array.isArray(message?.content) ? message.content : [];
  return contentParts
    .map((part) => {
      if (typeof part === "string") return part;
      if (typeof part?.text === "string") return part.text;
      if (typeof part?.content === "string") return part.content;
      return "";
    })
    .join("\n")
    .trim();
}

function parseOpenRouterVerdict(content) {
  const trimmed = String(content ?? "").trim();
  const unfenced = trimmed.replace(/^```(?:json)?\s*/i, "").replace(/\s*```$/i, "").trim();
  try {
    const parsed = JSON.parse(unfenced);
    return {
      pass: parsed?.pass === true,
      reason: typeof parsed?.reason === "string" ? parsed.reason : "",
    };
  } catch {
    return {
      pass: false,
      reason: `OpenRouter did not return JSON verdict: ${trimmed.slice(0, 300)}`,
    };
  }
}

function listCases(cases) {
  if (cases.length === 0) {
    console.log("No eval cases found.");
    return;
  }
  for (const testCase of cases) {
    const tags = Array.isArray(testCase.tags) && testCase.tags.length > 0 ? ` [${testCase.tags.join(",")}]` : "";
    console.log(`${testCase.id} (${testCase.suite}/${testCase.executor ?? "relayfile"})${tags}`);
  }
}

function createRunRecord({ selectedCases, mode }) {
  const timestampForName = new Date().toISOString().replace(/[:.]/g, "-");
  const git = getGitInfo(ROOT);
  const runName = `${timestampForName}-${sanitize(git.branch)}-${sanitize(mode)}`;
  return createHumanEvalRunRecord({
    timestamp: new Date().toISOString(),
    branch: git.branch,
    gitSha: git.sha,
    mode,
    selectedCaseCount: selectedCases.length,
    runDir: path.join(RUNS_DIR, runName),
  });
}

function getGitInfo(rootDir) {
  const branch = runGitInfoCommand(rootDir, ["rev-parse", "--abbrev-ref", "HEAD"], "branch");
  const sha = runGitInfoCommand(rootDir, ["rev-parse", "--short", "HEAD"], "sha");
  return { branch, sha };
}

function runGitInfoCommand(rootDir, args, label) {
  const result = spawnSync("git", args, { cwd: rootDir, encoding: "utf8" });
  const value = result.stdout?.trim();
  if (result.status !== 0 || !value) {
    const detail = result.error?.message || result.stderr?.trim() || `status=${result.status ?? "unknown"}`;
    console.warn(`getGitInfo: failed to read ${label} with git ${args.join(" ")}: ${detail}`);
    return "unknown";
  }
  return value;
}

function shouldFailOnSkipped(args) {
  return args.failOnSkipped || process.env.RELAYFILE_EVAL_FAIL_ON_SKIPPED === "1" || process.env.HUMAN_EVAL_FAIL_ON_SKIPPED === "1";
}

function readPositiveInt(raw, fallback) {
  const value = Number(raw ?? fallback);
  return Number.isInteger(value) && value > 0 ? value : fallback;
}

function sanitize(value) {
  return String(value).replace(/[^a-zA-Z0-9_.-]+/g, "-").slice(0, 80);
}

function redactRelayfileActual(actual) {
  const redacted = defaultRedactActual(actual);
  if (!actual || typeof actual !== "object") return redacted;
  return {
    ...redacted,
    sideEffects: actual.sideEffects,
    filesModified: actual.filesModified,
    writebackQueue: actual.writebackQueue,
    metadata: actual.metadata,
    mountSnapshot: actual.mountSnapshot,
    metrics: actual.metrics,
  };
}

function isSkippedError(error) {
  return Boolean(error && typeof error === "object" && "code" in error && (
    error.code === "HUMAN_EVAL_SKIPPED" || error.code === "SAGE_EVAL_SKIPPED"
  ));
}
