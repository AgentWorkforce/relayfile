import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import {
  mkdirSync,
  mkdtempSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const packageEntry = fileURLToPath(import.meta.resolve("agent-trajectories"));
const trailCli = join(dirname(packageEntry), "cli", "index.js");

function run(command, args, cwd, env = process.env) {
  return execFileSync(command, args, {
    cwd,
    env,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
}

function findFile(root, fileName) {
  for (const entry of readdirSync(root, { withFileTypes: true })) {
    const entryPath = join(root, entry.name);
    if (entry.isDirectory()) {
      const match = findFile(entryPath, fileName);
      if (match) return match;
    } else if (entry.name === fileName) {
      return entryPath;
    }
  }
  return undefined;
}

function listFiles(root) {
  return readdirSync(root, { withFileTypes: true }).flatMap((entry) => {
    const entryPath = join(root, entry.name);
    return entry.isDirectory() ? listFiles(entryPath) : [entryPath];
  });
}

test("Trail upgrades legacy storage without leaking absolute paths into new metadata", () => {
  const repoDir = mkdtempSync(join(tmpdir(), "relayfile-trail-portability-"));

  try {
    run("git", ["init", "--quiet"], repoDir);
    run(
      "git",
      [
        "remote",
        "add",
        "origin",
        "git@github.com:AgentWorkforce/relayfile.git",
      ],
      repoDir,
    );

    const legacyTrajectoryDir = join(
      repoDir,
      ".trajectories",
      "completed",
      "2026-01",
    );
    mkdirSync(legacyTrajectoryDir, { recursive: true });
    const legacyTrajectoryPath = join(
      legacyTrajectoryDir,
      "traj_legacy000001.json",
    );
    const legacyTrajectory = {
      id: "traj_legacy000001",
      version: 1,
      task: { title: "Legacy trajectory" },
      status: "completed",
      startedAt: "2026-01-01T00:00:00.000Z",
      completedAt: "2026-01-01T00:01:00.000Z",
      agents: [],
      chapters: [],
      commits: [],
      filesChanged: [],
      projectId: repoDir,
      tags: [],
    };
    const legacyContents = `${JSON.stringify(legacyTrajectory, null, 2)}\n`;
    writeFileSync(legacyTrajectoryPath, legacyContents);
    writeFileSync(
      join(repoDir, ".trajectories", "index.json"),
      `${JSON.stringify(
        {
          version: 1,
          trajectories: {
            [legacyTrajectory.id]: {
              id: legacyTrajectory.id,
              path: legacyTrajectoryPath,
              status: legacyTrajectory.status,
            },
          },
        },
        null,
        2,
      )}\n`,
    );

    const env = { ...process.env };
    delete env.TRAJECTORIES_DATA_DIR;
    delete env.TRAJECTORIES_PROJECT;
    delete env.TRAJECTORIES_SEARCH_PATHS;

    const trajectoryId = run(
      process.execPath,
      [trailCli, "start", "Portable trajectory", "--quiet"],
      repoDir,
      env,
    );
    assert.match(trajectoryId, /^traj_[a-z0-9]+$/);

    const dataDir = join(repoDir, ".agentworkforce", "trajectories");
    const migratedLegacyPath = join(
      dataDir,
      "completed",
      "2026-01",
      "traj_legacy000001.json",
    );
    assert.equal(
      readFileSync(migratedLegacyPath, "utf8"),
      legacyContents,
      "existing history must be preserved byte-for-byte during migration",
    );
    assert.equal(
      findFile(dataDir, "index.json"),
      undefined,
      "the absolute-path index must not be recreated",
    );

    const activePath = join(
      dataDir,
      "active",
      trajectoryId,
      "trajectory.json",
    );
    const activeContents = readFileSync(activePath, "utf8");
    const activeTrajectory = JSON.parse(activeContents);
    assert.equal(activeTrajectory.projectId, "AgentWorkforce/relayfile");
    assert.equal(activeContents.includes(repoDir), false);

    run(
      process.execPath,
      [
        trailCli,
        "complete",
        "--summary",
        "Verified portable metadata",
        "--approach",
        "Regression test",
        "--confidence",
        "1",
      ],
      repoDir,
      env,
    );

    const completedPath = findFile(
      join(dataDir, "completed"),
      "trajectory.json",
    );
    assert.ok(completedPath, "completed trajectory metadata must be written");
    const completedContents = readFileSync(completedPath, "utf8");
    const completedTrajectory = JSON.parse(completedContents);
    assert.equal(completedTrajectory.id, trajectoryId);
    assert.equal(completedTrajectory.projectId, "AgentWorkforce/relayfile");
    assert.equal(completedContents.includes(repoDir), false);
    assert.equal(findFile(dataDir, "index.json"), undefined);
    for (const metadataPath of listFiles(dataDir)) {
      if (metadataPath === migratedLegacyPath) continue;
      assert.equal(
        readFileSync(metadataPath, "utf8").includes(repoDir),
        false,
        `new Trail metadata leaked the checkout path: ${metadataPath}`,
      );
    }
  } finally {
    rmSync(repoDir, { recursive: true, force: true });
  }
});
