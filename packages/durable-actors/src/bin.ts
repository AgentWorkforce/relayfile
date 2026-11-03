#!/usr/bin/env node
import { AdminKeySource, BrokerSource, SessionWorkspaceActors } from "./gateway/actors.js";
import { BearerVerifier } from "./gateway/auth.js";
import { createBroker } from "./gateway/broker.js";
import { loadGatewayConfig } from "./gateway/config.js";
import { createGateway } from "./gateway/server.js";

/**
 * relayfile-actors <gateway|session-broker>
 * (`deploy` ships in the deployer image; see deploy/deploy.ts.)
 */
const command = process.argv[2] ?? "gateway";

function log(event: string, fields: Record<string, unknown> = {}): void {
  console.log(JSON.stringify({ ts: new Date().toISOString(), event, ...fields }));
}

function graceful(server: import("node:http").Server, name: string): void {
  const stop = (signal: string) => {
    log(`${name}.shutdown`, { signal });
    server.close(() => process.exit(0));
    setTimeout(() => process.exit(0), Number(process.env.RELAYFILE_SHUTDOWN_GRACE_MS ?? 10_000)).unref();
  };
  process.once("SIGTERM", () => stop("SIGTERM"));
  process.once("SIGINT", () => stop("SIGINT"));
}

if (command === "gateway") {
  const config = loadGatewayConfig();
  const source = config.brokerUrl
    ? new BrokerSource(config.brokerUrl, config.brokerToken!)
    : new AdminKeySource(config.controlPlaneUrl, config.projectId, config.adminKey!);
  const server = createGateway({
    actors: new SessionWorkspaceActors(source),
    verifier: new BearerVerifier({ jwksUrl: config.jwksUrl ?? undefined, hs256Secret: config.jwtSecret ?? undefined }),
    blobStore: config.blobStore,
    blobPrefix: config.blobPrefix,
    inlineMaxBytes: config.inlineMaxBytes,
    maxBodyBytes: config.maxBodyBytes,
    internalHmacSecret: config.internalHmacSecret,
    externalWritebackProviders: config.externalWritebackProviders,
  });
  server.listen(config.port, () =>
    log("gateway.listening", {
      port: config.port,
      projectId: config.projectId,
      credentials: config.brokerUrl ? "broker" : "admin-key(dev)",
      blobStore: config.blobStore?.kind ?? "inline",
    }),
  );
  graceful(server, "gateway");
} else if (command === "session-broker") {
  const env = process.env;
  const controlPlaneUrl = env.DURABLE_ACTORS_CONTROL_PLANE_URL?.trim();
  if (!controlPlaneUrl) throw new Error("DURABLE_ACTORS_CONTROL_PLANE_URL is required");
  const port = Number(env.PORT ?? 8090);
  const server = createBroker({
    port,
    controlPlaneUrl,
    projectId: env.DURABLE_ACTORS_PROJECT_ID?.trim() || "relayfile",
    adminKey: env.DURABLE_ACTORS_SECRET?.trim() || env.DURABLE_ACTORS_API_KEY?.trim() || "",
    brokerToken: (env.RELAYFILE_ACTORS_BROKER_TOKEN ?? "").trim(),
  });
  server.listen(port, () => log("broker.listening", { port }));
  graceful(server, "broker");
} else {
  console.error("usage: relayfile-actors <gateway|session-broker>");
  process.exit(2);
}
