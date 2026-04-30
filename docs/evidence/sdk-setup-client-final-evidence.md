# SDK setup client final evidence

## Relayfile diff
 .trajectories/index.json             | 51 +++++++++++++++++++++++++++++++++---
 docs/sdk-setup-client.md             | 10 +++----
 packages/sdk/typescript/package.json |  1 +
 packages/sdk/typescript/src/index.ts | 24 +++++++++++++++++
 4 files changed, 78 insertions(+), 8 deletions(-)

## Relayfile changed files
.trajectories/index.json
docs/sdk-setup-client.md
packages/sdk/typescript/package.json
packages/sdk/typescript/src/index.ts

## Relayfile status
 M .trajectories/index.json
 M docs/sdk-setup-client.md
 M packages/sdk/typescript/package.json
 M packages/sdk/typescript/src/index.ts
?? .trajectories/active/
?? .trajectories/completed/2026-04/traj_82lywlk9dcnc.json
?? .trajectories/completed/2026-04/traj_82lywlk9dcnc.md
?? .trajectories/completed/2026-04/traj_dmoc4slub7ox.json
?? .trajectories/completed/2026-04/traj_dmoc4slub7ox.md
?? .trajectories/completed/2026-04/traj_i1f02867dkxn.json
?? .trajectories/completed/2026-04/traj_i1f02867dkxn.md
?? .trajectories/completed/2026-04/traj_mfyus7zfgxt2.json
?? .trajectories/completed/2026-04/traj_mfyus7zfgxt2.md
?? .trajectories/completed/2026-04/traj_ubq95azheqpt.json
?? .trajectories/completed/2026-04/traj_ubq95azheqpt.md
?? docs/evidence/
?? docs/sdk-setup-client-acceptance.md
?? docs/sdk-setup-client-review.md
?? packages/sdk/typescript/scripts/
?? packages/sdk/typescript/src/setup-errors.ts
?? packages/sdk/typescript/src/setup-types.ts
?? packages/sdk/typescript/src/setup.test.ts
?? packages/sdk/typescript/src/setup.ts
?? workflows/062-sdk-setup-client.ts

## Cloud diff
 .trajectories/index.json                           | 86 +++++++++++++++++++++-
 .../integrations/[provider]/status/route.ts        | 62 +++++++++++++---
 .../integrations/connect-session/route.ts          | 37 +++++++---
 packages/web/lib/auth/request-auth.ts              | 64 +++++++++++++++-
 4 files changed, 227 insertions(+), 22 deletions(-)

## Cloud changed files
.trajectories/index.json
packages/web/app/api/v1/workspaces/[workspaceId]/integrations/[provider]/status/route.ts
packages/web/app/api/v1/workspaces/[workspaceId]/integrations/connect-session/route.ts
packages/web/lib/auth/request-auth.ts

## Cloud status
 M .trajectories/index.json
 M packages/web/app/api/v1/workspaces/[workspaceId]/integrations/[provider]/status/route.ts
 M packages/web/app/api/v1/workspaces/[workspaceId]/integrations/connect-session/route.ts
 M packages/web/lib/auth/request-auth.ts
?? tests/sdk-setup-client-routes.test.ts
?? workflows/cf-runtime/00-sage-factor-turn-exports.ts
?? workflows/cf-runtime/01-runtime-core.ts
?? workflows/cf-runtime/02-runtime-continuation-adapters.ts
?? workflows/cf-runtime/03-runtime-executor.ts
?? workflows/cf-runtime/04-webhook-runtime-hardening.ts
?? workflows/cf-runtime/05-cloud-agent-persona-factory.ts
?? workflows/cf-runtime/06-sage-migration.ts
?? workflows/cf-runtime/07-specialist-migration.ts
?? workflows/cf-runtime/ARCHITECTURE.md
?? workflows/cf-runtime/README.md
?? workflows/cf-runtime/SPEC.md
?? workflows/cf-runtime/lib/
