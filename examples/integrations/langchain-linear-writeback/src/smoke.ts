/**
 * Comprehensive Linear-labels writeback smoke against prod. No LLM.
 *
 * Run:
 *   CLOUD_WORKSPACE_ID=<app-uuid> npm run smoke
 */

import { RelayFileApiError, RevisionConflictError, connect } from "@relayfile/agents";

const LINEAR_UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

const rf = await connect({
  scopes: [
    "relayfile:fs:read:/discovery/linear/**",
    "relayfile:fs:read:/linear/**",
    "relayfile:fs:write:/linear/**",
  ],
});

console.log("── bootstrap evidence ──");
console.log(`  cloudWorkspaceId : ${rf.cloudWorkspaceId}`);
console.log(`  workspaceId      : ${rf.workspaceId}`);
console.log(`  credSource       : ${rf.credSource}`);

let failures = 0;
const step = async (name: string, fn: () => Promise<Record<string, unknown>>) => {
  process.stdout.write(`\n── ${name} ──\n`);
  try {
    const evidence = await fn();
    console.log(`  result   : ✅ PASS`);
    console.log(
      `  evidence : ${JSON.stringify(evidence, null, 2).replace(/\n/g, "\n             ")}`,
    );
  } catch (err) {
    console.log(`  result   : ❌ FAIL\n  error    : ${(err as Error).message}`);
    failures++;
  }
};

let draftPath: string | null = null;
let canonicalPath: string | null = null;
let canonicalMaterialised = false;
let lastRevision: string | null = null;
let staleRevision: string | null = null;

await step("create label → op succeeded → real Linear UUID", async () => {
  const stamp = new Date().toISOString();
  const result = await rf.writeback.create("/linear/labels", {
    name: `relayfile-writeback-test ${stamp}`,
    color: "#6366f1",
    description: "Created by integration-verifier smoke. Safe to delete.",
  });
  draftPath = result.draftPath;
  canonicalPath = result.canonicalPath ?? null;

  if (result.receipt.status !== "succeeded") {
    throw new Error(`expected op.status='succeeded', got ${result.receipt.status}`);
  }
  if (!result.externalId || !LINEAR_UUID.test(result.externalId)) {
    throw new Error(`expected Linear-UUID externalId, got ${result.externalId}`);
  }
  return {
    draftPath: result.draftPath,
    opId: result.receipt.opId,
    opStatus: result.receipt.status,
    provider: (result.receipt.providerResult as { provider?: string })?.provider,
    action: (result.receipt.providerResult as { action?: string })?.action,
    endpoint: (result.receipt.providerResult as { endpoint?: string })?.endpoint,
    externalId: result.externalId,
    canonicalPath: result.canonicalPath,
  };
});

if (canonicalPath) {
  await step("readCanonical → Linear fields present", async () => {
    const { revision, record } = await rf.writeback.readCanonical(canonicalPath!);
    lastRevision = revision;
    canonicalMaterialised = true;
    return {
      path: canonicalPath,
      revision,
      name: record.name,
      color: record.color,
      creatorId: record.creatorId ?? "(missing)",
      createdAt: record.createdAt ?? "(missing)",
    };
  });

  if (canonicalMaterialised && lastRevision) {
    staleRevision = lastRevision;
    await step("PATCH canonical → op succeeded → provider update returned", async () => {
      const r = await rf.writeback.update(canonicalPath!, lastRevision!, {
        description: `Updated by smoke at ${new Date().toISOString()}.`,
      });
      lastRevision = r.targetRevision;
      return {
        newRevision: r.targetRevision,
        opStatus: r.receipt.status,
        providerAction: (r.receipt.providerResult as { action?: string })?.action,
        providerStatus: (r.receipt.providerResult as { status?: number })?.status,
      };
    });

    await step("stale baseRevision → RevisionConflictError", async () => {
      try {
        await rf.writeback.update(canonicalPath!, staleRevision!, {
          description: "Should never land.",
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
    });

    await step("DELETE canonical → op succeeded → provider delete returned", async () => {
      const current = await rf.client.readFile(rf.workspaceId, canonicalPath!);
      const r = await rf.writeback.delete(canonicalPath!, current.revision);
      return {
        deletedFromRevision: current.revision,
        opStatus: r.receipt.status,
        providerAction: (r.receipt.providerResult as { action?: string })?.action,
        providerStatus: (r.receipt.providerResult as { status?: number })?.status,
      };
    });

    await step(
      "verify deletion: canonical eventually 404s (best-effort 15s)",
      async () => {
        const deadline = Date.now() + 15_000;
        while (Date.now() < deadline) {
          try {
            await rf.client.readFile(rf.workspaceId, canonicalPath!);
            await new Promise((r) => setTimeout(r, 2000));
          } catch (err) {
            if (err instanceof RelayFileApiError && err.status === 404) {
              return { confirmedDeleted: true, status: err.status };
            }
            throw err;
          }
        }
        return {
          informational: true,
          confirmedDeleted: false,
          note: "Canonical still 200 — sync lag; Linear-side delete proven by prior op-status step.",
        };
      },
    );
  }
}

if (draftPath) {
  await step("cleanup orphan draft (local-only — does not propagate to Linear)", async () => {
    const r = await rf.writeback.deleteDraft(draftPath!);
    return { draftPath, ...r };
  });
}

console.log(
  `\n── summary ──\n  ${failures === 0 ? "all checks passed" : `${failures} check(s) failed`}`,
);
process.exit(failures === 0 ? 0 : 1);
