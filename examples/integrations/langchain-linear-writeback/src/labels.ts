import Ajv2020, { type ValidateFunction } from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

import type { RelayfileWorkspace } from "./bootstrap.js";

export const LABEL_SCHEMA_PATH = "/discovery/linear/labels/.schema.json";
export const LABEL_CREATE_EXAMPLE_PATH = "/discovery/linear/labels/.create.example.json";
export const LABEL_ROOT = "/linear/labels";

const ajv = new Ajv2020({ allErrors: true, strict: false });
addFormats(ajv);

let cachedValidator: ValidateFunction | null = null;

async function loadValidator(ws: RelayfileWorkspace): Promise<ValidateFunction> {
  if (cachedValidator) return cachedValidator;
  const file = await ws.client.readFile(ws.workspaceId, LABEL_SCHEMA_PATH);
  const schema = JSON.parse(file.content);
  cachedValidator = ajv.compile(schema);
  return cachedValidator;
}

export interface LabelCreatePayload {
  name: string;
  color?: string;
  description?: string;
  teamId?: string;
  parentId?: string;
}

export interface OpReceipt {
  opId: string;
  status: string;
  providerResult?: Record<string, unknown>;
  lastError?: string | null;
  attemptCount?: number;
}

async function pollOp(
  ws: RelayfileWorkspace,
  opId: string,
  opts: { intervalMs?: number; timeoutMs?: number } = {},
): Promise<OpReceipt> {
  const deadline = Date.now() + (opts.timeoutMs ?? 60_000);
  const interval = opts.intervalMs ?? 1_500;
  let lastStatus = "(no op observed yet)";
  let lastProviderResult: Record<string, unknown> | undefined;
  let lastError: string | null | undefined;
  let lastAttempt: number | undefined;

  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, interval));
    try {
      const op = await ws.client.getOp(ws.workspaceId, opId);
      lastStatus = op.status;
      lastProviderResult = op.providerResult;
      lastError = op.lastError;
      lastAttempt = op.attemptCount;
      if (op.status === "succeeded" || op.status === "failed" || op.status === "dead_lettered") {
        return {
          opId,
          status: op.status,
          providerResult: op.providerResult,
          lastError: op.lastError,
          attemptCount: op.attemptCount,
        };
      }
    } catch {
      // op may briefly 404 right after enqueue — keep polling
    }
  }
  return {
    opId,
    status: lastStatus,
    providerResult: lastProviderResult,
    lastError: lastError,
    attemptCount: lastAttempt,
  };
}

export interface CreatedLabel {
  draftPath: string;
  receipt: OpReceipt;
  externalId?: string;
  canonicalPath?: string;
  url?: string;
}

export async function createLabel(
  ws: RelayfileWorkspace,
  payload: LabelCreatePayload,
  opts: { intervalMs?: number; timeoutMs?: number } = {},
): Promise<CreatedLabel> {
  const validate = await loadValidator(ws);
  if (!validate(payload)) {
    throw new Error(
      `Label payload failed schema validation: ${JSON.stringify(validate.errors)}`,
    );
  }

  const draftId = `relayfile-writeback-test--${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
  const draftPath = `${LABEL_ROOT}/${draftId}.json`;

  const writeResult = await ws.client.writeFile({
    workspaceId: ws.workspaceId,
    path: draftPath,
    baseRevision: "*",
    contentType: "application/json",
    content: JSON.stringify(payload, null, 2),
  });

  if (!writeResult.writeback || writeResult.writeback.provider !== "linear") {
    return {
      draftPath,
      receipt: { opId: writeResult.opId, status: "(no provider writeback enqueued)" },
    };
  }

  const receipt = await pollOp(ws, writeResult.opId, opts);
  const externalId =
    receipt.providerResult && typeof receipt.providerResult.externalId === "string"
      ? (receipt.providerResult.externalId as string)
      : undefined;

  const canonicalPath = externalId ? `${LABEL_ROOT}/${externalId}.json` : undefined;
  const url =
    receipt.providerResult && typeof receipt.providerResult.url === "string"
      ? (receipt.providerResult.url as string)
      : undefined;

  return { draftPath, receipt, externalId, canonicalPath, url };
}

export async function readCanonicalLabel(
  ws: RelayfileWorkspace,
  canonicalPath: string,
): Promise<{ revision: string; record: Record<string, unknown> }> {
  const file = await ws.client.readFile(ws.workspaceId, canonicalPath);
  return { revision: file.revision, record: JSON.parse(file.content) };
}

export async function updateLabel(
  ws: RelayfileWorkspace,
  canonicalPath: string,
  baseRevision: string,
  patch: Partial<LabelCreatePayload>,
  opts: { intervalMs?: number; timeoutMs?: number } = {},
): Promise<{ targetRevision: string; receipt: OpReceipt }> {
  const result = await ws.client.writeFile({
    workspaceId: ws.workspaceId,
    path: canonicalPath,
    baseRevision,
    contentType: "application/json",
    content: JSON.stringify(patch, null, 2),
  });
  const receipt = await pollOp(ws, result.opId, opts);
  return { targetRevision: result.targetRevision, receipt };
}

export async function deleteLabel(
  ws: RelayfileWorkspace,
  canonicalPath: string,
  baseRevision: string,
  opts: { intervalMs?: number; timeoutMs?: number } = {},
): Promise<{ receipt: OpReceipt }> {
  const result = await ws.client.deleteFile({
    workspaceId: ws.workspaceId,
    path: canonicalPath,
    baseRevision,
  });
  const receipt = await pollOp(ws, result.opId, opts);
  return { receipt };
}

export async function deleteDraftIfPresent(
  ws: RelayfileWorkspace,
  draftPath: string,
): Promise<{ deleted: boolean; reason?: string }> {
  try {
    const cur = await ws.client.readFile(ws.workspaceId, draftPath);
    await ws.client.deleteFile({
      workspaceId: ws.workspaceId,
      path: draftPath,
      baseRevision: cur.revision,
    });
    return { deleted: true };
  } catch (err) {
    return { deleted: false, reason: (err as Error).message };
  }
}
