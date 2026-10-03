import { appendFile } from "node:fs/promises";
import type { CaseResult, EvidenceSummary } from "./types.js";

export async function appendCaseStepSummary(title: string, cases: CaseResult[]): Promise<void> {
  const destination = process.env.GITHUB_STEP_SUMMARY?.trim();
  if (destination) {
    const rows = cases.map((result) => {
      const detail = result.error ?? result.skipReason ?? "";
      return `| ${cell(result.id)} | ${result.status.toUpperCase()} | ${cell(result.evidenceBasis ?? "-")} | ${cell(detail)} |`;
    });
    await appendFile(destination, [
      `## ${title}`,
      "",
      "| Case | Result | Evidence | Detail |",
      "| --- | --- | --- | --- |",
      ...rows,
      "",
    ].join("\n"));
  }
  if (process.env.GITHUB_ACTIONS === "true") console.log(renderCaseCheckAnnotation(title, cases));
}

export async function appendEvidenceStepSummary(summary: EvidenceSummary): Promise<void> {
  await appendCaseStepSummary(
    `Relayfile conformance: ${summary.target.id} (${summary.counts.passed} PASS / ${summary.counts.failed} FAIL / ${summary.counts.skipped} SKIP)`,
    summary.cases,
  );
}

export function renderCaseCheckAnnotation(title: string, cases: CaseResult[]): string {
  const results = cases.map((result) => {
    const detail = result.error ?? result.skipReason;
    const evidence = result.evidenceBasis ? ` [${result.evidenceBasis}]` : "";
    return `${result.id} ${result.status.toUpperCase()}${evidence}${detail ? ` — ${detail}` : ""}`;
  });
  return `::notice title=${workflowCommandValue(title)}::${workflowCommandValue(results.join("\n"))}`;
}

function cell(value: string): string {
  return value.replaceAll("|", "\\|").replaceAll("\n", " ");
}

function workflowCommandValue(value: string): string {
  return value.replaceAll("%", "%25").replaceAll("\r", "%0D").replaceAll("\n", "%0A");
}
