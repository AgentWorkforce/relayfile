/**
 * relayfile-knowledge-extraction.ts
 *
 * Implements the Knowledge Extraction spec: agents can write and query
 * project knowledge scoped to file paths. The relay broker can inject
 * relevant knowledge into task preambles at dispatch time.
 *
 * After this workflow:
 *   - POST /v1/workspaces/:id/knowledge writes annotations
 *   - GET /v1/workspaces/:id/knowledge?paths=... queries them
 *   - SDK has writeKnowledge() and queryKnowledge() methods
 *   - Diff-based extractors auto-capture conventions from writes
 *   - Trajectory retrospectives are mined for knowledge post-workflow
 *
 * Run: agent-relay run workflows/relayfile-knowledge-extraction.ts
 */

import { workflow } from '@agent-relay/sdk/workflows';

const RELAYFILE = '/Users/khaliqgant/Projects/AgentWorkforce/relayfile';

async function main() {
const result = await workflow('relayfile-knowledge-extraction')
  .description('Knowledge extraction and context injection for multi-agent collaboration')
  .pattern('dag')
  .channel('wf-relayfile-knowledge')
  .maxConcurrency(4)
  .timeout(3_600_000)

  .agent('architect', { cli: 'claude', role: 'Designs knowledge extraction system' })
  .agent('server-builder', { cli: 'codex', preset: 'worker', role: 'Implements server endpoints and storage' })
  .agent('sdk-builder', { cli: 'codex', preset: 'worker', role: 'Implements SDK methods and extractors' })
  .agent('reviewer', { cli: 'claude', preset: 'reviewer', role: 'Reviews implementation' })

  // ── Phase 1: Storage + API ──────────────────────────────────────

  .step('design-storage', {
    agent: 'architect',
    task: `Read ${RELAYFILE}/docs/knowledge-extraction-spec.md and the existing server code.

Design the D1 schema and API routes for knowledge annotations. Output:
1. D1 migration SQL (create table, indexes for path prefix queries)
2. API route signatures (POST /knowledge, GET /knowledge, PATCH /knowledge/:id, POST /knowledge/bulk)
3. Path matching strategy (how glob patterns query the D1 table efficiently)

Keep output under 80 lines. Use the existing Hono app pattern from ${RELAYFILE}/packages/server/src/.
End with STORAGE_DESIGN_COMPLETE.`,
    verification: { type: 'output_contains', value: 'STORAGE_DESIGN_COMPLETE' },
    timeout: 180_000,
  })

  .step('implement-server', {
    agent: 'server-builder',
    dependsOn: ['design-storage'],
    task: `Implement knowledge extraction server endpoints in ${RELAYFILE} based on:
{{steps.design-storage.output}}

Create/modify files:
1. packages/server/src/routes/knowledge.ts — Hono routes:
   - POST /v1/workspaces/:workspaceId/knowledge (single annotation)
   - POST /v1/workspaces/:workspaceId/knowledge/bulk (batch from trajectories)
   - GET /v1/workspaces/:workspaceId/knowledge (query by paths, categories, confidence)
   - PATCH /v1/workspaces/:workspaceId/knowledge/:id (confirm, supersede, delete)

2. packages/server/src/storage/knowledge.ts — D1 operations:
   - insertAnnotation(), queryByPaths(), confirmAnnotation(), supersedeAnnotation()
   - Path glob matching via SQL LIKE with prefix optimization

3. packages/server/src/migrations/003-knowledge-annotations.sql — D1 migration:
   - Table: knowledge_annotations (id, workspace_id, path, category, content, confidence, source, created_by, created_at, confirmed_at, confirmed_by, expires_at, superseded_by, trajectory_id, workflow_id)
   - Index on (workspace_id, path) for prefix queries
   - Index on (workspace_id, category) for category filtering

4. Register routes in packages/server/src/worker.ts

Types: use the KnowledgeAnnotation and KnowledgeCategory types from the spec.
Run: cd ${RELAYFILE} && pnpm build && pnpm test
End with SERVER_COMPLETE.`,
    verification: { type: 'output_contains', value: 'SERVER_COMPLETE' },
    timeout: 600_000,
  })

  .step('implement-sdk', {
    agent: 'sdk-builder',
    dependsOn: ['design-storage'],
    task: `Add knowledge methods to the Relayfile SDK in ${RELAYFILE}/packages/relayfile-sdk/src/.

1. Update types.ts — add KnowledgeAnnotation, KnowledgeCategory, KnowledgeSource interfaces (from spec)

2. Update client.ts — add methods to RelayFileClient:
   - writeKnowledge(workspaceId, annotation) → { id }
   - writeKnowledgeBulk(workspaceId, annotations, source?) → { ids }
   - queryKnowledge(workspaceId, { paths?, categories?, limit?, minConfidence? }) → { annotations, total }
   - confirmKnowledge(workspaceId, annotationId, confirmedBy) → void
   - supersedeKnowledge(workspaceId, annotationId, replacement) → { id }

3. Add tests: packages/relayfile-sdk/test/knowledge.test.ts
   - Test queryKnowledge path matching
   - Test writeKnowledge + queryKnowledge round-trip
   - Test confirmKnowledge updates confirmedAt
   - Test supersedeKnowledge marks old annotation

Run: cd ${RELAYFILE} && pnpm build && pnpm test
End with SDK_COMPLETE.`,
    verification: { type: 'output_contains', value: 'SDK_COMPLETE' },
    timeout: 600_000,
  })

  .step('review-phase1', {
    agent: 'reviewer',
    dependsOn: ['implement-server', 'implement-sdk'],
    task: `Review knowledge extraction Phase 1 in ${RELAYFILE}.

Check (keep output under 40 lines):
1. Server routes match the spec API design (Section 7)
2. D1 schema supports efficient path prefix queries
3. SDK methods match the spec interfaces (Section 10)
4. Types are consistent between server and SDK
5. Tests cover write, query, confirm, supersede flows
6. pnpm build passes

Fix anything broken. End with PHASE1_REVIEW_COMPLETE.`,
    verification: { type: 'output_contains', value: 'PHASE1_REVIEW_COMPLETE' },
    timeout: 300_000,
  })

  // ── Phase 2: Auto-Extraction ────────────────────────────────────

  .step('implement-extractors', {
    agent: 'sdk-builder',
    dependsOn: ['review-phase1'],
    task: `Implement diff-based knowledge extractors in ${RELAYFILE}/packages/server/src/extractors/.

1. extractors/index.ts — DiffExtractor interface and runner:
   - interface DiffExtractor { name: string; category: KnowledgeCategory; match(diff: DiffHunk): KnowledgeAnnotation | null }
   - function extractKnowledge(diffs: DiffHunk[]): KnowledgeAnnotation[]

2. extractors/import-detector.ts — extracts dependency annotations from import/require statements
3. extractors/test-framework-detector.ts — detects vitest/jest/mocha/playwright usage → convention
4. extractors/env-var-detector.ts — detects process.env / Deno.env usage → environment
5. extractors/package-json-detector.ts — detects new dependencies in package.json changes → dependency

6. extractors/deduplication.ts — before inserting:
   - Same path + category + similar content → update confirmedAt
   - Contradicting content → flag for review (set confidence: "low")

7. Tests: packages/server/test/extractors.test.ts
   - Test each extractor with sample diffs
   - Test deduplication logic

Keep extractors simple — pattern matching on diff lines, no AST parsing.
Run: cd ${RELAYFILE} && pnpm build && pnpm test
End with EXTRACTORS_COMPLETE.`,
    verification: { type: 'output_contains', value: 'EXTRACTORS_COMPLETE' },
    timeout: 600_000,
  })

  // ── Phase 3: Trajectory Integration ─────────────────────────────

  .step('implement-trajectory-extraction', {
    agent: 'server-builder',
    dependsOn: ['review-phase1'],
    task: `Implement trajectory-based knowledge extraction in ${RELAYFILE}/packages/server/src/extractors/trajectory.ts.

1. trajectory.ts:
   - function extractFromRetrospective(retrospective: string, trajectoryId: string): KnowledgeAnnotation[]
   - Parse the retrospective text for lessons, conventions, gotchas
   - For v1: use regex/heuristic extraction (look for "learned", "discovered", "had to", "switched from", "because", "convention", "requires")
   - Each extracted lesson becomes an annotation with source: "trajectory"
   - Path scope: infer from trajectory's changed files (or default to "**")

2. Hook into the POST /knowledge/bulk endpoint — add a "source: trajectory" path that:
   - Accepts { trajectoryId, retrospective, changedFiles }
   - Runs extractFromRetrospective()
   - Deduplicates against existing annotations
   - Bulk inserts results

3. Tests: test with sample retrospective text from the NightCTO trajectories

Keep output under 30 lines. End with TRAJECTORY_EXTRACTION_COMPLETE.`,
    verification: { type: 'output_contains', value: 'TRAJECTORY_EXTRACTION_COMPLETE' },
    timeout: 600_000,
  })

  // ── Final: Review + Commit ──────────────────────────────────────

  .step('final-review', {
    agent: 'reviewer',
    dependsOn: ['implement-extractors', 'implement-trajectory-extraction'],
    task: `Final review of knowledge extraction feature in ${RELAYFILE}.

Check (keep output under 40 lines):
1. Full write → extract → query flow works
2. Diff extractors produce sensible annotations from real diffs
3. Trajectory extraction handles typical retrospective text
4. Deduplication prevents duplicate annotations
5. All tests pass: pnpm test
6. Build succeeds: pnpm build

Fix anything broken. End with FINAL_REVIEW_COMPLETE.`,
    verification: { type: 'output_contains', value: 'FINAL_REVIEW_COMPLETE' },
    timeout: 300_000,
  })

  .step('commit', {
    agent: 'server-builder',
    dependsOn: ['final-review'],
    task: `In ${RELAYFILE}:
1. git checkout -b feat/knowledge-extraction
2. git add -A
3. git commit -m "feat: knowledge extraction — write, query, auto-extract, trajectory integration

Implements the Knowledge Extraction spec:
- POST/GET /v1/workspaces/:id/knowledge endpoints
- SDK writeKnowledge(), queryKnowledge(), confirmKnowledge() methods
- Diff-based extractors (imports, test frameworks, env vars, packages)
- Trajectory retrospective extraction
- Deduplication and supersede logic
- D1 migration for knowledge_annotations table"
4. git push origin feat/knowledge-extraction

Report commit hash. End with COMMIT_COMPLETE.`,
    verification: { type: 'output_contains', value: 'COMMIT_COMPLETE' },
    timeout: 120_000,
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({ onEvent: (event) => console.log(`[knowledge] ${event.type}`) });

console.log('Knowledge extraction complete:', result.status);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
