import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

import { parse } from 'yaml';
import ts from 'typescript';

const CLI_LEAVES = [
  'help',
  'version',
  'setup',
  'login',
  'logout',
  'workspace create',
  'workspace join',
  'workspace use',
  'workspace list',
  'workspace current',
  'workspace status',
  'workspace delete',
  'integration connect',
  'integration available',
  'integration search',
  'integration list',
  'integration disconnect',
  'integration adopt',
  'integration set-metadata',
  'integration bind',
  'integration resolve-path',
  'integration unbind',
  'integration writeback-secret',
  'ops list',
  'ops replay',
  'writeback list',
  'writeback push',
  'writeback update',
  'writeback delete',
  'writeback status',
  'writeback retry',
  'writeback skip-stuck',
  'writeback sweep-drafts',
  'digest rebuild',
  'pull',
  'mount',
  'restart',
  'supervisor install',
  'supervisor uninstall',
  'supervisor status',
  'tree',
  'read',
  'seed',
  'export',
  'status',
  'stop',
  'logs',
  'observer',
  'listen',
  'control-plane serve',
  'dev',
];

const CLI_ALIASES = [
  '-h=>help',
  '--help=>help',
  '--version=>version',
  '<no-args>=>setup',
  'integration catalog=>integration available',
  'integration providers=>integration available',
  'start=>mount',
  'on=>mount',
  'supervisor remove=>supervisor uninstall',
  'ls=>tree',
  'cat=>read',
  'off=>stop',
  'watch=>listen',
];

const HOSTED_HTTP = [
  'GET /v1/workspaces/{workspaceId}/fs/changes',
  'GET /v1/workspaces/{workspaceId}/fs/changes/resource',
  'POST /v1/workspaces/{workspaceId}/webhooks',
  'GET /v1/workspaces/{workspaceId}/webhooks',
  'DELETE /v1/workspaces/{workspaceId}/webhooks/{subscriptionId}',
  'GET /v1/workspaces/{workspaceId}/webhooks/dlq',
  'POST /v1/workspaces/{workspaceId}/webhooks/dlq/{id}/replay',
  'POST /api/v1/workspaces',
  'POST /api/v1/workspaces/{workspaceId}/join',
  'POST /api/v1/workspaces/{workspaceId}/delegated-token',
  'POST /api/v1/workspaces/{workspaceId}/mount-sessions',
  'GET /api/v1/integrations/catalog',
  'POST /api/v1/integrations/connect-session',
  'GET /api/v1/integrations/status',
  'POST /api/v1/integrations/adopt',
  'GET /api/v1/integrations/access-resources',
  'POST /api/v1/integrations/metadata',
];

const SDK_METHODS = {
  'relayfile-client': [
    'getToken', 'getBaseUrl', 'listTree', 'readFile', 'queryFiles', 'writeFile', 'bulkWrite',
    'deleteFile', 'createFork', 'discardFork', 'commitFork', 'getEvents', 'subscribe', 'open',
    'getResourceAtEvent', 'listChangesSince', 'listLastNChanges', 'exportWorkspace',
    'connectWebSocket', 'getOp', 'listOps', 'replayOp', 'replayAdminEnvelope', 'replayAdminOp',
    'getBackendStatus', 'getAdminIngressStatus', 'getAdminSyncStatus', 'getSyncStatus',
    'waitForData', 'getSyncIngressStatus', 'getSyncDeadLetters', 'getSyncDeadLetter',
    'replaySyncDeadLetter', 'ackSyncDeadLetter', 'triggerSyncRefresh', 'ingestWebhook',
    'registerWebhook', 'listWebhooks', 'deleteWebhook', 'getWebhookDeadLetters',
    'replayWebhookDeadLetter', 'listPendingWritebacks', 'ackWriteback', 'sweepWritebackDrafts',
  ],
  setup: [
    'login', 'fromCloudTokens', 'createWorkspace', 'joinWorkspace', 'mountWorkspace',
    'ensureMountedWorkspace', 'getCloudApiUrl', 'client', 'connectIntegration', 'connectNotion',
    'waitForConnection', 'waitForNotion', 'isConnected', 'disconnectIntegration',
    'adoptIntegration', 'listAccessibleResources', 'setIntegrationMetadata', 'getToken',
    'mountEnv', 'agentInvite', 'agentInviteScoped', 'refreshToken', 'getConnectionStatus',
    'WorkspaceHandle.env', 'WorkspaceHandle.status', 'WorkspaceHandle.stop',
  ],
  'control-plane': [
    'hello', 'resolvePath', 'bind', 'listBindings', 'unbind', 'providerStatus', 'connect',
    'writebackSecret', 'listWebhookSubscriptions', 'createWebhookSubscription',
    'deleteWebhookSubscription',
  ],
};

const ENVIRONMENT = {
  server: [
    'RELAYFILE_ADDR', 'RELAYFILE_BACKEND_PROFILE', 'RELAYFILE_DATA_DIR', 'RELAYFILE_STATE_FILE',
    'RELAYFILE_STATE_BACKEND_DSN', 'RELAYFILE_PRODUCTION_DSN', 'RELAYFILE_POSTGRES_DSN',
    'RELAYAUTH_JWKS_URL', 'RELAYFILE_INTERNAL_HMAC_SECRET', 'RELAYFILE_INTERNAL_MAX_SKEW',
    'RELAYFILE_RATE_LIMIT_MAX', 'RELAYFILE_RATE_LIMIT_WINDOW', 'RELAYFILE_MAX_BODY_BYTES',
    'RELAYFILE_DEV_MODE', 'RELAYFILE_ENVELOPE_QUEUE_DSN', 'RELAYFILE_ENVELOPE_QUEUE_FILE',
    'RELAYFILE_ENVELOPE_QUEUE_SIZE', 'RELAYFILE_ENVELOPE_RETRY_DELAY',
    'RELAYFILE_ENVELOPE_WORKERS', 'RELAYFILE_MAX_ENVELOPE_ATTEMPTS',
    'RELAYFILE_MAX_STORED_ENVELOPES', 'RELAYFILE_WRITEBACK_QUEUE_DSN',
    'RELAYFILE_WRITEBACK_QUEUE_FILE', 'RELAYFILE_WRITEBACK_QUEUE_SIZE',
    'RELAYFILE_WRITEBACK_RETRY_DELAY', 'RELAYFILE_WRITEBACK_WORKERS',
    'RELAYFILE_MAX_WRITEBACK_ATTEMPTS', 'RELAYFILE_PROVIDER_MAX_CONCURRENCY',
    'RELAYFILE_EXTERNAL_WRITEBACK', 'RELAYFILE_SUPPRESSION_WINDOW', 'RELAYFILE_COALESCE_WINDOW',
  ],
  cli: [
    'RELAYFILE_SERVER', 'RELAYFILE_BASE_URL', 'RELAYFILE_URL', 'RELAYFILE_TOKEN',
    'RELAYFILE_WORKSPACE', 'RELAYFILE_WORKSPACE_ID', 'RELAYFILE_CLOUD_API_URL',
    'RELAYFILE_CLOUD_TOKEN', 'RELAYFILE_DELEGATED_CREDENTIALS_FILE', 'RELAYFILE_SOCK',
    'RELAYFILE_BIN', 'RELAYFILE_REQUIRE_DAEMON', 'XDG_RUNTIME_DIR', 'AGENT_RELAY_BIN',
    'RELAYCAST_API_KEY', 'RELAYCAST_BASE_URL', 'RELAY_API_KEY', 'RELAY_BASE_URL',
    'RELAYFILE_OBSERVER_URL', 'RELAYFILE_SDK_DEBUG',
  ],
  mount: [
    'RELAYFILE_LOCAL_DIR', 'RELAYFILE_REMOTE_PATH', 'RELAYFILE_MOUNT_CREDS_FILE',
    'RELAYFILE_MOUNT_PATHS_FILE', 'RELAYFILE_MOUNT_PROVIDER', 'RELAYFILE_MOUNT_SCOPES',
    'RELAYFILE_MOUNT_STATE_FILE', 'RELAYFILE_MOUNT_STATE_DIR', 'RELAYFILE_MOUNT_KIND',
    'RELAYFILE_MOUNT_LOCAL_LAYOUT', 'RELAYFILE_MOUNT_SYNC_MODE', 'RELAYFILE_MOUNT_INTERVAL',
    'RELAYFILE_MOUNT_INTERVAL_JITTER', 'RELAYFILE_MOUNT_TIMEOUT', 'RELAYFILE_BOOTSTRAP_TIMEOUT',
    'RELAYFILE_BOOTSTRAP_IDLE_TIMEOUT', 'RELAYFILE_BOOTSTRAP_STALL_CYCLES',
    'RELAYFILE_BOOTSTRAP_READ_CONCURRENCY', 'RELAYFILE_CURSOR_TIMEOUT', 'RELAYFILE_EXPORT_TIMEOUT',
    'RELAYFILE_OUTBOX_TIMEOUT', 'RELAYFILE_INCREMENTAL_READ_NOT_READY_TTL',
    'RELAYFILE_FORCE_FULL_RECONCILE', 'RELAYFILE_MOUNT_FULL_PULL_EVERY', 'RELAYFILE_LAZY_REPOS',
    'RELAYFILE_MOUNT_LAZY_GITHUB_REPOS', 'RELAYFILE_LAZY_SKIP_UNTRACKED_PUSH',
    'RELAYFILE_MOUNT_LOW_MEMORY', 'RELAYFILE_MOUNT_MAX_WATCH_DIRS',
    'RELAYFILE_MOUNT_PPROF_ADDR', 'RELAYFILE_MOUNT_MEMLOG_INTERVAL',
    'RELAYFILE_MOUNT_LOG_HTTP_STATUS', 'RELAYFILE_MOUNT_MODE', 'RELAYFILE_MOUNT_FUSE',
    'RELAYFILE_MOUNT_FUSE_CONTENT_TTL', 'RELAYFILE_MOUNT_WEBSOCKET', 'RELAYFILE_MOUNT_BIN',
    'RELAYFILE_ROOT', 'RELAYFILE_MAX_WRITEBACK_BYTES', 'RELAYFILE_MAX_WRITEBACK_BATCH_BYTES',
    'RELAYFILE_SNAPSHOT_DELETE_MIN_RATIO', 'RELAYFILE_RESET_AFTER_CLOBBER',
    'RELAYFILE_CB_THRESHOLD', 'RELAYFILE_CB_WINDOW', 'RELAYFILE_CB_COOLDOWN', 'RELAYFILE_TZ',
  ],
  agents: [
    'CLOUD_API_ACCESS_TOKEN', 'CLOUD_API_REFRESH_TOKEN', 'CLOUD_API_ACCESS_TOKEN_EXPIRES_AT',
    'CLOUD_API_URL', 'CLOUD_WORKSPACE_ID', 'FILE_OBSERVER_BASE_PATH',
    'NEXT_PUBLIC_FILE_OBSERVER_BASE_PATH', 'NEXT_PUBLIC_RELAYFILE_BASE_URL',
    'NEXT_PUBLIC_RELAYFILE_TOKEN', 'NEXT_PUBLIC_RELAYFILE_WORKSPACE_ID',
    'NEXT_PUBLIC_RELAYFILE_WORKSPACE_IDS',
  ],
  automation: [
    'CI', 'RELAYFILE_PORT', 'RELAYFILE_WEBHOOK_RECEIVER_URL', 'RELAYFILE_TEST_POSTGRES_DSN',
    'SDK_E2E_*', 'POST_AUTH_*', 'HARNESS_*', 'LEAD_*', 'INVITE_FILE', 'OPENROUTER_API_KEY',
    'RELAYFILE_EVAL_*', 'HUMAN_EVAL_*', 'GITHUB_REPOSITORY', 'GITHUB_SERVER_URL',
    'GITHUB_TOKEN', 'GITHUB_STEP_SUMMARY', 'PR_NUMBER',
  ],
};

const CONFIG_FIELDS = {
  credentials: ['server', 'token', 'updatedAt', 'apiUrl', 'accessToken', 'accessTokenExpiresAt'],
  workspace: [
    'default', 'workspaces', 'name', 'id', 'relayWorkspaceId', 'createdAt', 'lastUsedAt',
    'localDir', 'server', 'cloudApiUrl', 'agentName', 'scopes', 'timezone',
  ],
  'mount-state': [
    'workspaceId', 'remoteRoot', 'mode', 'status', 'lastReconcileAt',
    'lastSuccessfulReconcileAt', 'lastEventAt', 'intervalMs', 'providers',
    'pendingWriteback', 'pendingConflicts', 'deniedPaths', 'failedWritebacks', 'stallReason',
    'lastError', 'guards', 'bootstrap',
  ],
  'provider-binding': [
    'provider', 'pathGlob', 'channel', 'webhookId', 'webhookToken', 'subscriptionId',
    'webhookSubscriptionId', 'webhookSubscriptionWorkspaceId', 'createdAt', 'updatedAt',
  ],
};

const PROVIDER_SURFACES = [
  'generic-ingest', 'catalog-cloud', 'catalog-fallback', 'connect-nango', 'connect-composio',
  'setup:github', 'setup:slack-sage', 'setup:slack-my-senior-dev', 'setup:slack-nightcto',
  'setup:notion', 'setup:linear', 'canonical:zendesk', 'canonical:shopify', 'canonical:github',
  'canonical:stripe', 'canonical:slack', 'canonical:linear', 'canonical:jira',
  'canonical:hubspot', 'canonical:salesforce', 'canonical:gmail', 'canonical:notion',
  'canonical:asana', 'canonical:trello', 'canonical:intercom', 'canonical:freshdesk',
  'canonical:discord', 'canonical:twilio', 'canonical:generic', 'schema:github-issue',
  'virtual:LAYOUT.md', 'virtual:.layout.md', 'virtual:_index.json', 'virtual:aliases',
  'virtual:.skills/activity-summary.md', 'virtual:issues/.schema.json',
  'virtual:.relay/dead-letter/*.error.json', 'digest:/digests/today.md',
  'digest:/digests/yesterday.md', 'digest:/digests/this-week.md',
  'digest:/digests/last-week.md', 'digest:date-stamped',
];

const IMPLEMENTATION_SURFACES = [
  'filesystem-crud-bulk-query-export', 'acl-directory-markers', 'revision-if-match',
  'fork-overlay-commit-discard', 'events-feed-websocket', 'ingest-dedup-semantics',
  'digest-regeneration-events', 'operation-retry-replay-dead-letter',
  'writeback-receipts-draft-sweep', 'storage-memory-file-postgres',
  'mount-initial-full-incremental', 'bootstrap-checkpoint-resume', 'durable-outbox-tombstones',
  'mount-delete-ratio-clobber-rehome-pid-fences', 'lazy-low-memory-diagnostics',
  'fuse-cache-invalidation-virtual-readonly', 'local-agent-mount-final-sync',
  'hosted-setup-scoped-invites', 'agent-framework-path-fences', 'ts-client-read-cache',
  'auth-rs256-hmac-replay-limits',
  'terminal-provider-state-retained', 'server-delete-storm-library-only',
  'server-stale-running-library-only',
];

const OBSERVABILITY_SURFACES = [
  'dashboard', 'admin-backends', 'admin-ingress', 'admin-sync', 'cli-status', 'cli-logs',
  'cli-observer', 'mount-state', 'pprof', 'memory-log', 'http-status-log', 'file-observer-ui',
  'eval-reports',
];

const RELEASE_PATHS = [
  '.github/workflows/ci.yml', '.github/workflows/contract.yml', '.github/workflows/publish.yml',
  '.github/workflows/publish-python.yml', '.github/workflows/relayfile-evals.yml',
  'scripts/e2e.ts', 'scripts/conformance.ts', 'scripts/live-e2e.sh', 'scripts/nango-e2e.sh',
  'scripts/check-contract-surface.sh', 'packages/cli/scripts/build-binaries.js',
  'scripts/build-mount-npm-packages.mjs', 'packages/sdk/parity.json',
  '.github/workflows/verify-features.yml', 'workflows/verify-features.ts',
  'scripts/validate-feature-catalog.mjs', 'scripts/verify-features.mjs',
  'scripts/packed-feature-e2e.mjs',
];

function add(set, prefix, values) {
  for (const value of values) set.add(`${prefix}${value}`);
}

function openApiSurfaces(root, relPath, namespace, surfaces) {
  const document = parse(readFileSync(join(root, relPath), 'utf8'));
  for (const [path, pathItem] of Object.entries(document.paths ?? {})) {
    for (const method of ['get', 'put', 'post', 'delete', 'patch', 'head', 'options']) {
      if (pathItem?.[method]) surfaces.add(`http:${namespace}:${method.toUpperCase()} ${path}`);
    }
  }
  for (const [schemaName, schema] of Object.entries(document.components?.schemas ?? {})) {
    for (const property of Object.keys(schema?.properties ?? {})) {
      surfaces.add(`schema:${namespace}:${schemaName}.${property}`);
    }
  }
}

function packageSurfaces(root, surfaces) {
  const packages = [
    ['packages/sdk/typescript/package.json', '@relayfile/sdk'],
    ['packages/core/package.json', '@relayfile/core'],
    ['packages/client/package.json', '@relayfile/client'],
    ['packages/local-mount/package.json', '@relayfile/local-mount'],
    ['packages/agents/package.json', '@relayfile/agents'],
    ['packages/cli/package.json', 'relayfile'],
  ];
  for (const [relPath, fallbackName] of packages) {
    const pkg = JSON.parse(readFileSync(join(root, relPath), 'utf8'));
    const name = pkg.name ?? fallbackName;
    for (const key of Object.keys(pkg.exports ?? {})) surfaces.add(`package-export:${name}:${key}`);
    if (!pkg.exports && (pkg.main || pkg.module || pkg.types)) surfaces.add(`package-export:${name}:.`);
    for (const key of Object.keys(pkg.bin ?? {})) surfaces.add(`package-bin:${name}:${key}`);
  }
  for (const platform of ['darwin-arm64', 'darwin-x64', 'linux-arm64', 'linux-x64']) {
    const pkg = JSON.parse(readFileSync(join(root, `packages/mount-${platform}/package.json`), 'utf8'));
    for (const key of Object.keys(pkg.bin ?? {})) surfaces.add(`package-bin:${pkg.name}:${key}`);
    if ((pkg.files ?? []).includes('bin')) surfaces.add(`package-artifact:${pkg.name}:bin`);
  }
}

function languageExportSurfaces(root, surfaces) {
  const parity = JSON.parse(readFileSync(join(root, 'packages/sdk/parity.json'), 'utf8'));
  const ts = new Set();
  const py = new Set();
  for (const capability of parity.capabilities ?? []) {
    for (const value of capability.tsExports ?? []) ts.add(value);
    for (const value of capability.pyExports ?? []) py.add(value);
  }
  add(surfaces, 'export:ts-sdk:', [...ts]);
  add(surfaces, 'export:python-parity:', [...py]);

  const python = readFileSync(join(root, 'packages/sdk/python/src/relayfile/__init__.py'), 'utf8');
  const block = python.match(/__all__\s*=\s*\[([\s\S]*?)\]/)?.[1] ?? '';
  const rootExports = [...block.matchAll(/["']([^"']+)["']/g)].map((match) => match[1]);
  add(surfaces, 'export:python-root:', rootExports);
}

function mountFlagSurfaces(root, surfaces) {
  const source = readFileSync(join(root, 'cmd/relayfile-mount/main.go'), 'utf8');
  const flags = new Set();
  for (const match of source.matchAll(/flag\.(?:String|Bool|Duration|Int|Float64)\(\s*"([^"]+)"/g)) {
    flags.add(match[1]);
  }
  for (const match of source.matchAll(/flag\.(?:String|Bool|Duration|Int|Float64)?Var\([^,]*,\s*"([^"]+)"/g)) {
    flags.add(match[1]);
  }
  add(surfaces, 'flag:relayfile-mount:--', [...flags]);
}

function typescriptConfigSurfaces(root, surfaces) {
  const groups = {
    sdk: 'packages/sdk/typescript/src',
    core: 'packages/core/src',
    client: 'packages/client/src',
    agents: 'packages/agents/src',
    'local-mount': 'packages/local-mount/src',
  };
  const walk = (directory) => readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    return entry.isDirectory() ? walk(path) : entry.isFile() && entry.name.endsWith('.ts') && !entry.name.includes('.test.') ? [path] : [];
  });
  for (const [group, relPath] of Object.entries(groups)) {
    for (const path of walk(join(root, relPath))) {
      const source = ts.createSourceFile(path, readFileSync(path, 'utf8'), ts.ScriptTarget.Latest, true);
      for (const statement of source.statements) {
        if (!ts.isInterfaceDeclaration(statement) || !/(Options|Config)$/.test(statement.name.text)) continue;
        if (!statement.modifiers?.some((modifier) => modifier.kind === ts.SyntaxKind.ExportKeyword)) continue;
        for (const member of statement.members) {
          if (!ts.isPropertySignature(member) || !member.name) continue;
          const name = ts.isIdentifier(member.name) || ts.isStringLiteral(member.name) ? member.name.text : member.name.getText(source);
          surfaces.add(`config:ts:${group}:${statement.name.text}.${name}`);
        }
      }
    }
  }
}

export function collectPublicSurfaces(root) {
  const surfaces = new Set();
  add(surfaces, 'cli:relayfile ', CLI_LEAVES);
  add(surfaces, 'cli-alias:', CLI_ALIASES);
  surfaces.add('executable:relayfile-server');
  surfaces.add('executable:relayfile-mount');
  openApiSurfaces(root, 'openapi/relayfile-v1.openapi.yaml', 'data', surfaces);
  openApiSurfaces(root, 'openapi/relayfile-control-plane-v1.openapi.yaml', 'control', surfaces);
  surfaces.add('http:data:GET /dashboard');
  add(surfaces, 'http:hosted:', HOSTED_HTTP);
  for (const [owner, methods] of Object.entries(SDK_METHODS)) {
    add(surfaces, `sdk-ts:${owner}:`, methods);
  }
  packageSurfaces(root, surfaces);
  languageExportSurfaces(root, surfaces);
  mountFlagSurfaces(root, surfaces);
  typescriptConfigSurfaces(root, surfaces);
  for (const [group, names] of Object.entries(ENVIRONMENT)) add(surfaces, `config:env:${group}:`, names);
  for (const [group, fields] of Object.entries(CONFIG_FIELDS)) add(surfaces, `config:field:${group}:`, fields);
  add(surfaces, 'provider:', PROVIDER_SURFACES);
  add(surfaces, 'implementation:', IMPLEMENTATION_SURFACES);
  add(surfaces, 'observability:', OBSERVABILITY_SURFACES);
  add(surfaces, 'release:path:', RELEASE_PATHS);
  surfaces.add('automation:integration-verifier-persona');
  surfaces.add('automation:relayfile-evals');
  surfaces.add('automation:feature-catalog');
  surfaces.add('automation:feature-guardian');
  return [...surfaces].sort();
}

export const catalogSurfaceInventory = {
  cliLeaves: CLI_LEAVES,
  cliAliases: CLI_ALIASES,
  environment: ENVIRONMENT,
  configFields: CONFIG_FIELDS,
  releasePaths: RELEASE_PATHS,
};
