'use client';

import type { ReactNode } from 'react';
import { startTransition, useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertCircle,
  ChevronDown,
  ChevronRight,
  FileCode2,
  FileText,
  Folder,
  FolderOpen,
  Loader2,
  RefreshCcw,
  Search,
  Shield,
  SlidersHorizontal,
  Sparkles
} from 'lucide-react';

type FileNodeType = 'file' | 'dir';

interface TreeEntry {
  path: string;
  type: FileNodeType;
  revision: string;
  provider?: string;
  providerObjectId?: string;
  size?: number;
  updatedAt?: string;
  propertyCount?: number;
  relationCount?: number;
  permissionCount?: number;
  commentCount?: number;
}

interface TreeResponse {
  path: string;
  entries: TreeEntry[];
  nextCursor: string | null;
}

interface FileSemantics {
  properties?: Record<string, string>;
  relations?: string[];
  permissions?: string[];
  comments?: string[];
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
  semantics?: FileSemantics;
}

interface FileQueryItem {
  path: string;
  revision: string;
  contentType: string;
  provider?: string;
  providerObjectId?: string;
  lastEditedAt?: string;
  size: number;
  properties?: Record<string, string>;
  relations?: string[];
  permissions?: string[];
  comments?: string[];
}

type SyncProviderStatusState = 'healthy' | 'lagging' | 'error' | 'paused';

interface SyncProviderStatus {
  provider: string;
  status: SyncProviderStatusState;
  lagSeconds?: number;
  lastError?: string | null;
  deadLetteredEnvelopes?: number;
  deadLetteredOps?: number;
}

interface SyncStatusResponse {
  workspaceId: string;
  providers: SyncProviderStatus[];
}

interface ApiErrorBody {
  code?: string;
  message?: string;
}

interface SelectedEntry {
  path: string;
  type: FileNodeType;
  revision?: string;
  provider?: string;
  providerObjectId?: string;
  size?: number;
  updatedAt?: string;
  contentType?: string;
  properties?: Record<string, string>;
  relations?: string[];
  permissions?: string[];
  comments?: string[];
}

type TreeCache = Record<string, TreeResponse>;

const DEFAULT_BASE_URL = process.env.NEXT_PUBLIC_RELAYFILE_BASE_URL ?? 'https://api.relayfile.dev';
const PUBLIC_TOKEN = process.env.NEXT_PUBLIC_RELAYFILE_TOKEN ?? '';
const WORKSPACE_ENV =
  process.env.NEXT_PUBLIC_RELAYFILE_WORKSPACE_IDS ?? process.env.NEXT_PUBLIC_RELAYFILE_WORKSPACE_ID ?? '';

function parseWorkspaceIds(raw: string): string[] {
  return Array.from(
    new Set(
      raw
        .split(/[,\n]/)
        .map((value) => value.trim())
        .filter(Boolean)
    )
  );
}

function buildQuery(params: Record<string, string | number | undefined>): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') {
      query.set(key, String(value));
    }
  }
  const encoded = query.toString();
  return encoded ? `?${encoded}` : '';
}

function createCorrelationId(): string {
  return `rf_${typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function' ? crypto.randomUUID() : Date.now()}`;
}

function normalizeError(error: unknown): string {
  if (error instanceof Error && error.message) {
    return error.message;
  }
  return 'Unexpected relayfile request failure.';
}

function formatDate(value?: string): string {
  if (!value) {
    return 'Unknown';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short'
  }).format(date);
}

function formatBytes(value?: number): string {
  if (value === undefined || Number.isNaN(value)) {
    return 'Unknown';
  }
  if (value < 1024) {
    return `${value} B`;
  }
  const units = ['KB', 'MB', 'GB', 'TB'];
  let size = value / 1024;
  let unitIndex = 0;
  while (size >= 1024 && unitIndex < units.length - 1) {
    size /= 1024;
    unitIndex += 1;
  }
  return `${size.toFixed(size >= 10 ? 0 : 1)} ${units[unitIndex]}`;
}

function truncateContent(content: string, encoding?: 'utf-8' | 'base64'): string {
  if (encoding === 'base64') {
    return 'Binary or base64-encoded content preview is not rendered in the dashboard.';
  }
  return content.length > 4000 ? `${content.slice(0, 4000)}\n\n… truncated …` : content;
}

function createRelayFileClient(baseUrl: string, token: string) {
  async function request<T>(path: string, signal?: AbortSignal): Promise<T> {
    const response = await fetch(`${baseUrl.replace(/\/+$/, '')}${path}`, {
      method: 'GET',
      signal,
      headers: {
        Accept: 'application/json',
        Authorization: token ? `Bearer ${token}` : '',
        'X-Correlation-Id': createCorrelationId()
      }
    });

    if (!response.ok) {
      let payload: ApiErrorBody | undefined;
      try {
        payload = (await response.json()) as ApiErrorBody;
      } catch {
        payload = undefined;
      }
      const detail = payload?.message ?? `${response.status} ${response.statusText}`;
      throw new Error(detail);
    }

    return (await response.json()) as T;
  }

  return {
    listTree(workspaceId: string, options: { path?: string; depth?: number; cursor?: string; signal?: AbortSignal } = {}) {
      const query = buildQuery({
        path: options.path ?? '/',
        depth: options.depth,
        cursor: options.cursor
      });
      return request<TreeResponse>(`/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/tree${query}`, options.signal);
    },
    readFile(workspaceId: string, path: string, signal?: AbortSignal) {
      const query = buildQuery({ path });
      return request<FileReadResponse>(`/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/file${query}`, signal);
    },
    queryFiles(
      workspaceId: string,
      options: { path?: string; provider?: string; limit?: number; signal?: AbortSignal } = {}
    ) {
      const query = buildQuery({
        path: options.path,
        provider: options.provider,
        limit: options.limit
      });
      return request<{ items: FileQueryItem[]; nextCursor: string | null }>(
        `/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/query${query}`,
        options.signal
      );
    },
    getSyncStatus(workspaceId: string, signal?: AbortSignal) {
      return request<SyncStatusResponse>(`/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/status`, signal);
    }
  };
}

function DirectorySkeleton() {
  return (
    <div className="space-y-2 p-4">
      {Array.from({ length: 6 }, (_, index) => (
        <div
          key={index}
          className="h-10 animate-pulse rounded-xl border border-[#27272a] bg-[#111113]"
        />
      ))}
    </div>
  );
}

function DetailSkeleton() {
  return (
    <div className="space-y-4 p-5">
      <div className="h-7 w-2/3 animate-pulse rounded bg-[#1f1f23]" />
      <div className="grid gap-3 sm:grid-cols-2">
        {Array.from({ length: 4 }, (_, index) => (
          <div
            key={index}
            className="h-20 animate-pulse rounded-2xl border border-[#27272a] bg-[#111113]"
          />
        ))}
      </div>
      <div className="h-72 animate-pulse rounded-2xl border border-[#27272a] bg-[#111113]" />
    </div>
  );
}

function ErrorBanner({
  title,
  message,
  actionLabel,
  onAction
}: {
  title: string;
  message: string;
  actionLabel?: string;
  onAction?: () => void;
}) {
  return (
    <div className="rounded-2xl border border-red-500/30 bg-red-500/10 p-4 text-sm text-red-100">
      <div className="flex items-start gap-3">
        <AlertCircle className="mt-0.5 h-5 w-5 shrink-0 text-red-300" />
        <div className="min-w-0 flex-1">
          <p className="font-medium text-red-50">{title}</p>
          <p className="mt-1 text-red-100/85">{message}</p>
          {actionLabel && onAction ? (
            <button
              type="button"
              onClick={onAction}
              className="mt-3 inline-flex items-center rounded-full border border-red-300/40 px-3 py-1.5 text-xs font-medium text-red-50 transition hover:border-red-200/60 hover:bg-red-400/10"
            >
              {actionLabel}
            </button>
          ) : null}
        </div>
      </div>
    </div>
  );
}

function StatusPill({ state }: { state: SyncProviderStatusState }) {
  const styles: Record<SyncProviderStatusState, string> = {
    healthy: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-200',
    lagging: 'border-amber-500/30 bg-amber-500/10 text-amber-200',
    error: 'border-red-500/30 bg-red-500/10 text-red-200',
    paused: 'border-zinc-500/40 bg-zinc-500/10 text-zinc-200'
  };

  return (
    <span className={`inline-flex rounded-full border px-2.5 py-1 text-[11px] font-medium uppercase tracking-[0.16em] ${styles[state]}`}>
      {state}
    </span>
  );
}

export default function Page() {
  const workspaceIds = useMemo(() => parseWorkspaceIds(WORKSPACE_ENV), []);
  const configReady = Boolean(DEFAULT_BASE_URL && PUBLIC_TOKEN && workspaceIds.length > 0);
  const client = useMemo(() => createRelayFileClient(DEFAULT_BASE_URL, PUBLIC_TOKEN), []);
  const [workspaceId, setWorkspaceId] = useState<string>(workspaceIds[0] ?? '');
  const [selectedEntry, setSelectedEntry] = useState<SelectedEntry | null>(null);
  const [searchInput, setSearchInput] = useState('');
  const [searchRequestVersion, setSearchRequestVersion] = useState(0);
  const deferredSearch = useDeferredValue(searchInput);
  const [providerFilter, setProviderFilter] = useState('all');

  const [treeCache, setTreeCache] = useState<TreeCache>({});
  const treeCacheRef = useRef<TreeCache>({});
  const [loadingPaths, setLoadingPaths] = useState<Record<string, boolean>>({});
  const [treeErrors, setTreeErrors] = useState<Record<string, string>>({});
  const [expandedPaths, setExpandedPaths] = useState<Record<string, boolean>>({ '/': true });

  const [searchResults, setSearchResults] = useState<FileQueryItem[]>([]);
  const [searchLoading, setSearchLoading] = useState(false);
  const [searchError, setSearchError] = useState<string | null>(null);

  const [fileDetails, setFileDetails] = useState<FileReadResponse | null>(null);
  const [fileLoading, setFileLoading] = useState(false);
  const [fileError, setFileError] = useState<string | null>(null);

  const [syncStatus, setSyncStatus] = useState<SyncStatusResponse | null>(null);
  const [syncLoading, setSyncLoading] = useState(false);
  const [syncError, setSyncError] = useState<string | null>(null);

  const activeSearch = deferredSearch.trim();

  useEffect(() => {
    treeCacheRef.current = treeCache;
  }, [treeCache]);

  const loadTreePath = useCallback(
    async (path: string, append = false) => {
      if (!configReady || !workspaceId) {
        return;
      }

      const controller = new AbortController();
      setLoadingPaths((current) => ({ ...current, [path]: true }));
      setTreeErrors((current) => {
        const next = { ...current };
        delete next[path];
        return next;
      });

      try {
        const cached = treeCacheRef.current[path];
        const response = await client.listTree(workspaceId, {
          path,
          depth: 1,
          cursor: append ? cached?.nextCursor ?? undefined : undefined,
          signal: controller.signal
        });

        setTreeCache((current) => {
          const previous = current[path];
          const entries = append && previous ? [...previous.entries, ...response.entries] : response.entries;
          return {
            ...current,
            [path]: {
              path: response.path,
              entries,
              nextCursor: response.nextCursor
            }
          };
        });
      } catch (error) {
        setTreeErrors((current) => ({ ...current, [path]: normalizeError(error) }));
      } finally {
        setLoadingPaths((current) => ({ ...current, [path]: false }));
      }
    },
    [client, configReady, workspaceId]
  );

  const loadSyncStatus = useCallback(async () => {
    if (!configReady || !workspaceId) {
      return;
    }

    const controller = new AbortController();
    setSyncLoading(true);
    setSyncError(null);

    try {
      const response = await client.getSyncStatus(workspaceId, controller.signal);
      setSyncStatus(response);
    } catch (error) {
      setSyncError(normalizeError(error));
    } finally {
      setSyncLoading(false);
    }
  }, [client, configReady, workspaceId]);

  useEffect(() => {
    if (!configReady || !workspaceId) {
      return;
    }

    setTreeCache({});
    setTreeErrors({});
    setExpandedPaths({ '/': true });
    setSelectedEntry(null);
    setFileDetails(null);
    setFileError(null);
    setSearchResults([]);
    setSearchError(null);

    void loadTreePath('/');
    void loadSyncStatus();
  }, [configReady, loadSyncStatus, loadTreePath, workspaceId]);

  useEffect(() => {
    if (!configReady || !workspaceId) {
      return;
    }

    const interval = window.setInterval(() => {
      void loadSyncStatus();
    }, 15000);

    return () => window.clearInterval(interval);
  }, [configReady, loadSyncStatus, workspaceId]);

  useEffect(() => {
    if (!configReady || !workspaceId || !activeSearch) {
      setSearchResults([]);
      setSearchLoading(false);
      setSearchError(null);
      return;
    }

    const controller = new AbortController();
    const timeout = window.setTimeout(async () => {
      setSearchLoading(true);
      setSearchError(null);
      try {
        const response = await client.queryFiles(workspaceId, {
          path: activeSearch,
          provider: providerFilter !== 'all' ? providerFilter : undefined,
          limit: 50,
          signal: controller.signal
        });
        setSearchResults(response.items);
      } catch (error) {
        setSearchError(normalizeError(error));
      } finally {
        setSearchLoading(false);
      }
    }, 300);

    return () => {
      controller.abort();
      window.clearTimeout(timeout);
    };
  }, [activeSearch, client, configReady, providerFilter, searchRequestVersion, workspaceId]);

  useEffect(() => {
    if (!configReady || !workspaceId || !selectedEntry || selectedEntry.type !== 'file') {
      setFileDetails(null);
      setFileLoading(false);
      setFileError(null);
      return;
    }

    const controller = new AbortController();
    setFileLoading(true);
    setFileError(null);

    client
      .readFile(workspaceId, selectedEntry.path, controller.signal)
      .then((response) => {
        setFileDetails(response);
      })
      .catch((error) => {
        if (controller.signal.aborted) {
          return;
        }
        setFileError(normalizeError(error));
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setFileLoading(false);
        }
      });

    return () => controller.abort();
  }, [client, configReady, selectedEntry, workspaceId]);

  const syncProviders = syncStatus?.providers ?? [];
  const providerOptions = useMemo(() => {
    const providers = new Set<string>();
    for (const entry of Object.values(treeCache)) {
      for (const item of entry.entries) {
        if (item.provider) {
          providers.add(item.provider);
        }
      }
    }
    for (const item of searchResults) {
      if (item.provider) {
        providers.add(item.provider);
      }
    }
    for (const provider of syncProviders) {
      providers.add(provider.provider);
    }
    return Array.from(providers).sort((a, b) => a.localeCompare(b));
  }, [searchResults, syncProviders, treeCache]);

  const healthyProviders = syncProviders.filter((provider) => provider.status === 'healthy').length;
  const selectedIsDirectory = selectedEntry?.type === 'dir';
  const rootTree = treeCache['/'];

  const filteredEntries = useCallback(
    (entries: TreeEntry[]) =>
      entries.filter((entry) => providerFilter === 'all' || entry.type === 'dir' || entry.provider === providerFilter),
    [providerFilter]
  );

  const refreshDashboard = useCallback(() => {
    if (!workspaceId) {
      return;
    }
    void loadTreePath('/');
    void loadSyncStatus();
    if (activeSearch) {
      setSearchRequestVersion((current) => current + 1);
    }
    if (selectedEntry?.type === 'file') {
      setSelectedEntry({ ...selectedEntry });
    }
  }, [activeSearch, loadSyncStatus, loadTreePath, selectedEntry, workspaceId]);

  const handleWorkspaceChange = (nextWorkspaceId: string) => {
    startTransition(() => {
      setWorkspaceId(nextWorkspaceId);
    });
  };

  const handleToggleDirectory = async (entry: TreeEntry) => {
    const isExpanded = Boolean(expandedPaths[entry.path]);
    setExpandedPaths((current) => ({
      ...current,
      [entry.path]: !isExpanded
    }));

    if (!isExpanded && !treeCache[entry.path] && !loadingPaths[entry.path]) {
      await loadTreePath(entry.path);
    }
  };

  const renderTree = (path: string, depth = 0): ReactNode => {
    const cached = treeCache[path];
    if (!cached) {
      if (loadingPaths[path]) {
        return (
          <div className="ml-3 border-l border-[#202024] pl-4">
            <DirectorySkeleton />
          </div>
        );
      }
      return null;
    }

    return (
      <div className={depth > 0 ? 'ml-3 border-l border-[#202024] pl-4' : ''}>
        {filteredEntries(cached.entries).map((entry) => {
          const expanded = Boolean(expandedPaths[entry.path]);
          const selected = selectedEntry?.path === entry.path;
          const isLoading = Boolean(loadingPaths[entry.path]);

          return (
            <div key={entry.path} className="py-1">
              <button
                type="button"
                onClick={() => {
                  if (entry.type === 'dir') {
                    void handleToggleDirectory(entry);
                  }
                  setSelectedEntry(entry);
                }}
                className={`flex w-full items-center gap-3 rounded-xl px-3 py-2 text-left transition ${
                  selected
                    ? 'bg-[#1a1a22] text-white ring-1 ring-[#3f3f46]'
                    : 'text-[#d4d4d8] hover:bg-[#141418]'
                }`}
                style={{ paddingLeft: `${depth * 14 + 12}px` }}
              >
                {entry.type === 'dir' ? (
                  expanded ? (
                    <ChevronDown className="h-4 w-4 shrink-0 text-[#a1a1aa]" />
                  ) : (
                    <ChevronRight className="h-4 w-4 shrink-0 text-[#a1a1aa]" />
                  )
                ) : (
                  <span className="inline-block h-4 w-4 shrink-0" />
                )}
                {entry.type === 'dir' ? (
                  expanded ? (
                    <FolderOpen className="h-4 w-4 shrink-0 text-sky-300" />
                  ) : (
                    <Folder className="h-4 w-4 shrink-0 text-sky-300" />
                  )
                ) : (
                  <FileText className="h-4 w-4 shrink-0 text-zinc-300" />
                )}
                <div className="min-w-0 flex-1">
                  <div className="truncate text-sm font-medium">
                    {entry.path === '/' ? workspaceId : entry.path.split('/').filter(Boolean).pop()}
                  </div>
                  <div className="truncate text-xs text-[#71717a]">{entry.path}</div>
                </div>
                {entry.provider ? (
                  <span className="rounded-full border border-[#3f3f46] px-2 py-0.5 text-[10px] uppercase tracking-[0.16em] text-[#a1a1aa]">
                    {entry.provider}
                  </span>
                ) : null}
                {isLoading ? <Loader2 className="h-4 w-4 animate-spin text-[#71717a]" /> : null}
              </button>

              {entry.type === 'dir' && expanded ? (
                <div className="mt-1">
                  {treeErrors[entry.path] ? (
                    <div className="ml-6">
                      <ErrorBanner
                        title="Directory load failed"
                        message={treeErrors[entry.path]}
                        actionLabel="Retry"
                        onAction={() => {
                          void loadTreePath(entry.path);
                        }}
                      />
                    </div>
                  ) : null}
                  {renderTree(entry.path, depth + 1)}
                  {treeCache[entry.path]?.nextCursor ? (
                    <div className="ml-6 pt-2">
                      <button
                        type="button"
                        onClick={() => {
                          void loadTreePath(entry.path, true);
                        }}
                        className="rounded-full border border-[#3f3f46] px-3 py-1.5 text-xs font-medium text-[#d4d4d8] transition hover:border-[#52525b] hover:bg-[#16161a]"
                      >
                        Load more
                      </button>
                    </div>
                  ) : null}
                </div>
              ) : null}
            </div>
          );
        })}
      </div>
    );
  };

  if (!configReady) {
    return (
      <main className="brand-grid min-h-[calc(100vh-73px)] px-6 py-8">
        <div className="mx-auto max-w-4xl">
          <div className="brand-glass overflow-hidden">
            <div className="border-b border-[#27272a] px-6 py-5">
              <div className="flex items-center gap-3">
                <Sparkles className="h-5 w-5 text-[#8b9bff]" />
                <div>
                  <h2 className="text-lg font-semibold text-white">Relayfile dashboard configuration required</h2>
                  <p className="mt-1 text-sm text-[#a1a1aa]">
                    This page expects public relayfile connection settings so it can load workspace data from the browser.
                  </p>
                </div>
              </div>
            </div>
            <div className="space-y-5 px-6 py-6">
              <ErrorBanner
                title="Missing environment variables"
                message="Set a public base URL, token, and at least one workspace id before loading the dashboard."
              />
              <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-5">
                <p className="text-sm font-medium text-white">Expected variables</p>
                <pre className="mt-3 overflow-x-auto rounded-xl border border-[#27272a] bg-[#09090b] p-4 text-xs text-[#d4d4d8]">
{`NEXT_PUBLIC_RELAYFILE_BASE_URL=http://localhost:8080
NEXT_PUBLIC_RELAYFILE_TOKEN=dev-token
NEXT_PUBLIC_RELAYFILE_WORKSPACE_ID=default
# Optional: comma-separated list for the selector
NEXT_PUBLIC_RELAYFILE_WORKSPACE_IDS=default,staging,production`}
                </pre>
              </div>
            </div>
          </div>
        </div>
      </main>
    );
  }

  return (
    <main className="brand-grid min-h-[calc(100vh-73px)] px-4 py-4 sm:px-6 sm:py-6">
      <div className="mx-auto flex max-w-[1600px] flex-col gap-4">
        <section className="brand-glass overflow-hidden">
          <div className="flex flex-col gap-4 border-b border-[#27272a] px-5 py-5 lg:flex-row lg:items-end lg:justify-between">
            <div className="space-y-2">
              <p className="text-xs uppercase tracking-[0.2em] text-[#8b9bff]">Workspace dashboard</p>
              <div className="flex flex-wrap items-center gap-3">
                <h2 className="text-2xl font-semibold text-white">{workspaceId}</h2>
                <div className="rounded-full border border-[#27272a] bg-[#121216] px-3 py-1 text-xs text-[#a1a1aa]">
                  {syncLoading
                    ? 'Refreshing sync status…'
                    : `${healthyProviders}/${syncProviders.length || 0} providers healthy`}
                </div>
              </div>
              <p className="text-sm text-[#a1a1aa]">
                Browse the workspace tree, search indexed files, and inspect relayfile metadata without leaving the dashboard.
              </p>
            </div>

            <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
              <label className="flex min-w-[220px] flex-col gap-1 text-sm text-[#a1a1aa]">
                <span className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Workspace</span>
                <select
                  value={workspaceId}
                  onChange={(event) => handleWorkspaceChange(event.target.value)}
                  className="rounded-xl border border-[#3f3f46] bg-[#111113] px-3 py-2.5 text-sm text-white outline-none transition focus:border-[#6366f1]"
                >
                  {workspaceIds.map((candidate) => (
                    <option key={candidate} value={candidate}>
                      {candidate}
                    </option>
                  ))}
                </select>
              </label>

              <button
                type="button"
                onClick={refreshDashboard}
                className="inline-flex items-center justify-center gap-2 rounded-xl border border-[#3f3f46] bg-[#111113] px-4 py-2.5 text-sm font-medium text-white transition hover:border-[#52525b] hover:bg-[#17171c]"
              >
                <RefreshCcw className="h-4 w-4" />
                Refresh
              </button>
            </div>
          </div>

          <div className="grid gap-4 px-5 py-5 xl:grid-cols-[minmax(340px,1.05fr)_minmax(0,1.55fr)_minmax(320px,0.95fr)]">
            <section className="brand-card overflow-hidden">
              <div className="border-b border-[#27272a] px-4 py-4">
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <h3 className="text-sm font-semibold text-white">Search and filter</h3>
                    <p className="mt-1 text-xs text-[#71717a]">Remote query uses relayfile `queryFiles`; tree browsing uses lazy `listTree` calls.</p>
                  </div>
                  <SlidersHorizontal className="h-4 w-4 text-[#71717a]" />
                </div>

                <div className="mt-4 space-y-3">
                  <label className="relative block">
                    <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-[#71717a]" />
                    <input
                      value={searchInput}
                      onChange={(event) => setSearchInput(event.target.value)}
                      type="search"
                      placeholder="Search by path or file name"
                      className="w-full rounded-xl border border-[#3f3f46] bg-[#111113] py-2.5 pl-10 pr-4 text-sm text-white outline-none transition placeholder:text-[#52525b] focus:border-[#6366f1]"
                    />
                  </label>

                  <label className="flex flex-col gap-1 text-sm text-[#a1a1aa]">
                    <span className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Provider filter</span>
                    <select
                      value={providerFilter}
                      onChange={(event) => setProviderFilter(event.target.value)}
                      className="rounded-xl border border-[#3f3f46] bg-[#111113] px-3 py-2.5 text-sm text-white outline-none transition focus:border-[#6366f1]"
                    >
                      <option value="all">All providers</option>
                      {providerOptions.map((provider) => (
                        <option key={provider} value={provider}>
                          {provider}
                        </option>
                      ))}
                    </select>
                  </label>
                </div>
              </div>

              <div className="min-h-[520px]">
                {activeSearch ? (
                  <div className="p-4">
                    {searchError ? (
                      <ErrorBanner
                        title="Search failed"
                        message={searchError}
                        actionLabel="Retry"
                        onAction={() => {
                          setSearchRequestVersion((current) => current + 1);
                        }}
                      />
                    ) : null}

                    {searchLoading ? (
                      <DirectorySkeleton />
                    ) : (
                      <div className="space-y-2">
                        <div className="flex items-center justify-between px-2 pb-2">
                          <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Search results</p>
                          <p className="text-xs text-[#71717a]">{searchResults.length} files</p>
                        </div>

                        {searchResults.length === 0 ? (
                          <div className="rounded-2xl border border-dashed border-[#3f3f46] bg-[#0d0d11] p-6 text-sm text-[#a1a1aa]">
                            No files matched the current query and provider filter.
                          </div>
                        ) : (
                          searchResults.map((item) => {
                            const selected = selectedEntry?.path === item.path;
                            return (
                              <button
                                key={item.path}
                                type="button"
                                onClick={() =>
                                  setSelectedEntry({
                                    path: item.path,
                                    type: 'file',
                                    revision: item.revision,
                                    provider: item.provider,
                                    providerObjectId: item.providerObjectId,
                                    size: item.size,
                                    updatedAt: item.lastEditedAt,
                                    contentType: item.contentType,
                                    properties: item.properties,
                                    relations: item.relations,
                                    permissions: item.permissions,
                                    comments: item.comments
                                  })
                                }
                                className={`flex w-full items-center gap-3 rounded-xl px-3 py-3 text-left transition ${
                                  selected
                                    ? 'bg-[#1a1a22] text-white ring-1 ring-[#3f3f46]'
                                    : 'bg-[#0f0f12] text-[#d4d4d8] hover:bg-[#141418]'
                                }`}
                              >
                                <FileCode2 className="h-4 w-4 shrink-0 text-[#c4b5fd]" />
                                <div className="min-w-0 flex-1">
                                  <div className="truncate text-sm font-medium">{item.path.split('/').pop()}</div>
                                  <div className="truncate text-xs text-[#71717a]">{item.path}</div>
                                </div>
                                <div className="text-right text-xs text-[#71717a]">
                                  <div>{formatBytes(item.size)}</div>
                                  <div>{item.provider ?? 'local'}</div>
                                </div>
                              </button>
                            );
                          })
                        )}
                      </div>
                    )}
                  </div>
                ) : rootTree ? (
                  <div className="p-3">
                    {treeErrors['/'] ? (
                      <div className="p-1">
                        <ErrorBanner
                          title="Workspace tree failed to load"
                          message={treeErrors['/']}
                          actionLabel="Retry"
                          onAction={() => {
                            void loadTreePath('/');
                          }}
                        />
                      </div>
                    ) : null}

                    <div className="mb-2 flex items-center justify-between px-2 pb-2">
                      <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">File tree</p>
                      <p className="text-xs text-[#71717a]">{rootTree.entries.length} loaded entries</p>
                    </div>

                    {renderTree('/')}

                    {rootTree.nextCursor ? (
                      <div className="px-2 pt-3">
                        <button
                          type="button"
                          onClick={() => {
                            void loadTreePath('/', true);
                          }}
                          className="rounded-full border border-[#3f3f46] px-3 py-1.5 text-xs font-medium text-[#d4d4d8] transition hover:border-[#52525b] hover:bg-[#16161a]"
                        >
                          Load more root entries
                        </button>
                      </div>
                    ) : null}
                  </div>
                ) : (
                  <DirectorySkeleton />
                )}
              </div>
            </section>

            <section className="brand-card overflow-hidden">
              <div className="border-b border-[#27272a] px-5 py-4">
                <div className="flex items-center justify-between gap-3">
                  <div>
                    <h3 className="text-sm font-semibold text-white">Workspace health</h3>
                    <p className="mt-1 text-xs text-[#71717a]">Polled with relayfile `getSyncStatus` every 15 seconds.</p>
                  </div>
                  {syncLoading ? <Loader2 className="h-4 w-4 animate-spin text-[#71717a]" /> : null}
                </div>
              </div>

              <div className="space-y-4 p-5">
                {syncError ? (
                  <ErrorBanner
                    title="Sync status unavailable"
                    message={syncError}
                    actionLabel="Retry"
                    onAction={() => {
                      void loadSyncStatus();
                    }}
                  />
                ) : null}

                {syncProviders.length === 0 && !syncError ? (
                  <div className="rounded-2xl border border-dashed border-[#3f3f46] bg-[#0d0d11] p-5 text-sm text-[#a1a1aa]">
                    No provider health records were returned for this workspace.
                  </div>
                ) : (
                  <div className="grid gap-3 md:grid-cols-2">
                    {syncProviders.map((provider) => (
                      <article key={provider.provider} className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
                        <div className="flex items-start justify-between gap-3">
                          <div>
                            <p className="text-sm font-medium text-white">{provider.provider}</p>
                            <p className="mt-1 text-xs text-[#71717a]">
                              {provider.lagSeconds !== undefined ? `${provider.lagSeconds}s lag` : 'No lag metrics'}
                            </p>
                          </div>
                          <StatusPill state={provider.status} />
                        </div>
                        <div className="mt-4 grid grid-cols-2 gap-3 text-xs">
                          <div className="rounded-xl border border-[#27272a] bg-[#111113] p-3">
                            <p className="text-[#71717a]">Dead letters</p>
                            <p className="mt-1 text-sm font-medium text-white">
                              {(provider.deadLetteredEnvelopes ?? 0) + (provider.deadLetteredOps ?? 0)}
                            </p>
                          </div>
                          <div className="rounded-xl border border-[#27272a] bg-[#111113] p-3">
                            <p className="text-[#71717a]">Last error</p>
                            <p className="mt-1 line-clamp-2 text-sm font-medium text-white">
                              {provider.lastError ?? 'None'}
                            </p>
                          </div>
                        </div>
                      </article>
                    ))}
                  </div>
                )}
              </div>
            </section>

            <aside className="brand-card overflow-hidden">
              <div className="border-b border-[#27272a] px-5 py-4">
                <h3 className="text-sm font-semibold text-white">File details</h3>
                <p className="mt-1 text-xs text-[#71717a]">Metadata and content preview are fetched on selection via `readFile`.</p>
              </div>

              {!selectedEntry ? (
                <div className="flex min-h-[520px] items-center justify-center p-6">
                  <div className="max-w-sm text-center">
                    <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-2xl border border-[#27272a] bg-[#111113]">
                      <FileText className="h-5 w-5 text-[#a1a1aa]" />
                    </div>
                    <h4 className="mt-4 text-base font-medium text-white">Select a file or directory</h4>
                    <p className="mt-2 text-sm text-[#a1a1aa]">
                      Choose an item from the tree or search results to inspect relayfile metadata, sync information, and content.
                    </p>
                  </div>
                </div>
              ) : fileLoading ? (
                <DetailSkeleton />
              ) : fileError ? (
                <div className="p-5">
                  <ErrorBanner
                    title="File details failed to load"
                    message={fileError}
                    actionLabel="Retry"
                    onAction={() => {
                      setSelectedEntry({ ...selectedEntry });
                    }}
                  />
                </div>
              ) : (
                <div className="space-y-5 p-5">
                  <div>
                    <div className="flex items-start gap-3">
                      <div className="mt-1 rounded-xl border border-[#27272a] bg-[#111113] p-2">
                        {selectedIsDirectory ? (
                          <FolderOpen className="h-5 w-5 text-sky-300" />
                        ) : (
                          <FileCode2 className="h-5 w-5 text-[#c4b5fd]" />
                        )}
                      </div>
                      <div className="min-w-0 flex-1">
                        <h4 className="truncate text-lg font-semibold text-white">
                          {selectedEntry.path.split('/').filter(Boolean).pop() || selectedEntry.path}
                        </h4>
                        <p className="mt-1 break-all text-sm text-[#71717a]">{selectedEntry.path}</p>
                      </div>
                    </div>
                  </div>

                  <div className="grid gap-3 sm:grid-cols-2">
                    <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
                      <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Revision</p>
                      <p className="mt-2 break-all text-sm font-medium text-white">
                        {fileDetails?.revision ?? selectedEntry.revision ?? 'Unknown'}
                      </p>
                    </div>
                    <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
                      <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Provider</p>
                      <p className="mt-2 text-sm font-medium text-white">
                        {fileDetails?.provider ?? selectedEntry.provider ?? 'Unspecified'}
                      </p>
                    </div>
                    <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
                      <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Last updated</p>
                      <p className="mt-2 text-sm font-medium text-white">
                        {formatDate(fileDetails?.lastEditedAt ?? selectedEntry.updatedAt)}
                      </p>
                    </div>
                    <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
                      <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">
                        {selectedIsDirectory ? 'Loaded children' : 'Size'}
                      </p>
                      <p className="mt-2 text-sm font-medium text-white">
                        {selectedIsDirectory
                          ? String(treeCache[selectedEntry.path]?.entries.length ?? 0)
                          : formatBytes(selectedEntry.size)}
                      </p>
                    </div>
                  </div>

                  {!selectedIsDirectory ? (
                    <>
                      <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
                        <div className="flex items-center justify-between gap-3">
                          <div>
                            <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Content preview</p>
                            <p className="mt-1 text-xs text-[#71717a]">{fileDetails?.contentType ?? selectedEntry.contentType ?? 'Unknown content type'}</p>
                          </div>
                          {fileDetails?.encoding === 'base64' ? (
                            <span className="rounded-full border border-[#3f3f46] px-2 py-1 text-[10px] uppercase tracking-[0.16em] text-[#a1a1aa]">
                              base64
                            </span>
                          ) : null}
                        </div>

                        <pre className="mt-4 max-h-[360px] overflow-auto rounded-2xl border border-[#27272a] bg-[#09090b] p-4 text-xs leading-6 text-[#d4d4d8]">
                          {truncateContent(fileDetails?.content ?? '', fileDetails?.encoding)}
                        </pre>
                      </div>

                      <div className="grid gap-3">
                        <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
                          <div className="flex items-center gap-2">
                            <Shield className="h-4 w-4 text-[#8b9bff]" />
                            <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Semantics</p>
                          </div>

                          <div className="mt-4 space-y-4 text-sm">
                            <div>
                              <p className="text-[#71717a]">Properties</p>
                              <div className="mt-2 flex flex-wrap gap-2">
                                {Object.entries(fileDetails?.semantics?.properties ?? selectedEntry.properties ?? {}).length > 0 ? (
                                  Object.entries(fileDetails?.semantics?.properties ?? selectedEntry.properties ?? {}).map(([key, value]) => (
                                    <span
                                      key={key}
                                      className="rounded-full border border-[#3f3f46] bg-[#111113] px-3 py-1 text-xs text-[#d4d4d8]"
                                    >
                                      {key}: {value}
                                    </span>
                                  ))
                                ) : (
                                  <span className="text-[#a1a1aa]">No properties</span>
                                )}
                              </div>
                            </div>

                            <div>
                              <p className="text-[#71717a]">Relations</p>
                              <div className="mt-2 flex flex-wrap gap-2">
                                {(fileDetails?.semantics?.relations ?? selectedEntry.relations ?? []).length > 0 ? (
                                  (fileDetails?.semantics?.relations ?? selectedEntry.relations ?? []).map((relation) => (
                                    <span
                                      key={relation}
                                      className="rounded-full border border-[#3f3f46] bg-[#111113] px-3 py-1 text-xs text-[#d4d4d8]"
                                    >
                                      {relation}
                                    </span>
                                  ))
                                ) : (
                                  <span className="text-[#a1a1aa]">No relations</span>
                                )}
                              </div>
                            </div>

                            <div>
                              <p className="text-[#71717a]">Permissions</p>
                              <div className="mt-2 flex flex-wrap gap-2">
                                {(fileDetails?.semantics?.permissions ?? selectedEntry.permissions ?? []).length > 0 ? (
                                  (fileDetails?.semantics?.permissions ?? selectedEntry.permissions ?? []).map((permission) => (
                                    <span
                                      key={permission}
                                      className="rounded-full border border-[#3f3f46] bg-[#111113] px-3 py-1 text-xs text-[#d4d4d8]"
                                    >
                                      {permission}
                                    </span>
                                  ))
                                ) : (
                                  <span className="text-[#a1a1aa]">No permissions</span>
                                )}
                              </div>
                            </div>
                          </div>
                        </div>
                      </div>
                    </>
                  ) : (
                    <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
                      <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Directory summary</p>
                      <p className="mt-3 text-sm text-[#d4d4d8]">
                        Expand this directory in the tree to lazy-load children with relayfile `listTree(workspaceId, {'{'} path, depth: 1 {'}'})`.
                      </p>
                      {treeErrors[selectedEntry.path] ? (
                        <div className="mt-4">
                          <ErrorBanner
                            title="Children unavailable"
                            message={treeErrors[selectedEntry.path]}
                            actionLabel="Retry"
                            onAction={() => {
                              void loadTreePath(selectedEntry.path);
                            }}
                          />
                        </div>
                      ) : null}
                    </div>
                  )}
                </div>
              )}
            </aside>
          </div>
        </section>
      </div>
    </main>
  );
}
