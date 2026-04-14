# Core Package

Keep `packages/core` pure.

- Do not add filesystem, network, process, or other runtime I/O in this package.
- Inject state and persistence through `StorageAdapter` and related interfaces instead of importing concrete backends.
- Keep the public surface exported from `src/index.ts` when you add, remove, or rename modules.
- Preserve ESM-style relative imports with explicit `.js` extensions.
- Verify changes with `npm run build --workspace=packages/core`, and run `npm run test --workspace=packages/core` when tests are added or changed.
