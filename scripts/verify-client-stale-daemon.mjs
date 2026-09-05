import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, unlinkSync, writeFileSync } from 'node:fs';
import { spawn } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';

const packageRoot = process.argv[2];
if (!packageRoot) throw new Error('usage: node verify-client-stale-daemon.mjs <installed-package-root>');
const expectFailure = process.argv.includes('--expect-failure');

const root = mkdtempSync(join(tmpdir(), 'relayfile-462-clean-install-'));
const socketPath = join(root, 'relayfile.sock');
const stalePidFile = join(root, 'stale.pid');
const newPidFile = join(root, 'new.pid');
const fakeBin = join(root, 'relayfile');
const fakeLsof = join(root, 'lsof');
const staleScript = join(root, 'stale-daemon.mjs');
const newScript = join(root, 'new-daemon.mjs');
const previousPath = process.env.PATH;
let stalePid;
let replacementPid;

const hello = (version, apiVersion, supportedApiVersions) =>
  JSON.stringify({ daemonVersion: version, apiVersion, supportedApiVersions });
const serverScript = (pidFile, version, apiVersion, supportedApiVersions) => `
import { createServer } from 'node:http';
import { writeFileSync, unlinkSync } from 'node:fs';
const socket = process.argv[2];
const server = createServer((request, response) => {
  if (request.url === '/v1/hello') {
    response.setHeader('content-type', 'application/json');
    response.end(${JSON.stringify(hello(version, apiVersion, supportedApiVersions))});
  } else { response.statusCode = 404; response.end('{}'); }
});
server.listen(socket, () => writeFileSync(${JSON.stringify(pidFile)}, String(process.pid)));
const cleanup = () => { try { unlinkSync(socket); } catch {} try { server.close(); } catch {} process.exit(0); };
process.on('SIGTERM', cleanup);
process.on('SIGINT', cleanup);
`;
const isAlive = (pid) => {
  try { process.kill(pid, 0); return true; } catch (error) { return error.code !== 'ESRCH'; }
};
const waitForCleanup = async (pid, socket) => {
  try { process.kill(pid, 'SIGKILL'); } catch {}
  try { unlinkSync(socket); } catch {}
  const deadline = Date.now() + 5000;
  while ((isAlive(pid) || existsSync(socket)) && Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  return { processGone: !isAlive(pid), socketAbsent: !existsSync(socket) };
};

async function run() {
try {
  writeFileSync(staleScript, serverScript(stalePidFile, '0.10.19', 1, [1]));
  writeFileSync(newScript, serverScript(newPidFile, '0.10.26', 3, [1, 2, 3]));
  writeFileSync(
    fakeBin,
    `#!/bin/sh
if [ "$1" = "--version" ]; then
  echo "relayfile 0.10.26"
else
  exec ${process.execPath} ${newScript} "$4"
fi
`
  );
  writeFileSync(fakeLsof, `#!/bin/sh
sleep 1.2
cat ${stalePidFile}
`);
  chmodSync(fakeBin, 0o755);
  chmodSync(fakeLsof, 0o755);

  const stale = spawn(process.execPath, [staleScript, socketPath], { stdio: 'ignore' });
  stalePid = stale.pid;
  if (!Number.isSafeInteger(stalePid) || stalePid <= 1) throw new Error('stale daemon did not expose a valid PID');
  await new Promise((resolve, reject) => {
    stale.once('spawn', resolve);
    stale.once('error', reject);
  });
  await new Promise((resolve, reject) => {
    const deadline = Date.now() + 5000;
    const check = () => {
      if (existsSync(stalePidFile) && existsSync(socketPath)) return resolve();
      if (Date.now() >= deadline) return reject(new Error('stale daemon did not become ready'));
      setTimeout(check, 25);
    };
    check();
  });

  process.env.PATH = `${root}:${previousPath ?? ''}`;
  process.env.RELAYFILE_BIN = fakeBin;
  const { RelayfileControlPlaneClient } = await import(
    pathToFileURL(join(packageRoot, 'dist/index.js')).href
  );
  const client = new RelayfileControlPlaneClient({ socketPath, autoStart: true, startTimeoutMs: 5000 });
  let result;
  let failure;
  try { result = await client.ensureReady(); } catch (error) { failure = error; }
  if (expectFailure) {
    if (!failure) throw new Error('expected released baseline to fail delayed stale-daemon replacement');
    const cleanup = await waitForCleanup(stalePid, socketPath);
    if (!cleanup.processGone || !cleanup.socketAbsent) throw new Error('failure-path cleanup did not remove stale daemon/socket');
    console.log(JSON.stringify({ outcome: 'expected-fail', error: failure instanceof Error ? failure.message : String(failure), stalePid, ...cleanup }));
    return;
  }
  if (failure) throw failure;
  let staleAlive = isAlive(stalePid);
  if (staleAlive) throw new Error(`stale daemon PID ${stalePid} still exists after replacement`);
  if (!existsSync(socketPath)) throw new Error('replacement daemon did not retain the control-plane socket');

  replacementPid = Number(readFileSync(newPidFile, 'utf8').trim());
  process.kill(replacementPid, 'SIGTERM');
  await new Promise((resolve, reject) => {
    const deadline = Date.now() + 5000;
    const check = () => {
      let alive = true;
      try { process.kill(replacementPid, 0); } catch (error) { alive = error.code !== 'ESRCH'; }
      if (!alive && !existsSync(socketPath)) return resolve();
      if (Date.now() >= deadline) return reject(new Error('replacement daemon cleanup timed out'));
      setTimeout(check, 25);
    };
    check();
  });
  console.log(JSON.stringify({ outcome: 'pass', daemonVersion: '0.10.26', apiVersion: 3, stalePid, newPid: replacementPid, cleaned: true, result }));
} finally {
  for (const pid of [replacementPid, stalePid]) {
    if (!Number.isSafeInteger(pid) || pid <= 1) continue;
    try { process.kill(pid, 'SIGKILL'); } catch {}
  }
  if (previousPath === undefined) delete process.env.PATH;
  else process.env.PATH = previousPath;
  delete process.env.RELAYFILE_BIN;
  rmSync(root, { recursive: true, force: true });
}
}

await run();
