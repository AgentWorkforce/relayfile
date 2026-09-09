import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const packageRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const workspaceMount = join(packageRoot, "dist", "workspace-mount.js");
const tempRoot = mkdtempSync(join(tmpdir(), "relayfile-sdk-bun-"));
const entryPath = join(tempRoot, "entry.mjs");
const outputPath = join(tempRoot, "workspace-mount-probe");
const cachedBinaryPath = join(tempRoot, ".agent-relay", "bin", "relayfile-mount");
const cachedVersionPath = join(tempRoot, ".agent-relay", "bin", "relayfile-mount.version");
const expectedVersion = JSON.parse(readFileSync(join(packageRoot, "package.json"), "utf8")).version;

try {
  mkdirSync(join(tempRoot, ".agent-relay", "bin"), { recursive: true });
  writeFileSync(
    cachedBinaryPath,
    "#!/bin/sh\nfor arg in \"$@\"; do\n  [ \"$arg\" = \"--once\" ] && exit 0\ndone\nsleep 30\n",
    "utf8"
  );
  chmodSync(cachedBinaryPath, 0o755);
  writeFileSync(cachedVersionPath, `${expectedVersion}\n`, "utf8");
  writeFileSync(
    entryPath,
    `import { ensureRelayfileMount } from ${JSON.stringify(workspaceMount)};\n` +
      `const handle = await ensureRelayfileMount({ relayfileUrl: "https://relayfile.mount.test", workspace: "bun-probe", token: "probe" });\n` +
      `await handle.stop();\n` +
      `console.log("workspace-mount-loaded");\n`,
    "utf8"
  );

  execFileSync("bun", ["build", entryPath, "--compile", "--outfile", outputPath], {
    cwd: tempRoot,
    stdio: "inherit",
  });

  const output = execFileSync(outputPath, [], {
    cwd: "/tmp",
    env: { ...process.env, HOME: tempRoot },
    encoding: "utf8",
  });

  if (!output.includes("workspace-mount-loaded")) {
    throw new Error(`compiled workspace-mount probe did not load: ${output}`);
  }

  console.log("compiled Bun workspace-mount probe passed");
} finally {
  rmSync(tempRoot, { recursive: true, force: true });
}
