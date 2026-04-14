# TypeScript SDK

Keep this package as an ESM SDK.

- Use explicit `.js` extensions in relative TypeScript imports and exports.
- Route public API additions and removals through `src/index.ts`; do not leave exported types or helpers stranded in leaf files.
- Match the existing Vitest style in `src/*.test.ts` for client and sync coverage.
- Preserve package-level build, typecheck, and publish settings in `package.json` when changing release behavior.
- Verify with `npm run build --workspace=packages/sdk/typescript`, `npm run typecheck --workspace=packages/sdk/typescript`, and `npm run test --workspace=packages/sdk/typescript`.
