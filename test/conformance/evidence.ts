import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";
import type { EvidenceSummary, Exchange, ResolvedTarget } from "./types.js";

const SECRET_KEYS = /authorization|token|secret|api[-_]?key|workspace[-_]?key|cookie/iu;
const SECRET_PREFIXES = ["rk_live_", "Bearer "];

export function redact<T>(value: T, target?: ResolvedTarget): T {
  const secrets = new Set<string>();
  if (target) {
    for (const token of Object.values(target.tokens)) if (token) secrets.add(token);
    if (target.control?.token) secrets.add(target.control.token);
    if (target.runtimeVerification?.adminKey) secrets.add(target.runtimeVerification.adminKey);
  }
  return redactValue(value, secrets) as T;
}

export function redactEnvironmentText(value: string, env: NodeJS.ProcessEnv = process.env): string {
  let result = value;
  for (const [key, secret] of Object.entries(env)) {
    if (secret && secret.length >= 8 && SECRET_KEYS.test(key)) {
      result = result.split(secret).join("[REDACTED]");
    }
  }
  return result;
}

function redactValue(value: unknown, secrets: Set<string>, key = ""): unknown {
  if (SECRET_KEYS.test(key)) return "[REDACTED]";
  if (typeof value === "string") {
    if (secrets.has(value) || SECRET_PREFIXES.some((prefix) => value.startsWith(prefix))) {
      return "[REDACTED]";
    }
    let result = value;
    for (const secret of secrets) {
      if (secret.length >= 8) result = result.split(secret).join("[REDACTED]");
    }
    return result;
  }
  if (Array.isArray(value)) return value.map((item) => redactValue(item, secrets));
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value).map(([childKey, child]) => [childKey, redactValue(child, secrets, childKey)]),
    );
  }
  return value;
}

export async function writeEvidence(
  outDir: string,
  summary: EvidenceSummary,
  exchanges: Exchange[],
  target: ResolvedTarget,
): Promise<void> {
  await mkdir(outDir, { recursive: true });
  const safeSummary = redact(summary, target);
  const safeExchanges = redact(exchanges, target);
  await writeFile(join(outDir, "summary.json"), `${JSON.stringify(safeSummary, null, 2)}\n`);
  await writeFile(
    join(outDir, "requests.jsonl"),
    `${safeExchanges.map((exchange) => JSON.stringify(exchange)).join("\n")}\n`,
  );
  await writeFile(join(outDir, "junit.xml"), junit(safeSummary));
}

function junit(summary: EvidenceSummary): string {
  const failures = summary.counts.failed;
  const skipped = summary.counts.skipped;
  const cases = summary.cases.map((result) => {
    const attrs = `classname="relayfile.conformance" name="${xml(`${result.id} ${result.name}`)}" time="${(
      result.durationMs / 1000
    ).toFixed(3)}"`;
    if (result.status === "failed") {
      return `  <testcase ${attrs}><failure message="${xml(result.error ?? "failed")}" /></testcase>`;
    }
    if (result.status === "skipped") {
      return `  <testcase ${attrs}><skipped message="${xml(result.skipReason ?? "skipped")}" /></testcase>`;
    }
    return `  <testcase ${attrs} />`;
  });
  return [
    '<?xml version="1.0" encoding="UTF-8"?>',
    `<testsuite name="relayfile-conformance:${xml(summary.target.id)}" tests="${summary.cases.length}" failures="${failures}" skipped="${skipped}">`,
    ...cases,
    "</testsuite>",
    "",
  ].join("\n");
}

function xml(value: string): string {
  return value
    .replace(/[^\u0009\u000A\u000D\u0020-\uD7FF\uE000-\uFFFD\u{10000}-\u{10FFFF}]/gu, "")
    .replaceAll("&", "&amp;")
    .replaceAll('"', "&quot;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;");
}
