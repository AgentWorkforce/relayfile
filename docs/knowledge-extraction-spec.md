# Relayfile Knowledge Extraction Spec

## 1. Document Status

- Status: Draft
- Date: 2026-03-30
- Scope: Convention extraction, path-scoped knowledge retrieval, and broker-time context injection
- Audience: relayfile server, SDK, relay broker, and workflow authors
- Depends on: `relayfile-v1-spec.md`
- Complements: [Agent Trajectories](https://github.com/AgentWorkforce/trajectories)

## 2. Problem Statement

When multiple agents work on the same codebase, each starts cold. Agent 3 discovers that the project uses Vitest, prices are stored in cents, and `pnpm install` needs `shamefully-hoist`. Agent 7, starting 20 minutes later on a related task, has none of this context. It writes a Jest test, uses dollars, and the build fails.

Today the workarounds are:
- **Massive task prompts** — inline everything ever learned. Causes E2BIG, wastes tokens on irrelevant context.
- **AGENTS.md / .claude/rules** — static files, not updated in real-time, not scoped to paths.
- **Step output injection** — `{{steps.X.output}}` passes one step's output to the next. Dies at workflow boundaries.

## 3. Goals

1. Agents attach **knowledge annotations** to file paths at write time.
2. Knowledge is queryable by **path scope** — "what do we know about `packages/billing/**`?"
3. Knowledge is automatically extractable from **diffs and trajectories** without agent cooperation (Phase 2+).
4. The relay broker can **inject relevant knowledge** into agent task preambles at dispatch time (Phase 2+).
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
  id: string;                          // unique ID (server-generated)
  workspaceId: string;                 // workspace scope
  path: string;                        // file path or glob: "packages/billing/**"
  category: KnowledgeCategory;
  content: string;                     // the lesson, max 500 chars
  confidence: "high" | "medium" | "low";
  source: KnowledgeSource;
  createdAt: string;                   // ISO timestamp
  createdBy: string;                   // agent name or "human"
  confirmedAt?: string;                // last time an agent reconfirmed this
  confirmedBy?: string;
  expiresAt?: string;                  // optional TTL
  supersededBy?: string;               // ID of annotation that replaces this
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
  | "diff-extraction" // server extracted from file diff (Phase 2)
  | "trajectory"      // extracted from trajectory retrospective (Phase 2)
  | "human"           // human wrote or edited directly
```

### 6.2 Storage

Annotations are stored in the Go server's backend (memory or Postgres, matching the existing storage profile pattern).

**Table: `knowledge_annotations`**

| Column | Type | Notes |
|--------|------|-------|
| id | TEXT PK | `ka_` prefix + ULID |
| workspace_id | TEXT | FK to workspace |
| path | TEXT | File path or glob |
| category | TEXT | One of KnowledgeCategory |
| content | TEXT | Max 500 chars |
| confidence | TEXT | high/medium/low |
| source | TEXT | One of KnowledgeSource |
| created_at | TIMESTAMP | Server-set |
| created_by | TEXT | Agent name or "human" |
| confirmed_at | TIMESTAMP | Nullable |
| confirmed_by | TEXT | Nullable |
| expires_at | TIMESTAMP | Nullable |
| superseded_by | TEXT | Nullable, FK to id |

**Index:** `(workspace_id, path)` for prefix queries.

Path queries use prefix matching: `packages/billing/**` matches annotations on `packages/billing/src/checkout.ts`, `packages/billing/test/checkout.test.ts`, and `packages/billing/**` itself.

### 6.3 Integration with FileSemantics

The existing `FileSemantics.properties` field can carry a `knowledge` key pointing to annotation IDs:

```typescript
await relayfile.writeFile({
  workspaceId,
  path: 'packages/billing/src/checkout.ts',
  content: fileContent,
  semantics: {
    properties: {
      'knowledge:convention:pricing': 'prices stored in cents, not dollars',
    }
  }
});
```

But the primary API for knowledge is separate endpoints (Section 7), not embedded in every writeFile call.

## 7. API Design

### 7.1 Write Knowledge

```
POST /v1/workspaces/{workspaceId}/knowledge
Authorization: Bearer <token>
X-Correlation-Id: <id>
```

Request body:
```json
{
  "path": "packages/billing/**",
  "category": "convention",
  "content": "prices stored in cents, not dollars",
  "confidence": "high",
  "source": "agent-explicit",
  "createdBy": "billing-builder-agent"
}
```

Response: `201 Created`
```json
{
  "id": "ka_01HXYZ...",
  "createdAt": "2026-03-30T12:00:00Z"
}
```

### 7.2 Bulk Write Knowledge

```
POST /v1/workspaces/{workspaceId}/knowledge/bulk
```

Request body:
```json
{
  "annotations": [
    { "path": "packages/billing/**", "category": "convention", "content": "...", ... },
    { "path": "packages/domain/**", "category": "dependency", "content": "...", ... }
  ]
}
```

Response: `201 Created` with `{ "ids": ["ka_...", "ka_..."] }`

### 7.3 Query Knowledge

```
GET /v1/workspaces/{workspaceId}/knowledge?paths=packages/billing/**,packages/domain/**&limit=50&categories=convention,gotcha
```

Query parameters:
- `paths` — comma-separated file paths or globs (prefix match)
- `categories` — comma-separated category filter
- `limit` — max results (default 50, max 200)
- `minConfidence` — filter by confidence (low/medium/high)
- `createdAfter` — ISO timestamp filter

Response:
```json
{
  "annotations": [ ... ],
  "total": 12
}
```

### 7.4 Confirm Knowledge

```
POST /v1/workspaces/{workspaceId}/knowledge/{id}/confirm
```

```json
{ "confirmedBy": "agent-name" }
```

Updates `confirmedAt` and `confirmedBy`. Resets confidence decay.

### 7.5 Supersede Knowledge

```
POST /v1/workspaces/{workspaceId}/knowledge/{id}/supersede
```

```json
{
  "replacement": {
    "path": "packages/billing/**",
    "category": "convention",
    "content": "prices stored in millicents (1/10 cent)",
    "confidence": "high",
    "source": "agent-explicit",
    "createdBy": "billing-refactor-agent"
  }
}
```

Marks original as superseded, creates new annotation.

### 7.6 Delete Knowledge

```
DELETE /v1/workspaces/{workspaceId}/knowledge/{id}
```

Returns `204 No Content`.

## 8. SDK Changes

### 8.1 TypeScript SDK

```typescript
class RelayFileClient {
  // ... existing methods ...

  async writeKnowledge(
    workspaceId: string,
    annotation: WriteKnowledgeInput
  ): Promise<{ id: string; createdAt: string }>;

  async writeKnowledgeBulk(
    workspaceId: string,
    annotations: WriteKnowledgeInput[]
  ): Promise<{ ids: string[] }>;

  async queryKnowledge(
    workspaceId: string,
    options?: {
      paths?: string[];
      categories?: KnowledgeCategory[];
      limit?: number;
      minConfidence?: "low" | "medium" | "high";
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
    replacement: WriteKnowledgeInput
  ): Promise<{ id: string }>;

  async deleteKnowledge(
    workspaceId: string,
    annotationId: string
  ): Promise<void>;
}
```

### 8.2 Python SDK

Matching methods on `RelayFileClient` with snake_case naming.

## 9. Broker Integration (Phase 2)

The highest-leverage feature: the relay broker auto-injects knowledge when dispatching tasks to agents.

### 9.1 Flow

1. Workflow step defines `task` text and `fileTargets` (or broker infers from task text).
2. Before spawning the agent, broker calls `GET /v1/workspaces/{wsId}/knowledge?paths={fileTargets}&limit=30`.
3. Broker prepends a knowledge preamble to the task:

```
## Project Knowledge (auto-injected)

**Conventions:**
- packages/billing: prices stored in cents, not dollars
- packages/domain: PlanTier enum exported from src/types.ts

**Gotchas:**
- root: pnpm install fails without shamefully-hoist in .npmrc
```

4. Agent receives enriched task. Knowledge is in context without the agent needing to discover it.

### 9.2 Budget

Knowledge preamble is capped at a configurable token limit (default: 2000 tokens, ~50 annotations). Priority:
- `gotcha` and `convention` over `dependency`
- High confidence over low
- Recent over old
- Path-specific over broad globs

### 9.3 Opt-In

```typescript
workflow('my-workflow')
  .knowledgeInjection({ enabled: true, maxTokens: 2000 })
```

Or per step:
```typescript
.step('implement-billing', {
  agent: 'builder',
  knowledge: { paths: ['packages/billing/**'], categories: ['convention', 'gotcha'] },
  task: '...',
})
```

## 10. Implementation Phases

### Phase 1: Storage + API + SDK (this PR)
- Go server: `knowledge_annotations` table + CRUD endpoints
- TypeScript SDK: `writeKnowledge`, `queryKnowledge`, `confirmKnowledge`, `supersedeKnowledge`, `deleteKnowledge`
- Python SDK: matching methods
- OpenAPI spec updates
- Tests for server endpoints and SDK methods

### Phase 2: Diff-Based Auto-Extraction (future)
- Background worker on write operations
- Pattern-matching extractors (imports, env vars, test frameworks)
- Deduplication logic

### Phase 3: Trajectory Mining (future)
- Post-workflow hook that reads trajectory retrospective
- Lightweight LLM extraction call
- Bulk write to knowledge store

### Phase 4: Broker Injection (future)
- Hook in relay SDK step dispatch
- Knowledge preamble formatting
- Token budget management

### Phase 5: Curation UI (future)
- Dashboard view of knowledge per workspace
- Confidence decay over time
