/**
 * 003-file-observer-dashboard.ts
 *
 * Builds a beautiful file observer dashboard for relayfile that displays files
 * from a relayfile workspace with real-time updates. Similar to relaycast observer
 * but for viewing files in relayfile.
 *
 * Run: agent-relay run workflows/003-file-observer-dashboard.ts
 */

const { workflow } = require('@agent-relay/sdk/workflows');

const RELAYFILE_ROOT = '/Users/khaliqgant/Projects/AgentWorkforce/relayfile';
const FILE_OBSERVER_DIR = `${RELAYFILE_ROOT}/packages/file-observer`;

async function main() {
  const result = await workflow('file-observer-dashboard')
    .description('Build a beautiful file observer dashboard for relayfile with E2E testing')
    .pattern('dag')
    .channel('wf-file-observer')
    .maxConcurrency(4)
    .timeout(3_600_000)

    .agent('researcher', {
      cli: 'claude',
      preset: 'analyst',
      role: 'Researches best practices for file explorer dashboards',
    })
    .agent('designer', {
      cli: 'claude',
      preset: 'analyst',
      role: 'Designs the dashboard UI and component structure',
    })
    .agent('builder-package', {
      cli: 'codex',
      preset: 'worker',
      role: 'Creates package structure and config files',
    })
    .agent('builder-components', {
      cli: 'codex',
      preset: 'worker',
      role: 'Implements React components for the dashboard',
    })
    .agent('builder-api', {
      cli: 'codex',
      preset: 'worker',
      role: 'Implements API client and hooks for relayfile',
    })
    .agent('tester', {
      cli: 'codex',
      preset: 'worker',
      role: 'Writes and runs E2E tests for the dashboard',
    })

    // Phase 1: Discovery - Research best practices
    .step('research-dashboard-patterns', {
      agent: 'researcher',
      task: `Research best practices for file explorer dashboards. Look at:
1. Modern file browser UIs (Figma, Linear, GitHub file browser)
2. Real-time file synchronization patterns
3. Beautiful dashboard aesthetics with dark mode
4. Relaycast observer-dashboard implementation (in ../relaycast/packages/observer-dashboard)

Focus on:
- Clean, minimal design with proper spacing
- Real-time updates
- File type icons and metadata display
- Smooth animations and transitions
- Mobile responsiveness

Provide a summary of design patterns and recommendations. End with DESIGN_RESEARCH_COMPLETE.`,
      verification: { type: 'output_contains', value: 'DESIGN_RESEARCH_COMPLETE' },
    })

    // Phase 2: Read existing structure
    .step('read-relayfile-structure', {
      type: 'deterministic',
      command: `ls -la ${RELAYFILE_ROOT} && ls -la ${RELAYFILE_ROOT}/site`,
      captureOutput: true,
    })

    .step('read-observer-dashboard', {
      type: 'deterministic',
      command: `cat ${RELAYFILE_ROOT}/../relaycast/packages/observer-dashboard/src/app/globals.css | head -80`,
      captureOutput: true,
    })

    // Phase 3: Design
    .step('design-dashboard', {
      agent: 'designer',
      dependsOn: ['research-dashboard-patterns', 'read-relayfile-structure', 'read-observer-dashboard'],
      task: `Design the file observer dashboard for relayfile. 

Research findings:
{{steps.research-dashboard-patterns.output}}

Current relayfile structure:
{{steps.read-relayfile-structure.output}}

Relaycast observer CSS (for design tokens):
{{steps.read-observer-dashboard.output}}

The dashboard should:
1. Be a Next.js app (like relaycast observer-dashboard)
2. Display files from a relayfile workspace using the API
3. Have a beautiful, modern design matching relaycast aesthetics
4. Show file tree, file details, metadata
5. Support real-time updates via WebSocket
6. Have a local dev server for testing

Create a detailed design document in packages/file-observer/DESIGN.md that includes:
- Component structure
- API integration points
- Styling approach (use CSS variables matching relaycast)
- File listing and filtering capabilities
- Metadata display (author, intent, status, relations)
- Real-time event handling

End with DESIGN_DONE.`,
      verification: { type: 'output_contains', value: 'DESIGN_DONE' },
    })

    // Phase 4: Implementation - Step by step (one file per step)
    .step('create-package-json', {
      agent: 'builder-package',
      dependsOn: ['design-dashboard'],
      task: `Create packages/file-observer/package.json with these contents:
{
  "name": "@relayfile/file-observer",
  "version": "0.0.1",
  "private": true,
  "scripts": {
    "dev": "next dev --port 3101",
    "build": "next build",
    "start": "next start",
    "test": "vitest"
  },
  "dependencies": {
    "next": "^14.2.0",
    "react": "^18.3.0",
    "react-dom": "^18.3.0",
    "lucide-react": "^0.400.0",
    "class-variance-authority": "^0.7.0",
    "clsx": "^2.1.0",
    "tailwind-merge": "^2.3.0"
  },
  "devDependencies": {
    "@types/node": "^22.0.0",
    "@types/react": "^18.3.0",
    "@types/react-dom": "^18.3.0",
    "typescript": "^5.7.0",
    "tailwindcss": "^4.0.0",
    "@tailwindcss/postcss": "^4.0.0",
    "postcss": "^8.5.0",
    "vitest": "^2.0.0",
    "@testing-library/react": "^16.0.0"
  }
}`,
      verification: { type: 'file_exists', value: 'packages/file-observer/package.json' },
    })

    .step('create-next-config', {
      agent: 'builder-package',
      dependsOn: ['create-package-json'],
      task: `Create packages/file-observer/next.config.js:
/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
};

module.exports = nextConfig;`,
      verification: { type: 'file_exists', value: 'packages/file-observer/next.config.js' },
    })

    .step('create-tsconfig', {
      agent: 'builder-package',
      dependsOn: ['create-next-config'],
      task: `Create packages/file-observer/tsconfig.json:
{
  "compilerOptions": {
    "target": "ES2020",
    "lib": ["dom", "dom.iterable", "esnext"],
    "allowJs": true,
    "skipLibCheck": true,
    "strict": true,
    "noEmit": true,
    "esModuleInterop": true,
    "module": "esnext",
    "moduleResolution": "bundler",
    "resolveJsonModule": true,
    "isolatedModules": true,
    "jsx": "preserve",
    "incremental": true,
    "plugins": [{ "name": "next" }],
    "paths": {
      "@/*": ["./src/*"]
    }
  },
  "include": ["next-env.d.ts", "**/*.ts", "**/*.tsx", ".next/types/**/*.ts"],
  "exclude": ["node_modules"]
}`,
      verification: { type: 'file_exists', value: 'packages/file-observer/tsconfig.json' },
    })

    .step('create-postcss-config', {
      agent: 'builder-package',
      dependsOn: ['create-tsconfig'],
      task: `Create packages/file-observer/postcss.config.mjs:
export default {
  plugins: {
    "@tailwindcss/postcss": {},
  },
};`,
      verification: { type: 'file_exists', value: 'packages/file-observer/postcss.config.mjs' },
    })

    .step('create-app-layout', {
      agent: 'builder-components',
      dependsOn: ['create-postcss-config'],
      task: `Create packages/file-observer/src/app/layout.tsx:
import './globals.css';

export const metadata = {
  title: 'RelayFile Observer',
  description: 'File observer dashboard for relayfile workspaces',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body className="bg-[#0a0a0b] text-[#fafafa] antialiased">
        <div className="min-h-screen">
          <header className="border-b border-[#27272a] px-6 py-4">
            <h1 className="text-xl font-semibold text-[#fafafa]">RelayFile Observer</h1>
          </header>
          {children}
        </div>
      </body>
    </html>
  );
}`,
      verification: { type: 'file_exists', value: 'packages/file-observer/src/app/layout.tsx' },
    })

    .step('create-globals-css', {
      agent: 'builder-components',
      dependsOn: ['create-app-layout'],
      task: `Create packages/file-observer/src/app/globals.css with relaycast-style design tokens:
@import "tailwindcss";

:root {
  --background: #0a0a0b;
  --foreground: #fafafa;
  --surface-soft: #18181b;
  --surface-muted: #27272a;
  --border-default: #3f3f46;
  --border-strong: #52525b;
  --brand-primary: #6366f1;
  --brand-primary-faint: rgba(99, 102, 241, 0.1);
  --text-secondary: #a1a1aa;
  --status-warning: #f59e0b;
  --status-success: #10b981;
  --status-error: #ef4444;
}

* {
  box-sizing: border-box;
}

body {
  background: var(--background);
  color: var(--foreground);
}

.brand-grid {
  background-image: 
    linear-gradient(rgba(99, 102, 241, 0.03) 1px, transparent 1px),
    linear-gradient(90deg, rgba(99, 102, 241, 0.03) 1px, transparent 1px);
  background-size: 40px 40px;
}

.brand-glass {
  background: rgba(24, 24, 27, 0.8);
  backdrop-filter: blur(12px);
  border: 1px solid var(--border-default);
  border-radius: 12px;
}

.brand-card {
  background: var(--surface-soft);
  border: 1px solid var(--border-default);
  border-radius: 12px;
}`,
      verification: { type: 'file_exists', value: 'packages/file-observer/src/app/globals.css' },
    })

    .step('create-main-page', {
      agent: 'builder-components',
      dependsOn: ['create-globals-css'],
      task: `Create packages/file-observer/src/app/page.tsx - main dashboard page that shows:
- Workspace selector dropdown
- File tree view with expand/collapse
- File details panel (sidebar)
- Search/filter bar

Use the relayfile API patterns. Include loading states and error handling.`,
      verification: { type: 'file_exists', value: 'packages/file-observer/src/app/page.tsx' },
    })

    .step('create-file-tree-component', {
      agent: 'builder-components',
      dependsOn: ['create-main-page'],
      task: `Create packages/file-observer/src/components/FileTree.tsx:
- Hierarchical file structure display
- Expand/collapse folders
- File type icons (folder, ts, js, json, md, etc.)
- Click to select and show details
- Highlight selected file`,
      verification: { type: 'file_exists', value: 'packages/file-observer/src/components/FileTree.tsx' },
    })

    .step('create-file-details-component', {
      agent: 'builder-components',
      dependsOn: ['create-file-tree-component'],
      task: `Create packages/file-observer/src/components/FileDetails.tsx:
- Show selected file metadata
- Display author, intent, status, relations
- Show revision info
- Color-coded status badges`,
      verification: { type: 'file_exists', value: 'packages/file-observer/src/components/FileDetails.tsx' },
    })

    .step('create-workspace-selector', {
      agent: 'builder-components',
      dependsOn: ['create-file-details-component'],
      task: `Create packages/file-observer/src/components/WorkspaceSelector.tsx:
- Dropdown to select workspace
- Show workspace name/label
- Handle workspace switching`,
      verification: { type: 'file_exists', value: 'packages/file-observer/src/components/WorkspaceSelector.tsx' },
    })

    // API Integration
    .step('create-relayfile-client', {
      agent: 'builder-api',
      dependsOn: ['create-workspace-selector'],
      task: `Create packages/file-observer/src/lib/relayfile-client.ts:
- Functions to call relayfile API: listTree(), readFile(), queryFiles()
- WebSocket connection for real-time events
- Use fetch() or similar for HTTP calls
- Handle auth via environment variables (RELAYFILE_URL, RELAYFILE_TOKEN)`,
      verification: { type: 'file_exists', value: 'packages/file-observer/src/lib/relayfile-client.ts' },
    })

    .step('create-use-file-tree-hook', {
      agent: 'builder-api',
      dependsOn: ['create-relayfile-client'],
      task: `Create packages/file-observer/src/hooks/useFileTree.ts:
- React hook for fetching file tree
- Loading and error states
- Refresh functionality`,
      verification: { type: 'file_exists', value: 'packages/file-observer/src/hooks/useFileTree.ts' },
    })

    .step('create-use-file-events-hook', {
      agent: 'builder-api',
      dependsOn: ['create-use-file-tree-hook'],
      task: `Create packages/file-observer/src/hooks/useFileEvents.ts:
- React hook for WebSocket real-time file events
- Handle file.created, file.updated, file.deleted events
- Update local state on events`,
      verification: { type: 'file_exists', value: 'packages/file-observer/src/hooks/useFileEvents.ts' },
    })

    // Install and Build
    .step('install-dependencies', {
      type: 'deterministic',
      dependsOn: ['create-use-file-events-hook'],
      command: `cd ${FILE_OBSERVER_DIR} && npm install 2>&1 | tail -20`,
      captureOutput: true,
      failOnError: true,
    })

    // Build Phase
    .step('build-dashboard', {
      type: 'deterministic',
      dependsOn: ['install-dependencies'],
      command: `cd ${FILE_OBSERVER_DIR} && npm run build 2>&1 | tail -30`,
      captureOutput: true,
      failOnError: false,
    })

    .step('fix-build-errors', {
      agent: 'builder-components',
      dependsOn: ['build-dashboard'],
      task: `Fix any build errors.

Build output:
{{steps.build-dashboard.output}}

Read the error messages and fix the issues in the code.
Re-run: cd packages/file-observer && npm run build`,
      verification: { type: 'exit_code' },
    })

    .step('build-final', {
      type: 'deterministic',
      dependsOn: ['fix-build-errors'],
      command: `cd ${FILE_OBSERVER_DIR} && npm run build 2>&1 | tail -20`,
      captureOutput: true,
      failOnError: true,
    })

    // Testing Phase (relay-80-100 pattern)
    .step('create-e2e-tests', {
      agent: 'tester',
      dependsOn: ['build-final'],
      task: `Create packages/file-observer/tests/file-observer.test.ts with:
- Test file tree renders correctly
- Test file selection and details panel
- Test workspace switching
- Test file filtering/search
- Mock the relayfile API client`,
      verification: { type: 'file_exists', value: 'packages/file-observer/tests/file-observer.test.ts' },
    })

    .step('run-e2e-tests', {
      type: 'deterministic',
      dependsOn: ['create-e2e-tests'],
      command: `cd ${FILE_OBSERVER_DIR} && npm test 2>&1 | tail -40`,
      captureOutput: true,
      failOnError: false,
    })

    .step('fix-test-failures', {
      agent: 'tester',
      dependsOn: ['run-e2e-tests'],
      task: `Fix any test failures.

Test output:
{{steps.run-e2e-tests.output}}

Read the failing tests and fix either the tests or the implementation.
Re-run: cd packages/file-observer && npm test`,
      verification: { type: 'exit_code' },
    })

    .step('run-e2e-final', {
      type: 'deterministic',
      dependsOn: ['fix-test-failures'],
      command: `cd ${FILE_OBSERVER_DIR} && npm test 2>&1`,
      captureOutput: true,
      failOnError: true,
    })

    .step('verify-tests-pass', {
      type: 'deterministic',
      dependsOn: ['run-e2e-final'],
      command: `cd ${FILE_OBSERVER_DIR} && npm run build 2>&1 | tail -10`,
      captureOutput: true,
      failOnError: true,
    })

    .step('test-local-dev', {
      type: 'deterministic',
      dependsOn: ['verify-tests-pass'],
      command: `cd ${FILE_OBSERVER_DIR} && timeout 10 npm run dev 2>&1 || true`,
      captureOutput: true,
      failOnError: false,
    })

    // Cloud deployment preparation
    .step('create-cloud-config', {
      agent: 'builder-package',
      dependsOn: ['test-local-dev'],
      task: `Create packages/file-observer/wrangler.file-observer.toml:
name = "relayfile-file-observer"
compatibility_date = "2024-12-01"

routes = [
  { pattern = "files.relayfile.dev/*", zone_name = "relayfile.dev" },
  { pattern = "staging-files.relayfile.dev/*", zone_name = "relayfile.dev" },
]

[vars]
FILE_OBSERVER_ORIGIN = "https://relayfile-file-observer.pages.dev"`,
      verification: { type: 'file_exists', value: 'packages/file-observer/wrangler.file-observer.toml' },
    })

    .step('verify-all-files', {
      type: 'deterministic',
      dependsOn: ['create-cloud-config'],
      command: `echo "=== Verifying all files exist ===" && \
test -f ${FILE_OBSERVER_DIR}/package.json && echo "✓ package.json" && \
test -f ${FILE_OBSERVER_DIR}/next.config.js && echo "✓ next.config.js" && \
test -f ${FILE_OBSERVER_DIR}/tsconfig.json && echo "✓ tsconfig.json" && \
test -f ${FILE_OBSERVER_DIR}/postcss.config.mjs && echo "✓ postcss.config.mjs" && \
test -f ${FILE_OBSERVER_DIR}/src/app/layout.tsx && echo "✓ layout.tsx" && \
test -f ${FILE_OBSERVER_DIR}/src/app/globals.css && echo "✓ globals.css" && \
test -f ${FILE_OBSERVER_DIR}/src/app/page.tsx && echo "✓ page.tsx" && \
test -f ${FILE_OBSERVER_DIR}/src/components/FileTree.tsx && echo "✓ FileTree.tsx" && \
test -f ${FILE_OBSERVER_DIR}/src/components/FileDetails.tsx && echo "✓ FileDetails.tsx" && \
test -f ${FILE_OBSERVER_DIR}/src/components/WorkspaceSelector.tsx && echo "✓ WorkspaceSelector.tsx" && \
test -f ${FILE_OBSERVER_DIR}/src/lib/relayfile-client.ts && echo "✓ relayfile-client.ts" && \
test -f ${FILE_OBSERVER_DIR}/src/hooks/useFileTree.ts && echo "✓ useFileTree.ts" && \
test -f ${FILE_OBSERVER_DIR}/src/hooks/useFileEvents.ts && echo "✓ useFileEvents.ts" && \
test -f ${FILE_OBSERVER_DIR}/wrangler.file-observer.toml && echo "✓ wrangler config" && \
echo "=== All files verified ==="`,
      captureOutput: true,
      failOnError: true,
    })

    // Commit
    .step('commit-changes', {
      type: 'deterministic',
      dependsOn: ['verify-all-files'],
      command: `cd ${RELAYFILE_ROOT} && \
git add packages/file-observer/package.json \
packages/file-observer/next.config.js \
packages/file-observer/tsconfig.json \
packages/file-observer/postcss.config.mjs \
packages/file-observer/wrangler.file-observer.toml \
packages/file-observer/src/app/layout.tsx \
packages/file-observer/src/app/globals.css \
packages/file-observer/src/app/page.tsx \
packages/file-observer/src/components/FileTree.tsx \
packages/file-observer/src/components/FileDetails.tsx \
packages/file-observer/src/components/WorkspaceSelector.tsx \
packages/file-observer/src/lib/relayfile-client.ts \
packages/file-observer/src/hooks/useFileTree.ts \
packages/file-observer/src/hooks/useFileEvents.ts && \
git commit -m "feat: add file-observer dashboard package

- Next.js app displaying relayfile workspace files
- File tree with expand/collapse and metadata
- File details panel showing author, intent, status
- Workspace selector dropdown
- API client and hooks for relayfile
- WebSocket support for real-time updates
- Cloudflare Pages deployment config
- E2E tests with Vitest"`,
      captureOutput: true,
      failOnError: true,
    })

    .onError('retry', { maxRetries: 2, retryDelayMs: 10_000 })
    .run({ cwd: RELAYFILE_ROOT });

  console.log('Workflow result:', result.status);
  return result;
}

main().catch((error) => {
  console.error('Workflow failed:', error);
  process.exit(1);
});
