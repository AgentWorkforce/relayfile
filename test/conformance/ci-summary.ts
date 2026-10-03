import { appendFile } from "node:fs/promises";
import type { CaseResult, EvidenceSummary } from "./types.js";

export async function appendCaseStepSummary(title: string, cases: CaseResult[]): Promise<void> {
  const destination = process.env.GITHUB_STEP_SUMMARY?.trim();
  if (!destination) return;
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

export async function appendEvidenceStepSummary(summary: EvidenceSummary): Promise<void> {
  await appendCaseStepSummary(
    `Relayfile conformance: ${summary.target.id} (${summary.counts.passed} PASS / ${summary.counts.failed} FAIL / ${summary.counts.skipped} SKIP)`,
    summary.cases,
  );
}

function cell(value: string): string {
  return value.replaceAll("|", "\\|").replaceAll("\n", " ");
}
