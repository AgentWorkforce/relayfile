#!/bin/bash
# Production aliases required by cloud's Nango persona. No credentials in argv
# or logs. The kit writes this root-owned file mode 0600 with owner agent.
set -euo pipefail
python3 - <<'PY'
from pathlib import Path
p = Path('/run/agent-host/secrets.env')
lines = p.read_text().splitlines()
key = next((line.split('=', 1)[1] for line in lines if line.startswith('NANGO_SECRET_KEY=')), '')
if not key:
    raise SystemExit('Nango integrations credential is missing')
names = ('NANGO_PRODUCTION_KEY', 'NANGO_SECRET_KEY_PRODUCTION', 'NANGO_SECRET_KEY_DEV')
lines = [line for line in lines if line.split('=', 1)[0] not in names]
p.write_text('\n'.join(lines + [name + '=' + key for name in names]) + '\n')
p.chmod(0o600)
PY
