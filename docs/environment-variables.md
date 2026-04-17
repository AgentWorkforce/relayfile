# Environment Variables Reference

Source-backed reference for environment variables currently read by code, scripts, Docker Compose, and workflow helpers in this monorepo.

Relayfile does not use `dotenv` or `godotenv` in application code. Runtime binaries read directly from the process environment. The only `.env` loading in-repo is:

- Docker Compose interpolation via [`compose.env.example`](../compose.env.example) and [`docker-compose.yml`](../docker-compose.yml)
- [`scripts/live-e2e.sh`](../scripts/live-e2e.sh), which sources an env file directly and defaults to `.env`

## Config Loading Summary

| Area | How values are loaded |
| --- | --- |
| `cmd/relayfile` | Direct `os.Getenv(...)` reads plus typed helpers for ints, int64s, durations, and booleans |
| `cmd/relayfile-mount` | Direct `os.Getenv(...)` reads plus typed helpers for durations, floats, and booleans |
| `cmd/relayfile-cli` | Direct `os.Getenv(...)` reads for login, workspace, and mount defaults; falls back to saved credentials for server/token and saved workspace catalog defaults |
| `scripts/*.sh` | Standard shell parameter expansion such as `${VAR:-default}` |
| `scripts/e2e.ts`, `workflows/*.ts` | `process.env.VAR` lookups |
| `compose.env.example` / `docker-compose.yml` | Docker Compose interpolation and container env injection |

## Root Compose And Docker Tooling

### `compose.env.example`

| Variable | Default | Notes |
| --- | --- | --- |
| `POSTGRES_USER` | `relayfile` | Postgres username for the local compose stack |
| `POSTGRES_PASSWORD` | `relayfile` | Postgres password for the local compose stack |
| `POSTGRES_DB` | `relayfile` | Postgres database name for the local compose stack |
| `POSTGRES_PORT` | `5438` | Host port published for Postgres |
| `RELAYFILE_PORT` | `8080` | Host port published for the Relayfile HTTP server |
| `RELAYFILE_JWT_SECRET` | `dev-secret` | Dev JWT secret passed to the server container |
| `RELAYFILE_INTERNAL_HMAC_SECRET` | `dev-internal-secret` | Dev HMAC secret passed to the server container |
| `RELAYFILE_WORKSPACE` | `ws_live` | Default workspace mounted by the `mountsync` container |
| `RELAYFILE_REMOTE_PATH` | `/` | Remote root synced by the `mountsync` container |
| `RELAYFILE_MOUNT_INTERVAL` | `1s` | Mount polling interval for the compose mount |
| `RELAYFILE_MOUNT_TIMEOUT` | `15s` | Per-sync timeout for the compose mount |
| `RELAYFILE_MOUNT_INTERVAL_JITTER` | `0.2` | Jitter ratio for mount polling |
| `RELAYFILE_TOKEN` | unset | Optional bearer token override for the `mountsync` container |

### `docker-compose.yml`

These names are interpolated by Compose even when they are not read directly by Go or TypeScript code:

| Variable | Default | Notes |
| --- | --- | --- |
| `POSTGRES_USER` | `relayfile` | Used in the Postgres container and in the Relayfile production DSN |
| `POSTGRES_PASSWORD` | `relayfile` | Used in the Postgres container and in the Relayfile production DSN |
| `POSTGRES_DB` | `relayfile` | Used in the Postgres container and in the Relayfile production DSN |
| `POSTGRES_PORT` | `5438` | Host port mapping for Postgres |
| `RELAYFILE_PORT` | `8080` | Host port mapping for Relayfile |
| `RELAYFILE_JWT_SECRET` | `dev-secret` | Passed into the Relayfile server container |
| `RELAYFILE_INTERNAL_HMAC_SECRET` | `dev-internal-secret` | Passed into the Relayfile server container |
| `RELAYFILE_ENVELOPE_WORKERS` | `2` | Passed into the Relayfile server container |
| `RELAYFILE_WRITEBACK_WORKERS` | `2` | Passed into the Relayfile server container |
| `RELAYFILE_PROVIDER_MAX_CONCURRENCY` | `4` | Passed into the Relayfile server container |
| `RELAYFILE_TOKEN` | built-in dev JWT | Passed into the `mountsync` container if no override is supplied |
| `RELAYFILE_WORKSPACE` | `ws_live` | Passed into the `mountsync` container |
| `RELAYFILE_REMOTE_PATH` | `/notion` | Passed into the `mountsync` container |
| `RELAYFILE_MOUNT_INTERVAL` | `1s` | Passed into the `mountsync` container |
| `RELAYFILE_MOUNT_TIMEOUT` | `15s` | Passed into the `mountsync` container |
| `RELAYFILE_MOUNT_INTERVAL_JITTER` | `0.2` | Passed into the `mountsync` container |
| `RELAYFILE_NOTION_TOKEN` | unset | Compose-only passthrough; not read elsewhere in this checkout |
| `RELAYFILE_NOTION_BASE_URL` | unset | Compose-only passthrough; not read elsewhere in this checkout |
| `RELAYFILE_NOTION_API_VERSION` | unset | Compose-only passthrough; not read elsewhere in this checkout |

## Server Package: `cmd/relayfile`

### Core HTTP and auth

| Variable | Type | Default | Notes |
| --- | --- | --- | --- |
| `RELAYFILE_ADDR` | string | `:8080` | Listen address for the HTTP server |
| `RELAYFILE_JWT_SECRET` | string | unset | JWT secret used by the HTTP API |
| `RELAYFILE_INTERNAL_HMAC_SECRET` | string | unset | HMAC secret for internal webhook envelopes |
| `RELAYFILE_INTERNAL_MAX_SKEW` | duration | `5m` | Allowed clock skew for signed internal requests |
| `RELAYFILE_RATE_LIMIT_MAX` | int | `0` | Request cap per rate-limit window; `0` means unlimited |
| `RELAYFILE_RATE_LIMIT_WINDOW` | duration | `1m` | Rate-limit window |
| `RELAYFILE_MAX_BODY_BYTES` | int64 | `0` | Max request body size; `0` means unlimited |

### Storage profile and state backend

| Variable | Type | Default | Notes |
| --- | --- | --- | --- |
| `RELAYFILE_STATE_BACKEND_DSN` | string | unset | Explicit state backend DSN |
| `RELAYFILE_STATE_FILE` | string | unset | File path fallback for state storage and also passed into `StoreOptions` |
| `RELAYFILE_BACKEND_PROFILE` | string | unset | Supported values: `custom`, `memory`, `inmemory`, `production`, `prod`, `durable-local`, `local-durable` |
| `RELAYFILE_DATA_DIR` | string | `.relayfile` | Used when `RELAYFILE_BACKEND_PROFILE` selects a durable local profile |
| `RELAYFILE_PRODUCTION_DSN` | string | unset | Required for `production`/`prod` profiles unless `RELAYFILE_POSTGRES_DSN` is set |
| `RELAYFILE_POSTGRES_DSN` | string | unset | Fallback alias used by the production profile |

State backend precedence is:

1. `RELAYFILE_STATE_BACKEND_DSN`
2. `RELAYFILE_STATE_FILE`
3. Profile-derived defaults from `RELAYFILE_BACKEND_PROFILE`

### Queue backends and store tuning

| Variable | Type | Default | Notes |
| --- | --- | --- | --- |
| `RELAYFILE_ENVELOPE_QUEUE_DSN` | string | unset | Explicit envelope queue DSN |
| `RELAYFILE_ENVELOPE_QUEUE_FILE` | string | unset | File path fallback for the envelope queue |
| `RELAYFILE_WRITEBACK_QUEUE_DSN` | string | unset | Explicit writeback queue DSN |
| `RELAYFILE_WRITEBACK_QUEUE_FILE` | string | unset | File path fallback for the writeback queue |
| `RELAYFILE_ENVELOPE_QUEUE_SIZE` | int | `0` | Envelope queue capacity |
| `RELAYFILE_WRITEBACK_QUEUE_SIZE` | int | `1024` | Writeback queue capacity |
| `RELAYFILE_ENVELOPE_WORKERS` | int | `0` | Number of envelope workers |
| `RELAYFILE_WRITEBACK_WORKERS` | int | `0` | Number of writeback workers |
| `RELAYFILE_MAX_ENVELOPE_ATTEMPTS` | int | `0` | Max envelope retry attempts |
| `RELAYFILE_ENVELOPE_RETRY_DELAY` | duration | `0` | Delay between envelope retries |
| `RELAYFILE_MAX_WRITEBACK_ATTEMPTS` | int | `0` | Max writeback retry attempts |
| `RELAYFILE_WRITEBACK_RETRY_DELAY` | duration | `0` | Delay between writeback retries |
| `RELAYFILE_SUPPRESSION_WINDOW` | duration | `0` | Duplicate-suppression window |
| `RELAYFILE_COALESCE_WINDOW` | duration | `0` | Event coalescing window |
| `RELAYFILE_MAX_STORED_ENVELOPES` | int | `0` | Max number of stored envelopes |
| `RELAYFILE_PROVIDER_MAX_CONCURRENCY` | int | `0` | Provider concurrency cap |
| `RELAYFILE_EXTERNAL_WRITEBACK` | bool | `true` | Enables external writeback mode |

Queue backend precedence is:

1. Explicit `*_DSN`
2. Explicit `*_FILE`
3. Profile-derived defaults from `RELAYFILE_BACKEND_PROFILE`

## Mount Package: `cmd/relayfile-mount`

| Variable | Type | Default | Required | Notes |
| --- | --- | --- | --- | --- |
| `RELAYFILE_BASE_URL` | string | `http://127.0.0.1:8080` | No | Base URL for the Relayfile API |
| `RELAYFILE_TOKEN` | string | unset | Yes | Bearer token for API auth |
| `RELAYFILE_WORKSPACE` | string | unset | Yes | Workspace ID to mount |
| `RELAYFILE_REMOTE_PATH` | string | `/` | No | Remote root path |
| `RELAYFILE_MOUNT_PROVIDER` | string | unset | No | Provider filter for events |
| `RELAYFILE_LOCAL_DIR` | string | unset | Yes | Local mirror directory |
| `RELAYFILE_MOUNT_STATE_FILE` | string | unset | No | Sync state file |
| `RELAYFILE_MOUNT_INTERVAL` | duration | `2s` | No | Polling interval |
| `RELAYFILE_MOUNT_INTERVAL_JITTER` | float | `0.2` | No | Clamped into the `0..1` range |
| `RELAYFILE_MOUNT_TIMEOUT` | duration | `15s` | No | Per-sync timeout |
| `RELAYFILE_MOUNT_WEBSOCKET` | bool | `true` | No | Enables WebSocket streaming when available |

## CLI Package: `cmd/relayfile-cli`

The CLI reads a small env surface directly. Workspace resolution for commands
that accept a workspace is: explicit positional workspace, `RELAYFILE_WORKSPACE`,
the `workspace_id`/`wks` claim in the active token, then the default stored by
`relayfile workspace use`.

| Variable | Type | Default | Notes |
| --- | --- | --- | --- |
| `RELAYFILE_SERVER` | string | unset | Preferred server override for `relayfile login` and server resolution |
| `RELAYFILE_BASE_URL` | string | `https://api.relayfile.dev` for login fallback | Used when `RELAYFILE_SERVER` is unset |
| `RELAYFILE_TOKEN` | string | unset | Used for `login` and as a token fallback before saved credentials |
| `RELAYFILE_WORKSPACE` | string | unset | Workspace override for CLI commands when no workspace argument is supplied |
| `RELAYFILE_OBSERVER_URL` | string | `https://agentrelay.com/observer/file` | Hosted observer URL used by `relayfile observer` |
| `RELAYFILE_REMOTE_PATH` | string | `/` | Mount command default |
| `RELAYFILE_MOUNT_PROVIDER` | string | unset | Mount command provider filter |
| `RELAYFILE_MOUNT_STATE_FILE` | string | unset | Mount command state-file default |
| `RELAYFILE_MOUNT_INTERVAL` | duration | `2s` | Mount command interval default |
| `RELAYFILE_MOUNT_INTERVAL_JITTER` | float | `0.2` | Mount command jitter default |
| `RELAYFILE_MOUNT_TIMEOUT` | duration | `15s` | Mount command timeout default |
| `RELAYFILE_MOUNT_WEBSOCKET` | bool | `true` | Mount command WebSocket default |

Server resolution order in the CLI is:

1. Explicit `--server`
2. `RELAYFILE_SERVER`
3. `RELAYFILE_BASE_URL`
4. Saved credentials
5. Built-in default server URL

Token resolution order is:

1. Explicit `--token`
2. `RELAYFILE_TOKEN`
3. Saved credentials

## Package: `packages/relayfile-sdk`

The SDK source under `packages/relayfile-sdk/src` does not read environment variables at runtime. The only env usage in this package is in `README.md`, where `process.env.RELAYFILE_TOKEN` is shown as an example token source for consumers.

| Variable | Scope | Notes |
| --- | --- | --- |
| `RELAYFILE_TOKEN` | README example only | Consumer-chosen token source, not SDK-internal config |

## Package: `packages/relayfile`

The npm wrapper package does not read environment variables in its published install script under `packages/relayfile/scripts/install.js`.

## Scripts

### `scripts/generate-dev-token.sh`

| Variable | Default | Notes |
| --- | --- | --- |
| `RELAYFILE_WORKSPACE` | `ws_live` | Used when no workspace ID argument is supplied |
| `RELAYFILE_JWT_SECRET` | `dev-secret` | HMAC secret used to sign the generated JWT |
| `RELAYFILE_AGENT_NAME` | `compose-agent` | `agent_name` claim in the generated JWT |
| `RELAYFILE_TOKEN_EXP` | `4102444800` | Expiration epoch written into the generated JWT |

The generated JWT includes `workspace_id`, `agent_name`, and `aud: ["relayfile"]`.

### `scripts/live-e2e.sh`

This script sources an env file directly. By default it reads `.env` in the repo root and will create it from `compose.env.example` if missing.

| Variable | Default | Notes |
| --- | --- | --- |
| `RELAYFILE_PORT` | `8080` | Used to build the health-check base URL |
| `RELAYFILE_WORKSPACE` | `ws_live` | Used for token generation, compose startup, and API checks |
| `RELAYFILE_REMOTE_PATH` | `/` | Used to derive the seeded file path |
| `RELAYFILE_JWT_SECRET` | `dev-secret` | Used indirectly via `generate-dev-token.sh` |
| `RELAYFILE_INTERNAL_HMAC_SECRET` | `dev-internal-secret` | Used to sign internal envelope test requests |
| `RELAYFILE_TOKEN` | generated if unset | If absent, the script generates one with `scripts/generate-dev-token.sh` |
| `RELAYFILE_BASE_URL` | computed | Exported only when calling `scripts/send-internal-envelope.sh` |

### `scripts/nango-e2e.sh`

| Variable | Default | Notes |
| --- | --- | --- |
| `RELAYFILE_BASE_URL` | `http://127.0.0.1:8080` | Base URL for API requests |
| `RELAYFILE_WORKSPACE` | `ws_nango_test` | Workspace under test |
| `RELAYFILE_TOKEN` | unset | Bearer token; falls back to a generated token or a dry-run placeholder |
| `RELAYFILE_JWT_SECRET` | `dev-secret` | Used indirectly when generating a token |
| `NANGO_PROVIDER` | `zendesk` | Provider name inserted into payloads and query filters |
| `NANGO_MODEL` | `tickets` | Object/model name inserted into payloads and query filters |
| `NANGO_OBJECT_ID` | current epoch seconds | Object ID inserted into payloads and file paths |

### `scripts/send-internal-envelope.sh`

| Variable | Default | Notes |
| --- | --- | --- |
| `RELAYFILE_BASE_URL` | `http://127.0.0.1:${RELAYFILE_PORT:-8080}` | Base URL for the internal webhook endpoint |
| `RELAYFILE_PORT` | `8080` | Used only when `RELAYFILE_BASE_URL` is not set |
| `RELAYFILE_WORKSPACE` | `ws_live` | Workspace written into the envelope |
| `RELAYFILE_INTERNAL_HMAC_SECRET` | `dev-internal-secret` | HMAC secret used to sign the request |
| `RELAYFILE_CONTENT_TYPE` | `text/markdown` | Content type inside the synthetic payload |
| `RELAYFILE_OBJECT_ID` | derived from the path | Optional override for the synthetic object ID |

### `scripts/install.sh`

| Variable | Default | Notes |
| --- | --- | --- |
| `INSTALL_PATH` | `/usr/local/bin/relayfile` | Final binary install location |
| `RELAYFILE_VERSION` | latest GitHub release | Optional release tag override |

## Workflows: `workflows/*.ts`

These variables are not product runtime config. They are path overrides for local workflow execution.

| Variable | Used By | Notes |
| --- | --- | --- |
| `RELAYFILE_PATH` | multiple workflows | Override the local checkout path for this repo |
| `RELAYCAST_PATH` | `relayfile-ci-and-publish.ts`, `relayfile-cloud-server.ts` | Override a local Relaycast checkout path |
| `RELAYCAST_SITE_PATH` | `relayfile-landing-page.ts` | Override a local Relaycast site checkout path |
| `RELAYFILE_CLOUD_PATH` | `relayfile-cloud-server.ts` | Override the local checkout path for the private cloud repo |

## Tests

### `scripts/e2e.ts`

| Variable | Default | Notes |
| --- | --- | --- |
| `CI` | false | Shortens wait thresholds when set |

The E2E harness also injects these values into the spawned Relayfile server process:

| Variable | Value |
| --- | --- |
| `RELAYFILE_ADDR` | `:9090` |
| `RELAYFILE_BACKEND_PROFILE` | `memory` |
| `RELAYFILE_JWT_SECRET` | `test-secret` |
| `RELAYFILE_EXTERNAL_WRITEBACK` | `false` |

### Parser coverage tests

These names exist only to exercise helper parsers in tests:

| Variable | Used In |
| --- | --- |
| `RELAYFILE_TEST_INT` | `cmd/relayfile/main_test.go` |
| `RELAYFILE_TEST_INT_BAD` | `cmd/relayfile/main_test.go` |
| `RELAYFILE_TEST_INT_UNSET` | `cmd/relayfile/main_test.go` |
| `RELAYFILE_TEST_INT64` | `cmd/relayfile/main_test.go` |
| `RELAYFILE_TEST_INT64_BAD` | `cmd/relayfile/main_test.go` |
| `RELAYFILE_TEST_INT64_UNSET` | `cmd/relayfile/main_test.go` |
| `RELAYFILE_TEST_DURATION` | `cmd/relayfile/main_test.go` |
| `RELAYFILE_TEST_DURATION_BAD` | `cmd/relayfile/main_test.go` |
| `RELAYFILE_TEST_DURATION_UNSET` | `cmd/relayfile/main_test.go` |
| `RELAYFILE_TEST_FLOAT` | `cmd/relayfile-mount/main_test.go` |
| `RELAYFILE_TEST_FLOAT_BAD` | `cmd/relayfile-mount/main_test.go` |

### Integration tests

| Variable | Default | Notes |
| --- | --- | --- |
| `RELAYFILE_TEST_POSTGRES_DSN` | unset | Enables Postgres integration tests in `internal/relayfile/postgres_backend_integration_test.go` |

## Intentionally Excluded From The Main Tables

The repo also contains env-like names in docs and design notes that are not currently read by code or Compose on this branch. Examples include `RELAYFILE_CORRELATION_ID`, `RELAYFILE_CACHE_*`, `RELAYFILE_CONFLICT_STRATEGY`, `RELAYFILE_MOUNT_MODE`, `RELAYFILE_FUSE_FALLBACK`, and `NANGO_SECRET_KEY`. They are examples or forward-looking design placeholders, not active config-loading surfaces.
