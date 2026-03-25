/**
 * relayfile-landing-page.ts
 *
 * Builds a landing page for relayfile — "Real-time filesystem for humans and agents."
 *
 * The site lives at relayfile/site/ and deploys to Cloudflare Pages.
 * Uses Astro + Tailwind (same stack as relaycast's site) for a fast,
 * static marketing site.
 *
 * Run: agent-relay run workflows/relayfile-landing-page.ts
 */

import { workflow } from '@agent-relay/sdk/workflows';

const RELAYFILE = '/Users/khaliqgant/Projects/AgentWorkforce-relayfile';
const RELAYCAST_SITE = '/Users/khaliqgant/Projects/AgentWorkforce/relaycast/site';

async function main() {
const result = await workflow('relayfile-landing-page')
  .description('Build the relayfile landing page — real-time filesystem for humans and agents')
  .pattern('dag')
  .channel('wf-relayfile-landing')
  .maxConcurrency(3)
  .timeout(3_600_000)

  .agent('designer', {
    cli: 'claude',
    preset: 'lead',
    role: 'Content strategy, copy, page structure, design direction',
    cwd: RELAYFILE,
  })
  .agent('frontend', {
    cli: 'codex',
    preset: 'worker',
    role: 'Implement Astro + Tailwind site',
    cwd: RELAYFILE,
  })
  .agent('illustrator', {
    cli: 'codex',
    preset: 'worker',
    role: 'Create SVG diagrams and visual assets',
    cwd: RELAYFILE,
  })

  // ── Phase 1: Read existing context ─────────────────────────────────

  .step('read-readme', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/README.md`,
    captureOutput: true,
  })

  .step('read-spec', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/docs/relayfile-v1-spec.md`,
    captureOutput: true,
  })

  .step('read-openapi', {
    type: 'deterministic',
    command: `head -100 ${RELAYFILE}/openapi/relayfile-v1.openapi.yaml`,
    captureOutput: true,
  })

  .step('read-relaycast-site', {
    type: 'deterministic',
    command: `ls ${RELAYCAST_SITE}/src/ 2>/dev/null && cat ${RELAYCAST_SITE}/package.json 2>/dev/null | head -20 || echo "No relaycast site found"`,
    captureOutput: true,
  })

  .step('read-architecture-doc', {
    type: 'deterministic',
    command: `cat ${RELAYFILE}/docs/architecture-ascii.md`,
    captureOutput: true,
  })

  // ── Phase 2: Designer creates content and structure ────────────────

  .step('design-site', {
    agent: 'designer',
    dependsOn: ['read-readme', 'read-spec', 'read-openapi', 'read-architecture-doc'],
    task: `Create the content and page structure for the relayfile landing page.

relayfile README:
{{steps.read-readme.output}}

Architecture:
{{steps.read-architecture-doc.output}}

Spec highlights:
{{steps.read-spec.output}}

Write ${RELAYFILE}/site/CONTENT.md with the complete page content:

**Hero section:**
- Headline: Real-time filesystem for humans and agents"
- Subhead: "A revision-controlled, programmable filesystem that syncs everywhere. Mount it locally, in the cloud, or in a sandbox — everyone sees the same files."
- CTA: "Get Started" → docs, "View on GitHub" → repo

**Problem section: "Files are the universal interface"**
- Every tool, every agent, every developer works with files
- But sharing files across machines, sandboxes, and agents is broken
- Git is async. FUSE volumes are slow. Cloud drives have no revision control.
- There's no real-time, programmable filesystem that just works everywhere.

**Solution section: "relayfile"**
Three columns:
1. Mount anywhere — "relayfile-mount syncs a local directory to a shared workspace. Run it on your laptop, in a Docker container, or in a cloud sandbox. Agents and humans see the same files."
2. Revision-controlled — "Every write is tracked with a revision. Conflicts are detected, not silently overwritten. Full event history of who changed what."
3. Programmable — "REST API for reads, writes, queries. Webhook ingestion for external sources. Writeback queues for bidirectional sync. Build workflows on top of files."

**Use cases section:**
1. "Multi-agent coding" — Backend and frontend agents work on the same codebase simultaneously. Changes sync in real-time.
2. "Human + agent collaboration" — Watch an agent work in real-time on your local machine. Edit a file to course-correct. The agent picks it up immediately.
3. "Cross-machine dev" — Mount the same workspace on your laptop and your cloud dev environment. No git push/pull ceremony.
4. "Tool integration" — Notion pages, GitHub files, Linear tickets — all projected into one filesystem via webhooks + writeback.

**How it works section:**
Visual diagram showing:
  Developer laptop ←→ relayfile ←→ Cloud sandbox (Agent A)
                                  ←→ Cloud sandbox (Agent B)

**API preview section:**
Show 3 curl examples: write a file, read a file, list events

**Architecture section:**
- Cloudflare Workers + Durable Objects + R2
- One DO per workspace — zero-conflict single-writer coordination
- Event stream via WebSocket for real-time push
- Mount daemon is a single Go binary — bake into any image

**Footer:**
- GitHub link, docs link, "Built by Agent Workforce"

Write the complete CONTENT.md with all copy.`,
    verification: { type: 'exit_code' },
  })

  // ── Phase 3: Build the site (parallel) ─────────────────────────────

  .step('scaffold-site', {
    agent: 'frontend',
    dependsOn: ['design-site', 'read-relaycast-site'],
    task: `Scaffold the Astro + Tailwind site at ${RELAYFILE}/site/.

Reference relaycast's site structure:
{{steps.read-relaycast-site.output}}

Create:

1. ${RELAYFILE}/site/package.json:
   - Dependencies: astro, @astrojs/tailwind, tailwindcss
   - Scripts: dev, build, preview

2. ${RELAYFILE}/site/astro.config.mjs:
   - Integrations: tailwind
   - Output: static

3. ${RELAYFILE}/site/tailwind.config.cjs:
   - Dark mode, zinc/cyan color scheme (match the agent workforce brand)

4. ${RELAYFILE}/site/tsconfig.json

5. ${RELAYFILE}/site/src/layouts/Layout.astro:
   - HTML shell with dark bg (#09090b), meta tags, favicon
   - Font: Inter or system-ui

6. ${RELAYFILE}/site/src/pages/index.astro:
   - Import Layout, render all sections from the content doc
   - Read content: cat ${RELAYFILE}/site/CONTENT.md
   - Hero with gradient text (cyan → blue)
   - Sections with prose styling
   - API preview with syntax-highlighted code blocks
   - Responsive: mobile-first, max-w-6xl container

7. ${RELAYFILE}/site/src/components/Hero.astro
8. ${RELAYFILE}/site/src/components/FeatureGrid.astro
9. ${RELAYFILE}/site/src/components/UseCases.astro
10. ${RELAYFILE}/site/src/components/ApiPreview.astro
11. ${RELAYFILE}/site/src/components/Architecture.astro
12. ${RELAYFILE}/site/src/components/Footer.astro

Use Tailwind utility classes. Dark theme throughout.
Zinc-950 backgrounds, zinc-100 text, cyan-400 accents.

Write all files to disk.`,
    verification: { type: 'exit_code' },
  })

  .step('create-diagrams', {
    agent: 'illustrator',
    dependsOn: ['design-site', 'read-architecture-doc'],
    task: `Create SVG diagrams for the landing page.

Architecture reference:
{{steps.read-architecture-doc.output}}

Content plan:
{{steps.design-site.output}}

Create these SVG files at ${RELAYFILE}/site/public/:

1. diagram-sync.svg — Shows the sync flow:
   Three boxes: Your Laptop", "relayfile", "Cloud Sandbox"
   Bidirectional arrows between them
   Inside each box: a file tree icon
   Style: dark background (#09090b), cyan (#22d3ee) lines, white text, rounded corners
   Keep it clean and minimal — no gradients, no shadows

2. diagram-architecture.svg — Shows the stack:
   Layers: "Cloudflare Workers" → "Durable Object (per workspace)" → "R2 (content)" / "D1 (metadata)"
   Side: "Mount Daemon" connecting to "Workers" via HTTP/WebSocket
   Style: same dark theme

3. diagram-agents.svg — Shows multi-agent collaboration:
   Center: "relayfile workspace"
   Around it: "Backend Agent", "Frontend Agent", "Human Developer"
   All connected with sync arrows
   File change events flowing between them
   Style: same dark theme

Each SVG should be self-contained, ~400x250px, viewBox-based (scales well).
Use basic SVG elements (rect, text, line, path). No external dependencies.

Write all SVGs to disk.`,
    verification: { type: 'exit_code' },
  })

  // ── Phase 4: Assemble and verify ──────────────────────────────────

  .step('verify-site', {
    type: 'deterministic',
    dependsOn: ['scaffold-site', 'create-diagrams'],
    command: `cd ${RELAYFILE}/site && \
echo === Files ===" && find src public -type f | sort && \
echo "" && echo "=== Install ===" && npm install 2>&1 | tail -3 && \
echo "" && echo "=== Build ===" && npx astro build 2>&1 | tail -10; echo "EXIT: $?"`,
    captureOutput: true,
    failOnError: false,
  })

  .step('fix-build', {
    agent: 'designer',
    dependsOn: ['verify-site'],
    task: `Fix any build errors in the site.

Build output:
{{steps.verify-site.output}}

If EXIT: 0, the site builds. Summarize what was created.
If there are errors, read the failing files and fix them.
Run npx astro build again to verify.

Also review the content — make sure the copy reads well and the sections flow logically.`,
    verification: { type: 'exit_code' },
  })

  .onError('retry', { maxRetries: 1, retryDelayMs: 10_000 })
  .run({
    cwd: RELAYFILE,
    onEvent: (e: any) => console.log(`[${e.type}] ${e.stepName ?? e.step ?? ''} ${e.error ?? ''}`.trim()),
  });

console.log(`\nLanding page workflow: ${result.status}`);
}

main().catch(console.error);
