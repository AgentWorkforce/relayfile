import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { describe, it } from 'node:test';
import { resolve } from 'node:path';

import { parse } from 'yaml';

import { catalogSurfaceInventory, collectPublicSurfaces } from '../../../scripts/feature-catalog-surfaces.mjs';
import {
  computeSummary,
  validateManifestData,
} from '../../../scripts/validate-feature-catalog.mjs';
import guardian from './agent.ts';

const root = resolve(new URL('../../../', import.meta.url).pathname);
const manifest = parse(readFileSync(resolve(root, '.agentworkforce/features/manifest.yaml'), 'utf8'));
const procedures = readFileSync(resolve(root, '.agentworkforce/features/verify/procedures.md'), 'utf8');

function clone<T>(value: T): T {
  return structuredClone(value);
}

function errors(value: unknown, proceduresText = procedures): string[] {
  return validateManifestData(value, { root, proceduresText });
}

describe('Relayfile manifest v1.1 contract', () => {
  it('is fully valid with exact computed target-native totals', () => {
    assert.deepEqual(errors(manifest), []);
    assert.deepEqual(computeSummary(manifest), {
      categories: 18,
      features: 122,
      criticality: { critical: 82, hot: 35, standard: 5 },
      verify_tiers: { '1': 55, '2': 27, '3': 1, '4': 9, '5': 19, '6': 11 },
    });
  });

  it('maps every and only every category to a unique named procedure', () => {
    const categories = Object.keys(manifest.categories).sort();
    const routes = Object.keys(manifest.verification.categories).sort();
    assert.deepEqual(routes, categories);
    assert.equal(new Set(Object.values(manifest.verification.categories)).size, categories.length);
    for (const procedure of Object.values<string>(manifest.verification.categories)) {
      assert.match(procedures, new RegExp(`^## ${procedure}$`, 'm'));
    }
  });

  it('rejects missing and unknown routes plus missing documents/headings', () => {
    const missing = clone(manifest);
    delete missing.verification.categories.cli;
    assert.ok(errors(missing).some((error) => error.includes('route missing')));

    const unknown = clone(manifest);
    unknown.verification.categories.unknown = 'unknown-procedure';
    assert.ok(errors(unknown).some((error) => error.includes('unknown category')));

    const document = clone(manifest);
    document.verification.document = '.agentworkforce/features/verify/not-present.md';
    assert.ok(validateManifestData(document, { root }).some((error) => error.includes('does not exist')));

    assert.ok(errors(manifest, procedures.replace('## cli-discovery-and-lifecycle', '## renamed')).some((error) => error.includes('heading missing')));
  });

  it('rejects duplicate IDs, stale totals, and bad tiers', () => {
    const duplicate = clone(manifest);
    duplicate.categories.cli.features[1].id = duplicate.categories.cli.features[0].id;
    assert.ok(errors(duplicate).some((error) => error.includes('duplicate feature id')));

    const stale = clone(manifest);
    stale.summary.features += 1;
    assert.ok(errors(stale).some((error) => error.includes('stale summary totals')));

    const tier = clone(manifest);
    tier.categories.cli.features[0].verify_tier = 0;
    assert.ok(errors(tier).some((error) => error.includes('invalid verify_tier')));
  });

  it('rejects path escapes, absolute paths, and missing locations', () => {
    for (const location of ['../outside', '/tmp/outside', 'not/a/real/source.ts']) {
      const changed = clone(manifest);
      changed.categories.cli.features[0].locations = [location];
      assert.ok(errors(changed).some((error) => /escapes|does not exist/.test(error)), location);
    }
  });

  it('rejects uncovered, overlapping, and stale public surface selectors', () => {
    const omission = clone(manifest);
    omission.categories.cli.features[0].covers = ['cli:relayfile help-does-not-exist'];
    const omissionErrors = errors(omission);
    assert.ok(omissionErrors.some((error) => error.includes('public surface omission: cli:relayfile help')));
    assert.ok(omissionErrors.some((error) => error.includes('matches no public surface')));

    const overlap = clone(manifest);
    overlap.categories.cli.features[1].covers.push('cli:relayfile help');
    assert.ok(errors(overlap).some((error) => error.includes('overlapping feature coverage')));
  });

  it('exposes the same strict structural validator to the scoped guardian runtime', () => {
    assert.deepEqual(validateManifestData(manifest, { structuralOnly: true }), []);
    const escaped = clone(manifest);
    escaped.categories.cli.features[0].locations = ['../outside'];
    assert.ok(validateManifestData(escaped, { structuralOnly: true }).some((error) => error.includes('escapes')));
    const malformedExclusions = clone(manifest);
    malformedExclusions.surface_exclusions = {};
    assert.ok(validateManifestData(malformedExclusions, { structuralOnly: true }).some((error) => error.includes('must be a sequence')));
  });
});

describe('target-specific public surface enumeration', () => {
  const surfaces = collectPublicSurfaces(root);

  it('enumerates every canonical CLI leaf and reviewed alias/default dispatch', () => {
    assert.equal(catalogSurfaceInventory.cliLeaves.length, 51);
    for (const leaf of catalogSurfaceInventory.cliLeaves) assert.ok(surfaces.includes(`cli:relayfile ${leaf}`));
    for (const alias of catalogSurfaceInventory.cliAliases) assert.ok(surfaces.includes(`cli-alias:${alias}`));
  });

  it('enumerates local, control-plane, hosted, and schema fields', () => {
    for (const expected of [
      'http:data:GET /health',
      'http:data:GET /dashboard',
      'http:data:POST /v1/workspaces/{workspaceId}/subscriptions',
      'http:data:POST /v1/workspaces/{workspaceId}/subscriptions/deliveries/{deliveryId}/accept',
      'http:control:POST /v1/integrations/connect',
      'http:hosted:GET /v1/workspaces/{workspaceId}/fs/changes',
      'schema:data:TreeEntry.path',
      'schema:control:HelloResponse.apiVersion',
    ]) assert.ok(surfaces.includes(expected), expected);
  });

  it('enumerates config/environment fields and mount flags', () => {
    for (const expected of [
      'config:env:server:RELAYFILE_BACKEND_PROFILE',
      'config:env:mount:RELAYFILE_SNAPSHOT_DELETE_MIN_RATIO',
      'config:field:mount-state:guards',
      'config:field:provider-binding:webhookSubscriptionId',
      'flag:relayfile-mount:--sync-mode',
      'flag:relayfile-mount:--flush-outbox-once',
    ]) assert.ok(surfaces.includes(expected), expected);
  });

  it('enumerates package maps and exact TS/Python registries', () => {
    for (const expected of [
      'package-export:@relayfile/sdk:./mount-harness',
      'package-export:@relayfile/agents:./openai',
      'package-export:@relayfile/core:.',
      'package-bin:relayfile:relayfile',
      'package-artifact:@relayfile/mount-linux-arm64:bin',
      'export:ts-sdk:RelayFileClient',
      'export:python-root:AsyncRelayFileClient',
    ]) assert.ok(surfaces.includes(expected), expected);
  });

  it('enumerates release-sensitive and dormant-safety implementation areas', () => {
    for (const expected of [
      'release:path:.github/workflows/publish.yml',
      'release:path:scripts/check-contract-surface.sh',
      'implementation:server-delete-storm-library-only',
      'implementation:server-stale-running-library-only',
      'implementation:terminal-provider-state-retained',
    ]) assert.ok(surfaces.includes(expected), expected);
  });
});

describe('guardian persona operating model', () => {
  const persona = JSON.parse(readFileSync(resolve(root, '.agentworkforce/agents/relayfile-feature-guardian/persona.json'), 'utf8'));

  it('matches the supported reference persona shape with target-narrowed scopes', () => {
    assert.deepEqual(persona, {
      id: 'relayfile-feature-guardian',
      intent: 'relay-orchestrator',
      tags: ['relayfile', 'verification', 'proactive', 'catalog', 'health'],
      description: "Reads Relayfile's authoritative feature catalog from a repository-scoped clone each hour, advances an exact revisioned bounded cycle, and optionally posts one idempotent receipt-confirmed check to an input-selected Slack channel.",
      cloud: true,
      harness: 'opencode',
      model: 'deepseek-v4-flash-free',
      harnessSettings: {
        reasoning: 'low',
        timeoutSeconds: 300,
      },
      integrations: {
        github: {
          scope: {
            repo: 'AgentWorkforce/relayfile',
          },
          relayfileMount: {
            requiredReadPaths: [
              '/github/repos/AgentWorkforce/relayfile/.agentworkforce/features/manifest.yaml',
              '/github/repos/AgentWorkforce/relayfile/.agentworkforce/features/critical-paths.md',
              '/github/repos/AgentWorkforce/relayfile/.agentworkforce/features/verify/procedures.md',
            ],
            writeOnlyPaths: [],
          },
        },
        slack: {
          optional: true,
          enabledByInput: 'SLACK_CHANNEL',
          scope: {
            paths: '/slack/channels/${SLACK_CHANNEL}/**',
          },
          relayfileMount: {
            requiredReadPaths: [],
            writeOnlyPaths: ['/slack/channels/${SLACK_CHANNEL}/**'],
          },
        },
      },
      inputs: {
        SLACK_CHANNEL: {
          description: 'Optional disposable Slack channel ID. No Slack mount or post occurs when absent.',
          env: 'SLACK_CHANNEL',
          optional: true,
          picker: {
            provider: 'slack',
            resource: 'channels',
          },
        },
      },
      memory: {
        enabled: true,
        scopes: ['workspace'],
        ttlDays: 14,
      },
      onEvent: './agent.ts',
    });
  });

  it('is hourly with a dedicated harness and target repository clone', () => {
    assert.equal(guardian.__workforceAgent, true);
    assert.deepEqual(guardian.schedules, [{ name: 'hourly-check', cron: '0 * * * *', tz: 'UTC' }]);
    assert.equal(persona.harness, 'opencode');
    assert.equal(persona.model, 'deepseek-v4-flash-free');
    assert.equal(Object.hasOwn(persona, 'useSubscription'), false);
    assert.deepEqual(persona.harnessSettings, { reasoning: 'low', timeoutSeconds: 300 });
    assert.equal(persona.integrations.github.scope.repo, 'AgentWorkforce/relayfile');
  });

  it('has catalog-only clone reads and optional input-gated write-only Slack', () => {
    const github = persona.integrations.github.relayfileMount;
    assert.deepEqual(github.writeOnlyPaths, []);
    assert.equal(github.requiredReadPaths.length, 3);
    assert.ok(github.requiredReadPaths.every((path: string) => path.startsWith('/github/repos/AgentWorkforce/relayfile/.agentworkforce/features/')));
    const slack = persona.integrations.slack;
    assert.equal(slack.optional, true);
    assert.equal(slack.enabledByInput, 'SLACK_CHANNEL');
    assert.equal(slack.scope.paths, '/slack/channels/${SLACK_CHANNEL}/**');
    assert.deepEqual(slack.relayfileMount.requiredReadPaths, []);
    assert.deepEqual(slack.relayfileMount.writeOnlyPaths, ['/slack/channels/${SLACK_CHANNEL}/**']);
    assert.equal(persona.inputs.SLACK_CHANNEL.default, undefined);
    assert.equal(persona.inputs.SLACK_CHANNEL.optional, true);
    assert.deepEqual(persona.memory, { enabled: true, scopes: ['workspace'], ttlDays: 14 });
  });
});
