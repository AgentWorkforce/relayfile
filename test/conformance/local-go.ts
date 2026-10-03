import { execFile } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";
import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createLocalRs256Auth, type LocalRs256Auth } from "../../scripts/test-utils/rsa-signer.js";
import type { ResolvedTarget } from "./types.js";

const execFileAsync = promisify(execFile);
const ALL_SCOPES = [
  "fs:read",
  "fs:write",
  "sync:read",
  "sync:trigger",
  "ops:read",
  "ops:replay",
  "admin:read",
  "admin:replay",
  "webhooks:read",
  "webhooks:write",
  "webhooks:replay",
];

export class LocalGoTarget {
  private child?: ChildProcessWithoutNullStreams;
  private auth?: LocalRs256Auth;
  private scratch?: string;
  private binary?: string;
  private port?: number;
  private logs: string[] = [];

  async start(target: ResolvedTarget): Promise<ResolvedTarget> {
    this.scratch = await mkdtemp(join(tmpdir(), "relayfile-conformance-"));
    this.binary = join(this.scratch, "relayfile");
    this.port = await freePort();
    this.auth = await createLocalRs256Auth();
    await execFileAsync("go", ["build", "-o", this.binary, "./cmd/relayfile"]);

    const relayfileSha = (await execFileAsync("git", ["rev-parse", "HEAD"])).stdout.trim();
    const resolved: ResolvedTarget = {
      ...target,
      relayfileSha,
      runtime: { ...target.runtime, version: relayfileSha },
      baseUrl: `http://127.0.0.1:${this.port}`,
      tokens: {
        primary: this.auth.generateToken(target.workspaces.primary, "conformance-primary", ALL_SCOPES, 3600),
        secondary: this.auth.generateToken(target.workspaces.secondary, "conformance-secondary", ALL_SCOPES, 3600),
        pathScoped: this.auth.generateToken(
          target.workspaces.primary,
          "conformance-path-scoped",
          [
            "relayfile:fs:read:/conformance/scoped/**",
            "relayfile:fs:write:/conformance/scoped/**",
          ],
          3600,
        ),
        readOnly: this.auth.generateToken(target.workspaces.primary, "conformance-read-only", ["fs:read"], 3600),
      },
    };
    await this.spawnServer();
    await waitForHealth(resolved.baseUrl, 15_000, () => this.logTail());
    return resolved;
  }

  async restart(baseUrl: string): Promise<void> {
    await this.stopServer();
    await this.spawnServer();
    await waitForHealth(baseUrl, 15_000, () => this.logTail());
  }

  async close(): Promise<void> {
    await this.stopServer();
    await this.auth?.close();
    this.auth = undefined;
    if (this.scratch) await rm(this.scratch, { recursive: true, force: true });
  }

  logTail(): string {
    return this.logs.slice(-20).join("\n");
  }

  private async spawnServer(): Promise<void> {
    if (!this.binary || !this.port || !this.scratch || !this.auth) {
      throw new Error("local Go target is not prepared");
    }
    this.logs = [];
    this.child = spawn(this.binary, [], {
      env: {
        ...process.env,
        RELAYFILE_ADDR: `127.0.0.1:${this.port}`,
        RELAYFILE_BACKEND_PROFILE: "durable-local",
        RELAYFILE_DATA_DIR: join(this.scratch, "data"),
        RELAYAUTH_JWKS_URL: this.auth.jwksUrl,
        RELAYFILE_VERIFIER_ACCEPT_HS256: "false",
        RELAYFILE_INTERNAL_HMAC_SECRET: "conformance-local-internal-secret-not-production",
        RELAYFILE_EXTERNAL_WRITEBACK: "true",
        E2E_TELEMETRY_DISABLED: "1",
      },
      stdio: ["ignore", "pipe", "pipe"],
    });
    this.child.stdout.on("data", (chunk: Buffer) => this.capture(chunk));
    this.child.stderr.on("data", (chunk: Buffer) => this.capture(chunk));
  }

  private capture(chunk: Buffer): void {
    this.logs.push(...chunk.toString("utf8").split("\n").filter(Boolean));
    if (this.logs.length > 200) this.logs.splice(0, this.logs.length - 200);
  }

  private async stopServer(): Promise<void> {
    const child = this.child;
    this.child = undefined;
    if (!child || child.exitCode !== null || child.signalCode !== null) return;
    child.kill("SIGTERM");
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      await Promise.race([
        new Promise<void>((resolve) => child.once("exit", () => resolve())),
        new Promise<void>((resolve) => {
          timer = setTimeout(resolve, 5_000);
          timer.unref();
        }),
      ]);
    } finally {
      if (timer) clearTimeout(timer);
    }
    if (child.exitCode === null && child.signalCode === null) {
      await new Promise<void>((resolve) => {
        const onExit = () => resolve();
        child.once("exit", onExit);
        if (child.exitCode !== null || child.signalCode !== null) {
          child.off("exit", onExit);
          resolve();
          return;
        }
        child.kill("SIGKILL");
      });
    }
  }
}

async function freePort(): Promise<number> {
  const server = createServer();
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => resolve());
  });
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("could not allocate local port");
  await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
  return address.port;
}

async function waitForHealth(baseUrl: string, timeoutMs: number, logs: () => string): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastError = "";
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`${baseUrl}/health`);
      if (response.ok) return;
      lastError = `status ${response.status}`;
    } catch (error) {
      lastError = error instanceof Error ? error.message : String(error);
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error(`local Go target did not become healthy: ${lastError}\n${logs()}`);
}
