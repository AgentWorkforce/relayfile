# File Observer Dashboard — Design Document

## Overview

The file observer dashboard is a Next.js web application that provides a real-time visual interface for browsing and monitoring files in a relayfile workspace. It mirrors the visual language of the relaycast observer-dashboard while exposing relayfile-specific concepts: file trees, semantic metadata, sync provider health, and the filesystem event feed.

---

## Tech Stack

| Layer | Choice | Rationale |
|-------|--------|-----------|
| Framework | Next.js 15 (App Router) | Same pattern as relaycast observer-dashboard; RSC for static shell, client components for real-time |
| Styling | Tailwind CSS v4 + CSS custom properties | Inherits the same design-token system as observer-dashboard |
| Icons | lucide-react | Already used across relaycast |
| Fonts | Outfit (UI), IBM Plex Mono (code/paths/revisions), Inter (body) | Same as observer-dashboard |
| SDK | `@relayfile/sdk` (`RelayFileClient`) | Native TypeScript client with WebSocket support |
| State | React `useState` / `useRef` + custom hooks | No external state library needed at this scale |
| Real-time | `RelayFileClient.connectWebSocket()` + polling fallback | WebSocket for low-latency events; polling for environments where WS is blocked |

---

## Application Shell

```
packages/file-observer/
├── app/
│   ├── layout.tsx            # Root layout — fonts, CSS vars, ThemeProvider
│   ├── page.tsx              # Entry — redirects to /workspace/[id] or shows workspace picker
│   └── workspace/
│       └── [id]/
│           └── page.tsx      # Main 3-pane dashboard page
├── components/
│   ├── layout/
│   │   ├── AppHeader.tsx     # Top bar: workspace selector, sync dot, theme toggle
│   │   ├── FileSidebar.tsx   # Left pane: workspace nav + directory tree
│   │   └── FileDetails.tsx   # Right pane: metadata, relations, sync status
│   ├── files/
│   │   ├── FileTree.tsx      # Recursive directory tree with expand/collapse
│   │   ├── FileTreeItem.tsx  # Single row: icon + path + revision pill
│   │   ├── FileContent.tsx   # Center pane: breadcrumb + file preview
│   │   ├── FileSearch.tsx    # Search bar wired to queryFiles API
│   │   └── FileIcon.tsx      # Extension → colored lucide icon mapping
│   ├── sync/
│   │   ├── SyncStatusPanel.tsx   # Per-provider health cards
│   │   ├── SyncStatusBadge.tsx   # Inline colored pill (healthy/lagging/error/paused)
│   │   └── DeadLetterAlert.tsx   # Warning banner when dead-lettered envelopes > 0
│   ├── events/
│   │   ├── EventFeed.tsx         # Collapsible bottom panel — live filesystem events
│   │   ├── EventRow.tsx          # Single event: type badge + path + relative time
│   │   └── EventOriginBadge.tsx  # provider_sync | agent_write | system color coding
│   ├── workspace/
│   │   ├── WorkspaceSelector.tsx # Dropdown in header listing available workspaces
│   │   └── WorkspacePicker.tsx   # Full-page picker when no workspace is selected
│   └── ui/
│       ├── Badge.tsx             # Reusable semantic badge (maps status → color)
│       ├── RevisionPill.tsx      # Monospace truncated revision with copy button
│       ├── ProviderBadge.tsx     # Provider icon + label chip (github, google-drive, …)
│       ├── RelativeTime.tsx      # "3m ago" / "2d ago" auto-updating display
│       ├── ConnectionDot.tsx     # Pulsing colored dot for WS connection state
│       └── ThemeToggle.tsx       # Dark ↔ light switch
├── hooks/
│   ├── useFileTree.ts        # Fetches and caches listTree responses
│   ├── useFileContent.ts     # Fetches readFile for a given path
│   ├── useFileSearch.ts      # Debounced queryFiles hook
│   ├── useFileEvents.ts      # WebSocket + polling event feed (capped at 300)
│   ├── useSyncStatus.ts      # Polls getSyncStatus every 15s
│   ├── useWorkspaces.ts      # Lists available workspace IDs
│   └── useTheme.ts           # data-theme attribute + localStorage persistence
├── lib/
│   ├── client.ts             # Singleton RelayFileClient factory
│   ├── format.ts             # Human-readable bytes, relative time, truncate revision
│   └── icons.ts             # fileIcon(path, type) → JSX element
├── styles/
│   └── globals.css           # Design tokens (see CSS Variables section)
└── next.config.ts
```

---

## Layout: 3-Pane Architecture

```
┌─────────────────────────────────────────────────────────────────────┐
│  AppHeader                                                           │
│  WorkspaceSelector ▼   ● Connected   [Dark/Light]                  │
├───────────────────┬─────────────────────────┬───────────────────────┤
│  FileSidebar      │  FileContent             │  FileDetails          │
│  290px            │  flex-1                  │  320px                │
│                   │                          │                       │
│  ▾ workspaces     │  ▸ src / components /    │  Metadata             │
│    my-workspace   │    FileTree.tsx           │  author  khaliq       │
│                   │                          │  intent  implement    │
│  ▾ directories    │  [file preview]          │  status  in-progress  │
│    ▸ src/         │                          │                       │
│    ▸ docs/        │                          │  Relations            │
│    ▾ scripts/     │                          │  ◉ src/types.ts       │
│      install.js   │                          │  ◉ DESIGN.md          │
│                   │                          │                       │
│  ─────────────    │                          │  Provider             │
│  Providers        │                          │  github  ✓ synced     │
│  ● github         │                          │                       │
│  ● gdrive         │  ─────────────────────── │  Revision             │
│                   │  Event Feed (collapsible) │  a3f8bc2 [copy]       │
│                   │  file.updated scripts/…  │                       │
│                   │  sync.error   github     │  Last edited          │
│                   │  file.created src/…      │  3 minutes ago        │
└───────────────────┴─────────────────────────┴───────────────────────┘
```

### Mobile (< lg breakpoint)

Three-tab pane switcher replacing the 3-column layout:

```
[  Browse  |  Files  |  Details  ]
```

Tab buttons: `flex h-11 flex-1 items-center justify-center rounded-2xl`

---

## CSS Variables (Design Tokens)

`styles/globals.css` inherits the relaycast token vocabulary exactly. File-observer adds a `--file-*` namespace for domain-specific tokens.

```css
@import url('https://fonts.googleapis.com/css2?family=Outfit:wght@400;500;600;700;800&family=IBM+Plex+Mono:wght@400;500;600&family=Inter:wght@400;500;600;700&display=swap');
@import "tailwindcss";

/* ── Relaycast-compatible dark defaults ── */
:root,
html[data-theme='dark'],
.theme-dark {
  color-scheme: dark;

  --brand-primary:        #6366f1;
  --brand-primary-strong: #4f52d0;
  --brand-primary-soft:   rgba(99,102,241,0.15);
  --brand-primary-faint:  rgba(99,102,241,0.10);

  --background:       #0a0a0b;
  --foreground:       #fafafa;
  --text-secondary:   #a1a1aa;
  --text-muted:       #71717a;
  --text-faint:       #52525b;
  --text-inverse:     #09090b;

  --surface-page:     rgba(10,10,11,0.9);
  --surface-card:     #18181b;
  --surface-glass:    rgba(24,24,27,0.8);
  --surface-soft:     #18181b;
  --surface-muted:    #27272a;
  --surface-strong:   #3f3f46;
  --surface-sidebar:  rgba(18,18,21,0.95);

  --border-default:   #3f3f46;
  --border-strong:    #52525b;
  --border-subtle:    #27272a;

  --status-success:   #10b981;
  --status-warning:   #f59e0b;
  --status-danger:    #f43f5e;
  --status-info:      #3b82f6;

  /* File-observer domain tokens */
  --file-provider-sync:   rgba(99,102,241,0.85);   /* purple — provider_sync origin */
  --file-agent-write:     rgba(79,209,197,0.85);   /* teal — agent_write origin */
  --file-system:          rgba(113,113,122,0.85);  /* muted — system origin */
  --file-tree-indent:     16px;
  --file-tree-item-h:     32px;
}

html[data-theme='light'],
.theme-light {
  color-scheme: light;

  --brand-primary:        #4a90c2;
  --brand-primary-strong: #2d6a9c;
  --brand-primary-soft:   #e7eff7;
  --brand-primary-faint:  rgba(74,144,194,0.08);

  --background:       #f9fafb;
  --foreground:       #111827;
  --text-secondary:   #4b5563;
  --text-muted:       #6b7280;
  --text-faint:       #9ca3af;
  --text-inverse:     #f8fafc;

  --surface-page:     rgba(255,255,255,0.82);
  --surface-card:     rgba(255,255,255,0.9);
  --surface-glass:    rgba(255,255,255,0.76);
  --surface-soft:     #f3f4f6;
  --surface-muted:    #eef2f7;
  --surface-strong:   #ffffff;
  --surface-sidebar:  rgba(245,248,251,0.9);

  --border-default:   #e5e7eb;
  --border-strong:    #d1d5db;
  --border-subtle:    #f3f4f6;

  --status-success:   #059669;
  --status-warning:   #d97706;
  --status-danger:    #e11d48;
  --status-info:      #2563eb;

  /* File-observer domain tokens — light mode */
  --file-provider-sync:   rgba(74,144,194,0.85);
  --file-agent-write:     rgba(20,184,166,0.85);
  --file-system:          rgba(107,114,128,0.85);
  --file-tree-indent:     16px;
  --file-tree-item-h:     32px;
}

/* Grid background texture (same as observer-dashboard) */
.brand-grid {
  background-image:
    linear-gradient(rgba(99,102,241,0.03) 1px, transparent 1px),
    linear-gradient(90deg, rgba(99,102,241,0.03) 1px, transparent 1px);
  background-size: 40px 40px;
}

/* Glassmorphism card */
.brand-glass {
  background: var(--surface-glass);
  backdrop-filter: blur(12px);
  -webkit-backdrop-filter: blur(12px);
  border: 1px solid var(--border-default);
  border-radius: 12px;
}

/* Solid surface card */
.brand-card {
  background: var(--surface-card);
  border: 1px solid var(--border-default);
  border-radius: 12px;
}

/* Theme transition */
html.theme-transitioning * {
  transition: background-color 0.3s ease, border-color 0.3s ease, color 0.15s ease !important;
}
```

---

## Component Specifications

### `AppHeader`

**Purpose:** Top navigation bar fixed across all views.

**Structure:**
```tsx
<header className="flex h-14 items-center justify-between px-4 border-b border-[var(--border-default)] bg-[var(--surface-sidebar)] backdrop-blur-md">
  <div className="flex items-center gap-3">
    <RelayfileLogo />
    <WorkspaceSelector />
  </div>
  <div className="flex items-center gap-3">
    <ConnectionDot status={wsStatus} />
    <span className="text-sm text-[var(--text-secondary)]">{wsStatusLabel}</span>
    <ThemeToggle />
  </div>
</header>
```

**Props:** `wsStatus: 'connected' | 'connecting' | 'disconnected'`

---

### `ConnectionDot`

Replicates the relaycast observer WebSocket status indicator:

```tsx
<span className={cn('h-2.5 w-2.5 rounded-full shrink-0', {
  'bg-green-500 shadow-[0_0_0_4px_rgba(16,185,129,0.16)]': status === 'connected',
  'bg-amber-500 animate-pulse shadow-[0_0_0_4px_rgba(245,158,11,0.14)]': status === 'connecting',
  'bg-rose-500 shadow-[0_0_0_4px_rgba(244,63,94,0.14)]': status === 'disconnected',
})} />
```

---

### `FileSidebar`

**Purpose:** Left 290px panel — workspace navigation and directory tree.

**Sections:**
1. **Workspaces** — list of workspace IDs from environment / config; clicking switches the active workspace and reloads the tree.
2. **Directory Tree** — `FileTree` rooted at `/`.
3. **Providers Filter** — chips for each provider in `SyncStatusResponse.providers`; clicking filters the tree to show only files from that provider.

**Selection pattern:**
```tsx
selectedPath === entry.path
  ? 'bg-[var(--brand-primary-faint)] text-[var(--foreground)] ring-1 ring-[var(--border-strong)]'
  : 'text-[var(--text-secondary)] hover:bg-[var(--surface-muted)] hover:text-[var(--foreground)]'
```

---

### `FileTree`

**Purpose:** Recursive expand/collapse directory tree.

**API integration:**
- Initial load: `client.listTree(workspaceId, { path: '/', depth: 2 })`
- Lazy expansion: when a dir node is expanded, call `client.listTree(workspaceId, { path: dir.path, depth: 1 })`
- Pagination: when `nextCursor` is non-null, show "Load more…" at the bottom of that directory

**Expand/collapse state:** `Record<string, boolean>` keyed by path, stored in component state.

**Keyboard navigation:**
- `↑` / `↓`: move selection
- `Enter` or `Space`: toggle expand (dirs) or open file (files)
- `→`: expand collapsed dir
- `←`: collapse expanded dir or move to parent

**Animation:**
```tsx
<div className={cn('overflow-hidden transition-all duration-150', {
  'max-h-0': !expanded,
  'max-h-[9999px]': expanded,
})}>
```

---

### `FileTreeItem`

Single row in the tree:

```tsx
<div
  role="treeitem"
  aria-selected={selected}
  aria-expanded={entry.type === 'dir' ? expanded : undefined}
  className={cn(
    'flex items-center gap-1.5 rounded-lg px-2 cursor-pointer select-none',
    'transition-colors',
    { height: 'var(--file-tree-item-h)' },
    selected ? selectedClass : defaultClass
  )}
  style={{ paddingLeft: `calc(${depth} * var(--file-tree-indent) + 8px)` }}
>
  <ChevronRight className={cn('h-3.5 w-3.5 shrink-0 transition-transform duration-150', {
    'rotate-90': expanded,
    'opacity-0': entry.type !== 'dir',
  })} />
  <FileIcon path={entry.path} type={entry.type} />
  <span className="flex-1 truncate text-sm font-mono">{basename(entry.path)}</span>
  {entry.revision && <RevisionPill revision={entry.revision} compact />}
  {entry.provider && <ProviderBadge provider={entry.provider} compact />}
</div>
```

---

### `FileIcon`

Maps file extension to a color-coded lucide icon:

```tsx
function fileIcon(path: string, type: 'file' | 'dir') {
  if (type === 'dir') return <Folder className="h-4 w-4 text-amber-400 shrink-0" />;
  const ext = path.split('.').pop()?.toLowerCase();
  switch (ext) {
    case 'ts':
    case 'tsx':  return <FileCode className="h-4 w-4 text-blue-400 shrink-0" />;
    case 'js':
    case 'jsx':  return <FileCode className="h-4 w-4 text-yellow-400 shrink-0" />;
    case 'json': return <Braces   className="h-4 w-4 text-green-400 shrink-0" />;
    case 'md':   return <FileText className="h-4 w-4 text-violet-400 shrink-0" />;
    case 'css':  return <FileCode className="h-4 w-4 text-pink-400 shrink-0" />;
    case 'py':   return <FileCode className="h-4 w-4 text-blue-500 shrink-0" />;
    case 'go':   return <FileCode className="h-4 w-4 text-cyan-400 shrink-0" />;
    case 'yml':
    case 'yaml': return <FileCode className="h-4 w-4 text-orange-400 shrink-0" />;
    default:     return <File     className="h-4 w-4 text-zinc-400 shrink-0" />;
  }
}
```

---

### `FileContent`

**Purpose:** Center panel — breadcrumb navigation, file preview, and search mode.

**Two modes:**
1. **Browse mode** (default): breadcrumb shows path segments; content area shows file text or binary indicator.
2. **Search mode** (when search bar is focused): replaces tree with `queryFiles` results.

**Breadcrumb:**
```tsx
<nav aria-label="File path" className="flex items-center gap-1 px-4 py-2 text-sm text-[var(--text-secondary)]">
  {segments.map((seg, i) => (
    <React.Fragment key={i}>
      {i > 0 && <ChevronRight className="h-3.5 w-3.5 text-[var(--text-faint)]" />}
      <button
        className="hover:text-[var(--foreground)] transition-colors rounded px-1"
        onClick={() => navigateTo(seg.path)}
      >
        {seg.label}
      </button>
    </React.Fragment>
  ))}
</nav>
```

**File preview:**
- Text files (contentType starts with `text/` or is `application/json`): rendered in a scrollable `<pre>` with `font-mono` and syntax-highlighted via class-based coloring.
- Binary files: show a placeholder card with file size and content type.
- Files > 100KB: show a truncated preview with a "Load full file" button.

**Search bar:**
```tsx
<div className="relative mx-4 mb-3">
  <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-[var(--text-faint)]" />
  <input
    type="search"
    placeholder="Search files by path, property, or relation…"
    className="w-full pl-9 pr-4 py-2 text-sm rounded-lg bg-[var(--surface-muted)] border border-[var(--border-default)]
               text-[var(--foreground)] placeholder:text-[var(--text-faint)] focus:outline-none
               focus:ring-1 focus:ring-[var(--brand-primary)]"
    value={query}
    onChange={e => setQuery(e.target.value)}
  />
</div>
```

**API integration:**
- `client.readFile(workspaceId, path)` → called when a file is selected.
- `client.queryFiles(workspaceId, { path: query })` → called with 300ms debounce when search input changes.

---

### `FileSearch`

**Purpose:** Displays `FileQueryItem[]` results returned by `queryFiles`.

**Filterable fields surfaced in UI:**
| Filter | API field | UI control |
|--------|-----------|------------|
| Path prefix | `path` | text input |
| Provider | `provider` | dropdown |
| Relation | `relation` | text chip input |
| Permission | `permission` | text chip input |
| Property | `properties` | key=value pairs |

Results are displayed as a flat list of `FileTreeItem`-style rows with their path, provider badge, and last-edited-at time.

---

### `FileDetails`

**Purpose:** Right 320px panel — full metadata for the selected file.

**Sections:**

#### 1. Revision & Provider
```tsx
<section className="p-4 border-b border-[var(--border-default)]">
  <div className="flex items-center justify-between mb-2">
    <span className="text-xs font-medium text-[var(--text-muted)] uppercase tracking-wide">Revision</span>
    <RevisionPill revision={file.revision} />
  </div>
  {file.provider && (
    <div className="flex items-center justify-between">
      <span className="text-xs font-medium text-[var(--text-muted)] uppercase tracking-wide">Provider</span>
      <ProviderBadge provider={file.provider} />
    </div>
  )}
  {file.lastEditedAt && (
    <div className="flex items-center justify-between mt-2">
      <span className="text-xs text-[var(--text-faint)]">Last edited</span>
      <RelativeTime ts={file.lastEditedAt} className="text-xs text-[var(--text-secondary)]" />
    </div>
  )}
</section>
```

#### 2. Properties
Each property in `semantics.properties` rendered as a key-value row with a colored badge for the value:

```tsx
{Object.entries(properties).map(([key, value]) => (
  <div key={key} className="flex items-center justify-between py-1">
    <span className="text-xs text-[var(--text-muted)]">{key}</span>
    <Badge variant={semanticBadgeVariant(key, value)}>{value}</Badge>
  </div>
))}
```

Semantic badge variant mapping:
- `status` field: `in-progress` → amber, `done` → emerald, `blocked` → rose, default → zinc
- `intent` field: always indigo
- `author` field: always blue
- All others: zinc

#### 3. Relations
Horizontal wrapping chip list:

```tsx
<div className="flex flex-wrap gap-1.5 p-4">
  {relations.map(rel => (
    <button
      key={rel}
      onClick={() => navigateTo(rel)}
      className="flex items-center gap-1 px-2 py-0.5 rounded-full text-xs
                 bg-[var(--surface-muted)] text-[var(--text-secondary)]
                 hover:bg-[var(--brand-primary-faint)] hover:text-[var(--foreground)]
                 border border-[var(--border-subtle)] transition-colors"
    >
      <Link2 className="h-3 w-3" />
      {rel}
    </button>
  ))}
</div>
```

#### 4. Permissions
Lock-icon chips, non-navigable:

```tsx
{permissions.map(perm => (
  <span key={perm} className="flex items-center gap-1 px-2 py-0.5 rounded-full text-xs
                              bg-[var(--surface-muted)] text-[var(--text-faint)] border border-[var(--border-subtle)]">
    <Lock className="h-3 w-3" />
    {perm}
  </span>
))}
```

#### 5. Comments
Expandable list (collapsed by default if > 3 items):

```tsx
{visibleComments.map((comment, i) => (
  <div key={i} className="text-xs text-[var(--text-secondary)] py-1 border-b border-[var(--border-subtle)] last:border-0">
    {comment}
  </div>
))}
{comments.length > 3 && (
  <button onClick={() => setExpanded(!expanded)} className="text-xs text-[var(--brand-primary)] mt-1">
    {expanded ? 'Show less' : `+${comments.length - 3} more`}
  </button>
)}
```

#### 6. Sync Status (for the file's provider)
Mini version of `SyncStatusBadge` showing provider health inline:

```tsx
<SyncStatusBadge status={providerStatus.status} lagSeconds={providerStatus.lagSeconds} />
```

---

### `SyncStatusPanel`

**Purpose:** Expandable panel (or modal) showing all providers' sync health.

**Layout:** Grid of provider cards, one per `SyncProviderStatus`:

```tsx
<div className="grid grid-cols-1 gap-3">
  {providers.map(p => (
    <div key={p.provider} className="brand-card p-4">
      <div className="flex items-center justify-between mb-2">
        <ProviderBadge provider={p.provider} />
        <SyncStatusBadge status={p.status} />
      </div>
      {p.lagSeconds != null && p.lagSeconds > 0 && (
        <div className="text-xs text-amber-400">Lag: {p.lagSeconds}s</div>
      )}
      {p.lastError && (
        <div className="text-xs text-rose-400 mt-1 truncate" title={p.lastError}>{p.lastError}</div>
      )}
      {p.deadLetteredEnvelopes != null && p.deadLetteredEnvelopes > 0 && (
        <DeadLetterAlert count={p.deadLetteredEnvelopes} provider={p.provider} />
      )}
    </div>
  ))}
</div>
```

**API integration:** `client.getSyncStatus(workspaceId)` polled every 15 seconds.

---

### `SyncStatusBadge`

```tsx
const variants: Record<SyncProviderStatusState, string> = {
  healthy:  'bg-emerald-500/12 text-emerald-300 border-emerald-500/25',
  lagging:  'bg-amber-500/12 text-amber-300 border-amber-500/25',
  error:    'bg-rose-500/12 text-rose-300 border-rose-500/25',
  paused:   'bg-stone-500/12 text-stone-300 border-stone-500/25',
};

<span className={cn('px-2 py-0.5 rounded-full text-xs border font-medium', variants[status])}>
  {status}
</span>
```

---

### `EventFeed`

**Purpose:** Collapsible bottom strip in the center panel showing real-time filesystem events.

**Collapse toggle:** chevron button in the strip header. Default: 5 rows visible; expanded: 15 rows.

**Row layout:**
```tsx
<div className="flex items-center gap-2 px-4 py-1.5 text-xs border-b border-[var(--border-subtle)] last:border-0">
  <EventTypeBadge type={event.type} />
  <EventOriginBadge origin={event.origin} />
  <span className="flex-1 font-mono text-[var(--text-secondary)] truncate">{event.path}</span>
  <RelativeTime ts={event.timestamp} className="text-[var(--text-faint)] shrink-0" />
</div>
```

**Event type badge colors:**
```tsx
const eventTypeColors: Record<FilesystemEventType, string> = {
  'file.created':       'text-emerald-400',
  'file.updated':       'text-blue-400',
  'file.deleted':       'text-rose-400',
  'dir.created':        'text-emerald-300',
  'dir.deleted':        'text-rose-300',
  'sync.error':         'text-red-500',
  'sync.ignored':       'text-zinc-400',
  'sync.suppressed':    'text-zinc-400',
  'sync.stale':         'text-amber-400',
  'writeback.failed':   'text-rose-500',
  'writeback.succeeded':'text-emerald-500',
};
```

**Event origin badge:**
```tsx
const originColors: Record<EventOrigin, string> = {
  provider_sync: 'bg-indigo-500/15 text-indigo-300 border-indigo-500/25',
  agent_write:   'bg-teal-500/15 text-teal-300 border-teal-500/25',
  system:        'bg-zinc-500/15 text-zinc-400 border-zinc-500/25',
};
```

---

### `RevisionPill`

```tsx
<span className={cn(
  'font-mono text-xs px-1.5 py-0.5 rounded bg-[var(--surface-muted)]',
  'border border-[var(--border-subtle)] text-[var(--text-muted)]',
  'cursor-copy hover:text-[var(--foreground)] transition-colors',
  compact ? 'max-w-[72px] truncate' : 'max-w-[120px] truncate'
)} onClick={() => navigator.clipboard.writeText(fullRevision)} title={fullRevision}>
  {truncate(revision, compact ? 7 : 12)}
</span>
```

---

### `ProviderBadge`

```tsx
const providerIcons: Record<string, React.ReactNode> = {
  'github':       <GithubIcon className="h-3 w-3" />,
  'google-drive': <DriveIcon className="h-3 w-3" />,
  'notion':       <NotionIcon className="h-3 w-3" />,
  'local':        <HardDrive className="h-3 w-3" />,
};

<span className="flex items-center gap-1 px-1.5 py-0.5 rounded text-xs
                 bg-[var(--surface-muted)] text-[var(--text-secondary)] border border-[var(--border-subtle)]">
  {providerIcons[provider] ?? <Plug className="h-3 w-3" />}
  {compact ? null : provider}
</span>
```

---

### `ThemeToggle`

```tsx
function ThemeToggle() {
  const { theme, toggle } = useTheme();
  return (
    <button onClick={toggle} className="p-2 rounded-lg hover:bg-[var(--surface-muted)] transition-colors">
      {theme === 'dark' ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
    </button>
  );
}
```

---

## Custom Hooks

### `useFileTree`

```ts
function useFileTree(workspaceId: string, path: string = '/') {
  const [entries, setEntries] = useState<TreeEntry[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function loadMore() {
    setLoading(true);
    try {
      const res = await client.listTree(workspaceId, { path, depth: 1, cursor: cursor ?? undefined });
      setEntries(prev => cursor ? [...prev, ...res.entries] : res.entries);
      setCursor(res.nextCursor);
    } catch (e) {
      setError(String(e));
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => { loadMore(); }, [workspaceId, path]);

  return { entries, hasMore: cursor !== null, loadMore, loading, error };
}
```

---

### `useFileContent`

```ts
function useFileContent(workspaceId: string, path: string | null) {
  const [file, setFile] = useState<FileReadResponse | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!path) return;
    setLoading(true);
    client.readFile(workspaceId, path)
      .then(setFile)
      .finally(() => setLoading(false));
  }, [workspaceId, path]);

  return { file, loading };
}
```

---

### `useFileSearch`

```ts
function useFileSearch(workspaceId: string, query: string) {
  const [results, setResults] = useState<FileQueryItem[]>([]);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (!query.trim()) { setResults([]); return; }
    const timeout = setTimeout(async () => {
      setLoading(true);
      try {
        const res = await client.queryFiles(workspaceId, { path: query });
        setResults(res.items);
      } finally {
        setLoading(false);
      }
    }, 300);
    return () => clearTimeout(timeout);
  }, [workspaceId, query]);

  return { results, loading };
}
```

---

### `useFileEvents` (WebSocket + polling fallback)

```ts
const MAX_EVENTS = 300;

function useFileEvents(workspaceId: string) {
  const [events, setEvents] = useState<FilesystemEvent[]>([]);
  const [wsStatus, setWsStatus] = useState<'connecting' | 'connected' | 'disconnected'>('connecting');

  useEffect(() => {
    // Attempt WebSocket connection first
    const conn = client.connectWebSocket(workspaceId);

    conn.on('open',  () => setWsStatus('connected'));
    conn.on('close', () => setWsStatus('disconnected'));
    conn.on('error', () => setWsStatus('disconnected'));
    conn.on('event', (ev) => {
      setEvents(prev => [...prev, ev].slice(-MAX_EVENTS));
    });

    // Polling fallback (used if WS is not supported / disconnected)
    let cursor: string | null = null;
    const poll = setInterval(async () => {
      if (wsStatus === 'connected') return;
      try {
        const res = await client.getEvents(workspaceId, { cursor: cursor ?? undefined, limit: 50 });
        if (res.events.length > 0) {
          setEvents(prev => [...prev, ...res.events].slice(-MAX_EVENTS));
          cursor = res.nextCursor;
        }
      } catch { /* ignore */ }
    }, 2000);

    return () => {
      conn.close();
      clearInterval(poll);
    };
  }, [workspaceId]);

  return { events, wsStatus };
}
```

---

### `useSyncStatus`

```ts
function useSyncStatus(workspaceId: string) {
  const [status, setStatus] = useState<SyncStatusResponse | null>(null);

  useEffect(() => {
    const fetch = () => client.getSyncStatus(workspaceId).then(setStatus).catch(() => {});
    fetch();
    const interval = setInterval(fetch, 15_000);
    return () => clearInterval(interval);
  }, [workspaceId]);

  return status;
}
```

---

### `useTheme`

```ts
function useTheme() {
  const [theme, setTheme] = useState<'dark' | 'light'>(() =>
    (localStorage.getItem('rf-theme') as 'dark' | 'light') ?? 'dark'
  );

  function toggle() {
    const next = theme === 'dark' ? 'light' : 'dark';
    document.documentElement.dataset.theme = next;
    document.documentElement.classList.add('theme-transitioning');
    setTimeout(() => document.documentElement.classList.remove('theme-transitioning'), 300);
    localStorage.setItem('rf-theme', next);
    setTheme(next);
  }

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, []);

  return { theme, toggle };
}
```

---

## API Integration Points

| Feature | SDK Method | Polling Interval |
|---------|-----------|-----------------|
| File tree root | `listTree(wsId, { depth: 2 })` | On workspace select |
| Directory expand | `listTree(wsId, { path, depth: 1 })` | On expand click |
| File content | `readFile(wsId, path)` | On file select |
| File search | `queryFiles(wsId, { path, provider, … })` | 300ms debounce |
| Filesystem events (primary) | `connectWebSocket(wsId)` | Real-time push |
| Filesystem events (fallback) | `getEvents(wsId, { cursor })` | 2s (when WS disconnected) |
| Sync provider health | `getSyncStatus(wsId)` | 15s |
| Dead letters | `getSyncDeadLetters(wsId)` | 30s |
| Write-back operations | `listOps(wsId, { status: 'pending' })` | 10s (if ops panel open) |

---

## File Listing and Filtering

### Tree Listing

The sidebar `FileTree` uses `listTree` with `depth: 1` per directory to enable lazy loading. Each expanded directory stores its entries in a map keyed by path:

```ts
type TreeCache = Record<string, { entries: TreeEntry[]; cursor: string | null }>;
```

The tree renders by walking this cache recursively, falling back to "not loaded" state (shows a loading skeleton row) for directories that haven't been fetched yet.

### Query-Based Filtering (`queryFiles`)

The `FileSearch` component supports these filter combinations surfaced in the UI:

| Filter | Notes |
|--------|-------|
| **Path prefix** | Matched against `path` parameter |
| **Provider** | Dropdown populated from `SyncStatusResponse.providers` |
| **Relation** | Tag-style chip input; each chip is a separate `relation` query |
| **Permission** | Tag-style chip input; matches `permission` |
| **Property key=value** | Key input + value input pairs; sent as `properties` record |

Filters compose as AND conditions (all must match).

Results display as a flat list with path, provider badge, and last-edited-at. Clicking a result opens the file in `FileContent` and shows details in `FileDetails`.

---

## Metadata Display

### Properties Table

Displayed in `FileDetails` as a two-column table:

| Property | Display Treatment |
|----------|------------------|
| `author` | Blue badge |
| `intent` | Indigo badge |
| `status: in-progress` | Amber badge |
| `status: done` | Emerald badge |
| `status: blocked` | Rose badge |
| `status: *` (other) | Zinc badge |
| All others | Zinc/muted plain text |

### Relations

Rendered as interactive chips. Clicking navigates to that path in the file tree (if it exists in the current workspace) or opens it in `FileContent` if it's an exact file path.

### Permissions

Non-interactive lock-icon chips. No navigation — permissions are labels only.

### Comments

Ordered list of strings. Collapsed to 3 items by default with an expand toggle.

---

## Real-Time Event Handling

### Event Feed Behavior

1. WebSocket connection established immediately on workspace load via `connectWebSocket()`.
2. Each `FilesystemEvent` is prepended to the event feed array (newest first).
3. Array is capped at `MAX_EVENTS = 300` to prevent memory bloat.
4. When the selected file receives a `file.updated` or `file.deleted` event, `FileContent` shows an inline "File changed — reload?" banner.
5. When a `sync.error` or `writeback.failed` event arrives, the `SyncStatusBadge` in `FileDetails` pulses briefly (CSS keyframe `animate-pulse` for 2s).

### Event Filtering

The `EventFeed` component supports a filter bar with toggles for:
- Event types (multi-select checkboxes grouped by category: `file.*`, `sync.*`, `writeback.*`)
- Origin (`provider_sync`, `agent_write`, `system`)

---

## Local Dev Server

```jsonc
// packages/file-observer/package.json
{
  "name": "@relayfile/file-observer",
  "scripts": {
    "dev":   "next dev --port 3001",
    "build": "next build",
    "start": "next start --port 3001"
  }
}
```

### Environment Variables

```env
# packages/file-observer/.env.local
NEXT_PUBLIC_RELAYFILE_BASE_URL=http://localhost:8080
NEXT_PUBLIC_RELAYFILE_TOKEN=dev-token
NEXT_PUBLIC_RELAYFILE_WORKSPACE_ID=default
```

The `lib/client.ts` singleton reads these at module load:

```ts
import { RelayFileClient } from '@relayfile/sdk';

export const client = new RelayFileClient({
  baseUrl: process.env.NEXT_PUBLIC_RELAYFILE_BASE_URL!,
  token:   process.env.NEXT_PUBLIC_RELAYFILE_TOKEN!,
});
```

### Running Locally

```bash
cd packages/file-observer
cp .env.local.example .env.local
# Edit .env.local with your workspace ID and API token
npm install
npm run dev
# Open http://localhost:3001
```

---

## Accessibility

- All interactive tree items have `role="treeitem"` with `aria-selected` and `aria-expanded`.
- The file tree root has `role="tree"` with `aria-label="File tree"`.
- The event feed list has `role="log"` with `aria-live="polite"` and `aria-label="Filesystem events"`.
- Color is never the sole indicator: status badges always include a text label.
- Focus is trapped in modal/overlay panels; `Escape` closes them.
- All icon-only buttons have `aria-label`.

---

## Animation Conventions

| Interaction | Animation |
|-------------|-----------|
| Tree expand/collapse | `transition-all duration-150` on max-height |
| Sidebar item hover/select | `transition-colors` |
| WS status reconnecting | `animate-pulse` |
| Theme switch | `.theme-transitioning` class adds 300ms color transitions globally |
| New event in feed | `animate-in fade-in slide-in-from-top-1 duration-150` on new rows |
| File changed banner | `animate-in slide-in-from-top-2 duration-200` |

---

## Mobile Responsiveness

| Breakpoint | Layout |
|-----------|--------|
| `< lg` (< 1024px) | Single pane with tab switcher |
| `≥ lg` | 3-column flex layout |

Mobile tab switcher:

```tsx
const tabs = [
  { id: 'browse',  label: 'Browse',  icon: <Sidebar /> },
  { id: 'files',   label: 'Files',   icon: <FileText /> },
  { id: 'details', label: 'Details', icon: <Info /> },
] as const;

<div className="flex h-11 rounded-2xl bg-[var(--surface-muted)] p-1 gap-1 lg:hidden">
  {tabs.map(tab => (
    <button key={tab.id}
      className={cn('flex h-9 flex-1 items-center justify-center gap-1.5 rounded-xl text-xs font-medium transition-colors',
        activeTab === tab.id
          ? 'bg-[var(--surface-card)] text-[var(--foreground)] shadow-sm'
          : 'text-[var(--text-secondary)] hover:text-[var(--foreground)]'
      )}
      onClick={() => setActiveTab(tab.id)}>
      {tab.icon}
      {tab.label}
    </button>
  ))}
</div>
```

---

DESIGN_DONE
