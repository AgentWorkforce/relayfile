#!/usr/bin/env node

const { execFileSync } = require("child_process");
const fs = require("fs");
const path = require("path");

const repoRoot = path.resolve(__dirname, "..", "..", "..");
const packageRoot = path.resolve(__dirname, "..");
const binDir = path.join(packageRoot, "bin");

const targets = [
  { goos: "darwin", goarch: "amd64" },
  { goos: "darwin", goarch: "arm64" },
  { goos: "linux", goarch: "amd64" },
  { goos: "linux", goarch: "arm64" },
  { goos: "windows", goarch: "amd64", ext: ".exe" },
  { goos: "windows", goarch: "arm64", ext: ".exe" },
];

fs.rmSync(binDir, { recursive: true, force: true });
fs.mkdirSync(binDir, { recursive: true });

for (const target of targets) {
  const filename = `relayfile-cli-${target.goos}-${target.goarch}${target.ext || ""}`;
  const output = path.join(binDir, filename);
  console.log(`Building ${filename}`);
  execFileSync(
    "go",
    [
      "build",
      "-trimpath",
      "-ldflags=-s -w",
      "-o",
      output,
      "./cmd/relayfile-cli",
    ],
    {
      cwd: repoRoot,
      env: {
        ...process.env,
        CGO_ENABLED: "0",
        GOOS: target.goos,
        GOARCH: target.goarch,
      },
      stdio: "inherit",
    }
  );
  if (target.goos !== "windows") {
    fs.chmodSync(output, 0o755);
  }
}
