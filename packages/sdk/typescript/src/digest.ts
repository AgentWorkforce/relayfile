// TODO(workspace-primitives): replace with re-exports from @relayfile/core
// once the base digest contract lands there.

import type { ChangeEvent } from "./types.js";

export type DigestRuntime = "node20";

export interface DigestBullet {
  readonly text: string;
  readonly canonicalPath: string;
}

export interface DigestSection {
  readonly provider: string;
  readonly bullets: ReadonlyArray<DigestBullet>;
}

export interface DigestChangeEventFilter {
  readonly providers?: ReadonlyArray<string>;
  readonly paths?: ReadonlyArray<string>;
}

export interface DigestContext {
  readonly provider: string;
  readonly window: {
    readonly from: string;
    readonly to: string;
  };
  changeEvents(filter?: DigestChangeEventFilter): Promise<ReadonlyArray<ChangeEvent>>;
  readonly workspaceId: string;
  readonly functionId: string;
}

export type DigestHandler = (ctx: DigestContext) => Promise<DigestSection | null>;

export interface DigestSourceFile {
  readonly path: string;
  readonly contents: string;
}

export interface DigestFunctionSource {
  readonly runtime: DigestRuntime;
  readonly entrypoint: string;
  readonly files: ReadonlyArray<DigestSourceFile>;
}

export interface DeployDigestFunctionInput {
  readonly slug: string;
  readonly displayName?: string | null;
  readonly source: DigestFunctionSource;
}

export interface DigestFunctionDeployResponse {
  readonly digestFunctionId: string;
  readonly version: number;
  readonly status: string;
  readonly sha256: string;
}

export interface DigestFunctionSummary {
  readonly digestFunctionId: string;
  readonly name: string;
  readonly version: number;
  readonly status: string;
  readonly sha256: string;
  readonly bytes: number;
  readonly createdAt: string;
}

export interface DigestFunctionListResponse {
  readonly digestFunctions: ReadonlyArray<DigestFunctionSummary>;
  readonly nextCursor: string | null;
}

export interface DigestFunctionDetail extends DigestFunctionSummary {
  readonly workspaceId: string;
  readonly entrypoint: string;
  readonly runtime: DigestRuntime | string;
  readonly updatedAt: string;
}

export interface DigestFunctionDisableResponse {
  readonly digestFunctionId: string;
  readonly status: "disabled";
  readonly disabledAt: string;
  readonly alreadyDisabled: boolean;
}

export interface DigestFunctionLogEntry {
  readonly invocationId: string;
  readonly occurredAt: string;
  readonly level: string;
  readonly message: string;
  readonly durationMs?: number;
}

export interface DigestFunctionLogsResponse {
  readonly digestFunctionId: string;
  readonly logs: ReadonlyArray<DigestFunctionLogEntry>;
  readonly nextCursor: string | null;
}

export interface ListDigestFunctionsOptions {
  readonly cursor?: string;
  readonly limit?: number;
}

export interface GetDigestFunctionLogsOptions {
  readonly since?: string | Date;
  readonly limit?: number;
}

export function digestFunctionSourceFromText(input: {
  readonly path: string;
  readonly contents: string;
  readonly runtime?: DigestRuntime;
  readonly entrypoint?: string;
}): DigestFunctionSource {
  const path = input.path.replace(/^\/+/, "");
  return {
    runtime: input.runtime ?? "node20",
    entrypoint: input.entrypoint ?? path,
    files: [
      {
        path,
        contents: input.contents
      }
    ]
  };
}
