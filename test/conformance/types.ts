export const CAPABILITIES = [
  "public-api",
  "tenant-auth",
  "webhook-ingest",
  "event-cursor",
  "websocket-resume",
  "durable-restart",
  "provider-faults",
  "state-inspection",
  "clock-control",
  "runtime-eviction",
  "large-effect-batch",
  "runtime-auth-probe",
  "runtime-crash",
  "runtime-failover",
  "state-migration",
  "mount-control",
  "digest-projection",
] as const;

export type Capability = (typeof CAPABILITIES)[number];
export type Profile = "core" | "full";
export type TokenName = "primary" | "secondary" | "pathScoped" | "readOnly";

export const CONTROL_OPERATIONS = [
  "reset",
  "provider.configure",
  "provider.calls",
  "state.inspect",
  "clock.advance",
  "runtime.evict",
  "runtime.crash",
  "runtime.restart",
  "runtime.failover",
  "state.export",
  "state.import",
  "mount.start",
  "mount.stop",
] as const;

export type ControlOperation = (typeof CONTROL_OPERATIONS)[number];

export interface TargetFile {
  schemaVersion: 1;
  id: string;
  runtime: {
    kind: "go-oracle" | "cloudflare-do" | "terse-durable-actors";
    version?: string;
    versionEnv?: string;
  };
  relayfileSha?: string;
  relayfileShaEnv?: string;
  baseUrl?: string;
  baseUrlEnv?: string;
  workspaces: {
    primary?: string;
    primaryEnv?: string;
    secondary?: string;
    secondaryEnv?: string;
  };
  auth: {
    strategy: "local-rs256" | "environment";
    primaryTokenEnv?: string;
    secondaryTokenEnv?: string;
    pathScopedTokenEnv?: string;
    readOnlyTokenEnv?: string;
    runtimeSharedSecretEnv?: string;
  };
  capabilities: Capability[];
  control?: {
    baseUrl?: string;
    baseUrlEnv?: string;
    tokenEnv?: string;
    operations: ControlOperation[];
  };
  runtimeVerification?: {
    baseUrl?: string;
    baseUrlEnv?: string;
    adminKeyEnv: string;
    projectId?: string;
    projectIdEnv?: string;
    actorName?: string;
    actorNameEnv?: string;
    actorId?: string;
    actorIdEnv?: string;
  };
}

export interface ResolvedTarget {
  id: string;
  runtime: { kind: TargetFile["runtime"]["kind"]; version: string };
  relayfileSha: string;
  baseUrl: string;
  workspaces: { primary: string; secondary: string };
  tokens: Partial<Record<TokenName, string>>;
  capabilities: Set<Capability>;
  control?: {
    baseUrl: string;
    token?: string;
    operations: Set<ControlOperation>;
  };
  runtimeVerification?: {
    baseUrl: string;
    adminKey: string;
    projectId: string;
    actorName: string;
    actorId: string;
  };
  sourcePath: string;
}

export interface Exchange {
  correlationId: string;
  method: string;
  url: string;
  startedAt: string;
  durationMs: number;
  request: { headers: Record<string, string>; body?: unknown };
  response: { status: number; headers: Record<string, string>; body?: unknown };
}

export type CaseStatus = "passed" | "failed" | "skipped";

export interface CaseResult {
  id: string;
  name: string;
  status: CaseStatus;
  durationMs: number;
  requiredCapabilities: Capability[];
  correlationIds: string[];
  error?: string;
  skipReason?: string;
  skipKind?: "missing-capability" | "not-applicable";
  evidenceBasis?: "runtime-native" | "adapter-attested";
}

export interface EvidenceSummary {
  schemaVersion: 1;
  seed: string;
  profile: Profile;
  target: {
    id: string;
    baseUrl: string;
    runtime: ResolvedTarget["runtime"];
    relayfileSha: string;
    capabilities: Capability[];
  };
  startedAt: string;
  finishedAt: string;
  counts: Record<CaseStatus, number>;
  cases: CaseResult[];
}
