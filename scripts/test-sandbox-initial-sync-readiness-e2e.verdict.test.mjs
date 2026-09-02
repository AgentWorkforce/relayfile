#!/usr/bin/env node
// Red-check for the #455 proof harness's own verdict logic.
//
// The harness gates future merges, so it has to fail like the thing it tests.
// Every case below is built from data actually observed on 2026-09-02 — the
// real pass, the real must-fail control, and the real FALSE 75 that a scope
// error produced. If the harness ever accepts that false 75 again, this fails.
//
// Run: node --test scripts/test-sandbox-initial-sync-readiness-e2e.verdict.test.mjs

import test from "node:test";
import assert from "node:assert/strict";

import {
  verdict,
  EXIT_PROOF_HELD,
  EXIT_PROOF_REFUTED,
  EXIT_UNKNOWN,
} from "./test-sandbox-initial-sync-readiness-e2e.mjs";

/** Arm A as actually observed: exit 75 having genuinely hit the budget. */
const goodControl = {
  arm: "A",
  guardExit: 75,
  timedOut: false,
  stateFile: "/ws-A/github/repos/AgentWorkforce/relay/.relay/state.json",
  stateMissing: false,
  mirroredFiles: 2000,
  bootstrapNull: false,
  filesSynced: 2000,
  traversalIncomplete: true,
  budgetReached: true,
  authFailed: false,
  lastSuccessfulReconcileAt: "2026-09-02T12:18:24.303486294Z",
  stateBytes: 324833,
};

/** Arm B as actually observed: exit 0, bootstrap gone, 5685 files. */
const goodCandidate = {
  arm: "B",
  guardExit: 0,
  timedOut: false,
  stateFile: "/ws-B/github/repos/AgentWorkforce/relay/.relay/state.json",
  stateMissing: false,
  mirroredFiles: 5685,
  bootstrapNull: true,
  filesSynced: null,
  traversalIncomplete: true,
  budgetReached: true,
  authFailed: false,
  lastSuccessfulReconcileAt: "2026-09-02T12:47:48.742036968Z",
  stateBytes: 1028186,
};

const A = (o = {}) => ({ ...goodControl, ...o });
const B = (o = {}) => ({ ...goodCandidate, ...o });

test("must-fire: the real observed run is a pass", () => {
  const [code, msg] = verdict(A(), B());
  assert.equal(code, EXIT_PROOF_HELD);
  assert.match(msg, /PROOF HELD/);
});

test("must-not-fire: the real FALSE 75 is rejected, not accepted as a control", () => {
  // Observed 2026-09-02 11:58: token minted with the wrong scopes produced
  // `403 missing required scope: fs:read`, filesSynced 0 — and exit 75, the
  // textbook-looking answer. The old harness would have called this a valid
  // control and reported PROOF HELD off the back of it.
  const falseControl = A({
    mirroredFiles: 0,
    filesSynced: 0,
    bootstrapNull: false,
    traversalIncomplete: false,
    budgetReached: false,
    authFailed: true,
  });
  const [code] = verdict(falseControl, B());
  assert.equal(code, EXIT_UNKNOWN);
  assert.notEqual(code, EXIT_PROOF_HELD);
});

test("exit 75 with no bootstrap block is not a valid control", () => {
  const [code, msg] = verdict(A({ bootstrapNull: true, filesSynced: null }), B());
  assert.equal(code, EXIT_UNKNOWN);
  assert.match(msg, /not a valid must-fail control/);
});

test("exit 75 below the per-cycle budget is not a valid control", () => {
  const [code, msg] = verdict(A({ filesSynced: 1200, mirroredFiles: 1200 }), B());
  assert.equal(code, EXIT_UNKNOWN);
  assert.match(msg, /below the 2000-file per-cycle budget/);
});

test("exit 75 without the budget/traversal log lines is not a valid control", () => {
  const [code, msg] = verdict(A({ traversalIncomplete: false, budgetReached: false }), B());
  assert.equal(code, EXIT_UNKNOWN);
  assert.match(msg, /did not provably hit the per-cycle budget/);
});

test("arm A passing means the fixture was too small — INCONCLUSIVE, never green", () => {
  const [code, msg] = verdict(A({ guardExit: 0, bootstrapNull: true }), B());
  assert.equal(code, EXIT_UNKNOWN);
  assert.match(msg, /bigger tree/);
});

test("a timeout is UNKNOWN, never a pass", () => {
  assert.equal(verdict(A(), B({ timedOut: true, guardExit: null }))[0], EXIT_UNKNOWN);
  assert.equal(verdict(A({ timedOut: true, guardExit: null }), B())[0], EXIT_UNKNOWN);
});

test("a credential failure in either arm is UNKNOWN, never REFUTED", () => {
  // An expired token mid-bootstrap must not read as "the fix does not work".
  const [code, msg] = verdict(A(), B({ guardExit: 75, bootstrapNull: false, authFailed: true }));
  assert.equal(code, EXIT_UNKNOWN);
  assert.match(msg, /credential failure/);
});

test("a missing state file is UNKNOWN, not a silent pass", () => {
  // The bug the wrong state path would have caused: absent file parses as
  // {"__missing":true}, bootstrap undefined => bootstrapNull true => arm B
  // looks converged without anything having been inspected.
  const [code, msg] = verdict(A(), B({ stateMissing: true, bootstrapNull: true }));
  assert.equal(code, EXIT_UNKNOWN);
  assert.match(msg, /left no state file/);
  assert.notEqual(code, EXIT_PROOF_HELD);
});

test("a genuine non-convergence IS refuted", () => {
  // The one case that may legitimately claim the fix does not work: arm B ran,
  // authenticated fine, and still left a non-null bootstrap.
  const [code, msg] = verdict(A(), B({ guardExit: 75, bootstrapNull: false, filesSynced: 2000 }));
  assert.equal(code, EXIT_PROOF_REFUTED);
  assert.match(msg, /REFUTED/);
});
