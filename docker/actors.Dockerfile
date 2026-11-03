# relayfile-actors: the stateless relayfile gateway for durable-actors
# (`gateway`, default) and its internal `session-broker`.
#
# Build from the relayfile repo root:
#   docker build -f docker/actors.Dockerfile -t relayfile-actors .
FROM node:22.23.0-bookworm-slim AS build
WORKDIR /src
COPY package.json package-lock.json ./
COPY packages/core/package.json ./packages/core/
COPY packages/durable-actors/package.json ./packages/durable-actors/
RUN npm ci --no-audit --no-fund --ignore-scripts --workspace packages/durable-actors --include-workspace-root=false
COPY packages/core ./packages/core
COPY packages/durable-actors ./packages/durable-actors
RUN cd packages/core && npx tsc -p tsconfig.json \
    && cd ../durable-actors && npx esbuild src/bin.ts --bundle --platform=node --target=node22 --format=esm \
       --outfile=/out/relayfile-actors.mjs \
       --banner:js="import { createRequire as __cr } from 'node:module'; const require = __cr(import.meta.url);"

FROM node:22.23.0-bookworm-slim
# No image-level PORT: each command has its own default (gateway 8080, session-broker 8090).
ENV NODE_ENV=production
COPY --from=build /out/relayfile-actors.mjs /opt/relayfile/relayfile-actors.mjs
USER 1000:1000
EXPOSE 8080
ENTRYPOINT ["node", "/opt/relayfile/relayfile-actors.mjs"]
CMD ["gateway"]
