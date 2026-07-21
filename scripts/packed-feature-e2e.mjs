#!/usr/bin/env node
import { cpSync, existsSync, mkdtempSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { basename, dirname, join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { spawnSync } from 'node:child_process';

const root = process.cwd();
const fixture = mkdtempSync(join(tmpdir(), 'relayfile-packed-e2e.'));
const packs = join(fixture, 'packs');
const consumer = join(fixture, 'node-consumer');
const pythonConsumer = join(fixture, 'python-consumer');
const crossBuildDist = join(fixture, 'cross-build-dist');
const stagedMountPackages = join(fixture, 'mount-packages');
const fixtureMarker = join(fixture, '.relayfile-packed-e2e-fixture');
writeFileSync(fixtureMarker, '');

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd ?? root,
    env: { ...process.env, ...options.env },
    encoding: 'utf8',
  });
  if (result.stdout) process.stdout.write(result.stdout);
  if (result.stderr) process.stderr.write(result.stderr);
  if (result.status !== 0) throw new Error(`${command} ${args.join(' ')} exited ${result.status}`);
  return result;
}

try {
  run('mkdir', ['-p', packs, consumer, pythonConsumer]);
  run('npm', ['run', 'build']);
  for (const name of readdirSync(join(root, 'packages')).filter((entry) => entry.startsWith('mount-'))) {
    cpSync(join(root, 'packages', name), join(stagedMountPackages, name), { recursive: true });
  }
  run('make', ['build-all', `DIST_DIR=${crossBuildDist}`]);
  run('node', ['scripts/build-mount-npm-packages.mjs'], {
    env: {
      RELAYFILE_MOUNT_DIST_DIR: crossBuildDist,
      RELAYFILE_MOUNT_PACKAGES_DIR: stagedMountPackages,
    },
  });
  for (const workspace of [
    'packages/core',
    'packages/sdk/typescript',
    '@relayfile/client',
    'packages/local-mount',
    'packages/agents',
    'packages/cli',
  ]) run('npm', ['pack', `--workspace=${workspace}`, '--pack-destination', packs]);

  const platform = `${process.platform}-${process.arch === 'x64' ? 'x64' : 'arm64'}`;
  const mountPackage = join(stagedMountPackages, `mount-${platform}`);
  if (!existsSync(mountPackage)) throw new Error(`unsupported packed mount platform ${platform}`);
  run('npm', ['pack', mountPackage, '--pack-destination', packs]);

  const tarballs = readdirSync(packs).filter((name) => name.endsWith('.tgz')).map((name) => join(packs, name));
  if (tarballs.length !== 7) throw new Error(`expected 7 npm tarballs, found ${tarballs.length}`);
  writeFileSync(join(consumer, 'package.json'), JSON.stringify({ name: 'relayfile-packed-consumer', private: true, type: 'module' }));
  // npm installs optional peer frameworks so every declared agent subpath can
  // be imported exactly as a real clean consumer would import it.
  run('npm', [
    'install', '--ignore-scripts',
    'ai@^4.0.20', '@openai/agents@^0.0.13', '@langchain/core@^0.3.25',
    ...tarballs,
  ], { cwd: consumer });
  run('node', ['--input-type=module', '-e', [
    "const modules = await Promise.all([",
    "  import('@relayfile/core'), import('@relayfile/sdk'), import('@relayfile/sdk/cli'),",
    "  import('@relayfile/sdk/cloud-login'), import('@relayfile/sdk/mount-launcher'),",
    "  import('@relayfile/sdk/mount-harness'), import('@relayfile/sdk/mount-path'),",
    "  import('@relayfile/sdk/workspace-seeder'), import('@relayfile/sdk/workspace-mount'),",
    "  import('@relayfile/client'), import('@relayfile/local-mount'), import('@relayfile/agents'),",
    "  import('@relayfile/agents/vercel'), import('@relayfile/agents/openai'), import('@relayfile/agents/langchain'),",
    "]);",
    "if (modules.some((value) => !value)) process.exit(1);",
    "if (!modules[1].RelayFileClient) throw new Error('RelayFileClient missing');",
  ].join('\n')], { cwd: consumer });
  const undeclared = spawnSync('node', ['--input-type=module', '-e', "await import('@relayfile/sdk/not-declared')"], {
    cwd: consumer, encoding: 'utf8',
  });
  if (undeclared.status === 0) throw new Error('undeclared SDK subpath unexpectedly imported');

  const cli = run('node', ['node_modules/relayfile/scripts/run.js', 'version'], { cwd: consumer });
  if (!/0\.10\.33/.test(cli.stdout)) throw new Error(`packed CLI version drifted: ${cli.stdout}`);
  const mountBinary = join(consumer, 'node_modules', '@relayfile', `mount-${platform}`, 'bin', 'relayfile-mount');
  if (!existsSync(mountBinary)) throw new Error(`packed mount binary is absent: ${mountBinary}`);

  run('uv', ['build', 'packages/sdk/python', '--out-dir', packs]);
  const wheel = readdirSync(packs).find((name) => name.endsWith('.whl'));
  if (!wheel) throw new Error('Python wheel was not built');
  run('uv', ['venv', pythonConsumer]);
  const python = join(pythonConsumer, process.platform === 'win32' ? 'Scripts/python.exe' : 'bin/python');
  run('uv', ['pip', 'install', '--python', python, join(packs, wheel)]);
  run(python, ['-c', 'import relayfile; assert relayfile.RelayFileClient; assert len(relayfile.__all__) == len(set(relayfile.__all__))']);

  console.log('PACKED_FEATURE_E2E_PASS npm_tarballs=7 python_wheel=1 declared_imports=15 cli_version=0.10.33 mount_binary=1 undeclared_negative=1');
} finally {
  if (
    !existsSync(fixtureMarker) ||
    dirname(resolve(fixture)) !== resolve(tmpdir()) ||
    !basename(fixture).startsWith('relayfile-packed-e2e.')
  ) throw new Error(`refusing fixture cleanup: ${fixture}`);
  rmSync(fixture, { recursive: true });
}
