import type { RelayFileClient } from "@relayfile/sdk";
import Ajv2020, { type ValidateFunction } from "ajv/dist/2020.js";
import addFormats from "ajv-formats";

const ajv = new Ajv2020({ allErrors: true, strict: false });
addFormats(ajv);

const schemaCache = new Map<string, ValidateFunction>();

export interface OpReceipt {
  opId: string;
  status: string;
  providerResult?: Record<string, unknown>;
  lastError?: string | null;
  attemptCount?: number;
}

async function pollOp(
  client: RelayFileClient,
  workspaceId: string,
  opId: string,
  opts: { intervalMs?: number; timeoutMs?: number } = {},
): Promise<OpReceipt> {
  const deadline = Date.now() + (opts.timeoutMs ?? 60_000);
  const interval = opts.intervalMs ?? 1_500;
  let last: OpReceipt = { opId, status: "(no op observed)" };
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, interval));
    try {
      const op = await client.getOp(workspaceId, opId);
      last = {
        opId,
        status: op.status,
        providerResult: op.providerResult,
        lastError: op.lastError,
        attemptCount: op.attemptCount,
      };
      if (op.status === "succeeded" || op.status === "failed" || op.status === "dead_lettered") {
        return last;
      }
    } catch {
      // op may briefly 404 right after enqueue
    }
  }
  return last;
}

export interface CreateResult {
  /** The non-canonical draft filename we wrote to. */
  draftPath: string;
  receipt: OpReceipt;
  /** Provider's returned id (e.g. Linear UUID). */
  externalId?: string;
  /** Path under the same root where the canonical record will materialise. */
  canonicalPath?: string;
  /** Provider-side URL, if returned. */
  url?: string;
}

export interface WritebackApi {
  /**
   * Create a provider record by writing a schema-validated draft.
   * Polls the resulting op to `succeeded`, returns `providerResult.externalId`
   * and the conventional canonical path `{root}/{externalId}.json`.
   *
   * @param root  e.g. `/linear/labels`
   * @param payload  must validate against `{root}/.schema.json`
   */
  create<T extends Record<string, unknown>>(
    root: string,
    payload: T,
    opts?: { intervalMs?: number; timeoutMs?: number },
  ): Promise<CreateResult>;
  /** PATCH an existing canonical record. Polls the resulting op. */
  update<T extends Record<string, unknown>>(
    canonicalPath: string,
    baseRevision: string,
    patch: T,
    opts?: { intervalMs?: number; timeoutMs?: number },
  ): Promise<{ targetRevision: string; receipt: OpReceipt }>;
  /** Delete a canonical record. Polls the resulting op. */
  delete(
    canonicalPath: string,
    baseRevision: string,
    opts?: { intervalMs?: number; timeoutMs?: number },
  ): Promise<{ receipt: OpReceipt }>;
  /** Delete an orphan draft receipt (local-only Relayfile cleanup, no provider effect). */
  deleteDraft(draftPath: string): Promise<{ deleted: boolean; reason?: string }>;
}

async function loadSchema(
  client: RelayFileClient,
  workspaceId: string,
  schemaPath: string,
): Promise<ValidateFunction> {
  const cached = schemaCache.get(schemaPath);
  if (cached) return cached;
  const file = await client.readFile(workspaceId, schemaPath);
  const schema = JSON.parse(file.content);
  const validate = ajv.compile(schema);
  schemaCache.set(schemaPath, validate);
  return validate;
}

export function createWriteback(client: RelayFileClient, workspaceId: string): WritebackApi {
  return {
    async create(root, payload, opts) {
      // Discovery schema lives under /discovery/<provider>/<resource>/ even
      // though the writeback target is /<provider>/<resource>/. Mirror the
      // adapter convention.
      const schemaPath = `/discovery${root}/.schema.json`;
      const validate = await loadSchema(client, workspaceId, schemaPath);
      if (!validate(payload)) {
        throw new Error(
          `Payload for ${root} failed schema validation: ${JSON.stringify(validate.errors)}`,
        );
      }

      const draftId = `relayfile-writeback--${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
      const draftPath = `${root}/${draftId}.json`;

      const writeResult = await client.writeFile({
        workspaceId,
        path: draftPath,
        baseRevision: "*",
        contentType: "application/json",
        content: JSON.stringify(payload, null, 2),
      });

      if (!writeResult.writeback) {
        return {
          draftPath,
          receipt: { opId: writeResult.opId, status: "(no provider writeback enqueued)" },
        };
      }

      const receipt = await pollOp(client, workspaceId, writeResult.opId, opts);
      const externalId =
        typeof receipt.providerResult?.externalId === "string"
          ? (receipt.providerResult.externalId as string)
          : undefined;
      const canonicalPath = externalId ? `${root}/${externalId}.json` : undefined;
      const url =
        typeof receipt.providerResult?.url === "string"
          ? (receipt.providerResult.url as string)
          : undefined;
      return { draftPath, receipt, externalId, canonicalPath, url };
    },

    async update(canonicalPath, baseRevision, patch, opts) {
      const result = await client.writeFile({
        workspaceId,
        path: canonicalPath,
        baseRevision,
        contentType: "application/json",
        content: JSON.stringify(patch, null, 2),
      });
      const receipt = await pollOp(client, workspaceId, result.opId, opts);
      return { targetRevision: result.targetRevision, receipt };
    },

    async delete(canonicalPath, baseRevision, opts) {
      const result = await client.deleteFile({
        workspaceId,
        path: canonicalPath,
        baseRevision,
      });
      const receipt = await pollOp(client, workspaceId, result.opId, opts);
      return { receipt };
    },

    async deleteDraft(draftPath) {
      try {
        const cur = await client.readFile(workspaceId, draftPath);
        await client.deleteFile({
          workspaceId,
          path: draftPath,
          baseRevision: cur.revision,
        });
        return { deleted: true };
      } catch (err) {
        return { deleted: false, reason: (err as Error).message };
      }
    },
  };
}
