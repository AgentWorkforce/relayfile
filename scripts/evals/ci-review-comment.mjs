#!/usr/bin/env node

import { appendFile } from "node:fs/promises";
import { existsSync, readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const RUNS_DIR = path.join(ROOT, ".relayfile", "evals", "runs");
const MARKER = "<!-- relayfile-eval-human-review -->";
const MAX_COMMENT_CHARS = 60000;
const MAX_OUTPUT_CHARS = 1200;

const runDir = findLatestRunDir();
if (!runDir) {
  console.log("No Relayfile eval run found; skipping PR comment.");
  process.exit(0);
}

const result = readResultJson(path.join(runDir, "result.json"));
const comment = renderComment({ result, runDir });

if (process.env.GITHUB_STEP_SUMMARY) {
  await appendFile(process.env.GITHUB_STEP_SUMMARY, [
    "",
    "## Relayfile Eval Review Comment",
    "",
    "A detailed human-review comment was generated for this PR.",
    "",
  ].join("\n"));
}

if (process.env.GITHUB_TOKEN && process.env.GITHUB_REPOSITORY && process.env.PR_NUMBER) {
  await upsertPullRequestComment(comment);
} else {
  console.log(comment);
}

function renderComment({ result, runDir }) {
  const failed = result.tests.filter((test) => test.status === "failed");
  const skipped = result.tests.filter((test) => test.status === "skipped");
  const needsHuman = result.tests.filter((test) => test.status === "needs-human");
  const reviewableNeedsHuman = needsHuman.filter(hasCapturedOutput);
  const missingOutputNeedsHuman = needsHuman.filter((test) => !hasCapturedOutput(test));
  const lines = [
    MARKER,
    "# Relayfile Eval Review",
    "",
    `Run: \`${path.relative(ROOT, runDir)}\``,
    `Mode: \`${result.mode}\``,
    `Git SHA: \`${result.git_sha}\``,
    "",
    `**Passed:** ${result.passed} | **Needs human:** ${result.needs_human} | **Reviewable:** ${reviewableNeedsHuman.length} | **Missing output:** ${missingOutputNeedsHuman.length} | **Failed:** ${result.failed} | **Skipped:** ${result.skipped}`,
    "",
  ];

  if (failed.length > 0 || skipped.length > 0) {
    lines.push("## Blocking Cases", "");
    for (const test of [...failed, ...skipped]) {
      appendCaseDetails(lines, test, { forceOpen: true });
    }
  }

  if (reviewableNeedsHuman.length > 0) {
    lines.push(
      "## Human Review Cases",
      "",
      "These cases passed deterministic checks and include captured Relayfile output for a human verdict against their `Must` / `Must Not` rubric.",
      "",
    );
    for (const test of reviewableNeedsHuman) {
      appendCaseDetails(lines, test, { forceOpen: false });
    }
  } else {
    lines.push("## Human Review Cases", "", "No reviewable human-review cases captured Relayfile output.", "");
  }

  if (missingOutputNeedsHuman.length > 0) {
    lines.push(
      "## Cases Missing Relayfile Output",
      "",
      "These cases are not expanded because there is no candidate Relayfile response to judge. Change them to `Executor: relayfile`, or provide `Candidate Output`, before treating them as human-review evidence.",
      "",
    );
    for (const test of missingOutputNeedsHuman) {
      lines.push(`- \`${test.id}\` (${test.suite}/${test.executor})`);
    }
    lines.push("");
  }

  const body = `${lines.join("\n")}\n`;
  if (body.length <= MAX_COMMENT_CHARS) return body;
  return `${body.slice(0, MAX_COMMENT_CHARS - 2000)}\n\n---\n\n_Comment truncated to stay within GitHub limits. Download the \`relayfile-eval-run\` artifact for the full \`human-review.md\`._\n`;
}

function appendCaseDetails(lines, test, { forceOpen }) {
  const summaryStatus = test.status === "failed" ? "FAIL" : test.status === "skipped" ? "SKIP" : "REVIEW";
  lines.push(`<details${forceOpen ? " open" : ""}>`);
  lines.push(`<summary><strong>${summaryStatus}</strong> <code>${escapeHtml(test.id)}</code> (${escapeHtml(test.suite)}/${escapeHtml(test.executor)})</summary>`);
  lines.push("");

  if (test.input?.message) {
    lines.push("**User message**");
    lines.push("");
    lines.push(blockquote(String(test.input.message)));
    lines.push("");
  }

  appendRelayfileOutput(lines, test);
  appendRubricList(lines, "Must", test.expected?.must);
  appendRubricList(lines, "Must Not", test.expected?.mustNot);

  const deterministicChecks = (test.checks ?? []).filter((check) => !String(check.name).startsWith("human:"));
  if (deterministicChecks.length > 0) {
    lines.push("**Deterministic checks**", "");
    for (const check of deterministicChecks) {
      lines.push(`- ${check.passed ? "PASS" : "FAIL"} \`${check.name}\`: ${check.message ?? ""}`);
    }
    lines.push("");
  }

  if (test.error) {
    lines.push("**Error**", "");
    lines.push("```text");
    lines.push(String(test.error).slice(0, MAX_OUTPUT_CHARS));
    lines.push("```", "");
  }

  lines.push("</details>", "");
}

function appendRelayfileOutput(lines, test) {
  const actualContent = getCapturedOutput(test).trim();
  lines.push("**Relayfile output**", "");
  if (actualContent.length > 0) {
    const preview = actualContent.length > MAX_OUTPUT_CHARS
      ? `${actualContent.slice(0, MAX_OUTPUT_CHARS)}\n...[truncated]`
      : actualContent;
    lines.push("```text");
    lines.push(preview);
    lines.push("```", "");
  } else {
    lines.push(`_No Relayfile output captured for this case. Executor: \`${test.executor}\`._`, "");
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

function appendRubricList(lines, title, items) {
  if (!Array.isArray(items) || items.length === 0) return;
  lines.push(`**${title}**`, "");
  for (const item of items) {
    lines.push(`- ${String(item)}`);
  }
  lines.push("");
}

function blockquote(text) {
  return text.split("\n").map((line) => `> ${line}`).join("\n");
}

function escapeHtml(value) {
  return String(value)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

function findLatestRunDir() {
  if (!existsSync(RUNS_DIR)) return null;
  const runs = readdirSync(RUNS_DIR)
    .map((dir) => path.join(RUNS_DIR, dir))
    .filter((dir) => existsSync(path.join(dir, "result.json")))
    .flatMap((dir) => {
      const result = safeReadResultJson(path.join(dir, "result.json"));
      return result ? [{ dir, result }] : [];
    })
    .sort((a, b) => String(b.result.timestamp).localeCompare(String(a.result.timestamp)));
  return runs[0]?.dir ?? null;
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

async function upsertPullRequestComment(body) {
  const [owner, repo] = process.env.GITHUB_REPOSITORY.split("/");
  const prNumber = process.env.PR_NUMBER;
  const headers = {
    "accept": "application/vnd.github+json",
    "authorization": `Bearer ${process.env.GITHUB_TOKEN}`,
    "content-type": "application/json",
    "x-github-api-version": "2022-11-28",
  };
  const commentsUrl = `https://api.github.com/repos/${owner}/${repo}/issues/${prNumber}/comments`;
  const existing = await findExistingReviewComment(commentsUrl, headers);

  const method = existing?.url ? "PATCH" : "POST";
  const url = existing?.url ?? commentsUrl;
  const response = await globalThis.fetch(url, {
    method,
    headers,
    body: JSON.stringify({ body }),
  });
  if (!response.ok) {
    throw new Error(`Failed to ${method === "PATCH" ? "update" : "create"} PR comment: ${response.status} ${await response.text()}`);
  }
  console.log(`${method === "PATCH" ? "Updated" : "Created"} Relayfile eval review comment.`);
}

async function findExistingReviewComment(commentsUrl, headers) {
  let url = `${commentsUrl}?per_page=100`;
  while (url) {
    const response = await globalThis.fetch(url, { headers });
    if (!response.ok) {
      throw new Error(`Failed to list PR comments: ${response.status} ${await response.text()}`);
    }
    const comments = await response.json();
    if (Array.isArray(comments)) {
      const existing = comments.find((comment) => typeof comment.body === "string" && comment.body.includes(MARKER));
      if (existing) return existing;
    }
    url = getNextLink(response.headers.get("link"));
  }
  return undefined;
}

function getNextLink(linkHeader) {
  if (!linkHeader) return null;
  for (const part of linkHeader.split(",")) {
    const match = /<([^>]+)>;\s*rel="next"/.exec(part.trim());
    if (match) return match[1];
  }
  return null;
}
