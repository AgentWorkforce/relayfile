'use client';

import { useCallback, useEffect, useRef, useState } from 'react';

import type { ListTreeOptions, RelayfileClient, TreeResponse } from '../lib/relayfile-client';

type FileTreeClient = Pick<RelayfileClient, 'listTree'>;

export interface UseFileTreeOptions {
  client: FileTreeClient | null;
  workspaceId?: string | null;
  path?: string;
  depth?: number;
  cursor?: string;
  enabled?: boolean;
  keepPreviousData?: boolean;
}

export interface UseFileTreeResult {
  tree: TreeResponse | null;
  loading: boolean;
  error: string | null;
  refresh: () => Promise<TreeResponse | null>;
}

function normalizeError(error: unknown): string {
  if (error instanceof Error && error.message) {
    return error.message;
  }

  return 'Unexpected relayfile request failure.';
}

export function useFileTree({
  client,
  workspaceId,
  path = '/',
  depth = 1,
  cursor,
  enabled = true,
  keepPreviousData = true
}: UseFileTreeOptions): UseFileTreeResult {
  const [tree, setTree] = useState<TreeResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const requestIdRef = useRef(0);
  const abortControllerRef = useRef<AbortController | null>(null);

  const refresh = useCallback(async (): Promise<TreeResponse | null> => {
    const trimmedWorkspaceId = workspaceId?.trim();

    if (!enabled || !client || !trimmedWorkspaceId) {
      abortControllerRef.current?.abort();
      abortControllerRef.current = null;
      setLoading(false);
      setError(null);
      if (!keepPreviousData) {
        setTree(null);
      }
      return null;
    }

    abortControllerRef.current?.abort();
    const controller = new AbortController();
    abortControllerRef.current = controller;

    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;

    setLoading(true);
    setError(null);
    if (!keepPreviousData) {
      setTree(null);
    }

    try {
      const options: ListTreeOptions = {
        path,
        depth,
        cursor,
        signal: controller.signal
      };
      const response = await client.listTree(trimmedWorkspaceId, options);

      if (controller.signal.aborted || requestIdRef.current !== requestId) {
        return null;
      }

      setTree(response);
      return response;
    } catch (error) {
      if (controller.signal.aborted || requestIdRef.current !== requestId) {
        return null;
      }

      setError(normalizeError(error));
      return null;
    } finally {
      if (!controller.signal.aborted && requestIdRef.current === requestId) {
        setLoading(false);
      }
    }
  }, [client, cursor, depth, enabled, keepPreviousData, path, workspaceId]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    return () => {
      abortControllerRef.current?.abort();
    };
  }, []);

  return {
    tree,
    loading,
    error,
    refresh
  };
}

export default useFileTree;
