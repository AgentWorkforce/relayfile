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
  Maximize2,
  RefreshCcw,
  Search,
  Shield,
  SlidersHorizontal,
  Sparkles,
  X
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

interface WriteFileResponse {
  opId: string;
  status: string;
  targetRevision: string;
  writeback?: {
    provider?: string;
    state?: string;
  };
}

interface ApiErrorBody {
  code?: string;
  message?: string;
  currentRevision?: string;
  expectedRevision?: string;
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

interface ObserverConfig {
  baseUrl: string;
  token: string;
  workspaceIds: string[];
}

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

function readParam(params: URLSearchParams, names: string[]): string {
  for (const name of names) {
    const value = params.get(name)?.trim();
    if (value) {
      return value;
    }
  }
  return '';
}

function readRuntimeObserverConfig(): Partial<ObserverConfig> {
  if (typeof window === 'undefined') {
    return {};
  }
  const hash = window.location.hash.replace(/^#\/?/, '');
  const hashParams = new URLSearchParams(hash);
  const searchParams = new URLSearchParams(window.location.search);
  const read = (names: string[]) => readParam(hashParams, names) || readParam(searchParams, names);
  const workspaceRaw = read(['workspaceIds', 'workspaceId', 'workspace', 'wks']);
  const config: Partial<ObserverConfig> = {};
  const baseUrl = read(['baseUrl', 'server', 'url']);
  const token = read(['token', 'relayfileToken']);
  if (baseUrl) {
    config.baseUrl = baseUrl;
  }
  if (token) {
    config.token = token;
  }
  if (workspaceRaw) {
    config.workspaceIds = parseWorkspaceIds(workspaceRaw);
  }
  return config;
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

function truncateContentPreview(content: string, maxLength?: number): string {
  if (!maxLength || content.length <= maxLength) {
    return content;
  }
  return `${content.slice(0, maxLength)}\n\n… truncated …`;
}

function isJsonContent(contentType?: string, path?: string): boolean {
  const normalizedContentType = contentType?.toLowerCase() ?? '';
  const normalizedPath = path?.toLowerCase() ?? '';
  return (
    normalizedContentType.includes('application/json') ||
    normalizedContentType.includes('+json') ||
    normalizedPath.endsWith('.json')
  );
}

function inferProviderFromPath(path?: string, type?: FileNodeType): string | undefined {
  const segments = path?.split('/').filter(Boolean) ?? [];
  if (segments.length === 0) {
    return undefined;
  }
  if (segments.length === 1 && type !== 'dir') {
    return undefined;
  }
  return segments[0];
}

function formatContentPreview(
  content: string,
  encoding?: 'utf-8' | 'base64',
  contentType?: string,
  path?: string,
  maxLength?: number
): string {
  if (encoding === 'base64') {
    return 'Binary or base64-encoded content preview is not rendered in the dashboard.';
  }

  if (!isJsonContent(contentType, path)) {
    return truncateContentPreview(content, maxLength);
  }

  try {
    return truncateContentPreview(JSON.stringify(JSON.parse(content), null, 2), maxLength);
  } catch {
    return truncateContentPreview(content, maxLength);
  }
}

function createRelayFileClient(baseUrl: string, token: string) {
  async function request<T>(
    path: string,
    options: {
      method?: 'GET' | 'PUT';
      body?: unknown;
      headers?: Record<string, string>;
      signal?: AbortSignal;
    } = {}
  ): Promise<T> {
    const response = await fetch(`${baseUrl.replace(/\/+$/, '')}${path}`, {
      method: options.method ?? 'GET',
      signal: options.signal,
      headers: {
        Accept: 'application/json',
        Authorization: token ? `Bearer ${token}` : '',
        'X-Correlation-Id': createCorrelationId(),
        ...(options.body !== undefined ? { 'Content-Type': 'application/json' } : {}),
        ...options.headers
      },
      body: options.body !== undefined ? JSON.stringify(options.body) : undefined
    });

    if (!response.ok) {
      let payload: ApiErrorBody | undefined;
      try {
        payload = (await response.json()) as ApiErrorBody;
      } catch {
        payload = undefined;
      }
      const revisionDetail = payload?.currentRevision ? ` Current revision: ${payload.currentRevision}.` : '';
      const detail = `${payload?.message ?? `${response.status} ${response.statusText}`}${revisionDetail}`;
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
      return request<TreeResponse>(`/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/tree${query}`, { signal: options.signal });
    },
    readFile(workspaceId: string, path: string, signal?: AbortSignal) {
      const query = buildQuery({ path });
      return request<FileReadResponse>(`/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/file${query}`, { signal });
    },
    writeFile(
      workspaceId: string,
      path: string,
      options: { contentType: string; content: string; ifMatch: string; signal?: AbortSignal }
    ) {
      const query = buildQuery({ path });
      return request<WriteFileResponse>(`/v1/workspaces/${encodeURIComponent(workspaceId)}/fs/file${query}`, {
        method: 'PUT',
        signal: options.signal,
        headers: {
          'If-Match': options.ifMatch
        },
        body: {
          contentType: options.contentType,
          content: options.content,
          encoding: 'utf-8'
        }
      });
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
        { signal: options.signal }
      );
    },
    getSyncStatus(workspaceId: string, signal?: AbortSignal) {
      return request<SyncStatusResponse>(`/v1/workspaces/${encodeURIComponent(workspaceId)}/sync/status`, { signal });
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

export default function Page() {
  const defaultConfig = useMemo<ObserverConfig>(
    () => ({
      baseUrl: DEFAULT_BASE_URL,
      token: PUBLIC_TOKEN,
      workspaceIds: parseWorkspaceIds(WORKSPACE_ENV)
    }),
    []
  );
  const [observerConfig, setObserverConfig] = useState<ObserverConfig>(defaultConfig);
  const workspaceIds = observerConfig.workspaceIds;
  const configReady = Boolean(observerConfig.baseUrl && observerConfig.token && workspaceIds.length > 0);
  const client = useMemo(
    () => createRelayFileClient(observerConfig.baseUrl, observerConfig.token),
    [observerConfig.baseUrl, observerConfig.token]
  );
  const [workspaceId, setWorkspaceId] = useState<string>(workspaceIds[0] ?? '');
  const [selectedEntry, setSelectedEntry] = useState<SelectedEntry | null>(null);
  const [fullViewEntry, setFullViewEntry] = useState<SelectedEntry | null>(null);
  const [fullViewMode, setFullViewMode] = useState<'view' | 'edit'>('view');
  const [editContent, setEditContent] = useState('');
  const [editSaving, setEditSaving] = useState(false);
  const [editError, setEditError] = useState<string | null>(null);
  const [editMessage, setEditMessage] = useState<string | null>(null);
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
    const runtimeConfig = readRuntimeObserverConfig();
    if (!runtimeConfig.baseUrl && !runtimeConfig.token && !runtimeConfig.workspaceIds) {
      return;
    }
    setObserverConfig((current) => ({
      baseUrl: runtimeConfig.baseUrl ?? current.baseUrl,
      token: runtimeConfig.token ?? current.token,
      workspaceIds: runtimeConfig.workspaceIds?.length ? runtimeConfig.workspaceIds : current.workspaceIds
    }));
  }, []);

  useEffect(() => {
    if (workspaceIds.length === 0) {
      if (workspaceId) {
        setWorkspaceId('');
      }
      return;
    }
    if (!workspaceId || !workspaceIds.includes(workspaceId)) {
      setWorkspaceId(workspaceIds[0] ?? '');
    }
  }, [workspaceId, workspaceIds]);

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
    setFullViewEntry(null);
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

  useEffect(() => {
    if (!fullViewEntry) {
      return;
    }

    const previousOverflow = document.body.style.overflow;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        setFullViewEntry(null);
      }
    };

    document.body.style.overflow = 'hidden';
    window.addEventListener('keydown', handleKeyDown);

    return () => {
      document.body.style.overflow = previousOverflow;
      window.removeEventListener('keydown', handleKeyDown);
    };
  }, [fullViewEntry]);

  const syncProviders = syncStatus?.providers ?? [];
  const providerOptions = useMemo(() => {
    const providers = new Set<string>();
    for (const entry of Object.values(treeCache)) {
      for (const item of entry.entries) {
        const provider = item.provider ?? inferProviderFromPath(item.path, item.type);
        if (provider) {
          providers.add(provider);
        }
      }
    }
    for (const item of searchResults) {
      const provider = item.provider ?? inferProviderFromPath(item.path, 'file');
      if (provider) {
        providers.add(provider);
      }
    }
    for (const provider of syncProviders) {
      providers.add(provider.provider);
    }
    return Array.from(providers).sort((a, b) => a.localeCompare(b));
  }, [searchResults, syncProviders, treeCache]);

  const healthyProviders = syncProviders.filter((provider) => provider.status === 'healthy').length;
  const unhealthyProviders = syncProviders.length - healthyProviders;
  const syncStatusLabel = syncLoading
    ? 'Refreshing sync status'
    : syncError
      ? 'Sync status unavailable'
      : syncProviders.length === 0
        ? 'No sync metrics'
        : unhealthyProviders === 0
          ? 'All providers healthy'
          : `${healthyProviders}/${syncProviders.length} providers healthy`;
  const syncStatusTitle = syncError
    ? syncError
    : syncProviders.length > 0
      ? syncProviders
          .map((provider) => `${provider.provider}: ${provider.status}${provider.lagSeconds !== undefined ? `, ${provider.lagSeconds}s lag` : ''}`)
          .join('\n')
      : 'No provider health records were returned for this workspace.';
  const syncStatusClassName = syncError
    ? 'border-red-500/30 bg-red-500/10 text-red-100'
    : syncProviders.length === 0
      ? 'border-[#27272a] bg-[#121216] text-[#a1a1aa]'
      : unhealthyProviders > 0
        ? 'border-amber-500/30 bg-amber-500/10 text-amber-100'
        : 'border-emerald-500/30 bg-emerald-500/10 text-emerald-100';
  const selectedIsDirectory = selectedEntry?.type === 'dir';
  const rootTree = treeCache['/'];
  const fullViewFile = fullViewEntry && fileDetails?.path === fullViewEntry.path ? fileDetails : null;
  const fullViewIsSelected = fullViewEntry?.path === selectedEntry?.path;
  const fullViewError = fullViewEntry && fullViewIsSelected ? fileError : null;
  const fullViewLoading = Boolean(fullViewEntry && fullViewIsSelected && fileLoading && !fullViewFile);
  const fullViewContent = fullViewFile
    ? formatContentPreview(fullViewFile.content, fullViewFile.encoding, fullViewFile.contentType, fullViewFile.path)
    : '';

  const closeFullView = useCallback(() => {
    setFullViewEntry(null);
    setFullViewMode('view');
    setEditContent('');
    setEditError(null);
    setEditMessage(null);
  }, []);

  const openFullView = useCallback((entry: SelectedEntry) => {
    if (entry.type !== 'file') {
      return;
    }
    setSelectedEntry(entry);
    setFullViewEntry(entry);
    setFullViewMode('view');
    setEditContent('');
    setEditError(null);
    setEditMessage(null);
  }, []);

  const enterEditMode = useCallback(() => {
    if (!fullViewFile || fullViewFile.encoding === 'base64') {
      return;
    }
    setEditContent(formatContentPreview(fullViewFile.content, fullViewFile.encoding, fullViewFile.contentType, fullViewFile.path));
    setEditError(null);
    setEditMessage(null);
    setFullViewMode('edit');
  }, [fullViewFile]);

  const saveEdit = useCallback(async () => {
    if (!fullViewFile || !fullViewEntry || editSaving) {
      return;
    }

    let contentToSave = editContent;
    if (isJsonContent(fullViewFile.contentType, fullViewFile.path)) {
      try {
        contentToSave = JSON.stringify(JSON.parse(editContent), null, 2);
      } catch {
        setEditError('This JSON is not valid. Fix the syntax before saving.');
        return;
      }
    }

    setEditSaving(true);
    setEditError(null);
    setEditMessage(null);

    try {
      const result = await client.writeFile(workspaceId, fullViewFile.path, {
        contentType: fullViewFile.contentType,
        content: contentToSave,
        ifMatch: fullViewFile.revision
      });
      const nextRevision = result.targetRevision || fullViewFile.revision;
      const nextEntry = { ...fullViewEntry, revision: nextRevision, updatedAt: new Date().toISOString() };
      const writebackMessage = result.writeback?.provider
        ? ` Writeback queued for ${result.writeback.provider}${result.writeback.state ? ` (${result.writeback.state})` : ''}.`
        : '';
      setEditContent(contentToSave);
      setEditMessage(`Saved to RelayFile.${writebackMessage}`);
      setFullViewMode('view');
      setFullViewEntry(nextEntry);
      setSelectedEntry(nextEntry);
      setFileDetails({
        ...fullViewFile,
        content: contentToSave,
        revision: nextRevision,
        lastEditedAt: nextEntry.updatedAt
      });
    } catch (error) {
      setEditError(normalizeError(error));
    } finally {
      setEditSaving(false);
    }
  }, [client, editContent, editSaving, fullViewEntry, fullViewFile, workspaceId]);

  const filteredEntries = useCallback(
    (entries: TreeEntry[]) =>
      entries.filter((entry) => {
        const provider = entry.provider ?? inferProviderFromPath(entry.path, entry.type);
        return providerFilter === 'all' || provider === providerFilter;
      }),
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
	                onDoubleClick={(event) => {
	                  if (entry.type === 'file') {
	                    event.preventDefault();
	                    openFullView(entry);
	                  }
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
        <section className="brand-glass overflow-hidden lg:flex lg:h-[calc(100vh-121px)] lg:flex-col">
          <div className="flex flex-col gap-4 border-b border-[#27272a] px-5 py-5 lg:flex-none lg:flex-row lg:items-end lg:justify-between">
            <div className="space-y-2">
              <p className="text-xs uppercase tracking-[0.2em] text-[#8b9bff]">Workspace dashboard</p>
              <div className="flex flex-wrap items-center gap-3">
                <h2 className="text-2xl font-semibold text-white">{workspaceId}</h2>
                <div
                  title={syncStatusTitle}
                  className={`inline-flex items-center gap-2 rounded-full border px-3 py-1 text-xs ${syncStatusClassName}`}
                >
                  {syncLoading ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : null}
                  <span>{syncStatusLabel}</span>
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

          <div className="grid items-start gap-4 px-5 py-5 lg:min-h-0 lg:flex-1 lg:grid-cols-[minmax(0,1fr)_minmax(320px,420px)] lg:items-stretch">
            <section className="brand-card flex min-h-0 flex-col overflow-hidden">
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

              <div className="min-h-[520px] lg:min-h-0 lg:flex-1 lg:overflow-y-auto">
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
	                            const fileEntry: SelectedEntry = {
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
	                            };
	                            return (
	                              <button
	                                key={item.path}
	                                type="button"
	                                onClick={() => setSelectedEntry(fileEntry)}
	                                onDoubleClick={(event) => {
	                                  event.preventDefault();
	                                  openFullView(fileEntry);
	                                }}
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

            <aside className="brand-card overflow-hidden lg:min-h-0 lg:overflow-y-auto">
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

                        <pre className="mt-4 max-h-[360px] overflow-auto whitespace-pre rounded-2xl border border-[#27272a] bg-[#09090b] p-4 text-xs leading-6 text-[#d4d4d8]">
                          {formatContentPreview(
	                            fileDetails?.content ?? '',
	                            fileDetails?.encoding,
	                            fileDetails?.contentType ?? selectedEntry.contentType,
	                            fileDetails?.path ?? selectedEntry.path,
	                            4000
	                          )}
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

        {fullViewEntry ? (
          <div
            className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 px-4 py-6 backdrop-blur-md"
            role="dialog"
            aria-modal="true"
            aria-labelledby="file-viewer-title"
            onMouseDown={closeFullView}
          >
            <div
              className="flex max-h-[92vh] w-full max-w-6xl flex-col overflow-hidden rounded-2xl border border-[#3f3f46] bg-[#111113] shadow-2xl shadow-black/50"
              onMouseDown={(event) => event.stopPropagation()}
            >
              <div className="border-b border-[#27272a] bg-[#18181b] px-5 py-4">
                <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
                  <div className="min-w-0">
                    <div className="flex items-center gap-3">
                      <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl border border-[#3f3f46] bg-[#0f0f12]">
                        <Maximize2 className="h-4 w-4 text-[#8b9bff]" />
                      </div>
                      <div className="min-w-0">
                        <h3 id="file-viewer-title" className="truncate text-lg font-semibold text-white">
                          {fullViewEntry.path.split('/').filter(Boolean).pop() || fullViewEntry.path}
                        </h3>
                        <p className="mt-1 break-all text-xs text-[#a1a1aa]">{fullViewEntry.path}</p>
                      </div>
                    </div>

                    <div className="mt-4 flex flex-wrap gap-2 text-xs">
                      <span className="rounded-full border border-[#3f3f46] bg-[#0f0f12] px-3 py-1 text-[#d4d4d8]">
                        {fullViewFile?.contentType ?? fullViewEntry.contentType ?? 'Unknown content type'}
                      </span>
                      <span className="rounded-full border border-[#3f3f46] bg-[#0f0f12] px-3 py-1 text-[#d4d4d8]">
                        {fullViewFile?.revision ?? fullViewEntry.revision ?? 'Unknown revision'}
                      </span>
                      {fullViewFile?.provider ?? fullViewEntry.provider ? (
                        <span className="rounded-full border border-[#3f3f46] bg-[#0f0f12] px-3 py-1 text-[#d4d4d8]">
                          {fullViewFile?.provider ?? fullViewEntry.provider}
                        </span>
                      ) : null}
                    </div>
                  </div>

                  <div className="flex shrink-0 flex-wrap items-center gap-2">
                    {fullViewFile && fullViewFile.encoding !== 'base64' ? (
                      <div className="inline-flex rounded-xl border border-[#3f3f46] bg-[#0f0f12] p-1">
                        <button
                          type="button"
                          onClick={() => setFullViewMode('view')}
                          className={`rounded-lg px-3 py-1.5 text-xs font-medium transition ${
                            fullViewMode === 'view' ? 'bg-[#27272a] text-white' : 'text-[#a1a1aa] hover:text-white'
                          }`}
                        >
                          View
                        </button>
                        <button
                          type="button"
                          onClick={enterEditMode}
                          className={`rounded-lg px-3 py-1.5 text-xs font-medium transition ${
                            fullViewMode === 'edit' ? 'bg-[#27272a] text-white' : 'text-[#a1a1aa] hover:text-white'
                          }`}
                        >
                          Edit
                        </button>
                      </div>
                    ) : null}

                    <button
                      type="button"
                      onClick={closeFullView}
                      className="inline-flex h-9 w-9 items-center justify-center rounded-xl border border-[#3f3f46] bg-[#0f0f12] text-[#a1a1aa] transition hover:border-[#52525b] hover:text-white"
                      aria-label="Close file viewer"
                    >
                      <X className="h-4 w-4" />
                    </button>
                  </div>
                </div>
              </div>

              <div className="min-h-0 flex-1 overflow-hidden bg-[#09090b]">
                {fullViewLoading ? (
                  <div className="flex h-[68vh] items-center justify-center">
                    <Loader2 className="h-6 w-6 animate-spin text-[#71717a]" />
                  </div>
                ) : fullViewError ? (
                  <div className="p-6">
                    <ErrorBanner
                      title="File failed to load"
                      message={fullViewError}
                      actionLabel="Retry"
                      onAction={() => {
                        setSelectedEntry({ ...fullViewEntry });
                      }}
                    />
                  </div>
                ) : fullViewFile ? (
                  fullViewMode === 'edit' ? (
                    <div className="flex h-[68vh] flex-col">
                      <div className="flex items-center justify-between gap-3 border-b border-[#27272a] bg-[#0f0f12] px-5 py-3">
                        <div className="min-w-0 text-xs text-[#a1a1aa]">
                          <span className="font-medium text-[#d4d4d8]">Editing</span>
                          <span className="ml-2">{fullViewFile.contentType}</span>
                        </div>
                        <div className="flex items-center gap-2">
                          <button
                            type="button"
                            onClick={() => {
                              setFullViewMode('view');
                              setEditError(null);
                            }}
                            className="rounded-xl border border-[#3f3f46] px-3 py-2 text-xs font-medium text-[#d4d4d8] transition hover:border-[#52525b] hover:bg-[#17171c]"
                          >
                            Cancel
                          </button>
                          <button
                            type="button"
                            onClick={() => {
                              void saveEdit();
                            }}
                            disabled={editSaving}
                            className="inline-flex items-center gap-2 rounded-xl border border-[#6366f1]/50 bg-[#6366f1]/20 px-3 py-2 text-xs font-medium text-white transition hover:border-[#8b9bff] hover:bg-[#6366f1]/30 disabled:cursor-not-allowed disabled:opacity-60"
                          >
                            {editSaving ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : null}
                            Save changes
                          </button>
                        </div>
                      </div>

                      {editError ? (
                        <div className="border-b border-red-500/20 bg-red-500/10 px-5 py-3 text-sm text-red-100">{editError}</div>
                      ) : null}

                      <textarea
                        value={editContent}
                        onChange={(event) => {
                          setEditContent(event.target.value);
                          setEditError(null);
                          setEditMessage(null);
                        }}
                        spellCheck={false}
                        className="min-h-0 flex-1 resize-none bg-[#09090b] p-5 font-mono text-sm leading-6 text-[#e4e4e7] outline-none selection:bg-[#6366f1]/30"
                      />
                    </div>
                  ) : (
                    <div className="h-[68vh] overflow-auto p-5">
                      {editMessage ? (
                        <div className="mb-4 rounded-2xl border border-emerald-500/30 bg-emerald-500/10 px-4 py-3 text-sm text-emerald-100">
                          {editMessage}
                        </div>
                      ) : null}
                      <pre className="min-h-full rounded-2xl border border-[#27272a] bg-[#0d0d11] p-5 font-mono text-sm leading-6 text-[#e4e4e7]">
                        {fullViewContent}
                      </pre>
                    </div>
                  )
                ) : (
                  <div className="flex h-[68vh] items-center justify-center text-sm text-[#a1a1aa]">
                    Select a file to load its content.
                  </div>
                )}
              </div>
            </div>
          </div>
        ) : null}
      </div>
    </main>
  );
}
