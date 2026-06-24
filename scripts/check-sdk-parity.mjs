#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const manifestPath = path.join(root, "packages/sdk/parity.json");
const tsSrcDir = path.join(root, "packages/sdk/typescript/src");
const tsIndexPath = path.join(tsSrcDir, "index.ts");
const pyInitPath = path.join(root, "packages/sdk/python/src/relayfile/__init__.py");

const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
const allowedStatuses = new Set(manifest.statuses ?? []);
const tsExports = collectTypeScriptExports(tsSrcDir);
const tsEntrypointExports = collectTypeScriptModuleExports(tsIndexPath, tsSrcDir);
const pyExports = collectPythonAll(pyInitPath);
const classifiedTsExports = new Set();
const errors = [];

for (const capability of manifest.capabilities ?? []) {
  if (!capability.id) {
    errors.push("capability is missing id");
    continue;
  }
  if (!allowedStatuses.has(capability.status)) {
    errors.push(`${capability.id}: unknown status ${JSON.stringify(capability.status)}`);
    continue;
  }

  for (const symbol of capability.tsExports ?? []) {
    classifiedTsExports.add(symbol);
    if (!tsExports.has(symbol)) {
      errors.push(`${capability.id}: missing TypeScript export ${symbol}`);
    }
  }

  if (capability.status === "both") {
    for (const symbol of capability.pyExports ?? []) {
      if (!pyExports.has(symbol)) {
        errors.push(`${capability.id}: missing Python export ${symbol}`);
      }
    }
    if ((capability.tsExports ?? []).length === 0) {
      errors.push(`${capability.id}: both capability must list tsExports`);
    }
    if ((capability.pyExports ?? []).length === 0) {
      errors.push(`${capability.id}: both capability must list pyExports`);
    }
  }
}

for (const symbol of [...tsEntrypointExports].sort()) {
  if (!classifiedTsExports.has(symbol)) {
    errors.push(`unclassified TypeScript public export ${symbol}`);
  }
}

if (errors.length > 0) {
  console.error("SDK parity check failed:");
  for (const error of errors) {
    console.error(`- ${error}`);
  }
  process.exit(1);
}

console.log("SDK parity check passed");

function collectTypeScriptExports(srcDir) {
  const exports = new Set();
  for (const filePath of listFiles(srcDir, ".ts")) {
    if (filePath.endsWith(".test.ts") || filePath.endsWith(".d.ts")) {
      continue;
    }
    const source = fs.readFileSync(filePath, "utf8");
    for (const match of source.matchAll(/\bexport\s+(?:async\s+)?(?:abstract\s+)?(?:class|function|const|interface|type)\s+([A-Za-z_$][\w$]*)/g)) {
      exports.add(match[1]);
    }
    for (const name of collectNamedReExports(source)) {
      exports.add(name);
    }
  }
  return exports;
}

function collectTypeScriptModuleExports(filePath, srcDir, seen = new Set()) {
  const resolvedPath = path.resolve(filePath);
  if (seen.has(resolvedPath)) {
    return new Set();
  }
  seen.add(resolvedPath);

  const source = fs.readFileSync(resolvedPath, "utf8");
  const exports = new Set();
  for (const match of source.matchAll(/\bexport\s+(?:async\s+)?(?:abstract\s+)?(?:class|function|const|interface|type)\s+([A-Za-z_$][\w$]*)/g)) {
    exports.add(match[1]);
  }
  for (const name of collectNamedReExports(source)) {
    exports.add(name);
  }
  for (const match of source.matchAll(/\bexport\s+\*\s+from\s*["']([^"']+)["']/g)) {
    const childPath = resolveTypeScriptSpecifier(resolvedPath, match[1], srcDir);
    if (!childPath) {
      continue;
    }
    for (const name of collectTypeScriptModuleExports(childPath, srcDir, seen)) {
      exports.add(name);
    }
  }
  return exports;
}

function collectNamedReExports(source) {
  const exports = new Set();
  for (const match of source.matchAll(/\bexport\s+(?:type\s+)?\{([\s\S]*?)\}\s*from\s*["'][^"']+["']/g)) {
    for (const raw of match[1].split(",")) {
      const cleaned = raw
        .replace(/\btype\s+/g, "")
        .trim();
      if (!cleaned) {
        continue;
      }
      const aliasMatch = cleaned.match(/\bas\s+([A-Za-z_$][\w$]*)$/);
      const name = aliasMatch ? aliasMatch[1] : cleaned.split(/\s+/)[0];
      if (/^[A-Za-z_$][\w$]*$/.test(name)) {
        exports.add(name);
      }
    }
  }
  return exports;
}

function resolveTypeScriptSpecifier(fromFile, specifier, srcDir) {
  if (!specifier.startsWith(".")) {
    return null;
  }
  const candidate = path.resolve(path.dirname(fromFile), specifier.replace(/\.js$/, ".ts"));
  const relative = path.relative(srcDir, candidate);
  if (relative.startsWith("..") || path.isAbsolute(relative)) {
    return null;
  }
  if (!fs.existsSync(candidate)) {
    return null;
  }
  return candidate;
}

function collectPythonAll(initPath) {
  const source = fs.readFileSync(initPath, "utf8");
  const match = source.match(/__all__\s*=\s*\[([\s\S]*?)\]/m);
  if (!match) {
    throw new Error(`Unable to find __all__ in ${initPath}`);
  }
  return new Set([...match[1].matchAll(/["']([^"']+)["']/g)].map((entry) => entry[1]));
}

function listFiles(dir, suffix) {
  const files = [];
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const resolved = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...listFiles(resolved, suffix));
    } else if (entry.isFile() && resolved.endsWith(suffix)) {
      files.push(resolved);
    }
  }
  return files;
}
