#!/usr/bin/env node
/**
 * Copy src/relay-cli/command-spec.json into dist during the SDK build.
 *
 * The surface reads the snapshot from disk (rather than importing it) so the
 * module needs no JSON import attributes and loads from both ESM and a CJS
 * `import()`. tsc does not emit non-imported assets, so the build copies it.
 */

import { copyFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const source = join(packageRoot, 'src', 'relay-cli', 'command-spec.json');
const target = join(packageRoot, 'dist', 'relay-cli', 'command-spec.json');

mkdirSync(dirname(target), { recursive: true });
copyFileSync(source, target);
