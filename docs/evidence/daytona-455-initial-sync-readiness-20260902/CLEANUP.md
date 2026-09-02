# Cleanup — relayfile#455 initial-sync readiness run, 2026-09-02

## Daytona sandbox

| | |
|---|---|
| id | `8a7d6049-ca63-447b-abdf-f750f121c994` |
| labels | `nodeName=relayfile-457-proof-0902`, `purpose=pr457-verification` |
| snapshot | `relay-orchestrator-sdk-11.8.2-relayfile-v0.10.50-runtime-4.1.52` |
| autoStopInterval | 120 min |

Delete when the evidence is no longer needed:

```sh
curl -X DELETE -H "Authorization: Bearer $DAYTONA_API_KEY" \
  https://app.daytona.io/api/sandbox/8a7d6049-ca63-447b-abdf-f750f121c994
```

It auto-stops after 120 idle minutes regardless, and auto-archives after 7 days.

## Relayfile tokens

Four short-lived RelayAuth tokens were minted for workspace `rw_7ccfea89` under
agent name `relayfile-457-proof-0902` / `relayfile-455-proof`, each with a
1-hour access TTL (`fs:read fs:write sync:read sync:trigger`). All had expired
by 2026-09-02T13:16Z. The refresh tokens carry the default retained TTL and
should be revoked if that matters:
`revokeRelayfileAgentTokens` in `cloud/packages/core/src/relayfile/client.ts`.

No token was ever written to argv (`tokenIngress: 'creds-file'`), and the
generated launcher scripts contain no credential literal — verified by grepping
the generated shell for the token before running it.

## Orphaned Cloud workspaces — needs action

Three workspaces were created while trying to provision an isolated fixture.
Each returned `201` from `POST /api/v1/workspaces` and then resolved with
`provisioned: false` and `cloudWorkspaceId: null`, so all three are
half-created and unusable (this is a live defect, see below):

- `pr457-proof`
- `pr457-proof-evidence`
- `pr457-raw-evidence` — `rw_a20c02f9`

They should be deleted server-side. `relayfile workspace delete NAME --yes`
only removes the locally tracked record.

**Credential note:** both `POST /api/v1/workspaces` and
`GET /api/v1/workspaces/{id}/resolve` return a live `relaycastApiKey`
(`rk_live_*`) in the response body. The key for `rw_a20c02f9` was disclosed to
the operator's terminal during this run and should be rotated along with the
workspace deletion. Whether `resolve` should return `rk_live_*` keys at all is
worth a separate look.

## Repo-side

The two mirrors (`/home/daytona/ws-A2`, `/home/daytona/ws-B2`) and the uploaded
candidate binary live only inside the sandbox and disappear with it. Nothing
was written to the `rw_7ccfea89` workspace: both arms are mirror-mode bootstrap
pulls, `pendingWriteback: 0` and `outbox.pending: 0` in both arms' final
state.json.
