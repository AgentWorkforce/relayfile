// This source barrel preserves the existing built runtime surface and keeps
// Phase 1 mock helpers behind a dedicated testing namespace.
export * from "../dist/index.js";
export * as testing from "./testing/mock-system.js";
