# Integration Runtime Roadmap

**Status:** Proposed
**Date:** 2026-05-07

This is the launch follow-up roadmap for hosted Agent Relay provider files. It splits the work into separate specs that can become workflows.

## Specs

1. [Provider Runtime Abstraction](provider-runtime-abstraction-spec.md)
   - Makes Nango one runtime driver instead of the integration product boundary.
   - Adds `runtime=nango|composio|pipedream` to workspace integrations.

2. [Existing Provider Connection Import](existing-provider-connection-import-spec.md)
   - Lets users attach existing Nango, Composio, and Pipedream connections.
   - Defines import validation, CLI/SDK shape, and unlink-vs-revoke semantics.

3. [BYO Schema And JIT Integrations](byo-schema-jit-integrations-spec.md)
   - Generalizes cloud#457 from Nango-specific sync push into `runtime + connection + schema + mapping`.
   - Defines workspace-scoped schemas, JIT manifests, read paths, and writeback paths.

4. [Agent Workspace Discovery And Readiness](agent-workspace-discovery-readiness-spec.md)
   - Resolves relayfile#79.
   - Adds integration discovery, mounted path discovery, and sync-complete readiness so agents do not silently read empty mounts.

## Recommended Order

1. Implement provider runtime abstraction with the existing Nango path as the regression target.
2. Add existing connection import for Nango, then Composio and Pipedream.
3. Fix SDK/CLI discovery and readiness so agents can safely consume dynamic provider roots.
4. Land Nango JIT compatibility from cloud#457 on top of the manifest model.
5. Extend BYO schema/JIT to non-Nango runtimes.

## Closing Conditions

- Hosted Agent Relay supports at least Nango, Composio, and Pipedream as runtime kinds.
- Users can reuse existing provider connections when Agent Relay can validate them.
- Users can define custom schemas and path mappings for new provider roots.
- Agents can discover provider roots and wait for non-empty synced mounts.
- relayfile#79 is closed with tests and skill docs updated.
