import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { describe, it } from 'node:test';

import {
  CYCLE_STATE_PATH,
  MAX_STATE_BYTES,
  ProgressStateConflictError,
  assertTransition,
  classifyManifestDelta,
  confirmedSlackReceipt,
  createHttpProgressStore,
  featureIdempotencyKey,
  parseFeatures,
  parseProgressContent,
  parseProgressState,
  pickNextFeature,
  resolveManifestPath,
  runGuardian,
  type Feature,
  type GuardianContext,
  type ProgressSnapshot,
  type ProgressState,
  type ProgressStore,
  type SlackTransport,
} from './agent.ts';

const manifestRaw = readFileSync(new URL('../../features/manifest.yaml', import.meta.url), 'utf8');
const allFeatures = parseFeatures(manifestRaw);

function features(): Feature[] {
  return [
    {
      id: 'critical-one', name: 'Critical One', description: 'first critical feature', tier: 1,
      criticality: 'critical', category: 'a', locations: ['src/critical-one.ts'], covers: ['cli:critical-one'],
      procedure: 'critical-procedure', verificationDocument: '.agentworkforce/features/verify/procedures.md',
    },
    {
      id: 'critical-two', name: 'Critical Two', description: 'second critical feature', tier: 2,
      criticality: 'critical', category: 'a', locations: ['src/critical-two.ts'], covers: ['http:data:GET /critical-two'],
      procedure: 'critical-procedure', verificationDocument: '.agentworkforce/features/verify/procedures.md',
    },
    {
      id: 'standard-one', name: 'Standard One', description: 'standard feature', tier: 1,
      criticality: 'standard', category: 'b', locations: ['src/standard-one.ts'], covers: ['implementation:standard-one'],
      procedure: 'standard-procedure', verificationDocument: '.agentworkforce/features/verify/procedures.md',
    },
  ];
}

function state(
  checkedIds: string[] = [],
  options: Partial<ProgressState> = {},
): ProgressState {
  const totalFeatures = options.totalFeatures ?? features().length;
  let knownIds = options.knownIds;
  if (!knownIds && totalFeatures === allFeatures.length) {
    knownIds = allFeatures.map((feature) => feature.id).sort();
  } else if (!knownIds && totalFeatures === allFeatures.length - 1) {
    knownIds = allFeatures.slice(0, -1).map((feature) => feature.id).sort();
  } else if (!knownIds && totalFeatures === allFeatures.length + 1) {
    knownIds = [...allFeatures.map((feature) => feature.id), 'retired-id'].sort();
  } else if (!knownIds && totalFeatures === allFeatures.length + 2) {
    knownIds = [...allFeatures.map((feature) => feature.id), 'retired-one', 'retired-two'].sort();
  }
  return {
    kind: 'relayfile-feature-guardian:progress',
    version: 3,
    generation: 1,
    knownIds: knownIds ?? features().map((feature) => feature.id).sort(),
    checkedIds,
    cycleStartedAt: '2026-07-21T00:00:00.000Z',
    totalFeatures,
    ...(checkedIds.length ? { lastPost: { featureId: checkedIds.at(-1)!, ts: '1710000000.000001' } } : {}),
    ...options,
  };
}

class MemoryStore implements ProgressStore {
  snapshot: ProgressSnapshot | null;
  revision = 0;
  saves = 0;
  loads = 0;
  conflicts = 0;
  failOnSave = 0;
  loadError: Error | null = null;

  constructor(initial: ProgressState | null = null) {
    this.snapshot = initial ? { state: structuredClone(initial), revision: 'r0' } : null;
  }

  async load(): Promise<ProgressSnapshot | null> {
    this.loads += 1;
    if (this.loadError) throw this.loadError;
    return this.snapshot ? structuredClone(this.snapshot) : null;
  }

  async save(next: ProgressState, expected: ProgressSnapshot | null, currentFeatures: Feature[]): Promise<ProgressSnapshot> {
    this.saves += 1;
    if (this.conflicts > 0) {
      this.conflicts -= 1;
      throw new ProgressStateConflictError();
    }
    if (this.failOnSave === this.saves) throw new Error('injected checkpoint failure');
    if (this.snapshot?.revision !== expected?.revision) throw new ProgressStateConflictError();
    assertTransition(expected, next, currentFeatures);
    this.revision += 1;
    this.snapshot = { state: structuredClone(next), revision: `r${this.revision}` };
    return structuredClone(this.snapshot);
  }
}

class IdempotentSlack implements SlackTransport {
  calls = 0;
  actualPosts = 0;
  texts: string[] = [];
  keys = new Map<string, string>();
  error: Error | null = null;
  receiptless = false;
  tsOnly = false;
  delayMs = 0;

  async post(input: { text: string; idempotencyKey: string; signal: AbortSignal }): Promise<{ externalId?: string; ts?: string }> {
    this.calls += 1;
    this.texts.push(input.text);
    if (this.error) throw this.error;
    if (this.delayMs) await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(resolve, this.delayMs);
      input.signal.addEventListener('abort', () => {
        clearTimeout(timer);
        reject(new Error('transport aborted'));
      }, { once: true });
    });
    if (this.receiptless) return {};
    let receipt = this.keys.get(input.idempotencyKey);
    if (!receipt) {
      this.actualPosts += 1;
      receipt = `1710000000.${String(this.actualPosts).padStart(6, '0')}`;
      this.keys.set(input.idempotencyKey, receipt);
    }
    return this.tsOnly ? { ts: ` ${receipt} ` } : { externalId: receipt };
  }
}

function context(overrides: Partial<GuardianContext> = {}): GuardianContext {
  return {
    workspaceDir: '/workspace',
    readFile: async (path) => {
      assert.equal(path, resolveManifestPath('/workspace'));
      return manifestRaw;
    },
    inputs: { SLACK_CHANNEL: 'C123' },
    now: () => new Date('2026-07-21T01:00:00.000Z'),
    ...overrides,
  };
}

describe('manifest clone and selection', () => {
  it('uses the target-specific repository clone path', () => {
    assert.equal(
      resolveManifestPath('/mnt/workspace/'),
      '/mnt/workspace/github/repos/AgentWorkforce/relayfile/.agentworkforce/features/manifest.yaml',
    );
    assert.equal(allFeatures.length, 122);
    assert.ok(allFeatures.every((feature) => feature.locations.length > 0));
    assert.ok(allFeatures.every((feature) => feature.covers.length > 0));
    assert.ok(allFeatures.every((feature) => feature.procedure.length > 0));
  });

  it('uses the target validator and rejects malformed routes, locations, surfaces, and totals', () => {
    assert.throws(() => parseFeatures('version: 1.0\ncategories: {}'), /target validator.*version must be 1.1/);
    for (const mutate of [
      (raw: string) => raw.replace("updated: '2026-07-21'", 'updated: no'),
      (raw: string) => raw.replace('    cli: cli-discovery-and-lifecycle\n', ''),
      (raw: string) => raw.replace('        locations: [cmd/relayfile-cli/main.go]', '        locations: [../outside]'),
      (raw: string) => raw.replace("        covers: ['cli:relayfile help', 'cli-alias:-h=>help', 'cli-alias:--help=>help']", '        covers: []'),
      (raw: string) => raw.replace('  features: 122', '  features: 123'),
    ]) assert.throws(() => parseFeatures(mutate(manifestRaw)), /target validator/);
  });

  it('orders criticality, then tier, then stable id', () => {
    assert.equal(pickNextFeature(features().reverse(), new Set())?.id, 'critical-one');
    assert.equal(pickNextFeature(features(), new Set(['critical-one']))?.id, 'critical-two');
    assert.equal(pickNextFeature(features(), new Set(features().map((feature) => feature.id))), null);
  });
});

describe('bounded exact progress state', () => {
  it('parses canonical exact state and rejects malformed, duplicate, and unknown IDs', () => {
    assert.deepEqual(parseProgressState(state(['critical-one']), features()), state(['critical-one']));
    assert.throws(() => parseProgressContent('{', features()), /malformed JSON/);
    assert.throws(() => parseProgressState({ ...state(), extra: true }, features()), /unknown field/);
    assert.throws(() => parseProgressState({
      ...state(),
      knownIds: ['critical-one', 'critical-one', 'standard-one'],
    }, features()), /duplicate known/);
    assert.throws(() => parseProgressState({
      ...state(),
      knownIds: ['critical-one', 'critical-two', 'retired'],
    }, features()), /does not match/);
    assert.throws(() => parseProgressState(state(['critical-one', 'critical-one']), features()), /duplicate/);
    assert.throws(() => parseProgressState(state(['retired']), features()), /unknown feature/);
  });

  it('measures the UTF-8 byte limit, not JavaScript character count', () => {
    const oversized = '😀'.repeat(Math.floor(MAX_STATE_BYTES / 2));
    assert.throws(() => parseProgressContent(oversized, features()), /oversized/);
  });

  it('accepts one historical ID only during reconcile loading', () => {
    const historical = state(['retired'], {
      knownIds: ['critical-one', 'critical-two', 'retired'],
    });
    assert.equal(parseProgressState(historical, features(), { allowHistoricalIds: true }).checkedIds[0], 'retired');
  });

  it('preserves additions and one unchecked retirement', () => {
    const prior = state(['critical-one']);
    const addition = [...features(), {
      ...features()[0], id: 'added', name: 'Added', description: 'new', criticality: 'hot' as const, category: 'c',
    }];
    assert.equal(classifyManifestDelta(prior, addition).kind, 'preserve');
    const uncheckedRetirement = features().slice(0, 2);
    assert.deepEqual(classifyManifestDelta(prior, uncheckedRetirement), {
      kind: 'preserve', removedCount: 1, retiredIds: [],
    });
  });

  it('resets one checked retirement and fails closed on unsafe shrink', () => {
    const prior = state(['standard-one']);
    assert.equal(classifyManifestDelta(prior, features().slice(0, 2)).kind, 'reset-checked-retirement');
    assert.equal(classifyManifestDelta(prior, features().slice(0, 1)).kind, 'unsafe');
    const twoRetired = state(['critical-two', 'standard-one']);
    assert.equal(classifyManifestDelta(twoRetired, features().slice(0, 1)).kind, 'unsafe');
  });

  it('fails closed when multiple unchecked retirements are masked by additions', () => {
    const prior = state([], {
      totalFeatures: 4,
      knownIds: ['critical-one', 'critical-two', 'retired-a', 'retired-b'],
    });
    const replacement = [
      ...features().slice(0, 2),
      { ...features()[0], id: 'added-a', name: 'Added A', description: 'new', criticality: 'hot' as const, category: 'c' },
      { ...features()[0], id: 'added-b', name: 'Added B', description: 'new', criticality: 'hot' as const, category: 'c' },
    ];
    assert.equal(classifyManifestDelta(prior, replacement).kind, 'unsafe');
  });

  it('rejects progress regression, checkpoint skips, and ambiguous reset', () => {
    const snapshot = { state: state(['critical-one']), revision: 'r1' };
    assert.throws(() => assertTransition(snapshot, state(), features()), /regressed|reset/);
    assert.throws(() => assertTransition(snapshot, state(['critical-one', 'critical-two', 'standard-one']), features()), /skipped/);
    assert.throws(() => assertTransition(snapshot, state([], { generation: 2, cycleStartedAt: '2026-07-21T01:00:00.000Z' }), features()), /reset/);
    assert.throws(() => parseProgressState({ ...state(), cycleStartedAt: '2026-07-21T00:00:00Z' }, features()), /canonical/);
    assert.throws(() => parseProgressState({ ...state(), lastPost: { featureId: 'critical-one', ts: 'draft' } }, features()), /lastPost|disagree/);
  });
});

describe('HTTP exact-revision store', () => {
  const credentials = { url: 'https://relayfile.invalid', token: 'secret', workspaceId: 'rw_test' };

  it('treats only HTTP 404 as absent and fails auth responses', async () => {
    const absent = createHttpProgressStore(credentials, { fetchImpl: async () => new Response('', { status: 404 }) });
    assert.equal(await absent.load(features()), null);
    const auth = createHttpProgressStore(credentials, { fetchImpl: async () => new Response('no', { status: 401 }) });
    await assert.rejects(() => auth.load(features()), /HTTP 401/);
  });

  it('maps 409 and 412 writes to CAS conflicts with If-Match zero on bootstrap', async () => {
    for (const status of [409, 412]) {
      let ifMatch = '';
      let contentType = '';
      let requestBody = '';
      const store = createHttpProgressStore(credentials, {
        fetchImpl: async (_url, init) => {
          const headers = new Headers(init?.headers);
          ifMatch = headers.get('if-match') ?? '';
          contentType = headers.get('content-type') ?? '';
          requestBody = String(init?.body ?? '');
          return new Response('', { status });
        },
      });
      await assert.rejects(() => store.save(state(), null, features()), ProgressStateConflictError);
      assert.equal(ifMatch, '0');
      assert.equal(contentType, 'application/json');
      assert.deepEqual(JSON.parse(requestBody), {
        contentType: 'application/json',
        content: `${JSON.stringify(state())}\n`,
      });
    }
  });

  it('requires matching readback and a revision advance', async () => {
    const expected = { state: state(), revision: 'r1' };
    const content = `${JSON.stringify(state())}\n`;
    let calls = 0;
    const unchanged = createHttpProgressStore(credentials, {
      fetchImpl: async () => {
        calls += 1;
        if (calls === 1) return new Response('', { status: 200 });
        return Response.json({ path: CYCLE_STATE_PATH, revision: 'r1', content });
      },
    });
    await assert.rejects(() => unchanged.save(state(), expected, features()), /did not advance/);

    calls = 0;
    const mismatch = createHttpProgressStore(credentials, {
      fetchImpl: async () => {
        calls += 1;
        if (calls === 1) return new Response('', { status: 200 });
        return Response.json({ path: CYCLE_STATE_PATH, revision: 'r2', content: `${JSON.stringify(state(['critical-one']))}\n` });
      },
    });
    await assert.rejects(() => mismatch.save(state(), expected, features()), /does not match/);
  });

  it('bounds a delayed state request', async () => {
    const store = createHttpProgressStore(credentials, {
      timeoutMs: 5,
      fetchImpl: async (_url, init) => new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(new Error('aborted')), { once: true });
      }),
    });
    await assert.rejects(() => store.load(features()), /timed out|aborted/);
  });

  it('bounds a delayed save/readback transaction and rejects ambiguous revisions', async () => {
    const hung = createHttpProgressStore(credentials, {
      timeoutMs: 5,
      fetchImpl: async (_url, init) => new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(new Error('aborted')), { once: true });
      }),
    });
    await assert.rejects(() => hung.save(state(), null, features()), /timed out|aborted/);

    const ambiguous = createHttpProgressStore(credentials, {
      fetchImpl: async () => Response.json(
        { path: CYCLE_STATE_PATH, revision: 'r1', content: `${JSON.stringify(state())}\n` },
        { headers: { ETag: '"r2"' } },
      ),
    });
    await assert.rejects(() => ambiguous.load(features()), /ambiguous/);
  });
});

describe('post-then-checkpoint guardian cycle', () => {
  it('validates the clone but skips Slack and state when input is absent', async () => {
    const outcome = await runGuardian(context({ inputs: {} }), { type: 'cron.tick' }, { transactionTimeoutMs: 100 });
    assert.equal(outcome.status, 'SKIP');
    assert.match(outcome.reason, /no Slack mount/);
  });

  it('does not post when bootstrap persistence fails', async () => {
    const store = new MemoryStore();
    store.failOnSave = 1;
    const slack = new IdempotentSlack();
    const outcome = await runGuardian(context(), {}, { store, slack });
    assert.equal(outcome.status, 'FAIL');
    assert.equal(slack.calls, 0);
  });

  it('retries pre-post CAS conflicts and still emits one message', async () => {
    const store = new MemoryStore();
    store.conflicts = 1;
    const slack = new IdempotentSlack();
    const outcome = await runGuardian(context(), {}, { store, slack });
    assert.equal(outcome.status, 'PASS');
    assert.equal(slack.actualPosts, 1);
  });

  it('uses one idempotency key after a post/checkpoint failure', async () => {
    const current = state([], { totalFeatures: allFeatures.length });
    const store = new MemoryStore(current);
    store.failOnSave = 1;
    const slack = new IdempotentSlack();
    const first = await runGuardian(context(), {}, { store, slack });
    assert.equal(first.status, 'FAIL');
    assert.equal(slack.actualPosts, 1);
    assert.match(first.reason, /checkpoint failed/);
    store.failOnSave = 0;
    const second = await runGuardian(context(), {}, { store, slack });
    assert.equal(second.status, 'PASS');
    assert.equal(slack.calls, 2);
    assert.equal(slack.actualPosts, 1);
  });

  it('resets a completed cycle before posting the next generation', async () => {
    const ids = allFeatures.map((feature) => feature.id);
    const complete = state(ids, {
      totalFeatures: ids.length,
      lastPost: { featureId: ids.at(-1)!, ts: '1710000000.000001' },
    });
    const store = new MemoryStore(complete);
    const slack = new IdempotentSlack();
    const outcome = await runGuardian(context(), {}, { store, slack });
    assert.equal(outcome.status, 'PASS');
    assert.equal(store.snapshot?.state.generation, 2);
    assert.equal(store.snapshot?.state.checkedIds.length, 1);
  });

  it('preserves progress across an addition and resets a checked retirement', async () => {
    const firstId = allFeatures[0].id;
    const prior = state([firstId], { totalFeatures: allFeatures.length - 1, lastPost: { featureId: firstId, ts: '1710000000.000001' } });
    const addedStore = new MemoryStore(prior);
    const added = await runGuardian(context(), {}, { store: addedStore, slack: new IdempotentSlack() });
    assert.equal(added.status, 'PASS');
    assert.equal(addedStore.snapshot?.state.generation, 1);
    assert.equal(addedStore.snapshot?.state.checkedIds[0], firstId);

    const retiredState = state(['retired-id'], { totalFeatures: allFeatures.length + 1, lastPost: { featureId: 'retired-id', ts: '1710000000.000001' } });
    const retiredStore = new MemoryStore(retiredState);
    const retired = await runGuardian(context(), {}, { store: retiredStore, slack: new IdempotentSlack() });
    assert.equal(retired.status, 'PASS');
    assert.equal(retiredStore.snapshot?.state.generation, 2);
  });

  it('reconciles one unchecked retirement without losing checked IDs or the receipt', async () => {
    const checkedId = allFeatures[0].id;
    const prior = state([checkedId], {
      totalFeatures: allFeatures.length + 1,
      knownIds: [...allFeatures.map((feature) => feature.id), 'retired-id'].sort(),
      lastPost: { featureId: checkedId, ts: '1710000000.000001' },
    });
    const store = new MemoryStore(prior);
    const outcome = await runGuardian(context(), {}, { store, slack: new IdempotentSlack() });
    assert.equal(outcome.status, 'PASS');
    assert.equal(store.snapshot?.state.generation, 1);
    assert.equal(store.snapshot?.state.checkedIds[0], checkedId);
    assert.equal(store.snapshot?.state.checkedIds.length, 2);
  });

  it('fails closed on an unsafe manifest shrink before Slack', async () => {
    const store = new MemoryStore(state([], { totalFeatures: allFeatures.length + 2 }));
    const slack = new IdempotentSlack();
    const outcome = await runGuardian(context(), {}, { store, slack });
    assert.equal(outcome.status, 'FAIL');
    assert.match(outcome.reason, /unsafe manifest reconcile/);
    assert.equal(slack.calls, 0);
  });

  it('fails closed when two retirements are masked by same-total additions', async () => {
    const replaced = manifestRaw
      .replace('      - id: cli-help', '      - id: added-one')
      .replace('      - id: cli-version', '      - id: added-two');
    const store = new MemoryStore(state([], { totalFeatures: allFeatures.length }));
    const slack = new IdempotentSlack();
    const outcome = await runGuardian(context({ readFile: async () => replaced }), {}, { store, slack });
    assert.equal(outcome.status, 'FAIL');
    assert.match(outcome.reason, /unsafe manifest reconcile/);
    assert.equal(slack.calls, 0);
    assert.deepEqual(store.snapshot?.state.checkedIds, []);
  });

  it('fails closed on manifest or exact-state reads without mutating progress', async () => {
    const initial = state([], { totalFeatures: allFeatures.length });
    const manifestStore = new MemoryStore(initial);
    const manifestSlack = new IdempotentSlack();
    const manifestFailure = await runGuardian(context({
      readFile: async () => { throw new Error('manifest clone unavailable'); },
    }), {}, { store: manifestStore, slack: manifestSlack });
    assert.equal(manifestFailure.status, 'FAIL');
    assert.equal(manifestStore.loads, 0);
    assert.equal(manifestSlack.calls, 0);
    assert.deepEqual(manifestStore.snapshot?.state, initial);

    const stateStore = new MemoryStore(initial);
    stateStore.loadError = new Error('exact state read denied');
    const stateSlack = new IdempotentSlack();
    const stateFailure = await runGuardian(context(), {}, { store: stateStore, slack: stateSlack });
    assert.equal(stateFailure.status, 'FAIL');
    assert.match(stateFailure.reason, /read denied/);
    assert.equal(stateSlack.calls, 0);
    assert.deepEqual(stateStore.snapshot?.state, initial);
  });

  it('fails on Slack error, delay, and receiptless response without progress', async () => {
    for (const mode of ['error', 'delay', 'receiptless'] as const) {
      const store = new MemoryStore(state([], { totalFeatures: allFeatures.length }));
      const slack = new IdempotentSlack();
      if (mode === 'error') slack.error = new Error('provider rejected');
      if (mode === 'delay') slack.delayMs = 30;
      if (mode === 'receiptless') slack.receiptless = true;
      const outcome = await runGuardian(context(), {}, { store, slack, slackTimeoutMs: 5 });
      assert.equal(outcome.status, 'FAIL', mode);
      assert.deepEqual(store.snapshot?.state.checkedIds, [], mode);
    }
  });

  it('uses the fallback feature message when the LLM fails', async () => {
    const store = new MemoryStore(state([], { totalFeatures: allFeatures.length }));
    const slack = new IdempotentSlack();
    const outcome = await runGuardian(context({
      llm: { complete: async () => { throw new Error('model unavailable'); } },
    }), {}, { store, slack });
    assert.equal(outcome.status, 'PASS');
    assert.match(slack.texts[0], /Relayfile feature check/);
    assert.match(slack.texts[0], /React ✅/);
    const selected = pickNextFeature(allFeatures, new Set());
    assert.ok(selected);
    for (const location of selected.locations) assert.match(slack.texts[0], new RegExp(location.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
    for (const surface of selected.covers) assert.ok(slack.texts[0].includes(surface), surface);
    assert.ok(slack.texts[0].includes(`${selected.verificationDocument}#${selected.procedure}`));
  });

  it('accepts a delayed, trimmed provider ts before checkpointing', async () => {
    const store = new MemoryStore(state([], { totalFeatures: allFeatures.length }));
    const slack = new IdempotentSlack();
    slack.delayMs = 15;
    slack.tsOnly = true;
    const outcome = await runGuardian(context(), {}, { store, slack, slackTimeoutMs: 100 });
    assert.equal(outcome.status, 'PASS');
    assert.equal(outcome.receipt, '1710000000.000001');
    assert.equal(store.snapshot?.state.lastPost?.ts, '1710000000.000001');
  });

  it('bounds the whole guardian transaction before any post', async () => {
    const store: ProgressStore = {
      load: async () => new Promise<ProgressSnapshot | null>(() => undefined),
      save: async () => { throw new Error('unreachable'); },
    };
    const slack = new IdempotentSlack();
    const outcome = await runGuardian(context(), {}, { store, slack, transactionTimeoutMs: 5 });
    assert.equal(outcome.status, 'FAIL');
    assert.match(outcome.reason, /guardian transaction timed out/);
    assert.equal(slack.calls, 0);
  });

  it('derives stable keys and accepts only provider-shaped Slack timestamps', () => {
    assert.equal(featureIdempotencyKey('2026-07-21T00:00:00.000Z', 'cli-help'), 'relayfile-feature-guardian:2026-07-21T00:00:00.000Z:cli-help');
    assert.equal(confirmedSlackReceipt({ externalId: ' 1710000000.000001 ' }), '1710000000.000001');
    assert.equal(confirmedSlackReceipt({ externalId: 'draft-id' }), '');
  });
});
