import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import {
  comparePep440,
  findTrustedPythonReleaseTags,
  parseStrictPep440,
  resolvePythonReleaseBaseline,
} from "./resolve-python-release-baseline.mjs";

function git(cwd, ...args) {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

function repository() {
  const cwd = mkdtempSync(join(tmpdir(), "relayfile-python-baseline-"));
  git(cwd, "init", "-q");
  git(cwd, "config", "user.name", "Release Test");
  git(cwd, "config", "user.email", "release-test@example.invalid");
  writeFileSync(join(cwd, "source.txt"), "source\n");
  git(cwd, "add", ".");
  git(cwd, "commit", "-qm", "source");
  return { cwd, sourceSha: git(cwd, "rev-parse", "HEAD") };
}

function annotatedTag(cwd, version, commit, metadata = true) {
  const args = ["tag", "-a", `sdk-python-v${version}`, commit, "-m", `Python SDK v${version}`];
  if (metadata) {
    args.push(
      "-m",
      `source-sha=${commit}`,
      "-m",
      `tag-tree=${git(cwd, "rev-parse", `${commit}^{tree}`)}`,
      "-m",
      "workflow-run-id=123",
      "-m",
      "workflow-run-attempt=1",
    );
  }
  git(cwd, ...args);
}

test("strict PEP 440 parser and ordering put final after beta", () => {
  assert.equal(parseStrictPep440("1.2.3b1").phase, "b");
  assert.equal(parseStrictPep440("1.2.3b01"), null);
  assert.equal(parseStrictPep440("99.0"), null);
  assert.ok(comparePep440("1.2.3b9", "1.2.3") < 0);
  assert.ok(comparePep440("1.2.3rc1", "1.2.3") < 0);
  assert.ok(comparePep440("1.2.3", "1.2.3b9") > 0);
});

test("forged, wrong-target, malformed, and missing-provenance tags are rejected", () => {
  const { cwd, sourceSha } = repository();
  try {
    annotatedTag(cwd, "1.2.3", sourceSha);
    annotatedTag(cwd, "90.0.0b01", sourceSha);
    annotatedTag(cwd, "91.0", sourceSha);
    annotatedTag(cwd, "92.0.0", sourceSha, false);
    writeFileSync(join(cwd, "wrong-target.txt"), "wrong target\n");
    git(cwd, "add", ".");
    git(cwd, "commit", "-qm", "wrong target");
    const wrongTarget = git(cwd, "rev-parse", "HEAD");
    annotatedTag(cwd, "93.0.0", wrongTarget);

    git(cwd, "checkout", "-qb", "side", sourceSha);
    writeFileSync(join(cwd, "side.txt"), "side branch\n");
    git(cwd, "add", ".");
    git(cwd, "commit", "-qm", "side branch");
    const sideCommit = git(cwd, "rev-parse", "HEAD");
    annotatedTag(cwd, "94.0.0", sideCommit);

    const trusted = findTrustedPythonReleaseTags({ cwd, sourceSha });
    assert.deepEqual(trusted.map(({ tag }) => tag), ["sdk-python-v1.2.3"]);
  } finally {
    rmSync(cwd, { recursive: true, force: true });
  }
});

test("draft or incomplete releases are skipped in favor of the newest completed release", () => {
  const { cwd, sourceSha } = repository();
  try {
    annotatedTag(cwd, "1.2.3", sourceSha);
    annotatedTag(cwd, "1.2.4b1", sourceSha);
    annotatedTag(cwd, "1.2.4", sourceSha);
    const result = resolvePythonReleaseBaseline({
      cwd,
      sourceSha,
      currentVersion: "1.2.2",
      releaseVerifier: ({ tag }) => tag !== "sdk-python-v1.2.4",
    });
    assert.equal(result.baselineVersion, "1.2.4b1");
    assert.equal(result.latestTag, "sdk-python-v1.2.4b1");
  } finally {
    rmSync(cwd, { recursive: true, force: true });
  }
});

test("no valid prior release safely bootstraps from the manifest", () => {
  const { cwd, sourceSha } = repository();
  try {
    annotatedTag(cwd, "99.0.0", sourceSha, false);
    const result = resolvePythonReleaseBaseline({
      cwd,
      sourceSha,
      currentVersion: "1.2.3",
      releaseVerifier: () => true,
    });
    assert.equal(result.baselineVersion, "1.2.3");
    assert.equal(result.latestTag, "");
  } finally {
    rmSync(cwd, { recursive: true, force: true });
  }
});
