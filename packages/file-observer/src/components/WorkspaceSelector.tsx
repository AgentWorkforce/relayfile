'use client';

import { ChevronDown, Loader2 } from 'lucide-react';

export interface WorkspaceSelectorOption {
  id: string;
  label?: string;
  name?: string;
}

export interface WorkspaceSelectorProps {
  workspaces: ReadonlyArray<string | WorkspaceSelectorOption>;
  value: string;
  onChange: (workspaceId: string) => void;
  label?: string;
  emptyLabel?: string;
  disabled?: boolean;
  loading?: boolean;
  className?: string;
}

function joinClasses(...values: Array<string | false | null | undefined>): string {
  return values.filter(Boolean).join(' ');
}

function normalizeWorkspaceOption(workspace: string | WorkspaceSelectorOption): WorkspaceSelectorOption {
  if (typeof workspace === 'string') {
    return {
      id: workspace,
      label: workspace
    };
  }

  return workspace;
}

function getWorkspaceLabel(workspace: WorkspaceSelectorOption): string {
  return workspace.label?.trim() || workspace.name?.trim() || workspace.id;
}

export default function WorkspaceSelector({
  workspaces,
  value,
  onChange,
  label = 'Workspace',
  emptyLabel = 'No workspaces available',
  disabled = false,
  loading = false,
  className
}: WorkspaceSelectorProps) {
  const seen = new Set<string>();
  const options = workspaces.map(normalizeWorkspaceOption).filter((workspace) => {
    if (!workspace.id || seen.has(workspace.id)) {
      return false;
    }

    seen.add(workspace.id);
    return true;
  });

  const selectedWorkspace = options.find((workspace) => workspace.id === value) ?? options[0] ?? null;
  const selectValue = selectedWorkspace?.id ?? '';
  const selectedLabel = selectedWorkspace ? getWorkspaceLabel(selectedWorkspace) : emptyLabel;
  const selectedId = selectedWorkspace && selectedLabel !== selectedWorkspace.id ? selectedWorkspace.id : null;
  const isDisabled = disabled || loading || options.length === 0;

  const handleChange = (nextWorkspaceId: string) => {
    if (!nextWorkspaceId || nextWorkspaceId === value) {
      return;
    }

    onChange(nextWorkspaceId);
  };

  return (
    <div className={joinClasses('flex min-w-[220px] flex-col gap-2', className)}>
      <label className="flex flex-col gap-1 text-sm text-[#a1a1aa]">
        <span className="text-xs uppercase tracking-[0.16em] text-[#71717a]">{label}</span>

        <div className="relative">
          <select
            value={selectValue}
            onChange={(event) => handleChange(event.target.value)}
            disabled={isDisabled}
            className={joinClasses(
              'w-full appearance-none rounded-xl border border-[#3f3f46] bg-[#111113] px-3 py-2.5 pr-10 text-sm text-white outline-none transition',
              'focus:border-[#6366f1] disabled:cursor-not-allowed disabled:border-[#27272a] disabled:text-[#71717a]'
            )}
          >
            {options.length === 0 ? (
              <option value="">{emptyLabel}</option>
            ) : (
              options.map((workspace) => (
                <option key={workspace.id} value={workspace.id}>
                  {getWorkspaceLabel(workspace)}
                </option>
              ))
            )}
          </select>

          <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-[#71717a]">
            <ChevronDown className="h-4 w-4" />
          </span>
        </div>
      </label>

      <div className="flex items-center justify-between gap-3 rounded-xl border border-[#27272a] bg-[#111113] px-3 py-2">
        <div className="min-w-0">
          <p className="text-[11px] uppercase tracking-[0.16em] text-[#71717a]">Active workspace</p>
          <p className="truncate text-sm font-medium text-white">{selectedLabel}</p>
          {selectedId ? <p className="truncate text-xs text-[#71717a]">{selectedId}</p> : null}
        </div>

        <div className="shrink-0 rounded-full border border-[#27272a] bg-[#18181b] px-2.5 py-1 text-xs text-[#a1a1aa]">
          {loading ? (
            <span className="inline-flex items-center gap-1.5">
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              Switching
            </span>
          ) : (
            `${options.length} workspace${options.length === 1 ? '' : 's'}`
          )}
        </div>
      </div>
    </div>
  );
}
