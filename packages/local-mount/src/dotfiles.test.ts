import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { readAgentDotfiles } from './dotfiles.js';

describe('readAgentDotfiles', () => {
  let projectDir: string;

  beforeEach(() => {
    projectDir = mkdtempSync(path.join(os.tmpdir(), 'local-mount-dotfiles-'));
  });

  afterEach(() => {
    try { rmSync(projectDir, { recursive: true, force: true }); } catch { /* best effort */ }
  });

  it('returns empty patterns when no dotfiles exist', () => {
    const result = readAgentDotfiles(projectDir);
    expect(result.ignoredPatterns).toEqual([]);
    expect(result.readonlyPatterns).toEqual([]);
  });

  it('merges generic + per-agent patterns when agentName is provided', () => {
    writeFileSync(path.join(projectDir, '.agentignore'), 'secrets/\n# comment\nlogs/\n', 'utf8');
    writeFileSync(path.join(projectDir, '.agentreadonly'), 'package.json\n', 'utf8');
    writeFileSync(path.join(projectDir, '.reviewer.agentignore'), 'TODO.md\n', 'utf8');
    writeFileSync(path.join(projectDir, '.reviewer.agentreadonly'), 'README.md\n', 'utf8');

    // Without agentName: only generic patterns.
    const generic = readAgentDotfiles(projectDir);
    expect(generic.ignoredPatterns).toEqual(['secrets/', 'logs/']);
    expect(generic.readonlyPatterns).toEqual(['package.json']);

    // With agentName: merged.
    const merged = readAgentDotfiles(projectDir, { agentName: 'reviewer' });
    expect(merged.ignoredPatterns).toEqual(['secrets/', 'logs/', 'TODO.md']);
    expect(merged.readonlyPatterns).toEqual(['package.json', 'README.md']);
  });

  it('ignores comments and blank lines', () => {
    writeFileSync(
      path.join(projectDir, '.agentignore'),
      '# header\n\n\nfoo\n  bar  \n# trailing\n',
      'utf8'
    );
    const result = readAgentDotfiles(projectDir);
    expect(result.ignoredPatterns).toEqual(['foo', 'bar']);
  });

  it('does not explode when only per-agent file exists', () => {
    writeFileSync(path.join(projectDir, '.reviewer.agentignore'), 'only-per-agent\n', 'utf8');
    const result = readAgentDotfiles(projectDir, { agentName: 'reviewer' });
    expect(result.ignoredPatterns).toEqual(['only-per-agent']);
    expect(result.readonlyPatterns).toEqual([]);
  });
});
