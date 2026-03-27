# Relayfile Knowledge Extraction Spec

## 1. Document Status

- Status: Draft
- Date: 2026-03-27
- Scope: Convention extraction, path-scoped knowledge retrieval, and broker-time context injection
- Audience: relayfile server, SDK, relay broker, and workflow authors
- Depends on: `relayfile-v1-spec.md`, `knowledge-graph-spec.md`
- Complements: [Agent Trajectories](https://github.com/AgentWorkforce/trajectories)

## 2. Problem Statement

When multiple agents work on the same codebase, each starts cold. Agent 3 discovers that the project uses Vitest, prices are stored in cents, and `pnpm install` needs `shamefully-hoist`. Agent 7, starting 20 minutes later on a related task, has none of this context. It writes a Jest test, uses dollars, and the build fails.

Today the workarounds are:
- **Massive task prompts** — inline everything ever learned. Causes E2BIG, wastes tokens on irrelevant context.
- **AGENTS.md / .claude/rules** — static files, not updated in real-time, not scoped to paths.
- **Step output injection** — `{{steps.X.output}}` passes one step's output to the next. Dies at workflow boundaries.

The knowledge-graph spec (v2) tracks **derivation relationships** — which files were produced from which sources, staleness detection, cascade invalidation. That's structural metadata about the file graph.

This spec addresses a different problem: **what have agents learned about working with this code, and how do we get that knowledge to the right agent at the right time?**

## 3. Goals

1. Agents can attach **knowledge annotations** to file paths at write time.
2. Knowledge is queryable by **path scope** — "what do we know about `packages/billing/**`?"
3. Knowledge is automatically extractable from **diffs and trajectories** without agent cooperation.
4. The relay broker can **inject relevant knowledge** into agent task preambles at dispatch time.
5. Knowledge accumulates over time, forming project-level "institutional memory."

## 4. Non-Goals

1. Semantic search over knowledge (no embeddings in v1 — path-based lookup is sufficient).
2. Replacing trajectories. Trajectories are the full historical record; knowledge annotations are the distilled, queryable derivative.
3. Cross-workspace knowledge sharing. Each workspace's knowledge is self-contained.
4. Agent memory / conversation history. This is project knowledge, not agent state.

## 5. Design Principles

1. **Knowledge lives on files, not in a separate store.** Annotations are file-level metadata using the existing `FileSemantics.properties` model. No second database.
2. **Write-time capture beats post-hoc extraction.** Agents that explicitly annotate produce better knowledge than automated extraction. But automated extraction is the fallback.
3. **Injection is scoped and bounded.** Never inject more than N tokens of knowledge into a task. Relevance is determined by path overlap, not semantic similarity.
4. **Knowledge has a TTL.** Conventions change. Annotations older than N days without reconfirmation are deprioritized.
5. **Humans can curate.** Knowledge annotations are visible and editable. A human can mark an annotation as wrong, authoritative, or expired.

## 6. Data Model

### 6.1 Knowledge Annotation

A knowledge annotation is a structured lesson attached to a file path (or path glob).

```typescript
interface KnowledgeAnnotation {
  id: string;                          // unique ID
  path: string;                        // file path or glob: "packages/billing/**"
  category: KnowledgeCategory;
  content: string;                     // the lesson, max 200 chars
  confidence: "high" | "medium" | "low";
  source: KnowledgeSource;
  createdAt: string;                   // ISO timestamp
  createdBy: string;                   // agent name or "human"
  confirmedAt?: string;                // last time an agent reconfirmed this
  confirmedBy?: string;
  expiresAt?: string;                  // optional TTL
  supersededBy?: string;               // ID of annotation that replaces this
  trajectoryId?: string;               // link to trajectory that produced this
  workflowId?: string;                 // link to workflow run
}

type KnowledgeCategory =
  | "convention"      // "uses Vitest, not Jest"
  | "dependency"      // "imports PlanTier from @nightcto/domain"
  | "gotcha"          // "pnpm install fails without shamefully-hoist"
  | "pattern"         // "all routes use zod validation middleware"
  | "decision"        // "chose Stripe over Paddle — no test mode API"
  | "constraint"      // "max 100 lines per step output"
  | "environment"     // "needs STRIPE_TEST_KEY in .env"

type KnowledgeSource =
  | "agent-explicit"  // agent called writeKnowledge()
  | "diff-extraction" // server extracted from file diff
  | "trajectory"      // extracted from trajectory retrospective
  | "human"           // human wrote or edited directly
```

### 6.2 Storage

Annotations are stored in the existing Relayfile state model as a knowledge index per workspace. Implementation options:

- **D1 table** (Cloudflare Workers): `knowledge_annotations` table with path prefix indexing
- **KV namespace**: Key = `ws:{workspaceId}:knowledge:{path_hash}:{id}`, value = JSON annotation

Path queries use prefix matching: `packages/billing/**` matches annotations on `packages/billing/src/checkout.ts`, `packages/billing/test/checkout.test.ts`, and `packages/billing/**` itself.

### 6.3 Integration with FileSemantics

The existing `FileSemantics.properties` field can carry a `knowledge` key pointing to annotation IDs:

```typescript
// On writeFile, the semantics.properties can reference knowledge
await relayfile.writeFile({
  workspaceId,
  path: 'packages/billing/src/checkout.ts',
  content: fileContent,
  semantics: {
    properties: {
      'knowledge:convention:pricing': 'prices stored in cents, not dollars',
      'knowledge:dependency:domain': '@nightcto/domain:PlanTier',
    }
  }
});
```

But the primary API for knowledge is separate endpoints (Section 7), not embedded in every writeFile call.

## 7. API Design

### 7.1 Write Knowledge

```
POST /v1/workspaces/{workspaceId}/knowledge
```

Request body:
```json
{
  "path": "packages/billing/**",
  "category": "convention",
  "content": "prices stored in cents, not dollars",
  "confidence": "high",
  "source": "agent-explicit",
  "createdBy": "billing-builder-agent",
  "trajectoryId": "traj_abc123"
}
```

Response: `201 Created` with the annotation ID.

Agents call this after completing work. The annotation is immediately queryable.

### 7.2 Query Knowledge

```
GET /v1/workspaces/{workspaceId}/knowledge?paths=packages/billing/**,packages/domain/**&limit=50&categories=convention,gotcha,dependency
```

Response:
```json
{
  "annotations": [
    {
      "id": "ka_001",
      "path": "packages/billing/**",
      "category": "convention",
      "content": "prices stored in cents, not dollars",
      "confidence": "high",
      "createdBy": "billing-builder-agent",
      "createdAt": "2026-03-27T01:30:00Z"
    },
    {
      "id": "ka_002",
      "path": "packages/domain/**",
      "category": "dependency",
      "content": "PlanTier enum exported from src/types.ts",
      "confidence": "high",
      "createdBy": "domain-builder-agent",
      "createdAt": "2026-03-27T00:45:00Z"
    }
  ],
  "total": 2
}
```

Path matching rules:
- `packages/billing/src/checkout.ts` matches annotations on `packages/billing/**`, `packages/billing/src/**`, and `packages/billing/src/checkout.ts`
- Glob patterns use standard gitignore-style matching
- Results sorted by: confidence (high first), then recency

### 7.3 Confirm / Supersede / Delete Knowledge

```
PATCH /v1/workspaces/{workspaceId}/knowledge/{annotationId}
```

```json
{
  "confirmedAt": "2026-03-27T06:00:00Z",
  "confirmedBy": "review-agent"
}
```

Or to supersede:
```json
{
  "supersededBy": "ka_005",
  "content": "prices stored in cents — EXCEPT subscriptions which use whole dollars"
}
```

### 7.4 Bulk Write (for trajectory extraction)

```
POST /v1/workspaces/{workspaceId}/knowledge/bulk
```

```json
{
  "annotations": [
    { "path": "packages/billing/**", "category": "convention", "content": "...", ... },
    { "path": "packages/billing/**", "category": "gotcha", "content": "...", ... }
  ],
  "source": "trajectory",
  "trajectoryId": "traj_abc123"
}
```

## 8. Automatic Extraction

### 8.1 Diff-Based Extraction (Server-Side)

After a write operation completes, the server can optionally extract knowledge from the diff. This runs as a background task, not in the write path.

**What to extract:**
- New `import` statements → `dependency` annotation
- New config values / env vars → `environment` annotation  
- Test framework usage (vitest/jest/mocha) → `convention` annotation
- Error handling patterns → `pattern` annotation
- Package.json dependency changes → `dependency` annotation

**Implementation:** Pattern matching on diff hunks. No LLM needed for v1. A set of extractors:

```typescript
interface DiffExtractor {
  name: string;
  category: KnowledgeCategory;
  match(diff: DiffHunk): KnowledgeAnnotation | null;
}

const extractors: DiffExtractor[] = [
  {
    name: 'import-detector',
    category: 'dependency',
    match(diff) {
      // Match added lines with import/require statements
      // Extract package name and what's imported
    }
  },
  {
    name: 'test-framework-detector',
    category: 'convention',
    match(diff) {
      // Match import from vitest/jest/mocha
      // "uses {framework} for testing"
    }
  },
  {
    name: 'env-var-detector',
    category: 'environment',
    match(diff) {
      // Match process.env.X or Deno.env.get("X")
      // "requires {VAR} environment variable"
    }
  },
];
```

### 8.2 Trajectory-Based Extraction (Post-Workflow)

After a workflow completes and a trajectory is written, extract knowledge from the trajectory's retrospective chapter.

The trajectory retrospective already contains agent reflections like:
- "Had to switch from Paddle to Stripe because Paddle lacks a test mode API"
- "The project uses strict TypeScript with composite references"
- "pnpm workspace protocol requires explicit version ranges"

These are high-value knowledge entries. Extraction can use a lightweight LLM call:

**Prompt:**
```
Extract reusable project lessons from this trajectory retrospective.
For each lesson, output: path scope, category, and a one-line description (max 200 chars).
Categories: convention, dependency, gotcha, pattern, decision, constraint, environment.

Retrospective:
{retrospective_text}

Output JSON array of annotations.
```

This runs once per workflow, not per step — cost is minimal.

### 8.3 Deduplication

Before inserting, check for existing annotations with the same path + category + similar content. If a match exists:
- Same content → update `confirmedAt` (reconfirmation)
- Updated content → create new annotation, mark old as `supersededBy`
- Contradicting content → flag for human review

## 9. Broker Integration

The highest-leverage feature: the relay broker auto-injects knowledge when dispatching tasks to agents.

### 9.1 Flow

1. Workflow step defines `task` text and `fileTargets` (or broker infers from task text)
2. Before spawning the agent, broker calls `GET /v1/workspaces/{wsId}/knowledge?paths={fileTargets}&limit=30`
3. Broker prepends a knowledge preamble to the task:

```
## Project Knowledge (auto-injected)
The following conventions and lessons have been established by previous agents working on this codebase:

**Conventions:**
- packages/billing: prices stored in cents, not dollars
- packages/domain: PlanTier enum exported from src/types.ts

**Gotchas:**
- root: pnpm install fails without shamefully-hoist in .npmrc

**Dependencies:**
- packages/billing → @nightcto/domain:PlanTier
- packages/billing → stripe

---

{original_task_text}
```

4. Agent receives enriched task. Knowledge is in context without the agent needing to discover it.

### 9.2 Budget

Knowledge preamble is capped at a configurable token limit (default: 2000 tokens, ~50 annotations). If more knowledge exists than fits:
- Prioritize by confidence (high > medium > low)
- Prioritize by recency
- Prioritize `gotcha` and `convention` over `dependency` (dependencies are in the code)
- Prioritize annotations with path specificity matching the task's file targets

### 9.3 Opt-In

Knowledge injection is opt-in per workflow:

```typescript
workflow('my-workflow')
  .knowledgeInjection({ enabled: true, maxTokens: 2000 })
```

Or per step:
```typescript
.step('implement-billing', {
  agent: 'builder',
  knowledge: { paths: ['packages/billing/**'], categories: ['convention', 'gotcha'] },
  task: `...`,
})
```

## 10. SDK Changes

### 10.1 RelayFileClient Additions

```typescript
class RelayFileClient {
  // Existing methods...

  async writeKnowledge(
    workspaceId: string,
    annotation: Omit<KnowledgeAnnotation, 'id' | 'createdAt'>
  ): Promise<{ id: string }>;

  async writeKnowledgeBulk(
    workspaceId: string,
    annotations: Omit<KnowledgeAnnotation, 'id' | 'createdAt'>[],
    source?: { trajectoryId?: string; workflowId?: string }
  ): Promise<{ ids: string[] }>;

  async queryKnowledge(
    workspaceId: string,
    options: {
      paths?: string[];
      categories?: KnowledgeCategory[];
      limit?: number;
      minConfidence?: 'low' | 'medium' | 'high';
      createdAfter?: string;
    }
  ): Promise<{ annotations: KnowledgeAnnotation[]; total: number }>;

  async confirmKnowledge(
    workspaceId: string,
    annotationId: string,
    confirmedBy: string
  ): Promise<void>;

  async supersedeKnowledge(
    workspaceId: string,
    annotationId: string,
    replacement: Omit<KnowledgeAnnotation, 'id' | 'createdAt'>
  ): Promise<{ id: string }>;
}
```

### 10.2 Relay SDK Broker Changes

The relay broker (`@agent-relay/sdk`) needs a hook in the step dispatch path:

```typescript
// In runner.ts, before spawning agent for a step
if (step.knowledge?.enabled || workflow.knowledgeInjection?.enabled) {
  const knowledge = await relayfileClient.queryKnowledge(workspaceId, {
    paths: step.knowledge?.paths || step.fileTargets,
    categories: step.knowledge?.categories,
    limit: Math.floor((step.knowledge?.maxTokens || 2000) / 40), // ~40 chars per annotation
  });
  
  if (knowledge.annotations.length > 0) {
    step.task = formatKnowledgePreamble(knowledge.annotations) + '\n\n' + step.task;
  }
}
```

## 11. Relationship to Other Specs

| Spec | What it tracks | When it's used |
|---|---|---|
| **Relayfile v1** | Files, revisions, sync state | Every read/write operation |
| **Knowledge Graph (v2)** | Derivation DAG, staleness, validation | When files are derived from other files |
| **Knowledge Extraction (this)** | Conventions, lessons, gotchas | When agents need context about unfamiliar code |
| **Trajectories** | Full agent work history | After-the-fact review, source for extraction |

Knowledge extraction is orthogonal to the knowledge graph. The graph tracks "file A was derived from file B." Extraction tracks "when working on file A, we learned X." They can coexist: a file can have derivation edges (graph) AND knowledge annotations (extraction).

## 12. Implementation Phases

### Phase 1: Explicit Write + Query (1-2 days)
- `POST /knowledge` and `GET /knowledge` endpoints on relayfile server
- `writeKnowledge()` and `queryKnowledge()` on SDK
- D1 table for annotation storage
- Path prefix matching

### Phase 2: Diff-Based Auto-Extraction (2-3 days)
- Background worker on write operations
- Pattern-matching extractors (imports, env vars, test frameworks)
- Deduplication logic

### Phase 3: Trajectory Extraction (1-2 days)
- Post-workflow hook that reads trajectory retrospective
- Lightweight LLM extraction call
- Bulk write to knowledge store

### Phase 4: Broker Injection (2-3 days)
- Hook in relay SDK step dispatch
- Knowledge preamble formatting
- Token budget management
- `knowledgeInjection` workflow/step config

### Phase 5: Curation UI (future)
- Dashboard view of knowledge per workspace
- Human can confirm, supersede, delete annotations
- Confidence decay over time (annotations not reconfirmed lose confidence)
