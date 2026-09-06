import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

const path = '.github/workflows/relayfile-mount-qualification.yml';
const workflow = await readFile(path, 'utf8');

const required = [
  'name: Relayfile mount qualification',
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
  'test "$GITHUB_REPOSITORY" = "AgentWorkforce/relayfile"',
  'repository:process.env.GITHUB_REPOSITORY',
  'test ! -L "$binary"',
  'chmod 755 "$binary"',
  'test "$("$binary" --version)" = "$EXPECTED_VERSION"',
];

for (const fragment of required) {
  assert(workflow.includes(fragment), `qualification workflow missing ${fragment}`);
}

assert.match(
  workflow,
  /^on:\s*\n\s+push:\s*\n\s+branches:\s*\n\s+-\s+main\s*$/m,
  'qualification workflow must trigger only on pushes to main',
);
const verifyBlock = workflow.match(/^  verify:\s*\n([\s\S]*)/m)?.[1];
assert(verifyBlock, 'qualification workflow must contain a verify job');
assert.match(verifyBlock, /^    needs:\s*build\s*$/m);
assert.match(
  verifyBlock,
  /uses:\s*actions\/download-artifact@v4[\s\S]*?name:\s*relayfile-mount-qualification-linux-amd64/,
);
assert.match(
  verifyBlock,
  /uses:\s*actions\/download-artifact@v4[\s\S]*?name:\s*relayfile-mount-qualification-attestation/,
);

assert.match(workflow, /event:"push"/);
assert.match(workflow, /ref:"main"/);
assert.match(workflow, /schemaVersion:1/);
assert.match(workflow, /workflowPath:"\.github\/workflows\/relayfile-mount-qualification\.yml"/);

const publish = await readFile('.github/workflows/publish.yml', 'utf8');
assert.match(publish, /-X main\.relayfileMountVersion=\$\{VERSION\}/);
assert.doesNotMatch(publish, /-X main\.version=/);

console.log(`validated ${required.length} qualification workflow invariants and structural producer shape`);
