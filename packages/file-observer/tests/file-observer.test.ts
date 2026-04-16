// @vitest-environment jsdom

import React from 'react';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

interface TreeEntry {
  path: string;
  type: 'file' | 'dir';
  revision: string;
  provider?: string;
  providerObjectId?: string;
  size?: number;
  updatedAt?: string;
}

interface TreeResponse {
  path: string;
  entries: TreeEntry[];
  nextCursor: string | null;
}

interface FileReadResponse {
  path: string;
  revision: string;
  contentType: string;
  content: string;
  encoding?: 'utf-8' | 'base64';
  provider?: string;
  providerObjectId?: string;
  lastEditedAt?: string;
  semantics?: {
    properties?: Record<string, string>;
    relations?: string[];
    permissions?: string[];
    comments?: string[];
  };
}

interface SyncStatusResponse {
  workspaceId: string;
  providers: Array<{
    provider: string;
    status: 'healthy' | 'lagging' | 'error' | 'paused';
    lagSeconds?: number;
    lastError?: string | null;
    deadLetteredEnvelopes?: number;
    deadLetteredOps?: number;
  }>;
}

interface WorkspaceFixture {
  tree: Record<string, TreeResponse>;
  files: Record<string, FileReadResponse>;
  search: Record<string, Array<{ path: string; revision: string; contentType: string; provider?: string; size: number; lastEditedAt?: string }>>;
  syncStatus: SyncStatusResponse;
}

type FetchMock = ReturnType<typeof vi.fn>;

const workspaceFixtures: Record<string, WorkspaceFixture> = {
  default: {
    tree: {
      '/': {
        path: '/',
        nextCursor: null,
        entries: [
          {
            path: '/docs',
            type: 'dir',
            revision: 'docs_rev_1',
            provider: 'google-drive',
            updatedAt: '2026-04-16T09:00:00.000Z'
          },
          {
            path: '/README.md',
            type: 'file',
            revision: 'readme_rev_1',
            provider: 'github',
            size: 2048,
            updatedAt: '2026-04-16T09:10:00.000Z'
          },
          {
            path: '/tickets.json',
            type: 'file',
            revision: 'tickets_rev_1',
            provider: 'zendesk',
            size: 512,
            updatedAt: '2026-04-16T09:15:00.000Z'
          }
        ]
      },
      '/docs': {
        path: '/docs',
        nextCursor: null,
        entries: [
          {
            path: '/docs/guide.md',
            type: 'file',
            revision: 'guide_rev_1',
            provider: 'google-drive',
            size: 1024,
            updatedAt: '2026-04-16T09:20:00.000Z'
          }
        ]
      }
    },
    files: {
      '/README.md': {
        path: '/README.md',
        revision: 'readme_rev_1',
        contentType: 'text/markdown',
        content: '# Relayfile\n\nDashboard overview',
        provider: 'github',
        providerObjectId: 'gh_123',
        lastEditedAt: '2026-04-16T09:10:00.000Z',
        semantics: {
          properties: {
            'provider.object_type': 'document',
            repo: 'relayfile'
          },
          relations: ['workspace:default'],
          permissions: ['read', 'write'],
          comments: ['Reviewed']
        }
      },
      '/tickets.json': {
        path: '/tickets.json',
        revision: 'tickets_rev_1',
        contentType: 'application/json',
        content: '{"count":2}',
        provider: 'zendesk',
        lastEditedAt: '2026-04-16T09:15:00.000Z'
      },
      '/docs/guide.md': {
        path: '/docs/guide.md',
        revision: 'guide_rev_1',
        contentType: 'text/markdown',
        content: '# Guide',
        provider: 'google-drive',
        lastEditedAt: '2026-04-16T09:20:00.000Z'
      }
    },
    search: {
      'all:sync': [
        {
          path: '/repo-sync.md',
          revision: 'search_repo_1',
          contentType: 'text/markdown',
          provider: 'github',
          size: 330,
          lastEditedAt: '2026-04-16T09:30:00.000Z'
        },
        {
          path: '/sync-handbook.md',
          revision: 'search_drive_1',
          contentType: 'text/markdown',
          provider: 'google-drive',
          size: 420,
          lastEditedAt: '2026-04-16T09:31:00.000Z'
        }
      ],
      'github:sync': [
        {
          path: '/repo-sync.md',
          revision: 'search_repo_1',
          contentType: 'text/markdown',
          provider: 'github',
          size: 330,
          lastEditedAt: '2026-04-16T09:30:00.000Z'
        }
      ]
    },
    syncStatus: {
      workspaceId: 'default',
      providers: [
        {
          provider: 'github',
          status: 'healthy',
          lagSeconds: 4,
          deadLetteredEnvelopes: 0,
          deadLetteredOps: 0,
          lastError: null
        },
        {
          provider: 'google-drive',
          status: 'healthy',
          lagSeconds: 8,
          deadLetteredEnvelopes: 0,
          deadLetteredOps: 0,
          lastError: null
        }
      ]
    }
  },
  staging: {
    tree: {
      '/': {
        path: '/',
        nextCursor: null,
        entries: [
          {
            path: '/staging-notes.md',
            type: 'file',
            revision: 'staging_rev_1',
            provider: 'notion',
            size: 900,
            updatedAt: '2026-04-16T10:00:00.000Z'
          }
        ]
      }
    },
    files: {
      '/staging-notes.md': {
        path: '/staging-notes.md',
        revision: 'staging_rev_1',
        contentType: 'text/markdown',
        content: '# Staging Notes',
        provider: 'notion',
        lastEditedAt: '2026-04-16T10:00:00.000Z'
      }
    },
    search: {
      'all:sync': []
    },
    syncStatus: {
      workspaceId: 'staging',
      providers: [
        {
          provider: 'notion',
          status: 'lagging',
          lagSeconds: 22,
          deadLetteredEnvelopes: 1,
          deadLetteredOps: 0,
          lastError: 'Backfill in progress'
        }
      ]
    }
  }
};

const originalEnv = {
  NEXT_PUBLIC_RELAYFILE_BASE_URL: process.env.NEXT_PUBLIC_RELAYFILE_BASE_URL,
  NEXT_PUBLIC_RELAYFILE_TOKEN: process.env.NEXT_PUBLIC_RELAYFILE_TOKEN,
  NEXT_PUBLIC_RELAYFILE_WORKSPACE_IDS: process.env.NEXT_PUBLIC_RELAYFILE_WORKSPACE_IDS,
  NEXT_PUBLIC_RELAYFILE_WORKSPACE_ID: process.env.NEXT_PUBLIC_RELAYFILE_WORKSPACE_ID
};

function jsonResponse(body: unknown, init?: ResponseInit): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: {
      'content-type': 'application/json'
    },
    ...init
  });
}

function getCallUrls(fetchMock: FetchMock): URL[] {
  return fetchMock.mock.calls.map(([input]) => new URL(String(input)));
}

function createRelayfileFetchMock(): FetchMock {
  return vi.fn(async (input: string | URL) => {
    const url = new URL(String(input));
    const match = url.pathname.match(/^\/v1\/workspaces\/([^/]+)\/(fs\/tree|fs\/file|fs\/query|sync\/status)$/);

    if (!match) {
      return jsonResponse({ message: `Unhandled path: ${url.pathname}` }, { status: 404, statusText: 'Not Found' });
    }

    const workspaceId = decodeURIComponent(match[1]);
    const endpoint = match[2];
    const fixture = workspaceFixtures[workspaceId];

    if (!fixture) {
      return jsonResponse({ message: `Unknown workspace: ${workspaceId}` }, { status: 404, statusText: 'Not Found' });
    }

    if (endpoint === 'fs/tree') {
      const path = url.searchParams.get('path') ?? '/';
      const tree = fixture.tree[path];
      return tree
        ? jsonResponse(tree)
        : jsonResponse({ message: `Unknown tree path: ${path}` }, { status: 404, statusText: 'Not Found' });
    }

    if (endpoint === 'fs/file') {
      const path = url.searchParams.get('path') ?? '';
      const file = fixture.files[path];
      return file
        ? jsonResponse(file)
        : jsonResponse({ message: `Unknown file path: ${path}` }, { status: 404, statusText: 'Not Found' });
    }

    if (endpoint === 'fs/query') {
      const path = url.searchParams.get('path') ?? '';
      const provider = url.searchParams.get('provider') ?? 'all';
      const items = fixture.search[`${provider}:${path}`] ?? [];
      return jsonResponse({ items, nextCursor: null });
    }

    return jsonResponse(fixture.syncStatus);
  });
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, ms);
  });
}

async function renderDashboard() {
  process.env.NEXT_PUBLIC_RELAYFILE_BASE_URL = 'https://relayfile.test';
  process.env.NEXT_PUBLIC_RELAYFILE_TOKEN = 'public-test-token';
  process.env.NEXT_PUBLIC_RELAYFILE_WORKSPACE_IDS = 'default,staging';
  process.env.NEXT_PUBLIC_RELAYFILE_WORKSPACE_ID = '';

  const fetchMock = createRelayfileFetchMock();
  vi.stubGlobal('fetch', fetchMock);
  vi.stubGlobal('React', React);
  vi.resetModules();

  const module = await import('../src/app/page');
  const Page = module.default;
  render(React.createElement(Page));

  await screen.findByText('File tree');
  await screen.findByRole('button', { name: /README\.md/i });

  return { fetchMock };
}

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
  process.env.NEXT_PUBLIC_RELAYFILE_BASE_URL = originalEnv.NEXT_PUBLIC_RELAYFILE_BASE_URL;
  process.env.NEXT_PUBLIC_RELAYFILE_TOKEN = originalEnv.NEXT_PUBLIC_RELAYFILE_TOKEN;
  process.env.NEXT_PUBLIC_RELAYFILE_WORKSPACE_IDS = originalEnv.NEXT_PUBLIC_RELAYFILE_WORKSPACE_IDS;
  process.env.NEXT_PUBLIC_RELAYFILE_WORKSPACE_ID = originalEnv.NEXT_PUBLIC_RELAYFILE_WORKSPACE_ID;
});

describe('file-observer dashboard', () => {
  it('renders the file tree and lazy-loads nested directories', async () => {
    const { fetchMock } = await renderDashboard();

    expect(screen.getByText('3 loaded entries')).toBeDefined();
    expect(screen.getByRole('button', { name: /docs/i })).toBeDefined();
    expect(screen.getByRole('button', { name: /README\.md/i })).toBeDefined();
    expect(screen.getAllByText('google-drive').length).toBeGreaterThan(0);

    fireEvent.click(screen.getByRole('button', { name: /docs/i }));

    expect(await screen.findByRole('button', { name: /guide\.md/i })).toBeDefined();
    expect(
      getCallUrls(fetchMock).some(
        (url) => url.pathname.includes('/v1/workspaces/default/fs/tree') && url.searchParams.get('path') === '/docs'
      )
    ).toBe(true);
  });

  it('selects a file and renders the details panel with metadata and content', async () => {
    const { fetchMock } = await renderDashboard();

    fireEvent.click(screen.getByRole('button', { name: /README\.md/i }));

    await waitFor(() => {
      expect(
        getCallUrls(fetchMock).some(
          (url) => url.pathname.includes('/v1/workspaces/default/fs/file') && url.searchParams.get('path') === '/README.md'
        )
      ).toBe(true);
    });

    expect((await screen.findAllByText('/README.md')).length).toBeGreaterThan(0);
    expect(screen.getByText('readme_rev_1')).toBeDefined();
    expect(
      screen.getByText((_, node) => node?.tagName === 'PRE' && node.textContent === '# Relayfile\n\nDashboard overview')
    ).toBeDefined();
    expect(screen.getByText('provider.object_type: document')).toBeDefined();
    expect(screen.getByText('repo: relayfile')).toBeDefined();
    expect(screen.getByText('workspace:default')).toBeDefined();
    expect(screen.getByText('write')).toBeDefined();
  });

  it('switches workspaces and reloads the tree and sync status for the selected workspace', async () => {
    const { fetchMock } = await renderDashboard();

    const [workspaceSelect] = screen.getAllByRole('combobox');
    fireEvent.change(workspaceSelect, { target: { value: 'staging' } });

    await waitFor(() => {
      expect(
        getCallUrls(fetchMock).some((url) => url.pathname.includes('/v1/workspaces/staging/fs/tree'))
      ).toBe(true);
    });

    expect(await screen.findByRole('heading', { name: 'staging' })).toBeDefined();
    expect(screen.getByRole('button', { name: /staging-notes\.md/i })).toBeDefined();
    expect(screen.queryByRole('button', { name: /README\.md/i })).toBeNull();
    expect(
      getCallUrls(fetchMock).some((url) => url.pathname.includes('/v1/workspaces/staging/sync/status'))
    ).toBe(true);
  });

  it('queries search results and reapplies the provider filter when searching files', async () => {
    const { fetchMock } = await renderDashboard();
    const searchInput = screen.getByPlaceholderText('Search by path or file name');
    const [, providerSelect] = screen.getAllByRole('combobox');

    fireEvent.change(searchInput, { target: { value: 'sync' } });
    await sleep(350);

    expect(await screen.findByText('2 files')).toBeDefined();
    expect(screen.getByRole('button', { name: /repo-sync\.md/i })).toBeDefined();
    expect(screen.getByRole('button', { name: /sync-handbook\.md/i })).toBeDefined();

    fireEvent.change(providerSelect, { target: { value: 'github' } });
    await sleep(350);

    await waitFor(() => {
      expect(
        getCallUrls(fetchMock).some(
          (url) =>
            url.pathname.includes('/v1/workspaces/default/fs/query') &&
            url.searchParams.get('path') === 'sync' &&
            url.searchParams.get('provider') === 'github'
        )
      ).toBe(true);
    });

    expect(await screen.findByText('1 files')).toBeDefined();
    expect(screen.getByRole('button', { name: /repo-sync\.md/i })).toBeDefined();
    expect(screen.queryByRole('button', { name: /sync-handbook\.md/i })).toBeNull();
  });
});
