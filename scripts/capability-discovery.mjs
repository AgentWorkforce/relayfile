/**
 * Capability Discovery Script
 *
 * Uses usePersona from @agentworkforce/workload-router to enumerate
 * available personas and skills relevant to:
 *  - Building file explorer dashboards
 *  - Real-time file watching/sync
 *  - Beautiful React UI components
 *  - Playwright E2E testing
 */

import {
  usePersona,
  personaCatalog,
  PERSONA_INTENTS,
  materializeSkills,
  HARNESS_SKILL_TARGETS,
} from '@agentworkforce/workload-router';

// ──────────────────────────────────────────────────────────────────────────────
// 1. Enumerate ALL personas and their skills
// ──────────────────────────────────────────────────────────────────────────────

console.log('=== PERSONA CATALOG (all intents) ===\n');

for (const intent of PERSONA_INTENTS) {
  const ctx = usePersona(intent);
  const { selection } = ctx;
  console.log(`Intent: ${intent}`);
  console.log(`  Persona ID : ${selection.personaId}`);
  console.log(`  Tier       : ${selection.tier}`);
  console.log(`  Harness    : ${selection.runtime.harness}`);
  console.log(`  Model      : ${selection.runtime.model}`);

  if (selection.skills.length > 0) {
    console.log(`  Skills (${selection.skills.length}):`);
    for (const skill of selection.skills) {
      console.log(`    - [${skill.id}]`);
      console.log(`      source : ${skill.source}`);
      console.log(`      desc   : ${skill.description}`);
    }
  } else {
    console.log('  Skills: (none registered)');
  }

  // Show install plan
  const { install } = ctx;
  if (install.plan.installs.length > 0) {
    console.log('  Install plan:');
    for (const inst of install.plan.installs) {
      console.log(`    prpm install ${inst.packageRef} --as ${inst.harness}`);
      console.log(`    → lands at: ${inst.installedDir}`);
    }
  }

  console.log();
}

// ──────────────────────────────────────────────────────────────────────────────
// 2. Relevant persona matching for the requested capabilities
// ──────────────────────────────────────────────────────────────────────────────

const CAPABILITY_QUERIES = [
  {
    capability: 'Building file explorer dashboards',
    matchedIntents: ['implement-frontend', 'architecture-plan'],
    notes: 'File explorer dashboards are UI-heavy; frontend implementer + architecture planner cover design & build.',
  },
  {
    capability: 'Real-time file watching/sync',
    matchedIntents: ['implement-frontend', 'architecture-plan', 'debugging'],
    notes: 'Real-time sync involves WebSocket/SSE design (architecture) and event-driven UI (frontend). Debugger for race conditions.',
  },
  {
    capability: 'Beautiful React UI components',
    matchedIntents: ['implement-frontend'],
    notes: 'Frontend implementer persona is the canonical owner: ships production-ready, accessible React UI.',
  },
  {
    capability: 'Playwright E2E testing',
    matchedIntents: ['test-strategy', 'verification', 'tdd-enforcement'],
    notes: 'Test strategist designs the plan; verifier checks evidence; TDD guard enforces red-green-refactor discipline.',
  },
];

console.log('=== CAPABILITY → PERSONA MAPPING ===\n');

for (const q of CAPABILITY_QUERIES) {
  console.log(`Capability: "${q.capability}"`);
  console.log(`Notes: ${q.notes}`);
  console.log('Matched personas:');
  for (const intent of q.matchedIntents) {
    const ctx = usePersona(intent);
    const { selection } = ctx;
    console.log(`  [${intent}]`);
    console.log(`    Persona  : ${selection.personaId}`);
    console.log(`    Model    : ${selection.runtime.model} (${selection.runtime.harness})`);
    console.log(`    Timeout  : ${selection.runtime.harnessSettings.timeoutSeconds}s`);
    if (selection.skills.length > 0) {
      console.log('    Skills:');
      for (const s of selection.skills) {
        console.log(`      • ${s.id} → ${s.source}`);
      }
      console.log('    Install command:');
      console.log(`      ${ctx.install.commandString}`);
    } else {
      console.log('    Skills: (no prpm skills — persona uses built-in system prompt)');
    }
  }
  console.log();
}

// ──────────────────────────────────────────────────────────────────────────────
// 3. Harness install targets (where skills land per harness)
// ──────────────────────────────────────────────────────────────────────────────

console.log('=== HARNESS SKILL INSTALL TARGETS ===\n');
for (const [harness, target] of Object.entries(HARNESS_SKILL_TARGETS)) {
  console.log(`${harness}:`);
  console.log(`  --as flag : ${target.asFlag}`);
  console.log(`  skill dir : ${target.dir}`);
}

// ──────────────────────────────────────────────────────────────────────────────
// 4. Suggested prpm.dev skills to register (not yet in catalog)
// ──────────────────────────────────────────────────────────────────────────────

console.log('\n=== SUGGESTED prpm.dev SKILLS TO ADD ===');
console.log('(These are recommended prpm.dev packages that would benefit the matched personas)\n');

const suggested = [
  {
    capability: 'Playwright E2E testing',
    packageRef: 'prpm/playwright-e2e',
    installExample: 'npx -y prpm install prpm/playwright-e2e --as claude',
    description: 'Canonical Playwright E2E skill: browser setup, page object patterns, assertions, screenshot diffing.',
    harness: 'claude',
  },
  {
    capability: 'React UI components (shadcn/ui)',
    packageRef: 'prpm/shadcn-ui',
    installExample: 'npx -y prpm install prpm/shadcn-ui --as claude',
    description: 'shadcn/ui component library skill: accessible components, theming, Tailwind integration.',
    harness: 'claude',
  },
  {
    capability: 'Real-time file watching (chokidar/FSEvents)',
    packageRef: 'prpm/file-watcher',
    installExample: 'npx -y prpm install prpm/file-watcher --as claude',
    description: 'File system watcher skill: chokidar patterns, debouncing, cross-platform FSEvents/inotify.',
    harness: 'claude',
  },
  {
    capability: 'File explorer dashboard (React + file tree)',
    packageRef: 'prpm/react-file-explorer',
    installExample: 'npx -y prpm install prpm/react-file-explorer --as claude',
    description: 'React file explorer skill: virtualised tree, drag-drop, context menus, file icon resolution.',
    harness: 'claude',
  },
];

for (const s of suggested) {
  console.log(`Capability : ${s.capability}`);
  console.log(`Package ref: ${s.packageRef}`);
  console.log(`Install    : ${s.installExample}`);
  console.log(`Description: ${s.description}`);
  console.log();
}

console.log('CAPABILITY_DISCOVERY_COMPLETE');
