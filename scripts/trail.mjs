import { createRequire } from 'node:module';
import { spawnSync } from 'node:child_process';
import { dirname, join } from 'node:path';

const require = createRequire(import.meta.url);
const packageEntry = require.resolve('agent-trajectories');
const cliEntry = join(dirname(packageEntry), 'cli', 'index.js');
const result = spawnSync(process.execPath, [cliEntry, ...process.argv.slice(2)], {
  env: {
    ...process.env,
    TRAJECTORIES_PROJECT: 'AgentWorkforce/relayfile',
  },
  stdio: 'inherit',
});

if (result.error) {
  throw result.error;
}
process.exit(result.status ?? 1);
