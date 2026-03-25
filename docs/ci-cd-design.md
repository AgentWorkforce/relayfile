# RelayFile CI/CD Pipeline Design

## Overview

Four GitHub Actions workflows covering continuous integration, SDK publishing, binary releases, and Cloudflare Workers deployment. Designed to match the proven patterns from the relaycast project.

---

## 1. `ci.yml` — Continuous Integration

**Trigger:** Pull requests, pushes to `main`, manual dispatch.

```yaml
name: CI

on:
  pull_request:
  push:
    branches: [main]
  workflow_dispatch:

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true
```

### Jobs

#### `go-test` — Go Unit Tests
- **Runner:** `ubuntu-latest`
- **Steps:**
  1. `actions/checkout@v4`
  2. `actions/setup-go@v5` with `go-version-file: go.mod`, `cache: true`
  3. `go test ./...` — run all Go package tests

#### `go-build` — Go Build Verification
- **Runner:** `ubuntu-latest`
- **Needs:** `go-test`
- **Steps:**
  1. Checkout + setup Go (same as above)
  2. `go build ./cmd/relayfile ./cmd/relayfile-mount ./cmd/relayfile-cli` — builds all three binaries to `bin/`
  3. `actions/upload-artifact@v4` — upload `bin/` as `relayfile-binaries` (needed by E2E)

#### `sdk-typecheck` — TypeScript SDK
- **Runner:** `ubuntu-latest`
- **Steps:**
  1. `actions/checkout@v4`
  2. `actions/setup-node@v4` with `node-version: "22"`
  3. `cd packages/relayfile-sdk && npm ci`
  4. `npm run build` — compile TypeScript to `dist/`
  5. `npx tsc --noEmit` — strict type-check (catches errors not surfaced by build)

#### `e2e` — End-to-End Tests
- **Runner:** `ubuntu-latest`
- **Needs:** `go-build`
- **Steps:**
  1. `actions/checkout@v4`
  2. Setup Go + Node 22
  3. `actions/download-artifact@v4` — download `relayfile-binaries` to `bin/`
  4. `chmod +x bin/*`
  5. `npx --yes tsx scripts/e2e.ts --ci` — runs full E2E suite (server lifecycle, mount sync, WebSocket events, bulk operations, export endpoints)

The E2E suite (`scripts/e2e.ts`) handles its own server startup/shutdown — no manual background process management needed.

#### `workers-typecheck` — CF Workers Type Check (conditional)
- **Runner:** `ubuntu-latest`
- **Condition:** `hashFiles('packages/server/tsconfig.json') != ''`
- **Steps:**
  1. `actions/checkout@v4`
  2. `actions/setup-node@v4` with `node-version: "22"`
  3. `cd packages/server && npm ci && npx tsc --noEmit`

**Note:** `packages/server/` does not exist yet. This job is a no-op until a CF Workers server package is added. The `hashFiles` condition ensures it is silently skipped.

### Secrets & Permissions
- **Permissions:** default (read)
- **Secrets:** none

### Job Dependency Graph
```
go-test ──> go-build ──> e2e
sdk-typecheck          (independent)
workers-typecheck      (independent, conditional)
```

---

## 2. `publish-npm.yml` — SDK Publishing

**Trigger:** `workflow_dispatch` (manual only, matching relaycast pattern).

```yaml
name: Publish NPM Package

on:
  workflow_dispatch:
    inputs:
      version:
        description: "Version bump type"
        required: true
        type: choice
        options:
          - patch
          - minor
          - major
          - prerelease
        default: patch
      custom_version:
        description: "Custom version (optional, overrides version type)"
        required: false
        type: string
      dry_run:
        description: "Dry run (do not actually publish)"
        required: false
        type: boolean
        default: false
      tag:
        description: "NPM dist-tag"
        required: false
        type: choice
        options:
          - latest
          - next
          - beta
          - alpha
        default: "latest"

concurrency:
  group: publish-npm
  cancel-in-progress: false

permissions:
  contents: write
  id-token: write          # REQUIRED for npm provenance (OIDC)
```

### Jobs

#### `publish-sdk` — Build, Version & Publish @relayfile/sdk
- **Runner:** `ubuntu-latest`
- **Steps:**
  1. `actions/checkout@v4` with `fetch-depth: 0`, `token: ${{ secrets.GITHUB_TOKEN }}`
  2. `actions/setup-node@v4` with `node-version: "22"`, `cache: npm`, `cache-dependency-path: packages/relayfile-sdk/package-lock.json`, **`registry-url: "https://registry.npmjs.org"`** (CRITICAL — required for NODE_AUTH_TOKEN wiring)
  3. `npm install -g npm@latest` — ensure OIDC/provenance support
  4. `npm ci` in `packages/relayfile-sdk`
  5. **Version bump:**
     - If `custom_version` set: `npm version "$CUSTOM_VERSION" --no-git-tag-version --allow-same-version`
     - Otherwise: `npm version "$VERSION_TYPE" --no-git-tag-version`
     - Output `new_version` and `tag_name=sdk-v${NEW_VERSION}`
  6. `npm run build` — compile SDK
  7. `npx tsc --noEmit` — type-check verification
  8. **Dry run path:** `npm publish --dry-run --access public --tag $TAG --ignore-scripts`
  9. **Publish path:** `npm publish --access public --provenance --tag $TAG --ignore-scripts`
  10. **Git tag (non-dry-run only):**
      - Configure git user as "GitHub Actions"
      - `git add packages/relayfile-sdk/package.json packages/relayfile-sdk/package-lock.json`
      - `git commit -m "chore(sdk): release v${NEW_VERSION}"`
      - `git tag -a "sdk-v${NEW_VERSION}" -m "SDK ${NEW_VERSION}"`
      - Push commit + tag to `main`
  11. `softprops/action-gh-release@v2` — create GitHub Release with install instructions

#### `publish-cli` — Publish relayfile CLI wrapper
- **Runner:** `ubuntu-latest`
- **Needs:** `publish-sdk`
- **Steps:**
  1. Checkout + setup Node with `registry-url: "https://registry.npmjs.org"`
  2. `npm install -g npm@latest`
  3. Sync version from SDK: read `packages/relayfile-sdk/package.json` version, apply to `packages/relayfile/package.json`
  4. **Dry run path:** `npm publish --dry-run --access public --tag $TAG`
  5. **Publish path:** `npm publish --access public --provenance --tag $TAG`

### CRITICAL Requirements
- **`registry-url: "https://registry.npmjs.org"`** must be set in `actions/setup-node` — this wires `NODE_AUTH_TOKEN` into `.npmrc`
- **`--provenance`** flag requires `id-token: write` permission (GitHub OIDC → npm Sigstore attestation)
- **`NODE_AUTH_TOKEN`** must be a repository secret containing an npm Automation token (bypasses 2FA)
- **`npm install -g npm@latest`** is needed because older npm versions don't support OIDC provenance

### Secrets & Permissions
| Secret | Purpose |
|--------|---------|
| `GITHUB_TOKEN` (automatic) | Push commits, create tags/releases |
| `NODE_AUTH_TOKEN` | NPM automation token **(CRITICAL — must be set as repo secret)** |

| Permission | Purpose |
|------------|---------|
| `contents: write` | Push version commits, create tags |
| `id-token: write` | OIDC token for npm provenance attestation |

### NPM Token Setup
1. Go to npmjs.com → Access Tokens → Generate New Token → **Automation** (not Granular — Automation tokens bypass 2FA)
2. Add as repository secret: Settings → Secrets and variables → Actions → `NODE_AUTH_TOKEN`
3. `actions/setup-node` with `registry-url` automatically creates `.npmrc` that references `NODE_AUTH_TOKEN`

---

## 3. `release-binaries.yml` — Binary Releases

**Trigger:** Tag push matching `v*`.

```yaml
name: Release Binaries

on:
  push:
    tags:
      - "v*"

permissions:
  contents: write
  packages: write          # For GHCR Docker push

concurrency:
  group: release-binaries-${{ github.ref }}
  cancel-in-progress: false

env:
  GO_VERSION: "1.22"
  REGISTRY_IMAGE: ghcr.io/agentworkforce/relayfile
```

### Jobs

#### `build` — Cross-Compile (matrix)
- **Runner:** `ubuntu-latest`
- **Matrix:**

| GOOS | GOARCH | Artifact Name |
|------|--------|---------------|
| linux | amd64 | `release-linux-amd64` |
| linux | arm64 | `release-linux-arm64` |
| darwin | amd64 | `release-darwin-amd64` |
| darwin | arm64 | `release-darwin-arm64` |

- **Steps:**
  1. `actions/checkout@v4`
  2. `actions/setup-go@v5` with `go-version: ${{ env.GO_VERSION }}`, `cache: true`
  3. Cross-compile with `CGO_ENABLED=0`:
     ```bash
     go build -o "dist/relayfile-${GOOS}-${GOARCH}" ./cmd/relayfile
     go build -o "dist/relayfile-mount-${GOOS}-${GOARCH}" ./cmd/relayfile-mount
     go build -o "dist/relayfile-cli-${GOOS}-${GOARCH}" ./cmd/relayfile-cli
     ```
  4. Generate per-platform SHA256 checksums:
     ```bash
     shasum -a 256 relayfile-* > checksums-${GOOS}-${GOARCH}.txt
     ```
  5. `actions/upload-artifact@v4` — upload binaries + checksum file

**Three binaries per platform:**
- `relayfile` — main server
- `relayfile-mount` — mount/sync daemon
- `relayfile-cli` — CLI tool

#### `release` — Create GitHub Release
- **Runner:** `ubuntu-latest`
- **Needs:** `build`
- **Steps:**
  1. `actions/download-artifact@v4` with `merge-multiple: true` — flatten all platform artifacts
  2. `softprops/action-gh-release@v2`:
     - `tag_name: ${{ github.ref_name }}`
     - `files: release-assets/*` (12 binaries + 4 checksum files)
     - `generate_release_notes: true`

#### `docker` — Docker Image
- **Runner:** `ubuntu-latest`
- **Needs:** `release`
- **Steps:**
  1. `actions/checkout@v4`
  2. `docker/setup-buildx-action@v3`
  3. `docker/login-action@v3` — login to `ghcr.io` with `GITHUB_TOKEN`
  4. `docker/build-push-action@v6`:
     - `context: .`, `file: ./Dockerfile`
     - `platforms: linux/amd64,linux/arm64`
     - `push: true`
     - Tags: `ghcr.io/agentworkforce/relayfile:latest` + `ghcr.io/agentworkforce/relayfile:${{ github.ref_name }}`

### Secrets & Permissions
| Secret | Purpose |
|--------|---------|
| `GITHUB_TOKEN` (automatic) | Create release, push Docker image to GHCR |

| Permission | Purpose |
|------------|---------|
| `contents: write` | Create GitHub releases, upload assets |
| `packages: write` | Push to GitHub Container Registry |

### Release Flow
```
git tag v1.2.0 && git push origin v1.2.0
  → build (4 platforms × 3 binaries = 12 binaries)
  → release (binaries + checksums → GitHub Release)
  → docker (multi-arch image → ghcr.io)
```

---

## 4. `deploy-workers.yml` — Cloudflare Workers Deployment

**Trigger:** Push to `main` with changes in `packages/server/`, or manual dispatch.

```yaml
name: Deploy Workers

on:
  push:
    branches: [main]
    paths:
      - "packages/server/**"
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: deploy-workers
  cancel-in-progress: false
```

**Note:** This workflow is a placeholder until `packages/server/` is created. It will not trigger until files exist at that path.

### Jobs

#### `typecheck` — Pre-Deploy Gate
- **Runner:** `ubuntu-latest`
- **Steps:**
  1. `actions/checkout@v4`
  2. `actions/setup-node@v4` with `node-version: "22"`
  3. `cd packages/server && npm ci`
  4. `npx tsc --noEmit` — type-check

#### `migrate` — D1 Database Migrations
- **Runner:** `ubuntu-latest`
- **Needs:** `typecheck`
- **Steps:**
  1. Checkout + setup Node
  2. `npm ci` in `packages/server`
  3. `npx wrangler d1 migrations apply $D1_DATABASE_NAME --remote`

#### `deploy` — Deploy to Production
- **Runner:** `ubuntu-latest`
- **Needs:** `migrate`
- **Steps:**
  1. Checkout + setup Node
  2. `npm ci` in `packages/server`
  3. `npx wrangler deploy` (reads `wrangler.toml` in `packages/server/`)

### Secrets & Permissions
| Secret | Purpose |
|--------|---------|
| `CLOUDFLARE_API_TOKEN` | Wrangler authentication (Workers edit permission) |
| `CLOUDFLARE_ACCOUNT_ID` | Target Cloudflare account |

| Variable | Purpose |
|----------|---------|
| `D1_DATABASE_NAME` | D1 database name for migrations |
| `D1_DATABASE_ID` | D1 database ID (referenced in wrangler.toml) |

| Permission | Purpose |
|------------|---------|
| `contents: read` | Checkout only |

### Wrangler Auth
Wrangler reads `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` from the environment automatically. Set both as repository secrets.

### Job Dependency Graph
```
typecheck ──> migrate ──> deploy
```

---

## Complete Secrets Summary

| Secret | Workflow(s) | How to Obtain |
|--------|------------|---------------|
| `GITHUB_TOKEN` | all | Automatic — provided by GitHub Actions |
| `NODE_AUTH_TOKEN` | `publish-npm` | npmjs.com → Access Tokens → Automation |
| `CLOUDFLARE_API_TOKEN` | `deploy-workers` | Cloudflare dashboard → API Tokens → Create (Workers edit) |
| `CLOUDFLARE_ACCOUNT_ID` | `deploy-workers` | Cloudflare dashboard → Overview → Account ID |

## Complete Permissions Summary

| Workflow | `contents` | `id-token` | `packages` |
|----------|-----------|------------|------------|
| `ci.yml` | read (default) | — | — |
| `publish-npm.yml` | **write** | **write** | — |
| `release-binaries.yml` | **write** | — | **write** |
| `deploy-workers.yml` | read | — | — |

## Workflow Interaction Diagram

```
PR opened / push to main
  │
  ▼
ci.yml ────► go-test → go-build → e2e
             sdk-typecheck        (parallel)
             workers-typecheck    (parallel, conditional)

push to main + packages/server/** changed
  │
  ▼
deploy-workers.yml ────► typecheck → migrate → deploy

manual trigger (workflow_dispatch)
  │
  ▼
publish-npm.yml ────► publish-sdk → publish-cli → git tag + GitHub Release

git tag v*
  │
  ▼
release-binaries.yml ────► build (4 platforms × 3 binaries) → release → docker
```

## Migration from Existing Workflows

The four workflows consolidate and replace the current set:

| Current File | Replaced By | Notes |
|-------------|-------------|-------|
| `ci.yml` | `ci.yml` | Already aligned — no changes needed |
| `contract.yml` | `ci.yml` | Contract surface check can be added as additional CI job |
| `publish-npm.yml` | `publish-npm.yml` | Already aligned — provenance enabled |
| `publish-sdk.yml` | `publish-npm.yml` | Consolidated into single publish workflow |
| `release.yml` | `release-binaries.yml` | Consolidated — Docker job folded in |
| `release-binary.yml` | `release-binaries.yml` | Superseded by matrix-based cross-compile |
