import test from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";

import {
  RELEASE_PACKAGE_PATHS,
  resolveReleaseBaseline,
} from "./resolve-release-baseline.mjs";

function git(cwd, ...args) {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}

function writePackages(cwd, version) {
  for (const path of RELEASE_PACKAGE_PATHS) {
    const file = join(cwd, path);
    mkdirSync(join(file, ".."), { recursive: true });
    writeFileSync(file, JSON.stringify({ name: path, version }) + "\n");
  }
}

function sandboxWithPriorRelease() {
  const cwd = mkdtempSync(join(tmpdir(), "relayfile-baseline-"));
  git(cwd, "init", "-q");
  git(cwd, "config", "user.name", "Release Test");
  git(cwd, "config", "user.email", "release-test@example.invalid");
  writePackages(cwd, "1.2.3");
  git(cwd, "add", ".");
  git(cwd, "commit", "-qm", "source");
  const sourceSha = git(cwd, "rev-parse", "HEAD");
  writePackages(cwd, "1.2.4");
  git(cwd, "commit", "-qam", "chore(release): v1.2.4");
  const releaseCommit = git(cwd, "rev-parse", "HEAD");
  git(cwd, "tag", "-a", "v1.2.4", releaseCommit, "-m", "Release v1.2.4");
  return { cwd, sourceSha, releaseCommit };
}

test("next dispatch advances from a trusted release tag not merged to source", () => {
  const { cwd, sourceSha } = sandboxWithPriorRelease();
  const result = resolveReleaseBaseline({
    cwd,
    sourceSha,
    currentVersion: "1.2.3",
  });
  assert.equal(result.baselineVersion, "1.2.4");
  assert.equal(result.latestTag, "v1.2.4");
  assert.equal(result.resumableVersion, "1.2.4");
  rmSync(cwd, { recursive: true, force: true });
});

test("sibling-branch release tags do not become a version baseline", () => {
  const { cwd, sourceSha } = sandboxWithPriorRelease();
  git(cwd, "checkout", "-qb", "dispatch-main", sourceSha);
  writePackages(cwd, "1.2.3");
  writeFileSync(join(cwd, "main.txt"), "main\n");
  git(cwd, "add", ".");
  git(cwd, "commit", "-qm", "main advances");
  const currentSourceSha = git(cwd, "rev-parse", "HEAD");

  git(cwd, "checkout", "-q", "-b", "sibling", sourceSha);
  writePackages(cwd, "9.9.9");
  writeFileSync(join(cwd, "sibling.txt"), "forged side branch\n");
  git(cwd, "add", ".");
  git(cwd, "commit", "-qm", "chore(release): v9.9.9");
  const siblingCommit = git(cwd, "rev-parse", "HEAD");
  git(cwd, "tag", "-a", "v9.9.9", siblingCommit, "-m", "Release v9.9.9");

  const result = resolveReleaseBaseline({
    cwd,
    sourceSha: currentSourceSha,
    currentVersion: "1.2.3",
  });
  assert.equal(result.baselineVersion, "1.2.4");
  assert.equal(result.latestTag, "v1.2.4");
  rmSync(cwd, { recursive: true, force: true });
});
