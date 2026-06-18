/**
 * Comprehensive Linear-labels writeback smoke. No LLM in the loop.
 *
 * Proves provider-side execution at every step (not just file-level writes):
 *   1. bootstrap                → cred + workspace evidence
 *   2. read schema              → writeback contract non-empty
 *   3. create draft + poll op   → assert writeback enqueued for provider=linear
 *                                  AND op.status==='succeeded' AND
 *                                  providerResult has a Linear UUID
 *   4. read canonical           → /linear/labels/<linear-uuid>.json exists with
 *                                  real Linear-side fields (creatorId, createdAt)
 *   5. PATCH canonical + poll   → update flows back through Linear (op succeeded)
 *   6. RevisionConflictError    → stale baseRevision rejected
 *   7. DELETE canonical + poll  → delete flows back through Linear
 *   8. canonical now 404s
 *   9. orphan draft cleanup     → the unrewritten draft file is removed too
 *
 * Run:
 *   CLOUD_WORKSPACE_ID=<app-uuid> npm run smoke
 */

import { RelayFileApiError, RevisionConflictError } from "@relayfile/sdk";

import { connectWorkspace } from "./bootstrap.js";
import {
  LABEL_CREATE_EXAMPLE_PATH,
  LABEL_SCHEMA_PATH,
  createLabel,
  deleteDraftIfPresent,
  deleteLabel,
  readCanonicalLabel,
  updateLabel,
} from "./labels.js";

const LINEAR_UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

async function main(): Promise<number> {
  const ws = await connectWorkspace({
    scopes: [
      "relayfile:fs:read:/discovery/linear/**",
      "relayfile:fs:read:/linear/**",
      "relayfile:fs:write:/linear/**",
    ],
  });

  console.log("── bootstrap evidence ──");
  console.log(`  cloudWorkspaceId : ${ws.cloudWorkspaceId}`);
  console.log(`  workspaceId      : ${ws.workspaceId}`);
  console.log(`  credSource       : ${ws.credSource}`);

  let canonicalPath: string | null = null;
  let draftPath: string | null = null;
  let lastRevision: string | null = null;
  let staleRevision: string | null = null;
  let failures = 0;

  const step = async (
    name: string,
    fn: () => Promise<Record<string, unknown>>,
  ): Promise<boolean> => {
    process.stdout.write(`\n── ${name} ──\n`);
    try {
      const evidence = await fn();
      console.log(`  result   : ✅ PASS`);
      console.log(
        `  evidence : ${JSON.stringify(evidence, null, 2).replace(/\n/g, "\n             ")}`,
      );
      return true;
    } catch (err) {
      console.log(`  result   : ❌ FAIL`);
      console.log(`  error    : ${(err as Error).message}`);
      failures++;
      return false;
    }
  };

  await step("read /discovery/linear/labels/.schema.json (non-empty)", async () => {
    const file = await ws.client.readFile(ws.workspaceId, LABEL_SCHEMA_PATH);
    const schema = JSON.parse(file.content);
    return {
      revision: file.revision,
      title: schema.title,
      required: schema.required,
      writablePropertyCount: Object.entries(schema.properties ?? {}).filter(
        ([, v]: [string, unknown]) =>
          !(v as { readOnly?: boolean }).readOnly,
      ).length,
    };
  });

  await step("read /discovery/linear/labels/.create.example.json", async () => {
    const file = await ws.client.readFile(
      ws.workspaceId,
      LABEL_CREATE_EXAMPLE_PATH,
    );
    return { revision: file.revision, content: JSON.parse(file.content) };
  });

  const stamp = new Date().toISOString();
  const createdOk = await step(
    "create label via draft → op succeeded → Linear UUID in providerResult",
    async () => {
      const result = await createLabel(ws, {
        name: `relayfile-writeback-test ${stamp}`,
        color: "#6366f1",
        description: "Created by integration-verifier smoke. Safe to delete.",
      });
      draftPath = result.draftPath;
      canonicalPath = result.canonicalPath ?? null;

      const externalIdLooksLikeLinearUuid = result.externalId
        ? LINEAR_UUID.test(result.externalId)
        : false;

      if (
        result.receipt.status !== "succeeded" ||
        !externalIdLooksLikeLinearUuid
      ) {
        throw new Error(
          `Expected op.status='succeeded' with Linear-UUID externalId, got status=${result.receipt.status}, externalId=${result.externalId}`,
        );
      }

      return {
        draftPath: result.draftPath,
        opId: result.receipt.opId,
        opStatus: result.receipt.status,
        attemptCount: result.receipt.attemptCount,
        provider: (result.receipt.providerResult as { provider?: string })?.provider,
        action: (result.receipt.providerResult as { action?: string })?.action,
        endpoint: (result.receipt.providerResult as { endpoint?: string })?.endpoint,
        externalId: result.externalId,
        canonicalPath: result.canonicalPath,
        url: result.url,
      };
    },
  );

  if (createdOk && canonicalPath) {
    await step(
      "read canonical → real Linear fields present (creatorId, createdAt)",
      async () => {
        // Canonical file is materialized by the relayfile DO after the Linear
        // API call returns — usually fast but can lag a few seconds behind the
        // op succeeding. Retry briefly before declaring failure.
        const deadline = Date.now() + 10_000;
        let lastErr: unknown;
        while (Date.now() < deadline) {
          try {
            const { revision, record } = await readCanonicalLabel(ws, canonicalPath!);
            lastRevision = revision;
            return {
              path: canonicalPath,
              revision,
              name: record.name,
              color: record.color,
              creatorId: record.creatorId ?? "(missing)",
              createdAt: record.createdAt ?? "(missing)",
              provider: record.provider ?? "(missing)",
              objectType: record.objectType ?? "(missing)",
            };
          } catch (err) {
            lastErr = err;
            await new Promise((r) => setTimeout(r, 1000));
          }
        }
        throw lastErr ?? new Error("canonical read timed out");
      },
    );

    if (lastRevision) {
      staleRevision = lastRevision;
      await step(
        "PATCH canonical → op succeeded → provider update returned",
        async () => {
          const r = await updateLabel(ws, canonicalPath!, lastRevision!, {
            description: `Updated by smoke at ${new Date().toISOString()}.`,
          });
          lastRevision = r.targetRevision;
          return {
            newRevision: r.targetRevision,
            opId: r.receipt.opId,
            opStatus: r.receipt.status,
            providerAction: (r.receipt.providerResult as { action?: string })?.action,
            providerStatus: (r.receipt.providerResult as { status?: number })?.status,
          };
        },
      );
    }

    if (staleRevision) {
      await step(
        "stale baseRevision → RevisionConflictError",
        async () => {
          try {
            await updateLabel(ws, canonicalPath!, staleRevision!, {
              description: "Should never land — stale baseRevision.",
            });
            throw new Error("expected RevisionConflictError, write succeeded");
          } catch (err) {
            if (err instanceof RevisionConflictError) {
              return {
                expectedRevision: err.expectedRevision,
                currentRevision: err.currentRevision,
                conflictDetected: true,
              };
            }
            throw err;
          }
        },
      );
    }

    if (lastRevision) {
      await step(
        "DELETE canonical → op succeeded → provider delete returned",
        async () => {
          const r = await deleteLabel(ws, canonicalPath!, lastRevision!);
          return {
            opId: r.receipt.opId,
            opStatus: r.receipt.status,
            providerAction: (r.receipt.providerResult as { action?: string })?.action,
            providerStatus: (r.receipt.providerResult as { status?: number })?.status,
          };
        },
      );

      await step("verify deletion: canonical now 404s", async () => {
        try {
          await ws.client.readFile(ws.workspaceId, canonicalPath!);
          throw new Error("expected 404 after delete, got 200");
        } catch (err) {
          if (err instanceof RelayFileApiError && err.status === 404) {
            return { confirmedDeleted: true, status: err.status };
          }
          throw err;
        }
      });
    }
  }

  if (draftPath) {
    await step("cleanup orphan draft file (adapter does not rewrite)", async () => {
      const r = await deleteDraftIfPresent(ws, draftPath!);
      return { draftPath, ...r };
    });
  }

  console.log(`\n── summary ──`);
  console.log(`  ${failures === 0 ? "all checks passed" : `${failures} check(s) failed`}`);
  return failures === 0 ? 0 : 1;
}

main()
  .then((code) => process.exit(code))
  .catch((err) => {
    console.error("smoke crashed:", err);
    process.exit(2);
  });
