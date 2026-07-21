/**
 * Relayfile feature guardian.
 *
 * The runtime reuses the repository's structural manifest validator so the
 * scoped clone cannot be interpreted with a weaker schema than local CI.
 * A deployment adapter supplies the clone reader, exact Relayfile credentials,
 * optional write-only Slack transport, logger, and LLM.
 */
import { randomUUID } from 'node:crypto';

import { input } from '@agentworkforce/delivery';
import {
  defineAgent,
  type CredentialsContext,
  type WorkforceCtx,
  type WorkforceEvent,
} from '@agentworkforce/runtime';
import { slackClient } from '@relayfile/relay-helpers';
import { parse } from 'yaml';

import { validateManifestData } from '../../../scripts/validate-feature-catalog.mjs';

export type Criticality = 'critical' | 'hot' | 'standard';

export interface Feature {
  id: string;
  name: string;
  description: string;
  tier: number;
  criticality: Criticality;
  category: string;
  locations: string[];
  covers: string[];
  procedure: string;
  verificationDocument: string;
}

interface Manifest {
  version: string;
  summary?: { features?: number };
  verification: {
    document: string;
    categories: Record<string, string>;
  };
  categories: Record<string, {
    criticality: Criticality;
    features: Array<{
      id: string;
      name: string;
      description: string;
      verify_tier: number;
      location?: string;
      locations?: string[];
      covers: string[];
    }>;
  }>;
}

export const REPOSITORY_CLONE_RELPATH = 'github/repos/AgentWorkforce/relayfile';
export const MANIFEST_RELPATH = '.agentworkforce/features/manifest.yaml';
export const CYCLE_STATE_PATH = '/memory/workspace/relayfile-feature-guardian/cycle-state.json';
export const STATE_VERSION = 3 as const;
export const MAX_STATE_BYTES = 64 * 1024;
export const MAX_FEATURES = 10_000;
export const MAX_SAFE_MANIFEST_SHRINK = 1;
export const STATE_IO_TIMEOUT_MS = 5_000;
export const SLACK_TIMEOUT_MS = 15_000;
export const SLACK_POLL_MS = 250;
export const TRANSACTION_TIMEOUT_MS = 45_000;
const UTF8 = new TextEncoder();

export interface ProgressState {
  kind: 'relayfile-feature-guardian:progress';
  version: 3;
  generation: number;
  knownIds: string[];
  checkedIds: string[];
  cycleStartedAt: string;
  totalFeatures: number;
  lastPost?: { featureId: string; ts: string };
}

export interface ProgressSnapshot {
  state: ProgressState;
  revision: string;
}

export interface ProgressStore {
  load(features: Feature[]): Promise<ProgressSnapshot | null>;
  save(state: ProgressState, expected: ProgressSnapshot | null, features: Feature[]): Promise<ProgressSnapshot>;
}

export interface RelayfileCredentials {
  url: string;
  token: string;
  workspaceId: string;
}

export interface SlackReceipt {
  externalId?: string;
  ts?: string;
}

export interface SlackTransport {
  post(input: {
    channelId: string;
    text: string;
    idempotencyKey: string;
    signal: AbortSignal;
  }): Promise<SlackReceipt>;
}

export interface GuardianContext {
  workspaceDir?: string;
  sandbox?: {
    cwd: string;
    readFile(path: string): Promise<string>;
  };
  readFile?(path: string): Promise<string>;
  inputs?: Record<string, string | undefined>;
  getInput?(name: string): string | undefined;
  credentials?: { relayfile: RelayfileCredentials } | RelayfileCredentials | CredentialsContext;
  slack?: SlackTransport;
  llm?: { complete(prompt: string, options: { maxTokens: number }): Promise<string> };
  log?(level: 'info' | 'warn' | 'error', event: string, detail?: Record<string, unknown>): void;
  now?(): Date;
}

export interface GuardianDependencies {
  store?: ProgressStore;
  slack?: SlackTransport;
  fetchImpl?: typeof fetch;
  stateTimeoutMs?: number;
  slackTimeoutMs?: number;
  transactionTimeoutMs?: number;
}

export interface GuardianOutcome {
  status: 'PASS' | 'FAIL' | 'SKIP';
  reason: string;
  featureId?: string;
  receipt?: string;
}

export class ProgressStateConflictError extends Error {
  constructor(message = 'cycle state compare-and-set conflict') {
    super(message);
    this.name = 'ProgressStateConflictError';
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function canonicalTimestamp(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  const date = new Date(value);
  return Number.isFinite(date.valueOf()) && date.toISOString() === value;
}

function slackTimestamp(value: unknown): value is string {
  return typeof value === 'string' && /^\d+\.\d+$/.test(value.trim());
}

function assertByteLimit(value: string): void {
  if (UTF8.encode(value).byteLength > MAX_STATE_BYTES) throw new Error('cycle state is oversized');
}

function featureIds(features: Feature[]): string[] {
  return features.map((feature) => feature.id).sort();
}

export function resolveManifestPath(workspaceDir: string): string {
  return `${workspaceDir.replace(/\/+$/, '')}/${REPOSITORY_CLONE_RELPATH}/${MANIFEST_RELPATH}`;
}

export function parseFeatures(raw: string): Feature[] {
  const value = parse(raw) as unknown;
  const validationErrors = validateManifestData(value, { structuralOnly: true });
  if (validationErrors.length > 0) {
    throw new Error(`feature manifest rejected by target validator: ${validationErrors.join('; ')}`);
  }
  const manifest = value as unknown as Manifest;
  const features: Feature[] = [];
  const ids = new Set<string>();
  for (const [category, entry] of Object.entries(manifest.categories)) {
    if (!isRecord(entry) || !['critical', 'hot', 'standard'].includes(entry.criticality) || !Array.isArray(entry.features)) {
      throw new Error(`feature category ${category} is invalid`);
    }
    for (const candidate of entry.features) {
      if (
        !isRecord(candidate) ||
        typeof candidate.id !== 'string' ||
        typeof candidate.name !== 'string' ||
        typeof candidate.description !== 'string' ||
        !Number.isInteger(candidate.verify_tier) ||
        candidate.verify_tier < 1 ||
        candidate.verify_tier > 6 ||
        !Array.isArray(candidate.covers) ||
        ids.has(candidate.id)
      ) {
        throw new Error(`feature in ${category} is invalid or duplicated`);
      }
      ids.add(candidate.id);
      features.push({
        id: candidate.id,
        name: candidate.name,
        description: candidate.description,
        tier: candidate.verify_tier,
        criticality: entry.criticality,
        category,
        locations: candidate.locations ?? (candidate.location ? [candidate.location] : []),
        covers: [...candidate.covers],
        procedure: manifest.verification.categories[category],
        verificationDocument: manifest.verification.document,
      });
    }
  }
  if (features.length === 0 || features.length > MAX_FEATURES) throw new Error('feature manifest count is invalid');
  if (manifest.summary?.features !== features.length) throw new Error('feature manifest summary is stale');
  return features;
}

export function parseProgressState(
  value: unknown,
  features: Feature[],
  options: { allowHistoricalIds?: boolean } = {},
): ProgressState {
  if (!isRecord(value)) throw new Error('cycle state must be an object');
  const exactKeys = new Set(['kind', 'version', 'generation', 'knownIds', 'checkedIds', 'cycleStartedAt', 'totalFeatures', 'lastPost']);
  for (const key of Object.keys(value)) if (!exactKeys.has(key)) throw new Error(`cycle state has unknown field ${key}`);
  if (value.kind !== 'relayfile-feature-guardian:progress' || value.version !== STATE_VERSION) {
    throw new Error('cycle state kind/version is invalid');
  }
  if (!Number.isSafeInteger(value.generation) || Number(value.generation) < 1) throw new Error('cycle state generation is invalid');
  if (!canonicalTimestamp(value.cycleStartedAt)) throw new Error('cycle state timestamp is not canonical');
  if (!Number.isSafeInteger(value.totalFeatures) || Number(value.totalFeatures) < 1 || Number(value.totalFeatures) > MAX_FEATURES) {
    throw new Error('cycle state total is invalid');
  }
  if (!Array.isArray(value.knownIds) || value.knownIds.length !== Number(value.totalFeatures)) {
    throw new Error('cycle state known feature set is invalid');
  }
  const currentIds = featureIds(features);
  const current = new Set(currentIds);
  const knownIds = value.knownIds.map((id) => {
    if (typeof id !== 'string' || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(id)) {
      throw new Error('cycle state contains an invalid known feature id');
    }
    return id;
  });
  if (new Set(knownIds).size !== knownIds.length) throw new Error('cycle state contains duplicate known feature ids');
  const sortedKnownIds = [...knownIds].sort();
  if (knownIds.some((id, index) => id !== sortedKnownIds[index])) {
    throw new Error('cycle state known feature IDs are not canonical');
  }
  if (!options.allowHistoricalIds && (
    knownIds.length !== currentIds.length || knownIds.some((id, index) => id !== currentIds[index])
  )) throw new Error('cycle state known feature set does not match the manifest');

  if (!Array.isArray(value.checkedIds) || value.checkedIds.length > Number(value.totalFeatures)) {
    throw new Error('cycle state progress is invalid');
  }
  const known = new Set(knownIds);
  const checkedIds = value.checkedIds.map((id) => {
    if (
      typeof id !== 'string' ||
      id.length < 1 ||
      id.length > 256 ||
      !known.has(id) ||
      (!options.allowHistoricalIds && !current.has(id))
    ) throw new Error('cycle state contains an unknown feature id');
    return id;
  });
  if (new Set(checkedIds).size !== checkedIds.length) throw new Error('cycle state contains duplicate feature ids');

  let lastPost: ProgressState['lastPost'];
  if (value.lastPost !== undefined) {
    if (
      !isRecord(value.lastPost) ||
      Object.keys(value.lastPost).some((key) => !['featureId', 'ts'].includes(key)) ||
      typeof value.lastPost.featureId !== 'string' ||
      value.lastPost.featureId !== checkedIds.at(-1) ||
      !slackTimestamp(value.lastPost.ts)
    ) throw new Error('cycle state lastPost is invalid');
    lastPost = { featureId: value.lastPost.featureId, ts: value.lastPost.ts.trim() };
  }
  if ((checkedIds.length === 0) !== (lastPost === undefined)) throw new Error('cycle state progress and lastPost disagree');
  return {
    kind: 'relayfile-feature-guardian:progress',
    version: STATE_VERSION,
    generation: Number(value.generation),
    knownIds,
    checkedIds,
    cycleStartedAt: value.cycleStartedAt,
    totalFeatures: Number(value.totalFeatures),
    ...(lastPost ? { lastPost } : {}),
  };
}

export function parseProgressContent(
  content: string,
  features: Feature[],
  options: { allowHistoricalIds?: boolean } = {},
): ProgressState {
  assertByteLimit(content);
  let value: unknown;
  try {
    value = JSON.parse(content);
  } catch {
    throw new Error('cycle state contains malformed JSON');
  }
  return parseProgressState(value, features, options);
}

export type ManifestDelta = {
  kind: 'preserve' | 'reset-checked-retirement' | 'unsafe';
  removedCount: number;
  retiredIds: string[];
  reason?: string;
};

export function classifyManifestDelta(state: ProgressState, features: Feature[]): ManifestDelta {
  const current = new Set(features.map((feature) => feature.id));
  const retiredIds = state.checkedIds.filter((id) => !current.has(id));
  const removedCount = state.knownIds.filter((id) => !current.has(id)).length;
  if (removedCount > MAX_SAFE_MANIFEST_SHRINK) {
    return { kind: 'unsafe', removedCount, retiredIds, reason: 'manifest shrank by more than one feature' };
  }
  if (retiredIds.length > 1) {
    return { kind: 'unsafe', removedCount, retiredIds, reason: 'multiple checked feature IDs disappeared' };
  }
  return { kind: retiredIds.length === 1 ? 'reset-checked-retirement' : 'preserve', removedCount, retiredIds };
}

function laterTimestamp(previous: string, now: Date): string {
  return new Date(Math.max(now.valueOf(), new Date(previous).valueOf() + 1)).toISOString();
}

export function assertTransition(
  expected: ProgressSnapshot | null,
  nextValue: ProgressState,
  features: Feature[],
): void {
  const next = parseProgressState(nextValue, features);
  if (next.totalFeatures !== features.length) throw new Error('cycle state total does not match manifest');
  if (!expected) {
    if (next.generation !== 1 || next.checkedIds.length !== 0 || next.lastPost) {
      throw new Error('bootstrap state must be empty generation one');
    }
    return;
  }
  const previous = expected.state;
  if (next.generation === previous.generation) {
    if (next.cycleStartedAt !== previous.cycleStartedAt) throw new Error('cycle timestamp changed within generation');
    const sameProgress = next.checkedIds.length === previous.checkedIds.length &&
      previous.checkedIds.every((id, index) => next.checkedIds[index] === id);
    const oneCheckpoint = next.checkedIds.length === previous.checkedIds.length + 1 &&
      previous.checkedIds.every((id, index) => next.checkedIds[index] === id);
    if (!sameProgress && !oneCheckpoint) throw new Error('cycle progress regressed or skipped a checkpoint');
    if (sameProgress && JSON.stringify(next.lastPost) !== JSON.stringify(previous.lastPost)) {
      throw new Error('reconcile overwrote the latest receipt');
    }
    if (sameProgress && classifyManifestDelta(previous, features).kind !== 'preserve') {
      throw new Error('unsafe manifest reconcile did not reset the cycle');
    }
    if (oneCheckpoint && next.lastPost?.featureId !== next.checkedIds.at(-1)) {
      throw new Error('checkpoint receipt does not describe the new feature');
    }
    return;
  }
  const delta = classifyManifestDelta(previous, features);
  const complete = features.every((feature) => previous.checkedIds.includes(feature.id));
  if (
    next.generation !== previous.generation + 1 ||
    (!complete && delta.kind !== 'reset-checked-retirement') ||
    next.checkedIds.length !== 0 ||
    next.lastPost ||
    new Date(next.cycleStartedAt) <= new Date(previous.cycleStartedAt)
  ) throw new Error('cycle reset transition is invalid');
}

async function withDeadline<T>(
  label: string,
  timeoutMs: number,
  run: (signal: AbortSignal) => Promise<T>,
): Promise<T> {
  const controller = new AbortController();
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => {
      controller.abort();
      reject(new Error(`${label} timed out after ${timeoutMs}ms`));
    }, timeoutMs);
  });
  try {
    return await Promise.race([run(controller.signal), timeout]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}

function stateUrl(credentials: RelayfileCredentials): URL {
  const url = new URL(
    `/v1/workspaces/${encodeURIComponent(credentials.workspaceId)}/fs/file`,
    `${credentials.url.replace(/\/+$/, '')}/`,
  );
  url.searchParams.set('path', CYCLE_STATE_PATH);
  return url;
}

function revisionFrom(response: Response, body: Record<string, unknown>): string {
  const bodyRevision = typeof body.revision === 'string' ? body.revision.trim() : '';
  const etag = (response.headers.get('etag') ?? '').trim().replace(/^W\//, '').replace(/^"|"$/g, '');
  if (!bodyRevision && !etag) throw new Error('cycle state read has no revision');
  if (bodyRevision && etag && bodyRevision !== etag) throw new Error('cycle state revision is ambiguous');
  return bodyRevision || etag;
}

async function httpRead(
  credentials: RelayfileCredentials,
  features: Feature[],
  fetchImpl: typeof fetch,
  signal: AbortSignal,
  correlationId: string,
  allowHistoricalIds: boolean,
): Promise<ProgressSnapshot | null> {
  const response = await fetchImpl(stateUrl(credentials), {
    method: 'GET',
    headers: { Authorization: `Bearer ${credentials.token}`, 'X-Correlation-Id': correlationId },
    signal,
  });
  if (response.status === 404) return null;
  if (!response.ok) throw new Error(`cycle state GET failed with HTTP ${response.status}`);
  const body = await response.json() as unknown;
  if (!isRecord(body) || body.path !== CYCLE_STATE_PATH || typeof body.content !== 'string') {
    throw new Error('cycle state GET returned an invalid file record');
  }
  return {
    state: parseProgressContent(body.content, features, { allowHistoricalIds }),
    revision: revisionFrom(response, body),
  };
}

export function createHttpProgressStore(
  credentials: RelayfileCredentials,
  options: { fetchImpl?: typeof fetch; timeoutMs?: number } = {},
): ProgressStore {
  const fetchImpl = options.fetchImpl ?? fetch;
  const timeoutMs = options.timeoutMs ?? STATE_IO_TIMEOUT_MS;
  return {
    load: (features) => withDeadline('cycle state load', timeoutMs, (signal) =>
      httpRead(credentials, features, fetchImpl, signal, `guardian-load-${randomUUID()}`, true)),
    save: (state, expected, features) => {
      const canonical = parseProgressState(state, features);
      assertTransition(expected, canonical, features);
      const content = `${JSON.stringify(canonical)}\n`;
      assertByteLimit(content);
      return withDeadline('cycle state compare-and-set', timeoutMs, async (signal) => {
        const correlationId = `guardian-save-${randomUUID()}`;
        const response = await fetchImpl(stateUrl(credentials), {
          method: 'PUT',
          headers: {
            Authorization: `Bearer ${credentials.token}`,
            'X-Correlation-Id': correlationId,
            'Content-Type': 'application/octet-stream',
            'X-Relayfile-Encoding': 'utf-8',
            'X-Relayfile-Content-Type': 'application/json',
            'If-Match': expected?.revision ?? '0',
          },
          body: content,
          signal,
        });
        if (response.status === 409 || response.status === 412) throw new ProgressStateConflictError();
        if (!response.ok) throw new Error(`cycle state PUT failed with HTTP ${response.status}`);
        const readback = await httpRead(credentials, features, fetchImpl, signal, correlationId, false);
        if (!readback || JSON.stringify(readback.state) !== JSON.stringify(canonical)) {
          throw new Error('cycle state readback does not match compare-and-set value');
        }
        if (expected && readback.revision === expected.revision) throw new Error('cycle state revision did not advance');
        return readback;
      });
    },
  };
}

export function pickNextFeature(features: Feature[], checked: Set<string>): Feature | null {
  const criticality: Record<Criticality, number> = { critical: 0, hot: 1, standard: 2 };
  return [...features]
    .sort((a, b) => criticality[a.criticality] - criticality[b.criticality] || a.tier - b.tier || a.id.localeCompare(b.id))
    .find((feature) => !checked.has(feature.id)) ?? null;
}

export function featureIdempotencyKey(cycleStartedAt: string, featureId: string): string {
  return `relayfile-feature-guardian:${cycleStartedAt}:${featureId}`;
}

export function confirmedSlackReceipt(receipt: SlackReceipt | null | undefined): string {
  const externalId = receipt?.externalId?.trim() ?? '';
  if (slackTimestamp(externalId)) return externalId;
  const ts = receipt?.ts?.trim() ?? '';
  return slackTimestamp(ts) ? ts : '';
}

function contextInput(ctx: GuardianContext, name: string): string {
  if (ctx.getInput) return (ctx.getInput(name) ?? '').trim();
  if (ctx.inputs) return (ctx.inputs[name] ?? '').trim();
  return (input(ctx as WorkforceCtx, name) ?? '').trim();
}

function contextNow(ctx: GuardianContext): Date {
  return ctx.now?.() ?? new Date();
}

function log(ctx: GuardianContext, level: 'info' | 'warn' | 'error', event: string, detail?: Record<string, unknown>) {
  ctx.log?.(level, event, detail);
}

async function loadFeatures(ctx: GuardianContext): Promise<Feature[]> {
  const workspace = ctx.sandbox?.cwd ?? ctx.workspaceDir;
  if (!workspace) throw new Error('guardian workspace clone root is absent');
  const path = resolveManifestPath(workspace);
  const raw = ctx.sandbox ? await ctx.sandbox.readFile(path) : await ctx.readFile?.(path);
  if (typeof raw !== 'string') throw new Error('guardian repository clone reader is absent');
  return parseFeatures(raw);
}

function credentials(ctx: GuardianContext): RelayfileCredentials | null {
  const value = ctx.credentials;
  if (!value) return null;
  if ('tryRequire' in value && typeof value.tryRequire === 'function') {
    return value.tryRequire()?.relayfile ?? null;
  }
  return 'relayfile' in value ? value.relayfile : value;
}

function runtimeSlackTransport(timeoutMs: number): SlackTransport {
  const client = slackClient({ writebackTimeoutMs: timeoutMs, writebackPollMs: SLACK_POLL_MS });
  return {
    post: async ({ channelId, text, idempotencyKey, signal }) => {
      signal.throwIfAborted();
      const result = await client.messages.write({ channelId }, { text, idempotencyKey });
      signal.throwIfAborted();
      return {
        externalId: typeof result.receipt?.externalId === 'string' ? result.receipt.externalId : undefined,
        ts: typeof result.receipt?.ts === 'string' ? result.receipt.ts : undefined,
      };
    },
  };
}

async function messageFor(ctx: GuardianContext, feature: Feature): Promise<string> {
  const sourceLine = `Sources: ${feature.locations.join(', ')}`;
  const surfaceLine = `Public/API/CLI surfaces: ${feature.covers.join(', ')}`;
  const procedureLine = `Procedure: ${feature.verificationDocument}#${feature.procedure}`;
  const fallback = [
    `Relayfile feature check: ${feature.name}.`,
    `Expected: ${feature.description}`,
    sourceLine,
    surfaceLine,
    procedureLine,
    `Verify tier ${feature.tier} (${feature.category}).`,
    'React ✅ if working, 🔧 if drifted, or ❓ if untested.',
  ].join('\n');
  if (!ctx.llm) return fallback;
  try {
    const result = await ctx.llm.complete(
      `Write a concise internal Slack check (3-5 sentences, no headers) for Relayfile feature ${feature.name}. ` +
      `Expected behavior: ${feature.description}. Tier: ${feature.tier}. ` +
      `${sourceLine}. ${surfaceLine}. ${procedureLine}. Include every supplied source, surface, and procedure ` +
      'verbatim, then end by asking for ✅, 🔧, or ❓.',
      { maxTokens: 300 },
    );
    return result.trim() || fallback;
  } catch {
    return fallback;
  }
}

function initialState(features: Feature[], now: Date): ProgressState {
  return {
    kind: 'relayfile-feature-guardian:progress', version: STATE_VERSION, generation: 1,
    knownIds: featureIds(features), checkedIds: [], cycleStartedAt: now.toISOString(), totalFeatures: features.length,
  };
}

function resetState(snapshot: ProgressSnapshot, features: Feature[], now: Date): ProgressState {
  return {
    kind: 'relayfile-feature-guardian:progress', version: STATE_VERSION,
    generation: snapshot.state.generation + 1, knownIds: featureIds(features), checkedIds: [],
    cycleStartedAt: laterTimestamp(snapshot.state.cycleStartedAt, now), totalFeatures: features.length,
  };
}

async function prepareSnapshot(
  ctx: GuardianContext,
  store: ProgressStore,
  features: Feature[],
): Promise<ProgressSnapshot> {
  for (let attempt = 0; attempt < 3; attempt += 1) {
    let snapshot = await store.load(features);
    try {
      if (!snapshot) return await store.save(initialState(features, contextNow(ctx)), null, features);
      snapshot = { state: parseProgressState(snapshot.state, features, { allowHistoricalIds: true }), revision: snapshot.revision };
      if (!snapshot.revision) throw new Error('cycle state revision is absent');
      const delta = classifyManifestDelta(snapshot.state, features);
      if (delta.kind === 'unsafe') throw new Error(`unsafe manifest reconcile: ${delta.reason}`);
      if (delta.kind === 'reset-checked-retirement') {
        return await store.save(resetState(snapshot, features, contextNow(ctx)), snapshot, features);
      }
      const currentIds = featureIds(features);
      if (
        snapshot.state.totalFeatures !== features.length ||
        snapshot.state.knownIds.length !== currentIds.length ||
        snapshot.state.knownIds.some((id, index) => id !== currentIds[index])
      ) {
        return await store.save({
          ...snapshot.state,
          knownIds: currentIds,
          totalFeatures: features.length,
        }, snapshot, features);
      }
      const checked = new Set(snapshot.state.checkedIds);
      if (!pickNextFeature(features, checked)) {
        return await store.save(resetState(snapshot, features, contextNow(ctx)), snapshot, features);
      }
      return snapshot;
    } catch (error) {
      if (error instanceof ProgressStateConflictError && attempt < 2) continue;
      throw error;
    }
  }
  throw new Error('cycle state CAS retry budget exhausted');
}

async function executeGuardian(
  ctx: GuardianContext,
  dependencies: GuardianDependencies,
): Promise<GuardianOutcome> {
  let features: Feature[];
  try {
    features = await loadFeatures(ctx);
  } catch (error) {
    log(ctx, 'error', 'relayfile-feature-guardian.manifest-load-failed', { error: String(error) });
    return { status: 'FAIL', reason: String(error) };
  }
  log(ctx, 'info', 'relayfile-feature-guardian.manifest-loaded', { features: features.length });

  const channel = contextInput(ctx, 'SLACK_CHANNEL');
  if (!channel) {
    log(ctx, 'warn', 'relayfile-feature-guardian.slack-skipped', { reason: 'SLACK_CHANNEL is absent' });
    return { status: 'SKIP', reason: 'SLACK_CHANNEL is absent; no Slack mount, post, or checkpoint occurred' };
  }
  const slackTimeoutMs = dependencies.slackTimeoutMs ?? SLACK_TIMEOUT_MS;
  const slack = dependencies.slack ?? ctx.slack ?? runtimeSlackTransport(slackTimeoutMs);
  const exactCredentials = credentials(ctx);
  const store = dependencies.store ?? (exactCredentials
    ? createHttpProgressStore(exactCredentials, { fetchImpl: dependencies.fetchImpl, timeoutMs: dependencies.stateTimeoutMs })
    : null);
  if (!store) return { status: 'FAIL', reason: 'exact Relayfile credentials or an exact progress store are required' };

  let snapshot: ProgressSnapshot;
  try {
    snapshot = await prepareSnapshot(ctx, store, features);
  } catch (error) {
    log(ctx, 'error', 'relayfile-feature-guardian.state-prepare-failed', { error: String(error) });
    return { status: 'FAIL', reason: String(error) };
  }
  const feature = pickNextFeature(features, new Set(snapshot.state.checkedIds));
  if (!feature) return { status: 'FAIL', reason: 'cycle reset produced no feature' };
  const text = await messageFor(ctx, feature);
  const idempotencyKey = featureIdempotencyKey(snapshot.state.cycleStartedAt, feature.id);
  let receipt: string;
  try {
    const result = await withDeadline('Slack provider receipt', slackTimeoutMs, (signal) =>
      slack.post({ channelId: channel, text, idempotencyKey, signal }));
    receipt = confirmedSlackReceipt(result);
    if (!receipt) throw new Error('Slack response has no confirmed provider receipt');
  } catch (error) {
    log(ctx, 'error', 'relayfile-feature-guardian.slack-failed', { featureId: feature.id, error: String(error) });
    return { status: 'FAIL', reason: String(error), featureId: feature.id };
  }

  const completed: ProgressState = {
    ...snapshot.state,
    totalFeatures: features.length,
    checkedIds: [...snapshot.state.checkedIds, feature.id],
    lastPost: { featureId: feature.id, ts: receipt },
  };
  try {
    const saved = await store.save(completed, snapshot, features);
    log(ctx, 'info', 'relayfile-feature-guardian.posted', {
      featureId: feature.id, receipt, revision: saved.revision,
    });
  } catch (error) {
    log(ctx, 'error', 'relayfile-feature-guardian.post-checkpoint-failed', {
      featureId: feature.id, receipt, error: String(error), idempotencyKey,
    });
    return {
      status: 'FAIL',
      reason: `provider receipt ${receipt} confirmed but checkpoint failed; retry with idempotency key ${idempotencyKey}: ${String(error)}`,
      featureId: feature.id,
      receipt,
    };
  }
  return { status: 'PASS', reason: 'one feature posted and exact state checkpointed', featureId: feature.id, receipt };
}

export async function runGuardian(
  ctx: GuardianContext,
  _event: unknown,
  dependencies: GuardianDependencies = {},
): Promise<GuardianOutcome> {
  try {
    return await withDeadline(
      'guardian transaction',
      dependencies.transactionTimeoutMs ?? TRANSACTION_TIMEOUT_MS,
      () => executeGuardian(ctx, dependencies),
    );
  } catch (error) {
    log(ctx, 'error', 'relayfile-feature-guardian.transaction-failed', { error: String(error) });
    return { status: 'FAIL', reason: String(error) };
  }
}

export default defineAgent({
  schedules: [{ name: 'hourly-check', cron: '0 * * * *', tz: 'UTC' }],
  handler: async (ctx: WorkforceCtx, event: WorkforceEvent) => {
    const outcome = await runGuardian(ctx, event);
    if (outcome.status === 'FAIL') throw new Error(outcome.reason);
  },
});
