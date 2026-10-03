import { readFile } from "node:fs/promises";
import { isAbsolute, resolve } from "node:path";
import {
  CAPABILITIES,
  CONTROL_OPERATIONS,
  type Capability,
  type ControlOperation,
  type Profile,
  type ResolvedTarget,
  type TargetFile,
} from "./types.js";

const capabilitySet = new Set<string>(CAPABILITIES);
const operationSet = new Set<string>(CONTROL_OPERATIONS);
const runtimeKinds = new Set(["go-oracle", "cloudflare-do", "terse-durable-actors"]);

export async function loadTarget(
  input: string,
  env: NodeJS.ProcessEnv = process.env,
): Promise<{ file: TargetFile; target: ResolvedTarget }> {
  const sourcePath = targetPath(input);
  const file = JSON.parse(await readFile(sourcePath, "utf8")) as TargetFile;
  validateTargetFile(file, sourcePath);

  const local = file.auth.strategy === "local-rs256";
  const baseUrl = resolveValue(file.baseUrl, file.baseUrlEnv, env);
  const primary = resolveValue(file.workspaces.primary, file.workspaces.primaryEnv, env);
  const secondary = resolveValue(file.workspaces.secondary, file.workspaces.secondaryEnv, env);
  const runtimeVersion = resolveValue(file.runtime.version, file.runtime.versionEnv, env);
  const relayfileSha = resolveValue(file.relayfileSha, file.relayfileShaEnv, env);

  if (!local) {
    requireResolved("base URL", baseUrl, sourcePath);
    requireResolved("primary workspace", primary, sourcePath);
    requireResolved("secondary workspace", secondary, sourcePath);
    requireResolved("runtime version/SHA", runtimeVersion, sourcePath);
    requireResolved("Relayfile SHA", relayfileSha, sourcePath);
  }
  if (baseUrl) requireHttpUrl("base URL", baseUrl, sourcePath);

  const tokens = local
    ? {}
    : {
        primary: envValue(file.auth.primaryTokenEnv, env),
        secondary: envValue(file.auth.secondaryTokenEnv, env),
        pathScoped: envValue(file.auth.pathScopedTokenEnv, env),
        readOnly: envValue(file.auth.readOnlyTokenEnv, env),
      };
  if (!local && !tokens.primary) {
    throw new Error(
      `target ${file.id} is unconfigured: ${file.auth.primaryTokenEnv ?? "primary token env"} is required`,
    );
  }
  if (!local && file.capabilities.includes("tenant-auth")) {
    for (const [label, value] of [
      [file.auth.secondaryTokenEnv ?? "secondary token env", tokens.secondary],
      [file.auth.pathScopedTokenEnv ?? "path-scoped token env", tokens.pathScoped],
      [file.auth.readOnlyTokenEnv ?? "read-only token env", tokens.readOnly],
    ] as const) {
      if (!value) throw new Error(`target ${file.id} is unconfigured: ${label} is required for tenant-auth`);
    }
  }
  if (file.runtime.kind === "terse-durable-actors") {
    const secretName = file.auth.runtimeSharedSecretEnv;
    if (!secretName || !envValue(secretName, env)) {
      throw new Error(
        `target ${file.id} is unconfigured: ${secretName ?? "auth.runtimeSharedSecretEnv"} is required; Terse must fail closed`,
      );
    }
  }

  let control: ResolvedTarget["control"];
  if (file.control) {
    const controlBaseUrl = resolveValue(file.control.baseUrl, file.control.baseUrlEnv, env);
    requireResolved("control adapter URL", controlBaseUrl, sourcePath);
    requireHttpUrl("control adapter URL", controlBaseUrl!, sourcePath);
    const controlToken = envValue(file.control.tokenEnv, env);
    if (!controlToken) {
      throw new Error(`target ${file.id} is unconfigured: ${file.control.tokenEnv ?? "control token env"} is required`);
    }
    control = {
      baseUrl: controlBaseUrl!,
      ...(controlToken ? { token: controlToken } : {}),
      operations: new Set(file.control.operations),
    };
  }

  return {
    file,
    target: {
      id: file.id,
      runtime: {
        kind: file.runtime.kind,
        version: runtimeVersion || "unknown",
      },
      relayfileSha: relayfileSha || "unknown",
      baseUrl: trimSlash(baseUrl || "http://127.0.0.1:19090"),
      workspaces: {
        primary: primary || "conformance-primary",
        secondary: secondary || "conformance-secondary",
      },
      tokens,
      capabilities: new Set(file.capabilities),
      ...(control ? { control } : {}),
      sourcePath,
    },
  };
}

export function assertFullRunSafety(
  target: ResolvedTarget,
  profile: Profile,
  env: NodeJS.ProcessEnv = process.env,
  locallySpawned = false,
): void {
  if (profile !== "full") return;
  if (locallySpawned && target.runtime.kind === "go-oracle") return;
  if (!target.control?.operations.has("reset")) {
    throw new Error(`target ${target.id} full profile requires the reset control operation`);
  }
  if (env.RELAYFILE_CONFORMANCE_DISPOSABLE !== "1") {
    throw new Error(
      `target ${target.id} full profile requires RELAYFILE_CONFORMANCE_DISPOSABLE=1; use dedicated disposable workspaces`,
    );
  }
}

function targetPath(input: string): string {
  if (isAbsolute(input) || input.includes("/") || input.endsWith(".json")) {
    return resolve(input);
  }
  return resolve("test/conformance/targets", `${input}.json`);
}

function resolveValue(
  literal: string | undefined,
  envName: string | undefined,
  env: NodeJS.ProcessEnv,
): string | undefined {
  return literal?.trim() || envValue(envName, env);
}

function envValue(name: string | undefined, env: NodeJS.ProcessEnv): string | undefined {
  if (!name) return undefined;
  const value = env[name]?.trim();
  return value || undefined;
}

function requireResolved(label: string, value: string | undefined, sourcePath: string): void {
  if (!value) throw new Error(`target ${sourcePath} is unconfigured: ${label} is required`);
}

function requireHttpUrl(label: string, value: string, sourcePath: string): void {
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error(`target ${sourcePath} has invalid ${label}: ${value}`);
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    throw new Error(`target ${sourcePath} ${label} must use http or https`);
  }
}

function trimSlash(value: string): string {
  return value.replace(/\/+$/u, "");
}

function validateTargetFile(file: TargetFile, sourcePath: string): void {
  if (file.schemaVersion !== 1) throw new Error(`${sourcePath}: unsupported schemaVersion`);
  if (!file.id?.trim()) throw new Error(`${sourcePath}: id is required`);
  if (!file.runtime?.kind) throw new Error(`${sourcePath}: runtime.kind is required`);
  if (!runtimeKinds.has(file.runtime.kind)) throw new Error(`${sourcePath}: unsupported runtime.kind ${file.runtime.kind}`);
  if (!file.auth?.strategy) throw new Error(`${sourcePath}: auth.strategy is required`);
  if (!Array.isArray(file.capabilities)) throw new Error(`${sourcePath}: capabilities must be an array`);

  const unknownCapabilities = file.capabilities.filter((item) => !capabilitySet.has(item));
  if (unknownCapabilities.length > 0) {
    throw new Error(`${sourcePath}: unknown capabilities: ${unknownCapabilities.join(", ")}`);
  }
  const duplicates = duplicateValues(file.capabilities);
  if (duplicates.length > 0) throw new Error(`${sourcePath}: duplicate capabilities: ${duplicates.join(", ")}`);

  if (file.control) {
    const unknownOperations = file.control.operations.filter((item) => !operationSet.has(item));
    if (unknownOperations.length > 0) {
      throw new Error(`${sourcePath}: unknown control operations: ${unknownOperations.join(", ")}`);
    }
    for (const capability of capabilitiesRequiringControl(file.capabilities)) {
      for (const required of requiredOperations(capability)) {
        if (!file.control.operations.includes(required)) {
          throw new Error(`${sourcePath}: ${capability} requires control operation ${required}`);
        }
      }
    }
  } else if (file.auth.strategy !== "local-rs256") {
    const controlled = capabilitiesRequiringControl(file.capabilities);
    if (controlled.length > 0) {
      throw new Error(`${sourcePath}: capabilities require a control adapter: ${controlled.join(", ")}`);
    }
  }
}

function capabilitiesRequiringControl(capabilities: Capability[]): Capability[] {
  return capabilities.filter((capability) => requiredOperations(capability).length > 0);
}

function requiredOperations(capability: Capability): ControlOperation[] {
  switch (capability) {
    case "durable-restart": return ["runtime.restart"];
    case "provider-faults": return ["provider.configure", "provider.calls"];
    case "state-inspection": return ["state.inspect"];
    case "clock-control": return ["clock.advance", "state.inspect"];
    case "runtime-eviction": return ["runtime.evict"];
    case "runtime-auth-probe": return ["auth.probe"];
    case "runtime-crash": return ["runtime.crash"];
    case "runtime-failover": return ["runtime.failover"];
    case "state-migration": return ["state.export", "state.import"];
    case "mount-control": return ["mount.start", "mount.stop"];
    default: return [];
  }
}

function duplicateValues<T>(values: T[]): T[] {
  const seen = new Set<T>();
  const duplicates = new Set<T>();
  for (const value of values) {
    if (seen.has(value)) duplicates.add(value);
    seen.add(value);
  }
  return [...duplicates];
}
