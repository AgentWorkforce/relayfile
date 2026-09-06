import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

const workflowPath = process.argv[2] ?? '.github/workflows/relayfile-mount-qualification.yml';
const publishPath = process.argv[3] ?? '.github/workflows/publish.yml';
const workflow = await readFile(workflowPath, 'utf8');

function blockAtIndent(source, header, indent) {
  const lines = source.split(/\r?\n/);
  const prefix = ' '.repeat(indent);
  const start = lines.findIndex((line) => line === `${prefix}${header}`);
  assert.notEqual(start, -1, `qualification workflow must contain ${header}`);

  let end = start + 1;
  while (end < lines.length) {
    const line = lines[end];
    if (line.trim() && !line.trimStart().startsWith('#')) {
      const currentIndent = line.length - line.trimStart().length;
      if (currentIndent <= indent) break;
    }
    end += 1;
  }
  return lines.slice(start + 1, end);
}

function directEntries(lines, indent) {
  return lines
    .filter((line) => line.trim() && !line.trimStart().startsWith('#'))
    .filter((line) => line.length - line.trimStart().length === indent)
    .map((line) => line.trim());
}

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
  'if(!/^[0-9a-f]{64}$/.test(a.payload.artifactDigest))',
  '(.payload.artifactDigest | test("^[0-9a-f]{64}$"))',
];

for (const fragment of required) {
  assert(workflow.includes(fragment), `qualification workflow missing ${fragment}`);
}

const onBlock = blockAtIndent(workflow, 'on:', 0);
assert.deepEqual(directEntries(onBlock, 2), ['push:'], 'qualification workflow must trigger only on push');
const pushBlock = blockAtIndent(onBlock.join('\n'), 'push:', 2);
assert.deepEqual(directEntries(pushBlock, 4), ['branches:'], 'push trigger must contain only branches');
assert.deepEqual(directEntries(pushBlock, 6), ['- main'], 'push trigger must contain only the main branch');

const verifyBlock = blockAtIndent(workflow, 'verify:', 2).join('\n');
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

const publish = await readFile(publishPath, 'utf8');
assert.match(publish, /-X main\.relayfileMountVersion=\$\{VERSION\}/);
assert.doesNotMatch(publish, /-X main\.version=/);

console.log(`validated ${required.length} qualification workflow invariants and structural producer shape`);
