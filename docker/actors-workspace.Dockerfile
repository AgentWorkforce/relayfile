# relayfile-actors-workspace: the relayfile Workspace actor project plus the
# durable-actors SDK compiler, on the published durable-actors TypeScript runtime.
#
#   deploy  (production) compile the actor, upload the bundle to the artifact
#           bucket and register it with the durable-actors control plane
#           (run as a Job/Helm hook; holds the admin api-key).
#   dev     (default; local/kind/CI proofs ONLY) single-process
#           `durable-actors dev` runtime: actor state on local disk, binds
#           127.0.0.1, runs as a sidecar next to the relayfile-actors gateway.
#
# Build from the relayfile repo root:
#   docker build -f docker/actors-workspace.Dockerfile -t relayfile-actors-workspace .
ARG DURABLE_ACTORS_TYPESCRIPT_IMAGE=us-central1-docker.pkg.dev/fluid-analogy-473415-c2/public/durable-actors@sha256:d5ad5fe0ade42e97670446666fa86f1c1dfcaaf1ef31246781642b7372ef5925

FROM node:22.23.0-bookworm-slim AS core
WORKDIR /src
COPY packages/core/package.json packages/core/tsconfig.json ./packages/core/
COPY packages/core/src ./packages/core/src
RUN npm install --no-audit --no-fund --prefix packages/core typescript@5.9.3 \
    && cd packages/core && npx tsc -p tsconfig.json

FROM oven/bun:1.4.2 AS deps
WORKDIR /app
RUN printf '{"name":"relayfile-actors-dev","private":true,"type":"module","dependencies":{"durable-actors":"0.14.6"}}\n' > package.json \
    && bun install --production
COPY --from=core /src/packages/core/package.json ./node_modules/@relayfile/core/package.json
COPY --from=core /src/packages/core/dist ./node_modules/@relayfile/core/dist
COPY packages/durable-actors/actor ./actor
COPY packages/durable-actors/src/runtime ./src/runtime
COPY packages/durable-actors/src/deploy ./src/deploy
RUN printf '#!/bin/sh\nset -e\ncase "${1:-dev}" in\n  dev) shift || true; exec /usr/local/bin/durable-actors dev --sdk-host /app/node_modules/durable-actors/dist/host.js "$@" ;;\n  deploy) exec /usr/local/bin/bun /app/actor/deploy.ts ;;\n  *) exec "$@" ;;\nesac\n' > /app/entrypoint.sh && chmod 755 /app/entrypoint.sh

FROM ${DURABLE_ACTORS_TYPESCRIPT_IMAGE}
COPY --from=deps --chown=10000:10000 /app /app
USER 0
RUN mkdir -p /data/actors && chown -R 10000:10000 /data
USER 10000:10000
WORKDIR /app/actor
ENV DURABLE_ACTORS_SDK_HOST=/app/node_modules/durable-actors/dist/host.js \
    DURABLE_ACTORS_DATA_DIR=/data/actors \
    DURABLE_ACTORS_PROJECT_ID=local \
    DURABLE_ACTORS_PORT=7100
VOLUME ["/data"]
ENTRYPOINT ["/app/entrypoint.sh"]
CMD ["dev"]
