# RelayFile Knowledge Graph Spec v2.0

## 1. Document Status

- Status: Draft
- Date: 2026-03-12
- Scope: Derivation graph, typed knowledge schema, and validation workers on top of RelayFile v1
- Audience: relayfile, relay-cloud, SDK, and agent framework implementers
- Depends on: `relayfile-v1-spec.md`

## 2. Problem Statement

RelayFile v1 solves bidirectional sync: external sources stay in sync with the VFS, and agent writes propagate back to providers. But sync is not understanding. When an agent synthesizes three source documents into a strategy memo, v1 has no record of that derivation. When one of those sources updates, nothing flags the memo as potentially stale. When two active decisions contradict each other, nothing detects the conflict.

Organizations accumulate context across dozens of systems. The value isn't in mirroring that context into files — v1 already does that. The value is in making the relationships between pieces of context queryable, the provenance of derived knowledge traceable, and the validity of synthesized beliefs automatically maintained.

Without this, agents can traverse a workspace beautifully and still fail to determine which parts are live, which are stale, which are binding, and which were always just one person's interpretation.

## 3. Goals

1. Track derivation relationships between files as a first-class DAG (directed acyclic graph).
2. Cascade invalidation when upstream sources change — by infrastructure, not agent discipline.
3. Provide a typed knowledge schema so agents can distinguish decisions from assumptions from meeting notes.
4. Run background validation workers that detect staleness, contradictions, and expired claims.
5. Record agent corrections and human overrides as structured feedback the system can learn from.
6. Expose all of the above through the existing REST API and SDK patterns.

## 4. Non-Goals

1. Full knowledge-graph query language (SPARQL, Cypher). The graph is traversable via REST, not via arbitrary queries.
2. Automated rewriting of stale content. Validators flag; agents or humans decide what to do.
3. Cross-workspace graph relationships. Each workspace graph is self-contained in v1.
4. Replacing the VFS. The graph is metadata layered on top of the existing filesystem model.

## 5. Design Principles

1. **Graph as metadata, not a separate store.** Derivation edges, types, and validation state live in the existing state model alongside files. No second database.
2. **Invalidation over regeneration.** When a source changes, mark downstream nodes as `needs_revalidation`. Don't silently rewrite them.
3. **Schema is opt-in but rewarded.** Untyped files still work. Typed files get validation, cascade tracking, and richer query support.
4. **Agents declare derivations at write time.** The system doesn't infer relationships — the writing agent states them explicitly, just like imports in code.
5. **Humans stay in the loop.** Validators surface problems. Agents can propose fixes. Humans (or authorized agents) approve changes to authoritative content.

---

## 6. Data Model Extensions

### 6.1 Derivation Edges

A derivation edge records that one file was produced from one or more other files.

```go
type DerivationEdge struct {
    TargetPath   string `json:"targetPath"`   // the derived file
    SourcePath   string `json:"sourcePath"`   // one upstream dependency
    SourceRev    string `json:"sourceRevision"` // revision of source at derivation time
    DerivedAt    string `json:"derivedAt"`     // timestamp
    DerivedBy    string `json:"derivedBy"`     // agent name or "human"
    Confidence   string `json:"confidence"`    // "high", "medium", "low"
    Relationship string `json:"relationship"`  // "synthesized_from", "summarized_from", "extracted_from", "supersedes"
}
```

Stored per-workspace as `DerivationEdges map[string][]DerivationEdge` keyed by `targetPath`. The reverse index `DownstreamIndex map[string][]string` maps `sourcePath → []targetPath` for cascade traversal.

### 6.2 Knowledge Types

A knowledge type defines the semantic shape of a file — what metadata it must carry, what states it can be in, and how it participates in validation.

```go
type KnowledgeType struct {
    Name           string            `json:"name"`
    RequiredProps  []string          `json:"requiredProperties"`   // e.g. ["status", "decided_by"]
    AllowedStatus  []string          `json:"allowedStatus"`        // e.g. ["active", "superseded", "draft"]
    DefaultStatus  string            `json:"defaultStatus"`
    Mutable        bool              `json:"mutable"`              // false = only updated by sync
    ExpiryField    string            `json:"expiryField,omitempty"` // property key for TTL
    ValidatesOn    []string          `json:"validatesOn"`          // triggers: ["source_change", "schedule", "expiry"]
}
```

Stored per-workspace as `KnowledgeTypes map[string]KnowledgeType`. Files reference their type via `semantics.properties["type"]`.

Built-in types shipped with every workspace:

| Type | Required Properties | Allowed Status | Notes |
|------|-------------------|---------------|-------|
| `source_mirror` | `provider`, `provider_object_id` | `synced`, `stale` | Immutable by agents. Updated only by sync pipeline. |
| `decision` | `status`, `decided_by`, `decided_at` | `active`, `superseded`, `draft` | Must declare `supersedes` relation if replacing another decision. |
| `synthesis` | `derived_from`, `confidence`, `last_validated` | `current`, `needs_revalidation`, `stale` | Must have at least one derivation edge. |
| `assumption` | `asserted_by`, `context` | `active`, `expired`, `disproven` | Optional `expiry` property for TTL. |
| `correction` | `corrects`, `corrected_by`, `reason` | `active` | Records human/agent override of a prior belief. |
| `sop` | `owner`, `last_reviewed` | `active`, `needs_review`, `archived` | Standard operating procedures. |

Custom types can be registered per workspace via the API.

### 6.3 Validation State

Every file with a knowledge type and derivation edges carries validation state.

```go
type ValidationState struct {
    Status          string `json:"status"`          // "valid", "needs_revalidation", "stale", "conflict", "expired"
    LastValidatedAt string `json:"lastValidatedAt"`
    LastValidatedRev string `json:"lastValidatedRevision"` // revision of this file when last validated
    InvalidatedBy   string `json:"invalidatedBy,omitempty"` // path of source that triggered invalidation
    InvalidatedAt   string `json:"invalidatedAt,omitempty"`
}
```

Stored per-file in `semantics.properties` as flattened keys:
- `_validation_status`
- `_validation_last_validated_at`
- `_validation_last_validated_rev`
- `_validation_invalidated_by`
- `_validation_invalidated_at`

The `_` prefix marks system-managed properties that agents can read but not write directly.

### 6.4 Validation Records

Output of validation workers. Immutable audit trail.

```go
type ValidationRecord struct {
    RecordID      string   `json:"recordId"`
    WorkspaceID   string   `json:"workspaceId"`
    FilePath      string   `json:"filePath"`
    ValidationType string  `json:"validationType"` // "staleness", "consistency", "expiry", "schema"
    Result        string   `json:"result"`         // "valid", "stale", "conflict", "expired", "schema_violation"
    Evidence      []string `json:"evidence"`       // paths or descriptions of why
    SuggestedAction string `json:"suggestedAction,omitempty"` // optional remediation hint
    CreatedAt     string   `json:"createdAt"`
    CorrelationID string   `json:"correlationId"`
}
```

Stored per-workspace as an append-only log: `ValidationRecords []ValidationRecord`.

### 6.5 Correction Records

When a human or authorized agent rejects an agent's derived content and provides the correct version, the system records the correction.

```go
type CorrectionRecord struct {
    RecordID     string `json:"recordId"`
    FilePath     string `json:"filePath"`
    CorrectedBy  string `json:"correctedBy"`  // agent name or "human"
    Reason       string `json:"reason"`
    OldRevision  string `json:"oldRevision"`
    NewRevision  string `json:"newRevision"`
    CreatedAt    string `json:"createdAt"`
}
```

Agents can query corrections to avoid repeating the same mistake. An agent about to synthesize from source X can check: "has a previous synthesis from X been corrected? what was the correction?"

---

## 7. Derivation Graph API

All endpoints are workspace-scoped under `/v1/workspaces/{workspaceId}/graph/...`.

Required scope: `graph:read` for queries, `graph:write` for mutations.

### 7.1 Declare Derivation

`POST /graph/edges`

Called by agents at write time to declare that a file was derived from one or more sources.

Request:

```json
{
  "targetPath": "/synthesis/q1-strategy.md",
  "sources": [
    {
      "path": "/external/Sales/q1-numbers.csv",
      "revision": "rev_abc"
    },
    {
      "path": "/external/Product/roadmap.md",
      "revision": "rev_def"
    }
  ],
  "relationship": "synthesized_from",
  "confidence": "high"
}
```

Response (`201`):

```json
{
  "edgeCount": 2,
  "targetPath": "/synthesis/q1-strategy.md",
  "validationStatus": "valid"
}
```

Behavior:
1. Validates that all source paths exist and revisions match current state.
2. Creates one `DerivationEdge` per source.
3. Updates the reverse `DownstreamIndex`.
4. Sets initial validation state to `valid` with current timestamp.
5. If the target file has a knowledge type of `synthesis`, enforces that at least one derivation edge exists.
6. Rejects cycles (target cannot be an ancestor of itself).

### 7.2 Query Upstream

`GET /graph/upstream?path=/synthesis/q1-strategy.md&depth=1`

Returns all sources that the given file was derived from, up to `depth` levels (default 1, max 10).

Response:

```json
{
  "path": "/synthesis/q1-strategy.md",
  "upstream": [
    {
      "path": "/external/Sales/q1-numbers.csv",
      "revision": "rev_abc",
      "currentRevision": "rev_xyz",
      "drifted": true,
      "relationship": "synthesized_from",
      "depth": 1
    },
    {
      "path": "/external/Product/roadmap.md",
      "revision": "rev_def",
      "currentRevision": "rev_def",
      "drifted": false,
      "relationship": "synthesized_from",
      "depth": 1
    }
  ]
}
```

The `drifted` field compares `sourceRevision` at derivation time against the current revision. This is a lightweight staleness check without running full validation.

### 7.3 Query Downstream

`GET /graph/downstream?path=/external/Sales/q1-numbers.csv&depth=1`

Returns all files derived from the given source, up to `depth` levels.

Response:

```json
{
  "path": "/external/Sales/q1-numbers.csv",
  "downstream": [
    {
      "path": "/synthesis/q1-strategy.md",
      "relationship": "synthesized_from",
      "validationStatus": "needs_revalidation",
      "depth": 1
    }
  ]
}
```

### 7.4 Remove Derivation

`DELETE /graph/edges?targetPath=/synthesis/q1-strategy.md&sourcePath=/external/Sales/q1-numbers.csv`

Removes a specific edge. If no `sourcePath` is given, removes all edges for the target.

### 7.5 Graph Stats

`GET /graph/stats`

Response:

```json
{
  "totalEdges": 847,
  "totalTypedFiles": 312,
  "validationSummary": {
    "valid": 280,
    "needs_revalidation": 22,
    "stale": 8,
    "conflict": 2,
    "expired": 0
  },
  "topSources": [
    { "path": "/external/Sales/q1-numbers.csv", "downstreamCount": 14 }
  ]
}
```

---

## 8. Knowledge Type API

### 8.1 Register Type

`POST /types`

Request:

```json
{
  "name": "competitive_intel",
  "requiredProperties": ["source_url", "captured_by", "captured_at"],
  "allowedStatus": ["current", "outdated", "unverified"],
  "defaultStatus": "unverified",
  "mutable": true,
  "expiryField": "expires_at",
  "validatesOn": ["schedule", "expiry"]
}
```

Response (`201`):

```json
{
  "name": "competitive_intel",
  "created": true
}
```

### 8.2 List Types

`GET /types`

Returns all registered knowledge types for the workspace, including built-ins.

### 8.3 Get Type

`GET /types/{typeName}`

### 8.4 Delete Type

`DELETE /types/{typeName}`

Fails if any files in the workspace reference this type. Returns `409` with a list of files using the type.

### 8.5 Schema Validation on Write

When a file is written via `PUT /fs/file` and `semantics.properties["type"]` is set to a registered knowledge type:

1. **Required properties check**: All properties listed in `requiredProperties` must be present in `semantics.properties`. Missing properties → `422` with details.
2. **Status validation**: If `semantics.properties["status"]` is set, it must be in `allowedStatus`. Invalid status → `422`.
3. **Default status**: If no status is set, the `defaultStatus` is applied automatically.
4. **Immutability check**: If `mutable: false`, only the sync pipeline can update the file. Agent writes → `403`.

This validation is opt-in: files without a `type` property are unconstrained, same as v1.

---

## 9. Validation Workers

Three background worker types run per workspace. They subscribe to the event stream and execute on triggers.

### 9.1 Staleness Validator

**Trigger**: `file.updated` or `file.created` event on any file that appears in the `DownstreamIndex` (i.e., a source file has changed).

**Behavior**:

1. Look up all downstream paths from `DownstreamIndex[updatedPath]`.
2. For each downstream file:
   a. Compare the `sourceRevision` in the derivation edge against the source's current revision.
   b. If they differ, set `_validation_status = "needs_revalidation"` and `_validation_invalidated_by = updatedPath`.
   c. Emit a `validation.invalidated` event on the change feed.
   d. Write a `ValidationRecord` with type `staleness`.
3. Walk transitive downstream edges (depth-limited to 5) and repeat.

**Does NOT**: Rewrite or regenerate the downstream file. Only marks it.

### 9.2 Consistency Validator

**Trigger**: Scheduled sweep (configurable interval, default every 6 hours) or on-demand via `POST /validation/run?type=consistency`.

**Behavior**:

1. For every pair of files sharing the same knowledge type and `status = "active"`:
   a. If type is `decision` and both have the same `scope` or `topic` property, flag as potential conflict.
   b. Write a `ValidationRecord` with type `consistency`, evidence listing both paths.
   c. Set `_validation_status = "conflict"` on both files.
   d. Emit `validation.conflict` events.

2. For every `synthesis` that references a `decision` with `status = "superseded"`:
   a. Flag the synthesis as referencing stale authority.
   b. Write a `ValidationRecord`.

**Design note**: Consistency validation is intentionally coarse in v1. It catches structural contradictions (two active decisions on the same topic, references to superseded content) but does not do semantic analysis of content. Semantic contradiction detection is a future extension point where an LLM validator could be plugged in.

### 9.3 Expiry Validator

**Trigger**: Scheduled sweep (configurable interval, default every 1 hour) or on-demand.

**Behavior**:

1. Query all files where the knowledge type has an `expiryField` set.
2. For each file, read `semantics.properties[expiryField]`.
3. If the expiry timestamp is in the past:
   a. Set `_validation_status = "expired"`.
   b. Set `semantics.properties["status"] = "expired"`.
   c. Write a `ValidationRecord` with type `expiry`.
   d. Emit `validation.expired` event.

### 9.4 Schema Validator

**Trigger**: On file write (inline, not async) and scheduled sweep.

**Behavior**:

1. For each file with a `type` property referencing a registered `KnowledgeType`:
   a. Check required properties are present.
   b. Check status is in allowed set.
   c. If violations found, write a `ValidationRecord` with type `schema`.

The inline check on write (§8.5) prevents new violations. The sweep catches files that became invalid due to type definition changes.

---

## 10. Cascade Invalidation

When a source file updates, the staleness validator walks the derivation graph downstream. This is the core mechanism that replaces "maintenance discipline" with infrastructure.

### 10.1 Cascade Rules

1. **Direct invalidation**: If file A is derived from file B, and B updates, A is marked `needs_revalidation`.
2. **Transitive invalidation**: If file C is derived from file A, and A is marked `needs_revalidation` due to B updating, C is also marked `needs_revalidation`. Depth limit: 5 levels.
3. **Selective cascade**: If a file has multiple sources and only one changes, the file is still marked `needs_revalidation` — but the `invalidatedBy` field records which specific source triggered it.
4. **Revalidation clears cascade**: When a `needs_revalidation` file is rewritten (new revision with updated derivation edges pointing to current source revisions), its validation state resets to `valid` and downstream cascade stops.

### 10.2 Cascade Events

Every cascade step emits a `validation.invalidated` event on the change feed:

```json
{
  "eventId": "evt_500",
  "type": "validation.invalidated",
  "path": "/synthesis/q1-strategy.md",
  "revision": "rev_current",
  "origin": "validation_worker",
  "metadata": {
    "invalidatedBy": "/external/Sales/q1-numbers.csv",
    "cascadeDepth": 1,
    "validationType": "staleness"
  },
  "correlationId": "corr_cascade_123",
  "timestamp": "2026-03-12T10:00:00Z"
}
```

Agents subscribed to the event stream can listen for `validation.*` events and take action.

---

## 11. Corrections API

### 11.1 Record Correction

`POST /corrections`

Called when a human or authorized agent rejects derived content and provides the right answer.

Request:

```json
{
  "filePath": "/synthesis/q1-strategy.md",
  "reason": "The synthesis incorrectly concluded Q1 revenue was flat. The sales CSV shows 12% growth when the APAC region is included.",
  "correctedRevision": "rev_new"
}
```

The `correctedRevision` must be a revision that already exists (the correction is the new file content, written via the normal `PUT /fs/file` flow first).

Response (`201`):

```json
{
  "recordId": "corr_456",
  "filePath": "/synthesis/q1-strategy.md",
  "correctedBy": "human",
  "createdAt": "2026-03-12T10:30:00Z"
}
```

### 11.2 Query Corrections

`GET /corrections?path=/synthesis/q1-strategy.md`
`GET /corrections?sourcePath=/external/Sales/q1-numbers.csv`

The second form returns all corrections on files derived from a given source — useful for an agent to check "have previous syntheses from this source been corrected?" before generating new content.

Response:

```json
{
  "corrections": [
    {
      "recordId": "corr_456",
      "filePath": "/synthesis/q1-strategy.md",
      "correctedBy": "human",
      "reason": "The synthesis incorrectly concluded Q1 revenue was flat...",
      "oldRevision": "rev_old",
      "newRevision": "rev_new",
      "createdAt": "2026-03-12T10:30:00Z"
    }
  ]
}
```

---

## 12. SDK Extensions

Package: `@agent-relay/relayfile-sdk`

```ts
// Derivation graph
interface RelayFileClient {
  // ... existing v1 methods ...

  // Graph
  declareDerivation(workspaceId: string, input: DeclareDerivationInput): Promise<DerivationResult>;
  queryUpstream(workspaceId: string, path: string, depth?: number): Promise<UpstreamResponse>;
  queryDownstream(workspaceId: string, path: string, depth?: number): Promise<DownstreamResponse>;
  removeDerivation(workspaceId: string, targetPath: string, sourcePath?: string): Promise<void>;
  getGraphStats(workspaceId: string): Promise<GraphStatsResponse>;

  // Knowledge types
  registerType(workspaceId: string, type: KnowledgeTypeInput): Promise<void>;
  listTypes(workspaceId: string): Promise<KnowledgeType[]>;
  getType(workspaceId: string, typeName: string): Promise<KnowledgeType>;
  deleteType(workspaceId: string, typeName: string): Promise<void>;

  // Validation
  getValidationRecords(workspaceId: string, opts?: ValidationQueryOpts): Promise<ValidationRecord[]>;
  triggerValidation(workspaceId: string, type: ValidationType): Promise<void>;

  // Corrections
  recordCorrection(workspaceId: string, input: CorrectionInput): Promise<CorrectionRecord>;
  queryCorrections(workspaceId: string, opts?: CorrectionQueryOpts): Promise<CorrectionRecord[]>;
}

type DeclareDerivationInput = {
  targetPath: string;
  sources: { path: string; revision: string }[];
  relationship: 'synthesized_from' | 'summarized_from' | 'extracted_from' | 'supersedes';
  confidence: 'high' | 'medium' | 'low';
};

type ValidationQueryOpts = {
  path?: string;
  type?: 'staleness' | 'consistency' | 'expiry' | 'schema';
  result?: string;
  since?: string;
  cursor?: string;
  limit?: number;
};

type CorrectionQueryOpts = {
  path?: string;
  sourcePath?: string;
  cursor?: string;
  limit?: number;
};
```

### 12.1 Derived Write Helper

Convenience method that combines file write + derivation declaration in one call:

```ts
async function writeDerived(
  client: RelayFileClient,
  workspaceId: string,
  input: {
    path: string;
    baseRevision: string;
    content: string;
    contentType?: string;
    sources: { path: string; revision: string }[];
    relationship: string;
    confidence: string;
    properties?: Record<string, string>;
  }
): Promise<{ writeResult: WriteQueuedResponse; derivationResult: DerivationResult }>;
```

This is the primary API agents should use when writing derived content. It ensures the derivation edge is always declared atomically with the write.

---

## 13. Event Stream Extensions

New event types on the existing `/fs/events` change feed:

| Event Type | Trigger | Metadata |
|-----------|---------|----------|
| `derivation.declared` | New derivation edge created | `targetPath`, `sourcePaths`, `relationship` |
| `derivation.removed` | Derivation edge deleted | `targetPath`, `sourcePath` |
| `validation.invalidated` | Cascade invalidation fired | `invalidatedBy`, `cascadeDepth`, `validationType` |
| `validation.conflict` | Consistency validator found conflict | `conflictingPaths`, `reason` |
| `validation.expired` | Expiry validator triggered | `expiryField`, `expiredAt` |
| `validation.resolved` | File revalidated (new revision clears invalid state) | `previousStatus` |
| `correction.recorded` | Human/agent correction recorded | `correctedBy`, `reason` |

Agents can filter the event stream by type: `GET /fs/events?type=validation.*` to subscribe only to validation events.

---

## 14. Auth Scope Extensions

New JWT scopes:

| Scope | Grants |
|-------|--------|
| `graph:read` | Query upstream/downstream, graph stats |
| `graph:write` | Declare/remove derivation edges |
| `types:read` | List/get knowledge types |
| `types:write` | Register/delete knowledge types |
| `validation:read` | Read validation records |
| `validation:trigger` | Trigger on-demand validation sweeps |
| `corrections:read` | Query correction records |
| `corrections:write` | Record corrections |

---

## 15. Persisted State Extensions

Additions to the workspace state:

```go
type WorkspaceState struct {
    // ... existing v1 fields ...

    // Knowledge graph
    DerivationEdges  map[string][]DerivationEdge  `json:"derivationEdges,omitempty"`  // targetPath -> edges
    DownstreamIndex  map[string][]string           `json:"downstreamIndex,omitempty"`  // sourcePath -> []targetPath
    KnowledgeTypes   map[string]KnowledgeType      `json:"knowledgeTypes,omitempty"`
    ValidationRecords []ValidationRecord            `json:"validationRecords,omitempty"`
    CorrectionRecords []CorrectionRecord            `json:"correctionRecords,omitempty"`
}
```

For Postgres backend, the snapshot table already stores the full workspace state as JSON. No schema migration needed for v1 of this feature. If validation records grow large, they can be moved to a separate table in a future optimization.

---

## 16. Agent Workflow Example

End-to-end flow showing how the system works in practice:

### Step 1: Source files sync from providers (existing v1)

Salesforce webhook → envelope → VFS:
- `/external/Sales/q1-pipeline.csv` (type: `source_mirror`, revision: `rev_1`)
- `/external/Sales/q1-closed.csv` (type: `source_mirror`, revision: `rev_2`)

### Step 2: Agent synthesizes

Agent reads both files, produces a strategy doc, and writes it using `writeDerived`:

```ts
await client.writeDerived(workspaceId, {
  path: '/synthesis/q1-revenue-analysis.md',
  baseRevision: null, // new file
  content: '# Q1 Revenue Analysis\n...',
  sources: [
    { path: '/external/Sales/q1-pipeline.csv', revision: 'rev_1' },
    { path: '/external/Sales/q1-closed.csv', revision: 'rev_2' }
  ],
  relationship: 'synthesized_from',
  confidence: 'high',
  properties: { type: 'synthesis', status: 'current' }
});
```

### Step 3: Source updates

Salesforce pipeline data updates. Webhook fires, VFS updates `/external/Sales/q1-pipeline.csv` to `rev_3`.

### Step 4: Cascade invalidation

Staleness validator:
1. Sees `file.updated` on `/external/Sales/q1-pipeline.csv`.
2. Looks up `DownstreamIndex["/external/Sales/q1-pipeline.csv"]` → `["/synthesis/q1-revenue-analysis.md"]`.
3. Compares: derivation edge says `sourceRevision: "rev_1"`, current is `"rev_3"` → drifted.
4. Sets `_validation_status = "needs_revalidation"` on the synthesis.
5. Emits `validation.invalidated` event.

### Step 5: Agent reacts

Agent subscribed to `validation.*` events sees the invalidation. It:
1. Queries `GET /graph/upstream?path=/synthesis/q1-revenue-analysis.md` to see what changed.
2. Queries `GET /corrections?sourcePath=/external/Sales/q1-pipeline.csv` to check for past mistakes.
3. Re-reads the updated sources.
4. Rewrites the synthesis with `writeDerived`, pointing to current revisions.
5. Validation state resets to `valid`.

### Step 6: Human corrects

Human reviews the synthesis, finds a mistake, edits via `PUT /fs/file`, then records the correction:

```ts
await client.recordCorrection(workspaceId, {
  filePath: '/synthesis/q1-revenue-analysis.md',
  reason: 'APAC numbers were excluded from the regional breakdown',
  correctedRevision: 'rev_corrected'
});
```

Next time the agent synthesizes from these sources, it checks corrections first and avoids repeating the error.

---

## 17. Validation Worker Configuration

Per-workspace configuration stored in workspace settings:

```json
{
  "validation": {
    "staleness": {
      "enabled": true,
      "maxCascadeDepth": 5,
      "batchSize": 50
    },
    "consistency": {
      "enabled": true,
      "intervalSeconds": 21600,
      "scopes": ["decision", "sop"]
    },
    "expiry": {
      "enabled": true,
      "intervalSeconds": 3600
    },
    "schema": {
      "enabled": true,
      "enforceOnWrite": true,
      "sweepIntervalSeconds": 86400
    }
  }
}
```

Workers run as goroutines within the relayfile process, sharing the same state backend. They use the existing event stream as their trigger mechanism — no additional queue infrastructure needed.

---

## 18. SLOs

| Metric | Target |
|--------|--------|
| Cascade invalidation after source update | < 5s p95 |
| Graph upstream/downstream query (depth 1) | < 50ms p95 |
| Graph upstream/downstream query (depth 5) | < 200ms p95 |
| Consistency sweep (1000 typed files) | < 30s |
| Expiry sweep (1000 typed files) | < 10s |
| Schema validation on write | < 10ms p95 (inline) |

---

## 19. Testing Strategy

1. **Unit tests**: Derivation edge CRUD, cycle detection, cascade walk, type validation, expiry checks.
2. **Integration tests**: Source update → cascade invalidation → event emission → agent revalidation flow.
3. **Consistency tests**: Seed workspace with known contradictions, verify consistency validator catches them.
4. **Correction tests**: Write → correct → re-derive cycle preserves correction history.
5. **Performance tests**: Graph traversal at 10k edges, cascade at 5 levels deep, sweep at 10k typed files.
6. **Regression tests**: Ensure v1 behavior is unchanged for workspaces that don't use graph features.

---

## 20. Rollout Plan

### Phase 1: Derivation Graph (2 weeks)

1. `DerivationEdge` and `DownstreamIndex` data structures.
2. Graph CRUD API endpoints.
3. `writeDerived` SDK helper.
4. Cycle detection.
5. `derivation.declared` and `derivation.removed` events.

Exit criteria: Agent can declare derivations and query the graph.

### Phase 2: Knowledge Types + Schema Validation (2 weeks)

1. `KnowledgeType` registry and API.
2. Built-in types.
3. Schema validation on write (inline).
4. Schema validation sweep (background).
5. Type-aware query extensions.

Exit criteria: Files with types are validated on write. Query API filters by type.

### Phase 3: Validation Workers + Cascade (2 weeks)

1. Staleness validator with cascade.
2. Expiry validator.
3. `validation.*` events on change feed.
4. Validation records API.
5. Worker configuration.

Exit criteria: Source update triggers cascade invalidation within SLO. Expired files are flagged automatically.

### Phase 4: Corrections + Consistency (2 weeks)

1. Corrections API.
2. Consistency validator.
3. SDK extensions for correction query.
4. End-to-end agent workflow validation.

Exit criteria: Full workflow from §16 runs end-to-end. Corrections are queryable by source path.

---

## 21. Open Decisions

1. **Validation record retention**: How long to keep validation records before compaction? Propose 90 days with configurable retention.
2. **Semantic contradiction detection**: Should the consistency validator call an LLM to detect content-level contradictions, or stay purely structural? Propose structural-only in v1, with an extension point for LLM validators.
3. **Cross-workspace edges**: Some organizations will want derivations across workspaces (e.g., a company-wide strategy doc referenced by team-level syntheses). Defer to v2.
4. **Derivation inference**: Should the system attempt to auto-detect derivations by analyzing file content for references? Propose no — explicit declaration only. Inference is a future agent capability, not a platform feature.
5. **Correction learning**: How should agents consume corrections at scale? Simple query is v1. A future "correction embedding" or "mistake pattern" index could help agents generalize from specific corrections.

---

## 22. References

1. `relayfile-v1-spec.md` — base spec this extends
2. `architecture-ascii.md` — system topology
3. `internal/relayfile/store.go` — current data model and VFS implementation
4. `sdk/relayfile-sdk/src/types.ts` — current SDK type contracts
