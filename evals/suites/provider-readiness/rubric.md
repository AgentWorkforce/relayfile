# Provider Readiness Rubric

These cases encode the six-provider checks from the Relayfile alias-tree
validation run. They are fixture-backed by default so CI can catch regressions
in the expected mount surface, writeback queueing semantics, and webhook
materialization without live OAuth credentials.

A live run against a mounted workspace should use the same path expectations:

- Reads: every provider root has an index or documented empty subtree.
- Writebacks: every provider uses a file-native path and queues one provider
  writeback without relying on `new.json`.
- Webhooks: provider events materialize canonical records or aliases with
  provider-specific evidence such as `_webhook.receivedAt`.
- Known gaps: Jira and Confluence webhook setup must stay explicit until their
  registration flow is implemented and passing.
