#!/usr/bin/env python3
"""Install the persona's skills from an immutable, SHA-256 checked manifest."""
import hashlib
import json
import os
from pathlib import Path
import urllib.request

lane = Path(os.environ.get('RELAYFILE_AGENT_LANE', '/opt/agent-host'))
home = Path(os.environ.get('RELAYFILE_AGENT_HOME', str(Path.home())))
manifest = json.loads((lane / 'nango-skills.lock.json').read_text())
downloaded = []
for entry in manifest['files']:
    relative = Path(entry['path'])
    if relative.is_absolute() or '..' in relative.parts:
        raise SystemExit('Unsafe skill path')
    with urllib.request.urlopen(entry['url'], timeout=30) as response:
        body = response.read()
    if hashlib.sha256(body).hexdigest() != entry['sha256']:
        raise SystemExit('Nango skill checksum mismatch: ' + entry['path'])
    downloaded.append((relative, body))
# Fetch and verify everything before replacing any installed skill file.
for root in [home / '.claude/skills', home / '.codex/skills']:
    for relative, body in downloaded:
        target = root / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(body)
        target.chmod(0o644)
