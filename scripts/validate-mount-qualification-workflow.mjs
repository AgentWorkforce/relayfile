import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

const path = '.github/workflows/relayfile-mount-qualification.yml';
const workflow = await readFile(path, 'utf8');

const required = [
  'name: Relayfile mount qualification',
  'branches:\n      - main',
  'runs-on: ubuntu-24.04',
  'GO_VERSION: "1.22.12"',
  'relayfileMountVersion=',
  'relayfile-mount-qualification-linux-amd64',
  'relayfile-mount-linux-amd64',
  'relayfile-mount-qualification-attestation',
  'attestationArtifactDigest',
  'sourceGitSha',
  'runAttempt',
  'artifactDigest',
  'fileSha256',
  'test ! -L "$binary"',
  'test "$("$binary" --version)" = "$EXPECTED_VERSION"',
];

for (const fragment of required) {
  assert(workflow.includes(fragment), `qualification workflow missing ${fragment}`);
}

assert.match(workflow, /event:"push"/);
assert.match(workflow, /ref:"main"/);
assert.match(workflow, /schemaVersion:1/);
assert.match(workflow, /workflowPath:"\.github\/workflows\/relayfile-mount-qualification\.yml"/);
console.log(`validated ${required.length} qualification workflow invariants`);
