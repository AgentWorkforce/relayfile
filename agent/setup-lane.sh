#!/bin/bash
# Idempotent lane setup. Credentials remain in the kit's runtime files.
set -euo pipefail
if ! node -e 'process.exit(Number(process.versions.node.split(".")[0]) >= 24 ? 0 : 1)'; then
  curl -fsSL https://deb.nodesource.com/setup_24.x | bash -
  apt-get install -y nodejs
fi
sudo -H -u agent git config --global url."https://github.com/".insteadOf git@github.com:
sudo -H -u agent python3 /opt/agent-host/install-skills.py
sudo -H -u agent python3 - <<'PY'
import json
from pathlib import Path
p = Path.home() / '.claude.json'
d = json.loads(p.read_text()) if p.exists() else {}
d.setdefault('mcpServers', {})['nango'] = {'type': 'http', 'url': 'https://nango.dev/docs/mcp'}
p.write_text(json.dumps(d, indent=2) + '\n')
p.chmod(0o600)
PY
