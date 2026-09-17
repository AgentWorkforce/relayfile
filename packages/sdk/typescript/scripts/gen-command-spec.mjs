#!/usr/bin/env node
/**
 * Regenerate src/relay-cli/command-spec.json from the Go CLI's own command
 * table by running `relayfile __command-spec --json`.
 *
 * The snapshot exists so `@relayfile/sdk/relay-cli` can declare `commands`
 * without the binary being present at import time. It is generated, never
 * hand-edited; src/relay-cli/command-spec.test.ts regenerates and diffs it so
 * it cannot drift from the Go tree.
 *
 * Usage:
 *   node scripts/gen-command-spec.mjs          # write the snapshot
 *   node scripts/gen-command-spec.mjs --check  # fail if it would change
 */

import { spawnSync } from 'node:child_process';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const snapshotPath = join(packageRoot, 'src', 'relay-cli', 'command-spec.json');

/**
 * Walk up from `start` to the relayfile source checkout root.
 *
 * @param {string} start - Directory to start from.
 * @returns {string} The checkout root.
 */
function findCheckoutRoot(start) {
  let current = start;
  for (;;) {
    if (
      existsSync(join(current, 'go.mod')) &&
      existsSync(join(current, 'cmd', 'relayfile-cli'))
    ) {
      return current;
    }
    const parent = dirname(current);
    if (parent === current) {
      throw new Error(
        'gen:command-spec must run inside a relayfile checkout (no go.mod + cmd/relayfile-cli found)'
      );
    }
    current = parent;
  }
}

/**
 * Emit the command tree from the Go CLI.
 *
 * Prefers an already-built binary so the generator does not require a Go
 * toolchain when one is present; falls back to `go run`.
 *
 * @returns {string} The emitted JSON.
 */
export function emitCommandSpec() {
  const checkoutRoot = findCheckoutRoot(packageRoot);
  const builtBinaries = [
    join(checkoutRoot, 'packages', 'cli', 'bin', 'relayfile'),
    join(checkoutRoot, 'relayfile-cli'),
  ];
  const built = builtBinaries.find((candidate) => existsSync(candidate));

  const [command, args] = built
    ? [built, ['__command-spec', '--json']]
    : ['go', ['run', './cmd/relayfile-cli', '__command-spec', '--json']];

  const result = spawnSync(command, args, {
    cwd: checkoutRoot,
    encoding: 'utf8',
    maxBuffer: 32 * 1024 * 1024,
  });

  if (result.error) {
    if (result.error.code === 'ENOENT' && !built) {
      throw new Error(
        'gen:command-spec needs either a built relayfile binary ' +
          '(npm run build --workspace=packages/cli) or a Go toolchain on PATH.'
      );
    }
    throw result.error;
  }
  if (result.status !== 0) {
    throw new Error(
      `${command} ${args.join(' ')} exited ${result.status}\n${result.stderr ?? ''}`
    );
  }

  // Normalize to exactly one trailing newline so the snapshot is byte-stable
  // regardless of which emitter produced it.
  return `${result.stdout.trimEnd()}\n`;
}

/**
 * CLI entry point. Guarded so the module can be imported for `emitCommandSpec`
 * without generating or exiting.
 */
function main() {
  const checkOnly = process.argv.includes('--check');
  const emitted = emitCommandSpec();
  const current = existsSync(snapshotPath) ? readFileSync(snapshotPath, 'utf8') : '';

  if (emitted === current) {
    if (!checkOnly) {
      process.stdout.write('command-spec.json is up to date\n');
    }
    return 0;
  }

  if (checkOnly) {
    process.stderr.write(
      'command-spec.json is out of date with the Go command tree.\n' +
        'Run: npm run gen:command-spec --workspace=packages/sdk/typescript\n'
    );
    return 1;
  }

  writeFileSync(snapshotPath, emitted, 'utf8');
  process.stdout.write(`wrote ${snapshotPath}\n`);
  return 0;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  process.exit(main());
}
