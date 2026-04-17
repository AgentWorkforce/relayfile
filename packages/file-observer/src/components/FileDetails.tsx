'use client';

import { AlertCircle, FileCode2, FileText, FolderOpen, GitBranch, Link2, ScrollText, Shield, Workflow } from 'lucide-react';

type FileNodeType = 'file' | 'dir';

export interface FileDetailsSemantics {
  properties?: Record<string, string>;
  relations?: string[];
  permissions?: string[];
  comments?: string[];
}

export interface FileDetailsEntry {
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

export interface FileDetailsContent {
  path: string;
  revision: string;
  contentType: string;
  provider?: string;
  providerObjectId?: string;
  lastEditedAt?: string;
  semantics?: FileDetailsSemantics;
}

export interface FileDetailsProps {
  selectedEntry: FileDetailsEntry | null;
  fileDetails?: FileDetailsContent | null;
  loading?: boolean;
  error?: string | null;
  childCount?: number;
  className?: string;
  onRetry?: () => void;
}

type StatusTone = 'neutral' | 'info' | 'success' | 'warning' | 'danger';

function joinClasses(...values: Array<string | false | null | undefined>): string {
  return values.filter(Boolean).join(' ');
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

function getDisplayName(path: string): string {
  const parts = path.split('/').filter(Boolean);
  return parts[parts.length - 1] ?? path;
}

function mergeProperties(selectedEntry: FileDetailsEntry, fileDetails?: FileDetailsContent | null): Record<string, string> {
  return {
    ...(selectedEntry.properties ?? {}),
    ...(fileDetails?.semantics?.properties ?? {})
  };
}

function getPropertyValue(properties: Record<string, string>, keys: string[]): string | null {
  for (const key of keys) {
    const value = properties[key];
    if (value?.trim()) {
      return value.trim();
    }
  }

  return null;
}

function getStatusTone(status: string | null): StatusTone {
  const normalized = status?.trim().toLowerCase() ?? '';

  if (!normalized) {
    return 'neutral';
  }

  if (['ready', 'healthy', 'stable', 'complete', 'completed', 'synced', 'approved', 'active'].includes(normalized)) {
    return 'success';
  }

  if (['draft', 'queued', 'pending', 'review', 'in-review', 'in progress', 'in-progress', 'processing', 'staged'].includes(normalized)) {
    return 'info';
  }

  if (['warning', 'stale', 'lagging', 'paused', 'blocked', 'hold', 'on-hold'].includes(normalized)) {
    return 'warning';
  }

  if (['error', 'failed', 'rejected', 'archived', 'deleted'].includes(normalized)) {
    return 'danger';
  }

  return 'neutral';
}

function getStatusClasses(tone: StatusTone): string {
  switch (tone) {
    case 'success':
      return 'border-emerald-500/30 bg-emerald-500/10 text-emerald-200';
    case 'info':
      return 'border-sky-500/30 bg-sky-500/10 text-sky-200';
    case 'warning':
      return 'border-amber-500/30 bg-amber-500/10 text-amber-200';
    case 'danger':
      return 'border-rose-500/30 bg-rose-500/10 text-rose-200';
    default:
      return 'border-zinc-500/40 bg-zinc-500/10 text-zinc-200';
  }
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
      <div className="h-56 animate-pulse rounded-2xl border border-[#27272a] bg-[#111113]" />
    </div>
  );
}

function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="p-5">
      <div className="rounded-2xl border border-red-500/30 bg-red-500/10 p-4 text-sm text-red-100">
        <div className="flex items-start gap-3">
          <AlertCircle className="mt-0.5 h-5 w-5 shrink-0 text-red-300" />
          <div className="min-w-0 flex-1">
            <p className="font-medium text-red-50">File details failed to load</p>
            <p className="mt-1 text-red-100/85">{message}</p>
            {onRetry ? (
              <button
                type="button"
                onClick={onRetry}
                className="mt-3 inline-flex items-center rounded-full border border-red-300/40 px-3 py-1.5 text-xs font-medium text-red-50 transition hover:border-red-200/60 hover:bg-red-400/10"
              >
                Retry
              </button>
            ) : null}
          </div>
        </div>
      </div>
    </div>
  );
}

function EmptyState() {
  return (
    <div className="flex min-h-[520px] items-center justify-center p-6">
      <div className="max-w-sm text-center">
        <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-2xl border border-[#27272a] bg-[#111113]">
          <FileText className="h-5 w-5 text-[#a1a1aa]" />
        </div>
        <h4 className="mt-4 text-base font-medium text-white">Select a file or directory</h4>
        <p className="mt-2 text-sm text-[#a1a1aa]">
          Choose an item from the tree or search results to inspect relayfile metadata, revisions, and semantic relations.
        </p>
      </div>
    </div>
  );
}

function ChipList({
  items,
  emptyLabel,
  tone = 'default'
}: {
  items: string[];
  emptyLabel: string;
  tone?: 'default' | 'subtle';
}) {
  if (!items.length) {
    return <span className="text-[#a1a1aa]">{emptyLabel}</span>;
  }

  return (
    <div className="mt-2 flex flex-wrap gap-2">
      {items.map((item) => (
        <span
          key={item}
          className={joinClasses(
            'rounded-full border px-3 py-1 text-xs',
            tone === 'subtle'
              ? 'border-[#2f2f35] bg-[#0d0d10] text-[#b4b4bb]'
              : 'border-[#3f3f46] bg-[#111113] text-[#d4d4d8]'
          )}
        >
          {item}
        </span>
      ))}
    </div>
  );
}

function StatCard({
  label,
  value
}: {
  label: string;
  value: string;
}) {
  return (
    <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
      <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">{label}</p>
      <p className="mt-2 break-words text-sm font-medium text-white">{value}</p>
    </div>
  );
}

export function FileDetails({
  selectedEntry,
  fileDetails,
  loading = false,
  error,
  childCount,
  className,
  onRetry
}: FileDetailsProps) {
  const selectedIsDirectory = selectedEntry?.type === 'dir';

  if (loading) {
    return (
      <aside className={joinClasses('brand-card overflow-hidden', className)}>
        <div className="border-b border-[#27272a] px-5 py-4">
          <h3 className="text-sm font-semibold text-white">File details</h3>
          <p className="mt-1 text-xs text-[#71717a]">Metadata and revision details update when a file is selected.</p>
        </div>
        <DetailSkeleton />
      </aside>
    );
  }

  if (error) {
    return (
      <aside className={joinClasses('brand-card overflow-hidden', className)}>
        <div className="border-b border-[#27272a] px-5 py-4">
          <h3 className="text-sm font-semibold text-white">File details</h3>
          <p className="mt-1 text-xs text-[#71717a]">Metadata and revision details update when a file is selected.</p>
        </div>
        <ErrorState message={error} onRetry={onRetry} />
      </aside>
    );
  }

  if (!selectedEntry) {
    return (
      <aside className={joinClasses('brand-card overflow-hidden', className)}>
        <div className="border-b border-[#27272a] px-5 py-4">
          <h3 className="text-sm font-semibold text-white">File details</h3>
          <p className="mt-1 text-xs text-[#71717a]">Metadata and revision details update when a file is selected.</p>
        </div>
        <EmptyState />
      </aside>
    );
  }

  const properties = mergeProperties(selectedEntry, fileDetails);
  const relations = fileDetails?.semantics?.relations ?? selectedEntry.relations ?? [];
  const permissions = fileDetails?.semantics?.permissions ?? selectedEntry.permissions ?? [];
  const comments = fileDetails?.semantics?.comments ?? selectedEntry.comments ?? [];
  const author =
    getPropertyValue(properties, ['author', 'owner', 'updatedBy', 'lastEditedBy', 'editor']) ??
    fileDetails?.provider ??
    'Unassigned';
  const intent =
    getPropertyValue(properties, ['intent', 'purpose', 'summary', 'description']) ??
    (selectedIsDirectory ? 'Folder structure and child revisions' : 'File metadata and semantic context');
  const status =
    getPropertyValue(properties, ['status', 'state', 'lifecycle', 'reviewStatus', 'syncStatus']) ??
    (selectedIsDirectory ? 'active' : 'draft');
  const revision = fileDetails?.revision ?? selectedEntry.revision ?? 'Unknown';
  const lastUpdated = formatDate(fileDetails?.lastEditedAt ?? selectedEntry.updatedAt);
  const statusTone = getStatusTone(status);
  const statusClasses = getStatusClasses(statusTone);

  return (
    <aside className={joinClasses('brand-card overflow-hidden', className)}>
      <div className="border-b border-[#27272a] px-5 py-4">
        <h3 className="text-sm font-semibold text-white">File details</h3>
        <p className="mt-1 text-xs text-[#71717a]">Metadata, semantic intent, relations, and revision history for the current selection.</p>
      </div>

      <div className="space-y-5 p-5">
        <div className="flex items-start gap-3">
          <div className="mt-1 rounded-xl border border-[#27272a] bg-[#111113] p-2">
            {selectedIsDirectory ? (
              <FolderOpen className="h-5 w-5 text-sky-300" />
            ) : (
              <FileCode2 className="h-5 w-5 text-[#c4b5fd]" />
            )}
          </div>
          <div className="min-w-0 flex-1">
            <h4 className="truncate text-lg font-semibold text-white">{getDisplayName(selectedEntry.path)}</h4>
            <p className="mt-1 break-all font-mono text-xs text-[#71717a]">{selectedEntry.path}</p>
          </div>
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          <StatCard label="Author" value={author} />
          <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
            <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Status</p>
            <div className="mt-2">
              <span className={joinClasses('inline-flex rounded-full border px-2.5 py-1 text-[11px] font-medium uppercase tracking-[0.16em]', statusClasses)}>
                {status}
              </span>
            </div>
          </div>
          <StatCard label="Intent" value={intent} />
          <StatCard label={selectedIsDirectory ? 'Loaded children' : 'Size'} value={selectedIsDirectory ? String(childCount ?? 0) : formatBytes(selectedEntry.size)} />
        </div>

        <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
          <div className="flex items-center gap-2">
            <GitBranch className="h-4 w-4 text-[#8b9bff]" />
            <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Revision info</p>
          </div>
          <dl className="mt-4 grid gap-3 sm:grid-cols-2">
            <div className="rounded-xl border border-[#27272a] bg-[#111113] px-3 py-3">
              <dt className="text-[11px] uppercase tracking-[0.16em] text-[#71717a]">Revision</dt>
              <dd className="mt-2 break-all font-mono text-sm text-white">{revision}</dd>
            </div>
            <div className="rounded-xl border border-[#27272a] bg-[#111113] px-3 py-3">
              <dt className="text-[11px] uppercase tracking-[0.16em] text-[#71717a]">Last updated</dt>
              <dd className="mt-2 text-sm text-white">{lastUpdated}</dd>
            </div>
            <div className="rounded-xl border border-[#27272a] bg-[#111113] px-3 py-3">
              <dt className="text-[11px] uppercase tracking-[0.16em] text-[#71717a]">Provider</dt>
              <dd className="mt-2 text-sm text-white">{fileDetails?.provider ?? selectedEntry.provider ?? 'Unspecified'}</dd>
            </div>
            <div className="rounded-xl border border-[#27272a] bg-[#111113] px-3 py-3">
              <dt className="text-[11px] uppercase tracking-[0.16em] text-[#71717a]">Content type</dt>
              <dd className="mt-2 text-sm text-white">{fileDetails?.contentType ?? selectedEntry.contentType ?? (selectedIsDirectory ? 'directory' : 'Unknown')}</dd>
            </div>
            <div className="rounded-xl border border-[#27272a] bg-[#111113] px-3 py-3 sm:col-span-2">
              <dt className="text-[11px] uppercase tracking-[0.16em] text-[#71717a]">Provider object</dt>
              <dd className="mt-2 break-all font-mono text-sm text-white">
                {fileDetails?.providerObjectId ?? selectedEntry.providerObjectId ?? 'Unavailable'}
              </dd>
            </div>
          </dl>
        </div>

        <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
          <div className="flex items-center gap-2">
            <Link2 className="h-4 w-4 text-[#8b9bff]" />
            <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Relations</p>
          </div>
          <ChipList items={relations} emptyLabel="No relations" />
        </div>

        <div className="grid gap-3 lg:grid-cols-2">
          <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
            <div className="flex items-center gap-2">
              <Shield className="h-4 w-4 text-[#8b9bff]" />
              <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Permissions</p>
            </div>
            <ChipList items={permissions} emptyLabel="No permissions" tone="subtle" />
          </div>

          <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
            <div className="flex items-center gap-2">
              <ScrollText className="h-4 w-4 text-[#8b9bff]" />
              <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Comments</p>
            </div>
            <p className="mt-2 text-sm text-white">{comments.length}</p>
            <p className="mt-1 text-xs text-[#71717a]">Linked comment threads or sync annotations recorded for this item.</p>
          </div>
        </div>

        {Object.keys(properties).length ? (
          <div className="rounded-2xl border border-[#27272a] bg-[#0f0f12] p-4">
            <div className="flex items-center gap-2">
              <Workflow className="h-4 w-4 text-[#8b9bff]" />
              <p className="text-xs uppercase tracking-[0.16em] text-[#71717a]">Metadata</p>
            </div>
            <dl className="mt-4 grid gap-3 sm:grid-cols-2">
              {Object.entries(properties).map(([key, value]) => (
                <div key={key} className="rounded-xl border border-[#27272a] bg-[#111113] px-3 py-3">
                  <dt className="text-[11px] uppercase tracking-[0.16em] text-[#71717a]">{key}</dt>
                  <dd className="mt-2 break-words text-sm text-white">{value}</dd>
                </div>
              ))}
            </dl>
          </div>
        ) : null}
      </div>
    </aside>
  );
}

export default FileDetails;
