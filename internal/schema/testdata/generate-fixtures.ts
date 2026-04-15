import { execFileSync } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

import {
  mockIssuePayload,
  mockRepoContext,
} from '../../../../relayfile-adapters/packages/github/src/__tests__/fixtures/index.ts';
import { mapIssue } from '../../../../relayfile-adapters/packages/github/src/issues/issue-mapper.ts';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const rootDir = path.resolve(__dirname, '..', '..', '..');
const adapterRepoDir = path.resolve(rootDir, '..', 'relayfile-adapters');
const testdataDir = path.resolve(rootDir, 'internal', 'schema', 'testdata');
const adapterRawPath = path.join(testdataDir, 'github-issue-adapter-raw-input.json');
const adapterEmittedPath = path.join(testdataDir, 'github-issue-adapter-emitted.json');

mkdirSync(testdataDir, { recursive: true });

const adapterRaw = structuredClone(mockIssuePayload);
writeFileSync(adapterRawPath, `${JSON.stringify(adapterRaw, null, 2)}\n`);

const generatedRaw = JSON.parse(readFileSync(adapterRawPath, 'utf8'));
const emitted = JSON.parse(
  mapIssue(generatedRaw, mockRepoContext.owner, mockRepoContext.repo).content,
);
writeFileSync(adapterEmittedPath, `${JSON.stringify(emitted, null, 2)}\n`);

const adapterCommit = execFileSync('git', ['-C', adapterRepoDir, 'rev-parse', 'HEAD'], {
  encoding: 'utf8',
}).trim();

console.log(`Wrote ${path.relative(rootDir, adapterRawPath)}`);
console.log(`Wrote ${path.relative(rootDir, adapterEmittedPath)}`);
console.log(`relayfile-adapters commit: ${adapterCommit}`);
