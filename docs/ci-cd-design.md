# RelayFile CI/CD Pipeline Design

## Overview

Four GitHub Actions workflows covering continuous integration, SDK publishing, binary releases, and Cloudflare Workers deployment.

---

## 1. `ci.yml` — Continuous Integration

**Trigger:** Pull requests targeting `main`, pushes to `main`, manual dispatch.

```yaml
name: CI

on:
  pull_request:
    branches: [main]
  push:
    branches: [main]
  workflow_dispatch:

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true
```

### Jobs

#### `go-test` — Go Tests & Build
- **Runner:** `ubuntu-latest`
- **Steps:**
  1. `actions/checkout@v4`
  2. `actions/setup-go@v5` with `go-version-file: go.mod`, `cache: true`
  3. `go test ./...` — run all Go package tests
  4. `go build ./cmd/relayfile ./cmd/relayfile-mount ./cmd/relayfile-cli` — verify all three binaries compile

#### `sdk` — TypeScript SDK
- **Runner:** `ubuntu-latest`
- **Steps:**
  1. `actions/checkout@v4`
  2. `actions/setup-node@v4` with `node-version: "20"`
  3. `cd sdk/relayfile-sdk && npm ci`
  4. `npm run build` — compile TypeScript
  5. `npx tsc --noEmit` — strict type-check (catches errors not surfaced by build)

#### `e2e` — End-to-End Tests
- **Runner:** `ubuntu-latest`
- **Needs:** `go-test`, `sdk`
- **Steps:**
  1. `actions/checkout@v4`
  2. Setup Go + Node (same as above)
  3. `go build -o bin/relayfile-server ./cmd/relayfile` — build the server binary
  4. Start server in background: `bin/relayfile-server &`
  5. Wait for server ready (curl health check with retry)
  6. `npx tsx scripts/e2e.ts --ci` — run E2E test suite
  7. Kill server, collect exit code

#### `workers-typecheck` — CF Workers Type Check (conditional)
- **Runner:** `ubuntu-latest`
- **Condition:** `hashFiles('packages/server/tsconfig.json') != ''`
- **Steps:**
  1. `actions/checkout@v4`
  2. `actions/setup-node@v4` with `node-version: "20"`
  3. `cd packages/server && npm ci && npx tsc --noEmit`

### Secrets & Permissions
- **Permissions:** default (read)
- **Secrets:** none

---

## 2. `publish-npm.yml` — SDK Publishing

**Trigger:** `workflow_dispatch` (manual only).

```yaml
name: Publish SDK

on:
  workflow_dispatch:
    inputs:
      version:
        description: "Version bump type"
        required: true
        type: choice
        options: [patch, minor, major, prepatch, preminor, premajor, prerelease]
      custom_version:
        description: "Custom version (optional, overrides version type)"
        required: false
        type: string
      preid:
        description: "Prerelease identifier (used with pre* version types)"
        required: false
        type: choice
        options: [beta, alpha, rc]
        default: "beta"
      dry_run:
        description: "Dry run (do not actually publish)"
        required: false
        type: boolean
        default: false
      tag:
        description: "NPM dist-tag"
        required: false
        type: choice
        options: [latest, next, beta, alpha]
        default: "latest"

concurrency:
  group: publish-sdk
  cancel-in-progress: false

permissions:
  contents: write
  id-token: write          # REQUIRED for npm provenance (OIDC)
```

### Jobs

#### `build` — Build & Version
- **Runner:** `ubuntu-latest`
- **Outputs:** `new_version`, `is_prerelease`
- **Steps:**
  1. `actions/checkout@v4` with `token: ${{ secrets.GITHUB_TOKEN }}`
  2. `actions/setup-node@v4` with `node-version: "20"`, `registry-url: "https://registry.npmjs.org"` **(CRITICAL)**
  3. `npm ci` in `sdk/relayfile-sdk`
  4. Version bump logic:
     - If `custom_version` is set, use `npm version "$CUSTOM_VERSION" --no-git-tag-version --allow-same-version`
     - Otherwise, `npm version "$VERSION_TYPE" --no-git-tag-version --preid="$PREID"`
  5. Read new version from `package.json`, detect prerelease (`*-*` pattern)
  6. `npm run build` — compile SDK
  7. `npm test` (if tests exist) — verify before publish
  8. `actions/upload-artifact@v4` — upload `package.json` + `dist/`

#### `publish` — Publish to NPM
- **Runner:** `ubuntu-latest`
- **Needs:** `build`
- **Steps:**
  1. `actions/checkout@v4`
  2. `actions/setup-node@v4` with `node-version: "20"`, `registry-url: "https://registry.npmjs.org"`
  3. `actions/download-artifact@v4` — restore built artifacts
  4. `npm install -g npm@latest` — ensure OIDC/provenance support
  5. Verify `dist/` contents (file count, dry-run pack)
  6. **Dry run path:** `npm publish --dry-run --access public --tag $TAG --ignore-scripts`
  7. **Publish path:** `npm publish --access public --provenance --tag $TAG --ignore-scripts`

#### `create-tag` — Git Tag & Release
- **Runner:** `ubuntu-latest`
- **Needs:** `build`, `publish`
- **Condition:** `github.event.inputs.dry_run != 'true'`
- **Steps:**
  1. `actions/checkout@v4` with `fetch-depth: 0`
  2. Download artifacts (for updated `package.json`)
  3. Commit bumped `package.json` to `main`
  4. Create annotated tag `sdk-v${NEW_VERSION}`
  5. Push tag

#### `summary` — Report
- **Condition:** `always()`
- Writes job summary table with version, tag, dry-run status, per-stage results

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
1. Generate an **Automation** token at npmjs.com (not Granular — Automation tokens bypass 2FA)
2. Add as repository secret: `Settings > Secrets > Actions > NODE_AUTH_TOKEN`
3. `actions/setup-node` with `registry-url` automatically wires `NODE_AUTH_TOKEN` into `.npmrc`

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
  group: release-binaries
  cancel-in-progress: false
```

### Jobs

#### `test` — Gate
- **Runner:** `ubuntu-latest`
- **Steps:**
  1. Checkout + setup Go
  2. `make test` — full test suite must pass before release

#### `build` — Cross-Compile (matrix)
- **Runner:** `${{ matrix.os }}`
- **Needs:** `test`
- **Matrix:**

| os | GOOS | GOARCH | suffix |
|----|------|--------|--------|
| `ubuntu-latest` | linux | amd64 | linux-amd64 |
| `ubuntu-latest` | linux | arm64 | linux-arm64 |
| `macos-latest` | darwin | amd64 | darwin-amd64 |
| `macos-latest` | darwin | arm64 | darwin-arm64 |

- **Steps:**
  1. Checkout + setup Go
  2. `make build-all VERSION=${{ github.ref_name }}` or direct `go build` with ldflags
  3. Produces three binaries per platform: `relayfile`, `relayfile-server`, `relayfile-mount`
  4. Upload each platform's binaries as artifact

#### `release` — Create GitHub Release
- **Runner:** `ubuntu-latest`
- **Needs:** `build`
- **Steps:**
  1. Download all artifacts
  2. Create tarballs: `{binary}_{goos}_{goarch}.tar.gz`
  3. Generate checksums: `sha256sum *.tar.gz > checksums.txt`
  4. `softprops/action-gh-release@v2` with:
     - `tag_name: ${{ github.ref_name }}`
     - `files: dist/*.tar.gz, dist/checksums.txt`
     - `generate_release_notes: true`

Alternatively, delegate to `make release VERSION=${{ github.ref_name }}` which already produces tarballs + checksums in `dist/`.

#### `docker` — Docker Image
- **Runner:** `ubuntu-latest`
- **Needs:** `release`
- **Steps:**
  1. Checkout
  2. `docker/setup-buildx-action@v3`
  3. `docker/login-action@v3` — login to `ghcr.io` with `GITHUB_TOKEN`
  4. `docker/metadata-action@v5` — extract tags (`type=ref,event=tag` + `type=raw,value=latest`)
  5. `docker/build-push-action@v6`:
     - `platforms: linux/amd64,linux/arm64`
     - `push: true`
     - `context: .`, `file: ./Dockerfile`

### Secrets & Permissions
| Secret | Purpose |
|--------|---------|
| `GITHUB_TOKEN` (automatic) | Create release, push Docker image to GHCR |

| Permission | Purpose |
|------------|---------|
| `contents: write` | Create GitHub releases |
| `packages: write` | Push to GitHub Container Registry |

### Release Flow
```
git tag v1.2.0 && git push origin v1.2.0
  -> test -> build (4 platforms x 3 binaries) -> release (tarballs + checksums) -> docker (multi-arch)
```

---

## 4. `deploy-workers.yml` — Cloudflare Workers Deployment

**Trigger:** Push to `main` with changes in `packages/server/`.

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

### Jobs

#### `test` — Pre-Deploy Gate
- **Runner:** `ubuntu-latest`
- **Steps:**
  1. Checkout + setup Node
  2. `cd packages/server && npm ci`
  3. `npx tsc --noEmit` — type-check
  4. Run tests if present

#### `migrate` — D1 Database Migrations
- **Runner:** `ubuntu-latest`
- **Needs:** `test`
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
  3. `npx wrangler deploy` (uses `wrangler.toml` in `packages/server/`)

### Secrets & Permissions
| Secret | Purpose |
|--------|---------|
| `CLOUDFLARE_API_TOKEN` | Wrangler authentication |
| `CLOUDFLARE_ACCOUNT_ID` | Target Cloudflare account |

| Variable | Purpose |
|----------|---------|
| `D1_DATABASE_NAME` | D1 database name for migrations |
| `D1_DATABASE_ID` | D1 database ID (if needed by wrangler config) |

| Permission | Purpose |
|------------|---------|
| `contents: read` | Checkout only |

### Wrangler Auth
Wrangler reads `CLOUDFLARE_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` from environment automatically. Set both as repository secrets.

---

## Secrets Summary

| Secret | Workflow(s) | How to Obtain |
|--------|------------|---------------|
| `GITHUB_TOKEN` | all | Automatic — provided by Actions |
| `NODE_AUTH_TOKEN` | `publish-npm` | npmjs.com > Access Tokens > Automation |
| `CLOUDFLARE_API_TOKEN` | `deploy-workers` | Cloudflare dashboard > API Tokens > Create (Workers edit) |
| `CLOUDFLARE_ACCOUNT_ID` | `deploy-workers` | Cloudflare dashboard > Account ID |

## Workflow Interaction Diagram

```
PR opened / push to main
  |
  v
ci.yml -----> go-test + sdk + e2e + workers-typecheck
                (gate for merge)

push to main + packages/server/** changed
  |
  v
deploy-workers.yml -----> test -> migrate -> deploy

manual trigger
  |
  v
publish-npm.yml -----> build -> publish (@relayfile/sdk) -> tag + release

git tag v*
  |
  v
release-binaries.yml -----> test -> build (4 platforms) -> release (tarballs) -> docker (GHCR)
```

## Migration from Existing Workflows

The four new workflows replace the current set:

| Current | Replaced By | Notes |
|---------|-------------|-------|
| `contract.yml` | `ci.yml` | Expanded: adds E2E, Go build check, workers typecheck |
| `publish-sdk.yml` | `publish-npm.yml` | Same pattern, kept aligned with relaycast reference |
| `release-binary.yml` | `release-binaries.yml` | Now triggered on `v*` tags (not every push to main), adds checksums, all 3 binaries, Docker |
| `release.yml` | `release-binaries.yml` | Consolidated — Docker job folded into binary release workflow |
