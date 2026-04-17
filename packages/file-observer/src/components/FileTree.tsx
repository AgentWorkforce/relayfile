'use client';

import { useEffect, useMemo, useState } from 'react';
import {
  ChevronDown,
  ChevronRight,
  FileCode2,
  FileText,
  Folder,
  FolderOpen
} from 'lucide-react';

export type FileTreeNodeType = 'file' | 'folder';

export interface FileTreeNode {
  path: string;
  name?: string;
  type: FileTreeNodeType;
  children?: FileTreeNode[];
  size?: number;
  extension?: string;
  contentType?: string;
  lastModified?: string;
  description?: string;
  metadata?: Record<string, string | number | boolean | null | undefined>;
}

export interface FileTreeProps {
  nodes: FileTreeNode[];
  className?: string;
  title?: string;
  emptyMessage?: string;
  initialExpandedPaths?: string[];
  initialSelectedPath?: string;
  onSelect?: (node: FileTreeNode) => void;
}

interface FileBadgeStyle {
  badge: string;
  badgeLabel: string;
  color: string;
  icon: typeof FileCode2;
}

function joinClasses(...values: Array<string | false | null | undefined>): string {
  return values.filter(Boolean).join(' ');
}

function getBaseName(path: string): string {
  const trimmed = path.replace(/\/+$/, '');
  if (!trimmed || trimmed === '/') {
    return '/';
  }

  const segments = trimmed.split('/').filter(Boolean);
  return segments[segments.length - 1] ?? trimmed;
}

function getNodeLabel(node: FileTreeNode): string {
  return node.name?.trim() || getBaseName(node.path);
}

function getFileExtension(node: FileTreeNode): string {
  if (node.extension?.trim()) {
    return node.extension.replace(/^\./, '').toLowerCase();
  }

  const label = getNodeLabel(node);
  const parts = label.split('.');
  return parts.length > 1 ? parts[parts.length - 1].toLowerCase() : '';
}

function getBadgeStyle(node: FileTreeNode): FileBadgeStyle {
  const extension = getFileExtension(node);

  if (extension === 'ts' || extension === 'tsx') {
    return {
      badge: 'border-sky-400/30 bg-sky-400/10 text-sky-200',
      badgeLabel: extension.toUpperCase(),
      color: 'text-sky-300',
      icon: FileCode2
    };
  }

  if (extension === 'js' || extension === 'jsx' || extension === 'mjs' || extension === 'cjs') {
    return {
      badge: 'border-amber-400/30 bg-amber-400/10 text-amber-200',
      badgeLabel: extension.toUpperCase(),
      color: 'text-amber-300',
      icon: FileCode2
    };
  }

  if (extension === 'json') {
    return {
      badge: 'border-emerald-400/30 bg-emerald-400/10 text-emerald-200',
      badgeLabel: '{}',
      color: 'text-emerald-300',
      icon: FileCode2
    };
  }

  if (extension === 'md' || extension === 'mdx') {
    return {
      badge: 'border-fuchsia-400/30 bg-fuchsia-400/10 text-fuchsia-200',
      badgeLabel: 'MD',
      color: 'text-fuchsia-300',
      icon: FileText
    };
  }

  if (extension === 'yml' || extension === 'yaml' || extension === 'toml') {
    return {
      badge: 'border-orange-400/30 bg-orange-400/10 text-orange-200',
      badgeLabel: extension.toUpperCase(),
      color: 'text-orange-300',
      icon: FileText
    };
  }

  if (extension === 'css' || extension === 'scss') {
    return {
      badge: 'border-cyan-400/30 bg-cyan-400/10 text-cyan-200',
      badgeLabel: extension.toUpperCase(),
      color: 'text-cyan-300',
      icon: FileCode2
    };
  }

  return {
    badge: 'border-zinc-400/20 bg-zinc-400/10 text-zinc-200',
    badgeLabel: extension ? extension.toUpperCase().slice(0, 4) : 'FILE',
    color: 'text-zinc-300',
    icon: FileText
  };
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

function findNodeByPath(nodes: FileTreeNode[], path: string): FileTreeNode | null {
  for (const node of nodes) {
    if (node.path === path) {
      return node;
    }

    if (node.children?.length) {
      const match = findNodeByPath(node.children, path);
      if (match) {
        return match;
      }
    }
  }

  return null;
}

function findFirstNodePath(nodes: FileTreeNode[]): string | null {
  for (const node of nodes) {
    if (node.type === 'file') {
      return node.path;
    }

    if (node.children?.length) {
      const childPath = findFirstNodePath(node.children);
      if (childPath) {
        return childPath;
      }
    }
  }

  return nodes[0]?.path ?? null;
}

function buildAncestorPaths(path?: string): string[] {
  if (!path) {
    return [];
  }

  const segments = path.split('/').filter(Boolean);
  const ancestors: string[] = [];

  for (let index = 0; index < segments.length - 1; index += 1) {
    ancestors.push(`/${segments.slice(0, index + 1).join('/')}`);
  }

  return ancestors;
}

function buildInitialExpandedState(nodes: FileTreeNode[], expandedPaths?: string[], selectedPath?: string): Record<string, boolean> {
  const nextState: Record<string, boolean> = {};

  for (const path of expandedPaths ?? []) {
    nextState[path] = true;
  }

  for (const node of nodes) {
    if (node.type === 'folder') {
      nextState[node.path] = true;
    }
  }

  for (const path of buildAncestorPaths(selectedPath)) {
    nextState[path] = true;
  }

  return nextState;
}

function MetadataList({ metadata }: { metadata?: FileTreeNode['metadata'] }) {
  const entries = Object.entries(metadata ?? {}).filter(([, value]) => value !== undefined && value !== null);

  if (!entries.length) {
    return <p className="text-sm text-[#71717a]">No extra metadata</p>;
  }

  return (
    <dl className="space-y-3">
      {entries.map(([key, value]) => (
        <div key={key} className="rounded-xl border border-[#27272a] bg-[#0f0f12] px-3 py-2.5">
          <dt className="text-[11px] uppercase tracking-[0.16em] text-[#71717a]">{key}</dt>
          <dd className="mt-1 break-words text-sm text-[#f4f4f5]">{String(value)}</dd>
        </div>
      ))}
    </dl>
  );
}

export function FileTree({
  nodes,
  className,
  title = 'Workspace files',
  emptyMessage = 'No files available.',
  initialExpandedPaths,
  initialSelectedPath,
  onSelect
}: FileTreeProps) {
  const [expandedPaths, setExpandedPaths] = useState<Record<string, boolean>>(() =>
    buildInitialExpandedState(nodes, initialExpandedPaths, initialSelectedPath)
  );
  const [selectedPath, setSelectedPath] = useState<string | null>(() => initialSelectedPath ?? findFirstNodePath(nodes));

  useEffect(() => {
    setExpandedPaths(buildInitialExpandedState(nodes, initialExpandedPaths, initialSelectedPath));
  }, [initialExpandedPaths, initialSelectedPath, nodes]);

  useEffect(() => {
    if (selectedPath && findNodeByPath(nodes, selectedPath)) {
      return;
    }

    setSelectedPath(initialSelectedPath ?? findFirstNodePath(nodes));
  }, [initialSelectedPath, nodes, selectedPath]);

  const selectedNode = useMemo(() => {
    if (!selectedPath) {
      return null;
    }

    return findNodeByPath(nodes, selectedPath);
  }, [nodes, selectedPath]);

  useEffect(() => {
    if (selectedNode) {
      onSelect?.(selectedNode);
    }
  }, [onSelect, selectedNode]);

  const renderNode = (node: FileTreeNode, depth = 0) => {
    const isSelected = selectedPath === node.path;
    const isFolder = node.type === 'folder';
    const isExpanded = Boolean(expandedPaths[node.path]);
    const badgeStyle = !isFolder ? getBadgeStyle(node) : null;
    const FileIcon = badgeStyle?.icon ?? FileText;

    return (
      <div key={node.path}>
        <button
          type="button"
          onClick={() => {
            setSelectedPath(node.path);

            if (isFolder) {
              setExpandedPaths((current) => ({
                ...current,
                [node.path]: !current[node.path]
              }));
            }
          }}
          className={joinClasses(
            'group flex w-full items-center gap-3 rounded-xl px-3 py-2 text-left transition',
            isSelected
              ? 'bg-[#1a1a22] text-white ring-1 ring-[#3f3f46]'
              : 'text-[#d4d4d8] hover:bg-[#141418]'
          )}
          style={{ paddingLeft: `${depth * 18 + 12}px` }}
        >
          <span className="flex h-4 w-4 shrink-0 items-center justify-center text-[#71717a]">
            {isFolder ? (
              isExpanded ? (
                <ChevronDown className="h-4 w-4" />
              ) : (
                <ChevronRight className="h-4 w-4" />
              )
            ) : null}
          </span>

          {isFolder ? (
            isExpanded ? (
              <FolderOpen className="h-4 w-4 shrink-0 text-sky-300" />
            ) : (
              <Folder className="h-4 w-4 shrink-0 text-sky-300" />
            )
          ) : (
            <div className="relative flex h-6 w-6 shrink-0 items-center justify-center rounded-lg border border-[#2f2f35] bg-[#111113]">
              <FileIcon className={joinClasses('h-4 w-4', badgeStyle?.color)} />
            </div>
          )}

          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium">{getNodeLabel(node)}</div>
            <div className="truncate text-xs text-[#71717a]">{node.path}</div>
          </div>

          {!isFolder ? (
            <span
              className={joinClasses(
                'rounded-full border px-2 py-0.5 text-[10px] font-medium uppercase tracking-[0.16em]',
                badgeStyle?.badge
              )}
            >
              {badgeStyle?.badgeLabel}
            </span>
          ) : (
            <span className="rounded-full border border-[#3f3f46] px-2 py-0.5 text-[10px] uppercase tracking-[0.16em] text-[#a1a1aa]">
              {node.children?.length ?? 0} items
            </span>
          )}
        </button>

        {isFolder && isExpanded && node.children?.length ? (
          <div className="mt-1 space-y-1 border-l border-[#202024] pl-3">
            {node.children.map((child) => renderNode(child, depth + 1))}
          </div>
        ) : null}
      </div>
    );
  };

  return (
    <section
      className={joinClasses(
        'grid gap-4 rounded-2xl border border-[#27272a] bg-[#101013] p-4 lg:grid-cols-[minmax(0,1.2fr)_minmax(320px,0.8fr)]',
        className
      )}
    >
      <div className="rounded-2xl border border-[#27272a] bg-[#0b0b0d]">
        <div className="border-b border-[#27272a] px-4 py-3">
          <h2 className="text-sm font-semibold uppercase tracking-[0.18em] text-[#a1a1aa]">{title}</h2>
        </div>

        <div className="space-y-1 p-3">
          {nodes.length ? (
            nodes.map((node) => renderNode(node))
          ) : (
            <div className="rounded-xl border border-dashed border-[#27272a] px-4 py-8 text-center text-sm text-[#71717a]">
              {emptyMessage}
            </div>
          )}
        </div>
      </div>

      <div className="rounded-2xl border border-[#27272a] bg-[#0b0b0d]">
        <div className="border-b border-[#27272a] px-4 py-3">
          <h2 className="text-sm font-semibold uppercase tracking-[0.18em] text-[#a1a1aa]">Details</h2>
        </div>

        <div className="space-y-4 p-4">
          {selectedNode ? (
            <>
              <div className="space-y-2">
                <div className="flex items-start gap-3">
                  {selectedNode.type === 'folder' ? (
                    <FolderOpen className="mt-0.5 h-5 w-5 shrink-0 text-sky-300" />
                  ) : (
                    <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-xl border border-[#2f2f35] bg-[#111113]">
                      {(() => {
                        const badgeStyle = getBadgeStyle(selectedNode);
                        const Icon = badgeStyle.icon;
                        return <Icon className={joinClasses('h-4 w-4', badgeStyle.color)} />;
                      })()}
                    </div>
                  )}

                  <div className="min-w-0">
                    <p className="truncate text-lg font-semibold text-white">{getNodeLabel(selectedNode)}</p>
                    <p className="mt-1 break-all text-sm text-[#71717a]">{selectedNode.path}</p>
                  </div>
                </div>

                {selectedNode.description ? (
                  <p className="rounded-xl border border-[#27272a] bg-[#111113] px-3 py-2 text-sm text-[#d4d4d8]">
                    {selectedNode.description}
                  </p>
                ) : null}
              </div>

              <div className="grid gap-3 sm:grid-cols-2">
                <div className="rounded-2xl border border-[#27272a] bg-[#111113] p-3">
                  <p className="text-[11px] uppercase tracking-[0.16em] text-[#71717a]">Type</p>
                  <p className="mt-2 text-sm font-medium text-[#f4f4f5]">
                    {selectedNode.type === 'folder' ? 'Folder' : 'File'}
                  </p>
                </div>

                <div className="rounded-2xl border border-[#27272a] bg-[#111113] p-3">
                  <p className="text-[11px] uppercase tracking-[0.16em] text-[#71717a]">Extension</p>
                  <p className="mt-2 text-sm font-medium text-[#f4f4f5]">
                    {selectedNode.type === 'folder' ? 'Folder' : getFileExtension(selectedNode) || 'Unknown'}
                  </p>
                </div>

                <div className="rounded-2xl border border-[#27272a] bg-[#111113] p-3">
                  <p className="text-[11px] uppercase tracking-[0.16em] text-[#71717a]">Size</p>
                  <p className="mt-2 text-sm font-medium text-[#f4f4f5]">{formatBytes(selectedNode.size)}</p>
                </div>

                <div className="rounded-2xl border border-[#27272a] bg-[#111113] p-3">
                  <p className="text-[11px] uppercase tracking-[0.16em] text-[#71717a]">
                    {selectedNode.type === 'folder' ? 'Children' : 'Modified'}
                  </p>
                  <p className="mt-2 text-sm font-medium text-[#f4f4f5]">
                    {selectedNode.type === 'folder'
                      ? `${selectedNode.children?.length ?? 0} items`
                      : formatDate(selectedNode.lastModified)}
                  </p>
                </div>
              </div>

              <div>
                <p className="mb-3 text-[11px] font-semibold uppercase tracking-[0.18em] text-[#71717a]">Metadata</p>
                <MetadataList metadata={selectedNode.metadata} />
              </div>
            </>
          ) : (
            <div className="rounded-xl border border-dashed border-[#27272a] px-4 py-10 text-center text-sm text-[#71717a]">
              Select a file or folder to inspect its details.
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

export default FileTree;
