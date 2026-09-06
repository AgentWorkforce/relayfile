import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

const validator = 'scripts/validate-mount-qualification-workflow.mjs';
const workflowPath = '.github/workflows/relayfile-mount-qualification.yml';
const publishPath = '.github/workflows/publish.yml';

async function validateMutation(mutate) {
  const directory = await mkdtemp(join(tmpdir(), 'relayfile-mount-workflow-'));
  try {
    const workflow = mutate(await readFile(workflowPath, 'utf8'));
    const candidate = join(directory, 'workflow.yml');
    await writeFile(candidate, workflow);
    return spawnSync(process.execPath, [validator, candidate, publishPath], { encoding: 'utf8' });
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}

test('accepts the canonical push-to-main workflow and normalized artifact digests', () => {
  const result = spawnSync(process.execPath, [validator], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
});

test('rejects an additional push branch', async () => {
  const result = await validateMutation((workflow) => workflow.replace('      - main\n', '      - main\n      - release\n'));
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /push trigger must contain only the main branch/);
});

test('rejects an additional workflow event', async () => {
  const result = await validateMutation((workflow) => workflow.replace('on:\n  push:', 'on:\n  workflow_dispatch:\n  push:'));
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /qualification workflow must trigger only on push/);
});

test('ignores a decoy push block outside the on trigger', async () => {
  const result = await validateMutation((workflow) =>
    workflow.replace(
      'on:\n',
      'decoy:\n  push:\n    branches:\n      - release\n\non:\n',
    ),
  );
  assert.equal(result.status, 0, result.stderr);
});

test('rejects a raw digest in the sealed attestation or consumer request', async () => {
  const result = await validateMutation((workflow) =>
    workflow
      .replace('artifactDigest:"sha256:"+rawDigest', 'artifactDigest:rawDigest')
      .replace('attestationArtifactDigest:"sha256:"+rawDigest', 'attestationArtifactDigest:rawDigest')
      .replace('(.payload.artifactDigest | test("^sha256:[0-9a-f]{64}$"))', '(.payload.artifactDigest | test("^[0-9a-f]{64}$"))'),
  );
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /qualification workflow missing/);
});
