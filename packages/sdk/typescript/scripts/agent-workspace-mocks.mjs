import { createServer } from "node:http";

function lowerCaseHeaders(headers) {
  return Object.fromEntries(
    Object.entries(headers).map(([key, value]) => [key.toLowerCase(), value])
  );
}

async function readRequestBody(request) {
  const chunks = [];
  for await (const chunk of request) {
    chunks.push(Buffer.from(chunk));
  }
  const text = Buffer.concat(chunks).toString("utf8");
  if (text === "") {
    return undefined;
  }
  const contentType = String(request.headers["content-type"] ?? "");
  if (contentType.includes("application/json")) {
    return JSON.parse(text);
  }
  return text;
}

function sendJson(response, status, body) {
  response.writeHead(status, { "content-type": "application/json" });
  response.end(JSON.stringify(body));
}

function resolveBaseUrl(value, fallback = "") {
  if (typeof value === "function") {
    return value() ?? fallback;
  }
  return value ?? fallback;
}

function normalizeProvider(input) {
  return String(input ?? "").trim() || "unknown";
}

function normalizeConnectionId(input, provider) {
  const explicit = String(input ?? "").trim();
  if (explicit !== "") {
    return explicit;
  }
  return provider === "notion" ? "conn_notion" : `${provider}_connection`;
}

function connectionKey(provider, connectionId) {
  return `${provider}:${connectionId}`;
}

export function createMockCloudServer({ relayfileBaseUrl, relaycastBaseUrl } = {}) {
  const state = {
    workspaceId: "ws_agent_workspace_demo",
    requests: [],
    joinCounter: 0,
    connections: new Map(),
  };

  const server = createServer(async (request, response) => {
    const requestUrl = new URL(request.url ?? "/", "http://127.0.0.1");
    const requestBody = await readRequestBody(request);
    const requestRecord = {
      method: request.method ?? "GET",
      pathname: requestUrl.pathname,
      searchParams: Object.fromEntries(requestUrl.searchParams.entries()),
      headers: lowerCaseHeaders(request.headers),
      body: requestBody,
    };
    state.requests.push(requestRecord);

    if (requestRecord.method === "POST" && requestRecord.pathname === "/api/v1/workspaces") {
      sendJson(response, 200, {
        workspaceId: state.workspaceId,
        relayfileUrl: resolveBaseUrl(relayfileBaseUrl),
        relaycastApiKey: "rc_mock_key",
        relaycastBaseUrl: resolveBaseUrl(relaycastBaseUrl, "https://relaycast.mock.local"),
        createdAt: "2026-05-01T00:00:00.000Z",
        name: requestBody?.name ?? "agent-workspace-demo",
      });
      return;
    }

    const joinMatch = requestRecord.pathname.match(
      /^\/api\/v1\/workspaces\/([^/]+)\/join$/
    );
    if (requestRecord.method === "POST" && joinMatch) {
      const workspaceId = decodeURIComponent(joinMatch[1]);
      const scopes = Array.isArray(requestBody?.scopes)
        ? requestBody.scopes.filter((scope) => typeof scope === "string")
        : [];
      state.joinCounter += 1;
      sendJson(response, 200, {
        workspaceId,
        token: `rf_token_${state.joinCounter}_${scopes.includes("fs:write") ? "rw" : "ro"}`,
        relayfileUrl: resolveBaseUrl(relayfileBaseUrl),
        wsUrl: "wss://relayfile.mock.local/ws",
        relaycastApiKey: "rc_mock_key",
        relaycastBaseUrl: resolveBaseUrl(relaycastBaseUrl, "https://relaycast.mock.local"),
      });
      return;
    }

    const connectMatch = requestRecord.pathname.match(
      /^\/api\/v1\/workspaces\/([^/]+)\/integrations\/connect-session$/
    );
    if (requestRecord.method === "POST" && connectMatch) {
      const workspaceId = decodeURIComponent(connectMatch[1]);
      const provider = normalizeProvider(
        Array.isArray(requestBody?.allowedIntegrations)
        ? requestBody.allowedIntegrations[0]
        : "unknown"
      );
      const connectionId = normalizeConnectionId(
        requestBody?.connectionId,
        provider
      );
      state.connections.set(connectionKey(provider, connectionId), {
        workspaceId,
        provider,
        connectionId,
        polls: 0,
        ready: false,
        webhookState: "waiting_for_webhook",
        mountedPath: provider === "notion" ? "/notion" : `/${provider}`,
      });
      sendJson(response, 200, {
        token: `session_${workspaceId}_${provider}`,
        expiresAt: "2026-05-01T01:00:00.000Z",
        connectLink: `https://connect.mock.local/${provider}`,
        connectionId,
      });
      return;
    }

    const statusMatch = requestRecord.pathname.match(
      /^\/api\/v1\/workspaces\/([^/]+)\/integrations\/([^/]+)\/status$/
    );
    if (requestRecord.method === "GET" && statusMatch) {
      const provider = decodeURIComponent(statusMatch[2]);
      const connectionId = requestRecord.searchParams.connectionId ?? "";
      const key = connectionKey(provider, connectionId);
      const connection = state.connections.get(key) ?? {
        workspaceId: decodeURIComponent(statusMatch[1]),
        provider,
        connectionId,
        polls: 0,
        ready: false,
        webhookState: "waiting_for_webhook",
        mountedPath: provider === "notion" ? "/notion" : `/${provider}`,
      };
      connection.polls += 1;
      state.connections.set(key, connection);
      sendJson(response, 200, {
        ready: connection.ready,
        provider,
        connectionId,
        state: connection.webhookState,
        mountedPath: connection.mountedPath,
      });
      return;
    }

    const mockWebhookMatch = requestRecord.pathname.match(
      /^\/mock\/integrations\/([^/]+)\/webhook$/
    );
    if (requestRecord.method === "POST" && mockWebhookMatch) {
      const provider = decodeURIComponent(mockWebhookMatch[1]);
      const connectionId = normalizeConnectionId(
        requestBody?.connectionId,
        provider
      );
      const key = connectionKey(provider, connectionId);
      const existing = state.connections.get(key);
      if (!existing) {
        sendJson(response, 404, {
          code: "connection_not_found",
          message: `Unknown mock connection ${key}`,
          correlationId: "cloud_mock_connection_not_found",
        });
        return;
      }
      existing.ready = requestBody?.ready !== false;
      existing.webhookState = String(requestBody?.state ?? "ready");
      state.connections.set(key, existing);
      sendJson(response, 200, {
        ok: true,
        provider,
        connectionId,
        ready: existing.ready,
        state: existing.webhookState,
      });
      return;
    }

    sendJson(response, 404, {
      code: "not_found",
      message: `Unhandled cloud route ${requestRecord.method} ${requestRecord.pathname}`,
      correlationId: "cloud_mock_not_found",
    });
  });

  return {
    server,
    state,
    baseUrl: "",
    markConnectionReady(provider, connectionId, overrides = {}) {
      const key = connectionKey(provider, connectionId);
      const existing = state.connections.get(key);
      if (!existing) {
        throw new Error(`Cannot mark unknown connection ready: ${key}`);
      }
      existing.ready = overrides.ready ?? true;
      existing.webhookState = overrides.state ?? "ready";
      state.connections.set(key, existing);
    },
    async close() {
      await closeServer(server);
    },
  };
}

export function createMockRelayfileServer() {
  const files = new Map();
  const state = {
    requests: [],
    revisionCounter: 0,
  };

  const server = createServer(async (request, response) => {
    const requestUrl = new URL(request.url ?? "/", "http://127.0.0.1");
    const requestBody = await readRequestBody(request);
    const requestRecord = {
      method: request.method ?? "GET",
      pathname: requestUrl.pathname,
      searchParams: Object.fromEntries(requestUrl.searchParams.entries()),
      headers: lowerCaseHeaders(request.headers),
      body: requestBody,
    };
    state.requests.push(requestRecord);

    const treeMatch = requestRecord.pathname.match(
      /^\/v1\/workspaces\/([^/]+)\/fs\/tree$/
    );
    if (requestRecord.method === "GET" && treeMatch) {
      const path = normalizePath(requestRecord.searchParams.path ?? "/");
      sendJson(response, 200, {
        path,
        entries: buildTreeEntries(files, path),
        nextCursor: null,
      });
      return;
    }

    const fileMatch = requestRecord.pathname.match(
      /^\/v1\/workspaces\/([^/]+)\/fs\/file$/
    );
    if (requestRecord.method === "GET" && fileMatch) {
      const targetPath = normalizePath(requestRecord.searchParams.path ?? "/");
      const file = files.get(targetPath);
      if (!file) {
        sendJson(response, 404, {
          code: "not_found",
          message: `No file at ${targetPath}`,
          correlationId: "relayfile_mock_not_found",
        });
        return;
      }
      sendJson(response, 200, {
        path: targetPath,
        revision: file.revision,
        contentType: file.contentType,
        content: file.content,
        encoding: "utf-8",
      });
      return;
    }

    sendJson(response, 404, {
      code: "not_found",
      message: `Unhandled relayfile route ${requestRecord.method} ${requestRecord.pathname}`,
      correlationId: "relayfile_mock_not_found",
    });
  });

  return {
    server,
    state,
    baseUrl: "",
    seedFile(targetPath, content, contentType = "text/markdown") {
      state.revisionCounter += 1;
      files.set(normalizePath(targetPath), {
        path: normalizePath(targetPath),
        content,
        contentType,
        revision: `rev_${state.revisionCounter}`,
      });
    },
    async close() {
      await closeServer(server);
    },
  };
}

export function createMockRelaycastServer() {
  const state = {
    requests: [],
    agents: new Map(),
    messages: [],
    nextMessageId: 0,
  };

  const server = createServer(async (request, response) => {
    const requestUrl = new URL(request.url ?? "/", "http://127.0.0.1");
    const requestBody = await readRequestBody(request);
    const requestRecord = {
      method: request.method ?? "GET",
      pathname: requestUrl.pathname,
      searchParams: Object.fromEntries(requestUrl.searchParams.entries()),
      headers: lowerCaseHeaders(request.headers),
      body: requestBody,
    };
    state.requests.push(requestRecord);

    const authHeader = String(requestRecord.headers.authorization ?? "");
    if (authHeader !== "Bearer rc_mock_key") {
      sendJson(response, 401, {
        code: "invalid_api_key",
        message: "Expected Bearer rc_mock_key",
        correlationId: "relaycast_mock_unauthorized",
      });
      return;
    }

    const registerMatch = requestRecord.pathname.match(
      /^\/v1\/workspaces\/([^/]+)\/agents\/register$/
    );
    if (requestRecord.method === "POST" && registerMatch) {
      const workspaceId = decodeURIComponent(registerMatch[1]);
      const agentName = String(requestBody?.agentName ?? "").trim();
      const scopes = Array.isArray(requestBody?.scopes)
        ? requestBody.scopes.filter((scope) => typeof scope === "string")
        : [];
      state.agents.set(agentName, {
        workspaceId,
        agentName,
        scopes,
      });
      sendJson(response, 200, {
        ok: true,
        workspaceId,
        agentName,
        scopes,
      });
      return;
    }

    const messageMatch = requestRecord.pathname.match(
      /^\/v1\/workspaces\/([^/]+)\/messages$/
    );
    if (requestRecord.method === "POST" && messageMatch) {
      const workspaceId = decodeURIComponent(messageMatch[1]);
      state.nextMessageId += 1;
      const message = {
        id: `msg_${state.nextMessageId}`,
        workspaceId,
        from: String(requestBody?.from ?? ""),
        to:
          typeof requestBody?.to === "string" && requestBody.to.trim() !== ""
            ? requestBody.to
            : null,
        type: String(requestBody?.type ?? "message"),
        text: String(requestBody?.text ?? ""),
        path:
          typeof requestBody?.path === "string" && requestBody.path.trim() !== ""
            ? requestBody.path
            : null,
      };
      state.messages.push(message);
      sendJson(response, 201, {
        ok: true,
        message,
      });
      return;
    }

    if (requestRecord.method === "GET" && messageMatch) {
      const workspaceId = decodeURIComponent(messageMatch[1]);
      const agentName = String(requestRecord.searchParams.agentName ?? "");
      const afterId = Number(
        String(requestRecord.searchParams.afterId ?? "0").replace(/^msg_/, "")
      );
      const messages = state.messages.filter((message) => {
        if (message.workspaceId !== workspaceId) {
          return false;
        }
        const numericId = Number(String(message.id).replace(/^msg_/, ""));
        if (numericId <= afterId) {
          return false;
        }
        return (
          message.to === null ||
          message.to === agentName ||
          message.from === agentName
        );
      });
      sendJson(response, 200, {
        messages,
      });
      return;
    }

    sendJson(response, 404, {
      code: "not_found",
      message: `Unhandled relaycast route ${requestRecord.method} ${requestRecord.pathname}`,
      correlationId: "relaycast_mock_not_found",
    });
  });

  return {
    server,
    state,
    baseUrl: "",
    async close() {
      await closeServer(server);
    },
  };
}

export async function listenServer(serverHandle) {
  const server = "server" in serverHandle ? serverHandle.server : serverHandle;
  const baseUrl = await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", reject);
      const address = server.address();
      resolve(`http://127.0.0.1:${address.port}`);
    });
  });
  if ("baseUrl" in serverHandle) {
    serverHandle.baseUrl = baseUrl;
  }
  return baseUrl;
}

export async function closeServer(server) {
  await new Promise((resolve, reject) => {
    server.close((error) => {
      if (error) {
        reject(error);
        return;
      }
      resolve();
    });
  });
}

function normalizePath(input) {
  const value = String(input ?? "").trim();
  if (value === "" || value === "/") {
    return "/";
  }
  const normalized = value.replace(/\\/g, "/").replace(/\/{2,}/g, "/");
  return normalized.startsWith("/") ? normalized : `/${normalized}`;
}

function buildTreeEntries(files, rootPath) {
  const root = normalizePath(rootPath);
  const entries = new Map();

  for (const [filePath, file] of files.entries()) {
    if (root !== "/" && filePath !== root && !filePath.startsWith(`${root}/`)) {
      continue;
    }
    if (root === "/" && filePath === "/") {
      continue;
    }

    const relative = root === "/" ? filePath.slice(1) : filePath.slice(root.length + 1);
    if (relative === "") {
      continue;
    }

    const segments = relative.split("/").filter(Boolean);
    if (segments.length === 1) {
      entries.set(filePath, {
        path: filePath,
        type: "file",
        revision: file.revision,
      });
      continue;
    }

    const childDir = `${root === "/" ? "" : root}/${segments[0]}`.replace(/\/{2,}/g, "/");
    if (!entries.has(childDir)) {
      entries.set(childDir, {
        path: childDir.startsWith("/") ? childDir : `/${childDir}`,
        type: "dir",
        revision: "dir_rev_1",
      });
    }
  }

  return [...entries.values()].sort((left, right) => left.path.localeCompare(right.path));
}
