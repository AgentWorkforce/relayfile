---
paths:
  - "packages/core/src/**/*.ts"
  - "packages/sdk/typescript/src/**/*.ts"
---
# TypeScript ESM Imports

Use explicit `.js` extensions in relative imports and exports from TypeScript source files.

Keep public package surfaces centralized in `src/index.ts` when you add, rename, or remove exported modules or types.
