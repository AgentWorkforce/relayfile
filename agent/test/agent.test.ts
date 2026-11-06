import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { gunzipSync } from "node:zlib";
import { execFileSync } from "node:child_process";
import { decide, fileReader, hostBoundaryPolicy, hostRolePolicy, userDataBase64 } from "@agentworkforce/layered-agent";
import agent from "../agent.config.ts";

const read = fileReader(fileURLToPath(new URL("..", import.meta.url)));

test("fresh RelayFile host: owned resources, repositories and bounded runtime secrets", () => {
  assert.equal(agent.names.computeStack, "layered-agent-relayfile-dev");
  assert.equal(agent.names.platformPrefix, "/relaycast-agent-host/dev");
  assert.deepEqual(agent.input.repos, ["AgentWorkforce/relayfile", "AgentWorkforce/relayfile-cloud", "AgentWorkforce/cloud", "AgentWorkforce/relayfile-adapters"]);
  assert.equal(agent.input.workers?.maxConcurrent, 2);
  const expected = [
    "/relaycast-agent-host/dev/anthropic-api-key",
    "/relaycast-agent-host/dev/nango-secret-key",
    "/relayfile/dev/nango-production-key",
  ];
  assert.deepEqual(agent.secrets.map(s => s.param), expected);
  assert.ok(agent.secrets.every(s => !s.write));
  for (const policy of [hostRolePolicy(agent, "500268966557"), hostBoundaryPolicy(agent, "500268966557")]) {
    const reads = policy.Statement.filter(s => s.Action.includes("ssm:GetParameter")).flatMap(s => [s.Resource].flat());
    for (const param of expected) assert.ok(reads.includes("arn:aws:ssm:us-west-2:500268966557:parameter" + param));
    assert.ok(!reads.some(r => r.endsWith("*")));
  }
});

test("boot includes the persona and skill installer within EC2's user-data budget", () => {
  const encoded = userDataBase64(agent, read);
  console.log(`RelayFile user data: ${encoded.length} bytes base64`);
  assert.ok(encoded.length + 500 < 23_600, `user data plus 500-byte compression slack exceeds budget: ${encoded.length}`);
  const boot = gunzipSync(Buffer.from(encoded, "base64")).toString("utf8");
  for (const name of agent.input.laneFiles) assert.ok(boot.includes(`/opt/agent-host/${name}`), name);
  assert.doesNotMatch(boot, /sk-ant-|ghp_|ghs_|gho_|github_pat_|BEGIN [A-Z ]*PRIVATE KEY/);
  const lock = JSON.parse(readFileSync(new URL("../nango-skills.lock.json", import.meta.url), "utf8"));
  assert.equal(lock.files.length, 3);
  for (const f of lock.files) {
    assert.match(f.url, new RegExp(`^https://raw\\.githubusercontent\\.com/NangoHQ/skills/${lock.commit}/skills/`));
    assert.match(f.sha256, /^[0-9a-f]{64}$/);
  }
});

test("first deployment is full, with no attempt to touch an absent host", async () => {
  const result = await decide(agent, read, {
    async getTemplate() { return undefined; },
    async getInstanceId() { throw new Error("must not look up absent host"); },
    async getStackParameterResolvedValue() { throw new Error("must not inspect absent stack"); },
    async getSsmParameter() { throw new Error("not needed"); },
    async run() { throw new Error("must not run on absent host"); },
  });
  assert.equal(result.kind, "full");
});

test("skill installation verifies the entire download before writing either harness directory", () => {
  const installer = fileURLToPath(new URL("../install-skills.py", import.meta.url));
  execFileSync("python3", ["-c", `
import hashlib, io, json, os, runpy, sys, tempfile
from pathlib import Path
from unittest.mock import patch
body = b'checked skill content'
with tempfile.TemporaryDirectory() as tmp:
    root = Path(tmp)
    lane, home = root / 'lane', root / 'home'
    lane.mkdir()
    (lane / 'trigger-autocomplete-catalog.md').write_text('local trigger skill')
    files = [{'path':'building-nango-functions-locally/SKILL.md','url':'https://example.invalid/skill','sha256':hashlib.sha256(body).hexdigest()},
             {'path':'building-nango-functions-locally/references/syncs.md','url':'https://example.invalid/ref','sha256':'0' * 64}]
    manifest = lane / 'nango-skills.lock.json'
    manifest.write_text(json.dumps({'files':files}))
    os.environ.update(RELAYFILE_AGENT_LANE=str(lane), RELAYFILE_AGENT_HOME=str(home))
    with patch('urllib.request.urlopen', side_effect=lambda *args, **kwargs: io.BytesIO(body)):
        try:
            runpy.run_path(sys.argv[1], run_name='__main__')
        except SystemExit as error:
            assert 'checksum mismatch' in str(error)
        else:
            raise AssertionError('tampered download was accepted')
    assert not home.exists(), 'no partial installation after checksum failure'
    files[1]['sha256'] = hashlib.sha256(body).hexdigest()
    manifest.write_text(json.dumps({'files':files}))
    with patch('urllib.request.urlopen', side_effect=lambda *args, **kwargs: io.BytesIO(body)):
        runpy.run_path(sys.argv[1], run_name='__main__')
    for harness in ['.claude', '.codex']:
        assert (home / harness / 'skills/building-nango-functions-locally/SKILL.md').read_bytes() == body
        assert (home / harness / 'skills/building-nango-functions-locally/references/syncs.md').read_bytes() == body
        assert (home / harness / 'skills/trigger-autocomplete-catalog/SKILL.md').read_text() == 'local trigger skill'
`, installer]);
});
