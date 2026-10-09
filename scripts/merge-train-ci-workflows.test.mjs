import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import { parse } from "yaml";

/**
 * The merge-train sweeper (AgentWorkforce/cloud packages/web/lib/merge-train)
 * and these workflows share three names: the label that runs promotion CI
 * (`ci:run`), the check every promotion CI run creates (the sweeper's "CI ran
 * on this head" marker), and the feature ready check (`Merge-train ready
 * check`, kicked by `ready:check`). A rename on either side silently breaks
 * the gate, so they are pinned here.
 */
const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const load = (file) => parse(readFileSync(path.join(ROOT, ".github/workflows", file), "utf8"));
const LABEL_FILTER = "(github.event.action != 'labeled' || github.event.label.name == 'ci:run')";
const IGNORED_GROUP =
  "(github.event.action == 'labeled' && github.event.label.name != 'ci:run') && format('ignored-{0}', github.run_id)";

/** Workflows that run on the trunk -> main promotion PR. */
const PROMOTION_WORKFLOWS = ["ci.yml", "contract.yml", "relayfile-evals.yml"];
/** Workflows with a concurrency group a stray label event could otherwise cancel. */
const CONCURRENCY_WORKFLOWS = ["ci.yml", "contract.yml", "relayfile-evals.yml"];
const MARKER = { file: "ci.yml", job: "go-test", name: "Go Test" };

for (const file of PROMOTION_WORKFLOWS) {
  test(`${file} runs the trunk PR only on opened/reopened/ci:run, never on synchronize`, () => {
    const workflow = load(file);
    assert.deepEqual(workflow.on.pull_request?.types, ["opened", "reopened", "labeled"]);
    const jobs = Object.entries(workflow.jobs);
    assert.ok(jobs.length > 0);
    for (const [id, job] of jobs) {
      // A job with `needs` and no `if` is skipped whenever its gated parent skips.
      if (!job.if && job.needs) continue;
      assert.ok(String(job.if ?? "").includes("github.head_ref == 'trunk'"), `${file} ${id}: trunk PR gate`);
      assert.ok(String(job.if ?? "").includes(LABEL_FILTER), `${file} ${id}: must skip label events other than ci:run`);
    }
  });
}

for (const file of CONCURRENCY_WORKFLOWS) {
  test(`${file}: an unrelated label event can never cancel a real promotion CI run`, () => {
    const { concurrency } = load(file);
    assert.ok(String(concurrency.group).includes(IGNORED_GROUP), `${file}: throwaway group for ignored labels`);
    assert.equal(concurrency["cancel-in-progress"], "${{ github.event_name == 'pull_request' }}");
  });
}

test("the sweeper's CI marker job has no needs, so every real promotion run creates it", () => {
  const job = load(MARKER.file).jobs[MARKER.job];
  assert.ok(job, "marker job present");
  assert.equal(job.name ?? MARKER.job, MARKER.name);
  assert.equal(job.needs, undefined);
});

test("feature PRs into trunk get the ready check the sweeper requires", () => {
  const ready = load("merge-train-ready.yml");
  assert.deepEqual(ready.on.pull_request?.branches, ["trunk"]);
  assert.deepEqual(ready.on.pull_request?.types, ["labeled", "synchronize", "reopened"]);
  const jobs = Object.values(ready.jobs);
  assert.equal(jobs.length, 1);
  const [job] = jobs;
  assert.equal(job.name, "Merge-train ready check");
  // Fork PRs get the check too (no secrets under `pull_request`); trust is the sweeper's gate.
  assert.doesNotMatch(job.if, /head\.repo\.full_name/);
  assert.deepEqual(Object.keys(ready.on), ["pull_request"]);
  const raw = readFileSync(path.join(ROOT, ".github/workflows/merge-train-ready.yml"), "utf8");
  assert.doesNotMatch(raw, /secrets\./);
  assert.equal(job.steps[0].with?.["persist-credentials"], false);
  assert.match(job.if, /contains\(github\.event\.pull_request\.labels\.\*\.name, 'mergeable'\)/);
  // The sweeper kicks missing runs with `ready:check`: the job runs on ANY label event.
  assert.doesNotMatch(job.if, /event\.label\.name/);
});
