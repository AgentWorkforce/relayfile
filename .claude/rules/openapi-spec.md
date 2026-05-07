---
paths:
  - "openapi/**/*.yaml"
  - "internal/httpapi/server.go"
  - "internal/httpapi/**/*.go"
---
# OpenAPI Spec Currency

Keep `openapi/relayfile-v1.openapi.yaml` in sync with the HTTP server in `internal/httpapi/server.go`.

- When adding or removing an HTTP handler, add or remove the corresponding path entry in the spec in the same commit.
- When adding a query parameter to a handler (e.g. `forkId` or similar), document it in the spec — either as a new `components/parameters` ref or inline on the endpoint.
- When adding or changing a request or response body struct, update the matching schema in `components/schemas`.
- When adding a new response status code to a handler, add it to the endpoint's `responses` map.
- After making server-side changes, run `scripts/check-contract-surface.sh` to verify no silent drift.
- The spec is the contract: no undocumented endpoints, no undocumented parameters.
