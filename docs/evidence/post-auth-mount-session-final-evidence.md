# Post-auth mount session final evidence

Cloud PR reviewed: AgentWorkforce/cloud#498

## Relayfile diff
 .trajectories/index.json                    |  30 +-
 package-lock.json                           |   2 +-
 packages/sdk/typescript/package.json        |   2 +
 packages/sdk/typescript/src/client.ts       |  11 +
 packages/sdk/typescript/src/index.ts        |  25 ++
 packages/sdk/typescript/src/onWrite.test.ts |  88 +++--
 packages/sdk/typescript/src/onWrite.ts      |  18 +-
 packages/sdk/typescript/src/setup-errors.ts | 102 +++++
 packages/sdk/typescript/src/setup-types.ts  | 116 ++++++
 packages/sdk/typescript/src/setup.test.ts   | 544 ++++++++++++++++++++++++++-
 packages/sdk/typescript/src/setup.ts        | 560 +++++++++++++++++++++++++++-
 11 files changed, 1443 insertions(+), 55 deletions(-)

## Cloud diff
 .trajectories/index.json                           |   16 +-
 package-lock.json                                  | 2223 +++++++-------------
 package.json                                       |   11 +-
 packages/web/app/api/v1/linear/query/route.test.ts |    5 +-
 4 files changed, 748 insertions(+), 1507 deletions(-)

## E2E marker
POST_AUTH_MOUNT_E2E_OK

## Reviews
PEER_REVIEW_APPROVED
SELF_REVIEW_CHANGES_REQUESTED: align the documented ProviderNotReadyError surface with the exported SDK API, and add the missing mount-session route coverage required by contract §8.1.

POST_AUTH_MOUNT_SESSION_FINAL_EVIDENCE_READY
