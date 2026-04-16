'use client';

import { useCallback, useEffect, useRef, useState } from 'react';

import type {
  FilesystemEvent,
  RelayfileClient,
  TreeEntry,
  TreeResponse
} from '../lib/relayfile-client';

type FileEventsClient = Pick<RelayfileClient, 'connectWebSocket'>;

const DEFAULT_MAX_EVENTS = 300;
const FILE_EVENT_TYPES = new Set<FilesystemEvent['type']>([
  'file.created',
  'file.updated',
  'file.deleted'
]);

export type UseFileEventsStatus = 'idle' | 'connecting' | 'connected' | 'disconnected';

export interface UseFileEventsOptions {
  client: FileEventsClient | null;
  workspaceId?: string | null;
  initialTree?: TreeResponse | null;
  enabled?: boolean;
  maxEvents?: number;
  onEvent?: (event: FilesystemEvent) => void;
}

export interface UseFileEventsResult {
  tree: TreeResponse | null;
  events: FilesystemEvent[];
  status: UseFileEventsStatus;
  error: string | null;
  clearEvents: () => void;
}

function normalizeError(error: unknown): string {
  if (error instanceof Error && error.message) {
    return error.message;
  }

  return 'Unexpected relayfile WebSocket failure.';
}

function normalizeDirectoryPath(path: string): string {
  const trimmed = path.trim().replace(/\/+$/, '');
  return trimmed === '' ? '/' : trimmed;
}

function normalizeFilePath(path: string): string {
  const trimmed = path.trim().replace(/\/+$/, '');
  return trimmed === '' ? '/' : trimmed;
}

function isDirectChildPath(parentPath: string, candidatePath: string): boolean {
  const normalizedParent = normalizeDirectoryPath(parentPath);
  const normalizedCandidate = normalizeFilePath(candidatePath);

  if (normalizedCandidate === normalizedParent) {
    return false;
  }

  if (normalizedParent === '/') {
    const remainder = normalizedCandidate.slice(1);
    return remainder !== '' && !remainder.includes('/');
  }

  const prefix = `${normalizedParent}/`;
  if (!normalizedCandidate.startsWith(prefix)) {
    return false;
  }

  const remainder = normalizedCandidate.slice(prefix.length);
  return remainder !== '' && !remainder.includes('/');
}

function createTreeEntryFromEvent(event: FilesystemEvent): TreeEntry {
  return {
    path: normalizeFilePath(event.path),
    type: 'file',
    revision: event.revision,
    provider: event.provider,
    updatedAt: event.timestamp
  };
}

function applyFileEventToTree(current: TreeResponse | null, event: FilesystemEvent): TreeResponse | null {
  if (!current || !FILE_EVENT_TYPES.has(event.type) || !isDirectChildPath(current.path, event.path)) {
    return current;
  }

  const normalizedPath = normalizeFilePath(event.path);

  if (event.type === 'file.deleted') {
    const nextEntries = current.entries.filter((entry) => entry.path !== normalizedPath);
    if (nextEntries.length === current.entries.length) {
      return current;
    }

    return {
      ...current,
      entries: nextEntries
    };
  }

  const nextEntry = createTreeEntryFromEvent(event);
  const existingIndex = current.entries.findIndex((entry) => entry.path === normalizedPath);

  if (existingIndex === -1) {
    return {
      ...current,
      entries: [...current.entries, nextEntry]
    };
  }

  const existingEntry = current.entries[existingIndex];
  const mergedEntry: TreeEntry = {
    ...existingEntry,
    path: normalizedPath,
    type: 'file',
    revision: event.revision,
    provider: event.provider ?? existingEntry.provider,
    updatedAt: event.timestamp
  };

  const nextEntries = [...current.entries];
  nextEntries[existingIndex] = mergedEntry;

  return {
    ...current,
    entries: nextEntries
  };
}

function appendEvent(
  events: FilesystemEvent[],
  nextEvent: FilesystemEvent,
  maxEvents: number
): FilesystemEvent[] {
  const cappedEvents = [...events, nextEvent];

  if (cappedEvents.length <= maxEvents) {
    return cappedEvents;
  }

  return cappedEvents.slice(cappedEvents.length - maxEvents);
}

export function useFileEvents({
  client,
  workspaceId,
  initialTree = null,
  enabled = true,
  maxEvents = DEFAULT_MAX_EVENTS,
  onEvent
}: UseFileEventsOptions): UseFileEventsResult {
  const trimmedWorkspaceId = workspaceId?.trim();
  const resolvedMaxEvents = Number.isFinite(maxEvents) ? Math.max(1, Math.floor(maxEvents)) : DEFAULT_MAX_EVENTS;

  const [tree, setTree] = useState<TreeResponse | null>(initialTree);
  const [events, setEvents] = useState<FilesystemEvent[]>([]);
  const [status, setStatus] = useState<UseFileEventsStatus>('idle');
  const [error, setError] = useState<string | null>(null);

  const onEventRef = useRef<typeof onEvent>(onEvent);

  useEffect(() => {
    onEventRef.current = onEvent;
  }, [onEvent]);

  useEffect(() => {
    setTree(initialTree);
  }, [initialTree, trimmedWorkspaceId]);

  useEffect(() => {
    setEvents([]);
    setError(null);
  }, [trimmedWorkspaceId]);

  useEffect(() => {
    if (!enabled || !client || !trimmedWorkspaceId) {
      setStatus('idle');
      setError(null);
      return;
    }

    setStatus('connecting');
    setError(null);

    let isActive = true;
    let connection: ReturnType<FileEventsClient['connectWebSocket']> | null = null;

    try {
      connection = client.connectWebSocket(trimmedWorkspaceId);
    } catch (connectionError) {
      setStatus('disconnected');
      setError(normalizeError(connectionError));
      return;
    }

    const unsubscribeOpen = connection.on('open', () => {
      if (!isActive) {
        return;
      }

      setStatus('connected');
      setError(null);
    });

    const unsubscribeClose = connection.on('close', () => {
      if (!isActive) {
        return;
      }

      setStatus('disconnected');
    });

    const unsubscribeError = connection.on('error', (event) => {
      if (!isActive) {
        return;
      }

      setStatus('disconnected');
      setError(normalizeError(event));
    });

    const unsubscribeEvent = connection.on('event', (event) => {
      if (!isActive) {
        return;
      }

      setEvents((current) => appendEvent(current, event, resolvedMaxEvents));

      if (FILE_EVENT_TYPES.has(event.type)) {
        setTree((current) => applyFileEventToTree(current, event));
      }

      onEventRef.current?.(event);
    });

    return () => {
      isActive = false;
      unsubscribeEvent();
      unsubscribeError();
      unsubscribeClose();
      unsubscribeOpen();
      connection?.close(1000, 'useFileEvents cleanup');
    };
  }, [client, enabled, resolvedMaxEvents, trimmedWorkspaceId]);

  const clearEvents = useCallback(() => {
    setEvents([]);
  }, []);

  return {
    tree,
    events,
    status,
    error,
    clearEvents
  };
}

export default useFileEvents;
