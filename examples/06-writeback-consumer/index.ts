/**
 * Example 06 — Writeback consumer with GitHub handler
 *
 * Polls for pending writebacks and pushes VFS changes back to GitHub
 * via the GitHub API. Demonstrates the full writeback lifecycle:
 * list → read → dispatch → acknowledge.
 *
 * Run:  npx tsx index.ts
 * Env:  RELAYFILE_TOKEN  — JWT with fs:read + ops:read scopes
 *       WORKSPACE_ID     — target workspace ID
 *       GITHUB_TOKEN     — GitHub PAT with repo scope
 */

import {
  RelayFileClient,
  type FileReadResponse,
  type WritebackItem,
} from "@relayfile/sdk";

const token = process.env.RELAYFILE_TOKEN;
const workspaceId = process.env.WORKSPACE_ID ?? "ws_demo";
const githubToken = process.env.GITHUB_TOKEN;

if (!token || !githubToken) {
  console.error("Set RELAYFILE_TOKEN and GITHUB_TOKEN before running.");
  process.exit(1);
}

const client = new RelayFileClient({ token });

interface WritebackRoute {
  owner: string;
  repo: string;
  resource: string;
  id: string;
}

interface WritebackHandler {
  canHandle(path: string): boolean;
  handle(item: WritebackItem, file: FileReadResponse): Promise<void>;
}

class GitHubWritebackHandler implements WritebackHandler {
  constructor(
    private readonly accessToken: string,
    private readonly fetchImpl: typeof fetch = fetch,
  ) {}

  canHandle(path: string): boolean {
    return this.parsePath(path) !== null;
  }

  async handle(item: WritebackItem, file: FileReadResponse): Promise<void> {
    const route = this.parsePath(item.path);
    if (!route) {
      throw new Error(`Unsupported GitHub writeback path: ${item.path}`);
    }

    const body = JSON.parse(file.content);
    const endpoint = this.buildEndpoint(route);

    const response = await this.fetchImpl(`https://api.github.com/${endpoint}`, {
      method: "PATCH",
      headers: {
        Authorization: `Bearer ${this.accessToken}`,
        Accept: "application/vnd.github+json",
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
    });

    if (!response.ok) {
      const errorText = await response.text();
      throw new Error(`GitHub API ${response.status}: ${errorText.slice(0, 200)}`);
    }
  }

  private parsePath(path: string): WritebackRoute | null {
    // /github/{owner}/{repo}/{resource}/{id}.json
    const match = path.match(
      /^\/github\/([^/]+)\/([^/]+)\/([^/]+)\/(\d+)\.json$/,
    );
    if (!match) {
      return null;
    }

    return {
      owner: match[1],
      repo: match[2],
      resource: match[3],
      id: match[4],
    };
  }

  private buildEndpoint(route: WritebackRoute): string {
    const { owner, repo, resource, id } = route;

    switch (resource) {
      case "issues":
        return `repos/${owner}/${repo}/issues/${id}`;
      case "pulls":
        return `repos/${owner}/${repo}/pulls/${id}`;
      case "comments":
        return `repos/${owner}/${repo}/issues/comments/${id}`;
      default:
        throw new Error(`Unsupported GitHub resource: ${resource}`);
    }
  }
}

class WritebackConsumer {
  constructor(
    private readonly relayfile: RelayFileClient,
    private readonly workspaceId: string,
    private readonly handlers: WritebackHandler[],
  ) {}

  async runOnce(): Promise<void> {
    console.log("── listPendingWritebacks ──");
    const pending = await this.relayfile.listPendingWritebacks(this.workspaceId);
    console.log(`  ${pending.length} pending writeback(s)\n`);

    for (const item of pending) {
      await this.processItem(item);
    }
  }

  private async processItem(item: WritebackItem): Promise<void> {
    console.log(`── processing ${item.path} (id=${item.id}) ──`);

    const handler = this.handlers.find((candidate) =>
      candidate.canHandle(item.path),
    );

    if (!handler) {
      await this.fail(item, "No handler registered for this path");
      return;
    }

    try {
      const file = await this.relayfile.readFile(this.workspaceId, item.path);
      console.log(`  read ${file.path} rev=${file.revision}`);

      await handler.handle(item, file);
      console.log("  pushed to upstream provider");

      await this.relayfile.ackWriteback({
        workspaceId: this.workspaceId,
        itemId: item.id,
        success: true,
      });
      console.log("  ack: success");
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      await this.fail(item, message);
    }
  }

  private async fail(item: WritebackItem, message: string): Promise<void> {
    console.log(`  ack: failed — ${message}`);
    await this.relayfile.ackWriteback({
      workspaceId: this.workspaceId,
      itemId: item.id,
      success: false,
      error: message,
    });
  }
}

const consumer = new WritebackConsumer(client, workspaceId, [
  new GitHubWritebackHandler(githubToken),
]);

await consumer.runOnce();
console.log("\nDone.");
