# @relayfile/agents

## [Unreleased]

### Added
- Initial release of `@relayfile/agents` — thin agent-framework adapters for Relayfile.
  - `connect()` — one-call workspace bootstrap with credential resolution (`agent-relay cloud login` or env overrides) + `joinWorkspace` + #306 ID plumbing.
  - `rf.client` — escape hatch to the raw `RelayFileClient`.
  - `rf.read(path)` — convenience for `client.readFile(workspaceId, path)`.
  - `rf.writeback.create / readCanonical / update / delete / deleteDraft` — provider-agnostic writeback lifecycle with op-status polling.
  - `rf.onEvent(globs, handler, opts?)` — WebSocket push subscription with async token refresh and exponential backoff (1s → 30s, reset on stable connection).
  - `tools.vercel(rf, opts?)` — Vercel AI SDK tool set.
  - `tools.openai(rf, opts?)` — OpenAI Agents SDK tool set.
  - `tools.langchain(rf, opts?)` — LangChain tool set.
- Re-exports `RelayFileApiError`, `RevisionConflictError`, `RelayFileClient`, `FilesystemEvent`, `WebSocketConnection`, `Subscription`, and key file/event types so consumers have a single import surface and the dual-package hazard is avoided.
