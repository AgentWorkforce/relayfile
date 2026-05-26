import { mkdtemp, mkdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { RelayFileClient } from './client.js';

import { seedAclRules, seedWorkflowAcls, seedWorkspace } from './workspace-seeder.js';

const originalFetch = globalThis.fetch;

async function createWorkspace(
  files: Record<string, string | Buffer>
): Promise<{ dir: string; cleanup: () => Promise<void> }> {
  const dir = await mkdtemp(path.join(tmpdir(), 'relay-seeder-'));

  for (const [relativePath, content] of Object.entries(files)) {
    const filePath = path.join(dir, relativePath);
    await mkdir(path.dirname(filePath), { recursive: true });
    await writeFile(filePath, content);
  }

  return {
    dir,
    cleanup: () => rm(dir, { recursive: true, force: true }),
  };
}

function jsonResponseText(body: string, status = 200): { ok: boolean; status: number; text(): Promise<string> } {
  return {
    ok: status >= 200 && status < 300,
    status,
    async text(): Promise<string> {
      return body;
    },
  };
}

interface FetchCall {
  url: string;
  options: RequestInit;
}

function singleFetchCall(fetchMock: ReturnType<typeof vi.fn>): FetchCall {
  expect(fetchMock).toHaveBeenCalledTimes(1);
  const [url, options] = fetchMock.mock.calls[0] as [string, RequestInit];
  return { url, options };
}

function parseFetchBody(fetchMock: ReturnType<typeof vi.fn>): { files: unknown[] } {
  const { options } = singleFetchCall(fetchMock);
  expect(typeof options.body).toBe('string');
  return JSON.parse(options.body as string) as { files: unknown[] };
}

afterEach(() => {
  vi.restoreAllMocks();
  globalThis.fetch = originalFetch;
});

describe('workspace-seeder', () => {
  it('seedWorkspace posts the expected HTTP payload after SDK fallback', async () => {
    const workspace = await createWorkspace({
      'alpha.txt': 'alpha payload',
      'binary.bin': Buffer.from([0xff, 0x00, 0x61]),
      '.relay/ignored.txt': 'skip',
      '.git/config': 'skip',
      'node_modules/pkg/index.js': 'skip',
      'custom-skip/ignored.txt': 'skip',
      '.relayfile-mount-state.json': '{"skip":true}',
    });

    try {
      vi.spyOn(RelayFileClient.prototype, 'bulkWrite').mockImplementation(async () => {
        throw new Error('fall back to HTTP');
      });

      const fetchMock = vi.fn(async () =>
        jsonResponseText(JSON.stringify({ written: 2, errorCount: 0, errors: [] }))
      );
      globalThis.fetch = fetchMock as unknown as typeof fetch;

      const seededCount = await seedWorkspace(
        'https://relay.example///',
        'admin-token',
        ' workspace-123 ',
        workspace.dir,
        ['custom-skip']
      );

      expect(seededCount).toBe(2);

      const { url, options } = singleFetchCall(fetchMock);
      expect(url).toBe('https://relay.example/v1/workspaces/workspace-123/fs/bulk');
      expect(options.method).toBe('POST');
      const headers = options.headers as Record<string, string>;
      expect(headers.Authorization).toBe('Bearer admin-token');
      expect(headers['Content-Type']).toBe('application/json');
      expect(headers['X-Correlation-Id']).toMatch(/^seed-workspace-workspace-123-\d+-0$/u);

      const body = parseFetchBody(fetchMock);
      expect(body.files).toEqual([
        {
          path: '/alpha.txt',
          content: 'alpha payload',
          encoding: 'utf-8',
        },
        {
          path: '/binary.bin',
          content: Buffer.from([0xff, 0x00, 0x61]).toString('base64'),
          encoding: 'base64',
        },
      ]);
    } finally {
      await workspace.cleanup();
    }
  });

  it('seedWorkspace surfaces errorCount from bulk-write responses', async () => {
    const workspace = await createWorkspace({
      'alpha.txt': 'alpha payload',
    });

    try {
      vi.spyOn(RelayFileClient.prototype, 'bulkWrite').mockImplementation(async () => {
        throw new Error('fall back to HTTP');
      });

      const fetchMock = vi.fn(async () =>
        jsonResponseText(
          JSON.stringify({
            written: 0,
            errorCount: 1,
            errors: [{ path: '/alpha.txt', error: 'permission denied' }],
          })
        )
      );
      globalThis.fetch = fetchMock as unknown as typeof fetch;

      await expect(
        seedWorkspace(
          'https://relay.example',
          'admin-token',
          'workspace-errs',
          workspace.dir,
          []
        )
      ).rejects.toThrow(/workspace-errs/u);
    } finally {
      await workspace.cleanup();
    }
  });

  it('seedAclRules formats ACL files for root and nested directories', async () => {
    vi.spyOn(RelayFileClient.prototype, 'bulkWrite').mockImplementation(async () => {
      throw new Error('fall back to HTTP');
    });

    const fetchMock = vi.fn(async () =>
      jsonResponseText(JSON.stringify({ written: 2, errorCount: 0, errors: [] }))
    );
    globalThis.fetch = fetchMock as unknown as typeof fetch;

    await seedAclRules('https://relay.example/', 'acl-token', 'workspace-acl', {
      '/': ['allow:agent:lead:read'],
      '/docs/': ['allow:agent:writer:write', 'deny:agent:reader'],
    });

    const body = parseFetchBody(fetchMock);
    expect(body.files).toEqual([
      {
        path: '/.relayfile.acl',
        content: JSON.stringify({
          semantics: { permissions: ['allow:agent:lead:read'] },
        }),
        encoding: 'utf-8',
      },
      {
        path: '/docs/.relayfile.acl',
        content: JSON.stringify({
          semantics: {
            permissions: ['allow:agent:writer:write', 'deny:agent:reader'],
          },
        }),
        encoding: 'utf-8',
      },
    ]);
  });

  it('seedWorkflowAcls merges multiple agents onto shared directories', async () => {
    vi.spyOn(RelayFileClient.prototype, 'bulkWrite').mockImplementation(async () => {
      throw new Error('fall back to HTTP');
    });

    const fetchMock = vi.fn(async () =>
      jsonResponseText(JSON.stringify({ written: 2, errorCount: 0, errors: [] }))
    );
    globalThis.fetch = fetchMock as unknown as typeof fetch;

    await seedWorkflowAcls({
      relayfileUrl: 'https://relay.example',
      adminToken: 'workflow-token',
      workspace: 'workflow-merge',
      agents: [
        { name: 'qa-reviewer', acl: { src: ['read'] } },
        { name: 'builder', acl: { 'src\\': ['write'], '/docs/': ['read'] } },
        { name: 'analyst', acl: { docs: ['read'] } },
      ],
    });

    const body = parseFetchBody(fetchMock);
    expect(body.files).toEqual([
      {
        path: '/docs/.relayfile.acl',
        content: JSON.stringify({
          semantics: {
            permissions: [
              'allow:agent:analyst:read',
              'allow:agent:builder:read',
              'allow:agent:qa-reviewer:read',
            ],
          },
        }),
        encoding: 'utf-8',
      },
      {
        path: '/src/.relayfile.acl',
        content: JSON.stringify({
          semantics: {
            permissions: [
              'allow:agent:builder:read',
              'allow:agent:builder:write',
              'allow:agent:qa-reviewer:read',
              'deny:agent:analyst',
            ],
          },
        }),
        encoding: 'utf-8',
      },
    ]);
  });

  it('seedWorkflowAcls unions deny rules for agents missing directory access', async () => {
    vi.spyOn(RelayFileClient.prototype, 'bulkWrite').mockImplementation(async () => {
      throw new Error('fall back to HTTP');
    });

    const fetchMock = vi.fn(async () =>
      jsonResponseText(JSON.stringify({ written: 2, errorCount: 0, errors: [] }))
    );
    globalThis.fetch = fetchMock as unknown as typeof fetch;

    await seedWorkflowAcls({
      relayfileUrl: 'https://relay.example',
      adminToken: 'workflow-token',
      workspace: 'workflow-deny',
      agents: [
        { name: 'alpha', acl: { src: ['read'] } },
        { name: 'beta', acl: { docs: ['write'] } },
      ],
    });

    const body = parseFetchBody(fetchMock);
    expect(body.files).toEqual([
      {
        path: '/docs/.relayfile.acl',
        content: JSON.stringify({
          semantics: {
            permissions: ['allow:agent:beta:read', 'allow:agent:beta:write', 'deny:agent:alpha'],
          },
        }),
        encoding: 'utf-8',
      },
      {
        path: '/src/.relayfile.acl',
        content: JSON.stringify({
          semantics: {
            permissions: ['allow:agent:alpha:read', 'deny:agent:beta'],
          },
        }),
        encoding: 'utf-8',
      },
    ]);
  });

  it('seedWorkflowAcls is a no-op when there are no ACL directories to seed', async () => {
    const bulkWriteMock = vi.spyOn(RelayFileClient.prototype, 'bulkWrite').mockImplementation(async () => {
      throw new Error('bulkWrite should not be called');
    });
    const fetchMock = vi.fn(async () => {
      throw new Error('fetch should not be called');
    });
    globalThis.fetch = fetchMock as unknown as typeof fetch;

    await seedWorkflowAcls({
      relayfileUrl: 'https://relay.example',
      adminToken: 'workflow-token',
      workspace: 'workflow-empty',
      agents: [
        { name: 'builder', acl: {} },
        { name: 'qa-reviewer', acl: {} },
      ],
    });

    expect(bulkWriteMock).toHaveBeenCalledTimes(0);
    expect(fetchMock).toHaveBeenCalledTimes(0);
  });

  it('seedAclRules surfaces HTTP failures from the fallback API', async () => {
    vi.spyOn(RelayFileClient.prototype, 'bulkWrite').mockImplementation(async () => {
      throw new Error('fall back to HTTP');
    });

    const fetchMock = vi.fn(async () => jsonResponseText('relay unavailable', 503));
    globalThis.fetch = fetchMock as unknown as typeof fetch;

    await expect(
      seedAclRules('https://relay.example', 'acl-token', 'workspace-http', {
        '/': ['allow:agent:builder:read'],
      })
    ).rejects.toThrow('failed to seed workspace workspace-http: HTTP 503 relay unavailable');
  });
});
