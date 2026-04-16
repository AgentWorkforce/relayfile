---
name: creating-claude-rules
description: Use when creating or fixing .claude/rules/ files - provides correct paths frontmatter (not globs), glob patterns, and avoids Cursor-specific fields like alwaysApply
---

### Overview

Rules in `.claude/rules/` are modular instructions scoped to specific files via glob patterns. They load automatically with same priority as `CLAUDE.md`.

### When to Use

- Creating new rules in `.claude/rules/`
- Fixing rules that use wrong frontmatter (`globs` instead of `paths`)
- Migrating Cursor rules to Claude format
- Organizing project-specific conventions

### Quick Reference

| Field | Claude | Cursor |
|-------|--------|--------|
| Path patterns | `paths` | `globs` |
| Always apply | Omit `paths` | `alwaysApply: true` |
| Description | Not documented | `description` |

### Frontmatter

#### Path-Scoped Rules

```yaml
---
paths:
  - "src/api/**/*.ts"
  - "tests/**/*.test.ts"
---
```

#### Global Rules

```markdown
# TypeScript Conventions

Use .js extensions in imports.
```


### Common Mistakes

#### ```yaml

```yaml
# ❌ WRONG - globs is Cursor format
---
globs:
  - "**/*.ts"
---

# ✅ CORRECT - Claude uses paths
---
paths:
  - "**/*.ts"
---
```


### Directory Structure

#### ```

```
.claude/rules/
├── testing.md          # Path-scoped or global
├── typescript.md
└── frontend/           # Subdirectories supported
    └── react.md
```


### Glob Patterns

| Pattern | Matches |
|---------|---------|
| `**/*.ts` | All .ts files anywhere |
| `src/**/*` | Everything under src/ |
| `*.md` | Markdown in root only |
| `**/*.{ts,tsx}` | .ts and .tsx files |
| `{src,lib}/**/*.ts` | .ts in src/ or lib/ |

### Reference

https://code.claude.com/docs/en/memory#modular-rules-with-claude/rules/
