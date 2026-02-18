package httpapi

import (
	"fmt"
	"net/http"
)

const dashboardHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>RelayFile Control Surface</title>
  <style>
    :root {
      --ink: #102223;
      --paper: #f8f4ea;
      --card: #fffdf9;
      --line: #d7cbb3;
      --accent: #1f9d88;
      --accent-2: #e88a3d;
      --danger: #c2483f;
      --muted: #6f7d7d;
      --shadow: 0 18px 36px rgba(16, 34, 35, 0.16);
    }

    * { box-sizing: border-box; }

    body {
      margin: 0;
      font-family: "Space Grotesk", "Avenir Next", "Segoe UI", sans-serif;
      color: var(--ink);
      background:
        radial-gradient(1200px 500px at -5% -10%, rgba(232, 138, 61, 0.18), transparent 60%),
        radial-gradient(900px 500px at 110% -10%, rgba(31, 157, 136, 0.2), transparent 65%),
        linear-gradient(140deg, #fff9ef 0%, #f1f8f7 45%, #fffdf9 100%);
      min-height: 100vh;
      padding: 20px;
    }

    .shell {
      max-width: 1240px;
      margin: 0 auto;
      display: grid;
      gap: 14px;
      animation: rise 420ms ease-out;
    }

    .bar {
      background: linear-gradient(140deg, #fffefc, #fcf6eb);
      border: 1px solid var(--line);
      border-radius: 18px;
      padding: 16px;
      box-shadow: var(--shadow);
    }

    h1 {
      margin: 0;
      font-size: clamp(1.2rem, 2vw, 1.75rem);
      letter-spacing: 0.02em;
    }

    .sub {
      margin-top: 6px;
      color: var(--muted);
      font-size: 0.9rem;
    }

    .controls {
      display: grid;
      gap: 10px;
      grid-template-columns: 1.2fr 0.7fr 0.7fr 0.6fr 0.6fr;
      margin-top: 12px;
    }

    .controls input {
      width: 100%;
      border-radius: 10px;
      border: 1px solid var(--line);
      background: #ffffff;
      color: var(--ink);
      padding: 10px 12px;
      font-size: 0.92rem;
      outline: none;
    }

    .controls input:focus {
      border-color: var(--accent);
      box-shadow: 0 0 0 3px rgba(31, 157, 136, 0.15);
    }

    button {
      border: 0;
      border-radius: 10px;
      padding: 10px 12px;
      font-family: inherit;
      font-weight: 700;
      letter-spacing: 0.01em;
      cursor: pointer;
      transition: transform 120ms ease, opacity 120ms ease, box-shadow 120ms ease;
    }

    button:hover { transform: translateY(-1px); }
    button:active { transform: translateY(0); }

    .btn-primary {
      background: linear-gradient(125deg, var(--accent), #2ab399);
      color: #ffffff;
      box-shadow: 0 10px 18px rgba(31, 157, 136, 0.22);
    }

    .btn-secondary {
      background: linear-gradient(120deg, #f2ede2, #efe6d7);
      color: var(--ink);
      border: 1px solid var(--line);
    }

    .pulse { animation: pulse 360ms ease; }

    .cards {
      display: grid;
      gap: 10px;
      grid-template-columns: repeat(6, minmax(120px, 1fr));
    }

    .card {
      background: var(--card);
      border: 1px solid var(--line);
      border-radius: 14px;
      padding: 12px;
      min-height: 86px;
      box-shadow: 0 8px 18px rgba(16, 34, 35, 0.08);
      animation: stagger 340ms ease both;
    }

    .card:nth-child(2) { animation-delay: 40ms; }
    .card:nth-child(3) { animation-delay: 80ms; }
    .card:nth-child(4) { animation-delay: 120ms; }
    .card:nth-child(5) { animation-delay: 160ms; }
    .card:nth-child(6) { animation-delay: 200ms; }

    .label {
      text-transform: uppercase;
      letter-spacing: 0.09em;
      font-size: 0.66rem;
      color: var(--muted);
    }

    .value {
      margin-top: 6px;
      font-size: 1.02rem;
      font-weight: 700;
      line-height: 1.2;
      word-break: break-word;
    }

    .grid {
      display: grid;
      gap: 12px;
      grid-template-columns: 1fr 1fr 1.2fr;
    }

    .grid-files {
      grid-template-columns: 1fr 1.6fr;
    }

    .panel {
      background: var(--card);
      border: 1px solid var(--line);
      border-radius: 16px;
      padding: 12px;
      box-shadow: 0 10px 20px rgba(16, 34, 35, 0.08);
      min-height: 280px;
    }

    .panel h2 {
      margin: 0 0 10px;
      font-size: 0.92rem;
      letter-spacing: 0.06em;
      text-transform: uppercase;
    }

    .feed {
      margin: 0;
      padding: 0;
      list-style: none;
      display: grid;
      gap: 8px;
      max-height: 360px;
      overflow: auto;
    }

    .feed li {
      border: 1px solid #e3d9c4;
      border-left: 5px solid var(--accent);
      border-radius: 10px;
      padding: 9px 10px;
      background: #fffcf7;
      font-size: 0.85rem;
      line-height: 1.35;
    }

    .feed li.warning { border-left-color: var(--accent-2); }
    .feed li.critical { border-left-color: var(--danger); }

    .tree {
      margin: 0;
      padding: 0;
      list-style: none;
      display: grid;
      gap: 6px;
      max-height: 380px;
      overflow: auto;
    }

    .tree button {
      width: 100%;
      text-align: left;
      background: #fffcf7;
      border: 1px solid #e3d9c4;
      border-radius: 10px;
      padding: 8px 10px;
      font-size: 0.84rem;
      color: var(--ink);
      box-shadow: none;
      font-weight: 500;
    }

    .tree button.dir {
      border-left: 4px solid #7c9b9a;
    }

    .tree button.file {
      border-left: 4px solid var(--accent);
    }

    .tree button.active {
      background: #eaf8f5;
      border-color: #9fd6ca;
    }

    .tree .meta {
      display: block;
      margin-top: 2px;
      font-size: 0.72rem;
      color: #6f7d7d;
    }

    .panel-note {
      margin: -4px 0 8px;
      color: var(--muted);
      font-size: 0.78rem;
    }

    .preview {
      margin: 0;
      border: 1px solid #e3d9c4;
      border-radius: 10px;
      background: #fffefb;
      padding: 10px;
      font-size: 0.8rem;
      line-height: 1.4;
      min-height: 260px;
      max-height: 380px;
      overflow: auto;
      white-space: pre-wrap;
      word-break: break-word;
    }

    .file-meta {
      margin: 0 0 8px;
      color: var(--muted);
      font-size: 0.76rem;
      line-height: 1.35;
      word-break: break-word;
    }

    table {
      width: 100%;
      border-collapse: collapse;
      font-size: 0.82rem;
    }

    th, td {
      text-align: left;
      border-bottom: 1px solid #ece3d1;
      padding: 7px 6px;
      vertical-align: top;
    }

    th {
      color: #556262;
      text-transform: uppercase;
      font-size: 0.69rem;
      letter-spacing: 0.08em;
    }

    .ok { color: #0f8f53; }
    .warn { color: #b66a21; }
    .err { color: var(--danger); }

    .status-line {
      margin-top: 10px;
      font-size: 0.84rem;
      color: var(--muted);
      display: flex;
      flex-wrap: wrap;
      gap: 10px;
    }

    .mono {
      font-family: "IBM Plex Mono", "SFMono-Regular", Menlo, Consolas, monospace;
    }

    @keyframes rise {
      from { opacity: 0; transform: translateY(8px); }
      to { opacity: 1; transform: translateY(0); }
    }

    @keyframes pulse {
      0% { transform: scale(1); }
      50% { transform: scale(0.99); }
      100% { transform: scale(1); }
    }

    @keyframes stagger {
      from { opacity: 0; transform: translateY(6px); }
      to { opacity: 1; transform: translateY(0); }
    }

    @media (max-width: 1100px) {
      .controls { grid-template-columns: 1fr 1fr; }
      .cards { grid-template-columns: repeat(3, minmax(120px, 1fr)); }
      .grid { grid-template-columns: 1fr; }
    }

    @media (max-width: 640px) {
      body { padding: 12px; }
      .controls { grid-template-columns: 1fr; }
      .cards { grid-template-columns: repeat(2, minmax(120px, 1fr)); }
    }
  </style>
</head>
<body>
  <main class="shell">
    <section class="bar" id="topBar">
      <h1>RelayFile Control Surface</h1>
      <div class="sub">Live view over backends, ingress pressure, sync health, ops, and workspace filesystem tree.</div>
      <div class="controls">
        <input id="token" type="password" placeholder="Bearer token (admin:read + ops:read + fs:read)" autocomplete="off" />
        <input id="workspace" type="text" placeholder="workspace id (for ops + fs panels)" autocomplete="off" />
        <input id="treePath" type="text" placeholder="tree path (default /notion)" autocomplete="off" />
        <button id="refresh" class="btn-primary" type="button">Refresh Now</button>
        <button id="toggle" class="btn-secondary" type="button">Pause Auto</button>
      </div>
      <div class="status-line">
        <span>API: <span class="mono" id="apiBase"></span></span>
        <span>Last: <span id="lastUpdated">never</span></span>
        <span id="statusMessage">idle</span>
      </div>
    </section>

    <section class="cards">
      <article class="card"><div class="label">Profile</div><div id="profile" class="value mono">-</div></article>
      <article class="card"><div class="label">State Backend</div><div id="stateBackend" class="value mono">-</div></article>
      <article class="card"><div class="label">Envelope Queue</div><div id="envelopeQueue" class="value mono">-</div></article>
      <article class="card"><div class="label">Writeback Queue</div><div id="writebackQueue" class="value mono">-</div></article>
      <article class="card"><div class="label">Ingress Alerts</div><div id="ingressAlertCount" class="value">-</div></article>
      <article class="card"><div class="label">Sync Alerts</div><div id="syncAlertCount" class="value">-</div></article>
    </section>

    <section class="grid">
      <article class="panel">
        <h2>Ingress Alerts</h2>
        <ul id="ingressAlerts" class="feed"></ul>
      </article>

      <article class="panel">
        <h2>Sync Alerts</h2>
        <ul id="syncAlerts" class="feed"></ul>
      </article>

      <article class="panel">
        <h2>Recent Ops</h2>
        <table>
          <thead>
            <tr>
              <th>OpID</th>
              <th>Status</th>
              <th>Action</th>
              <th>Path</th>
              <th>Provider</th>
            </tr>
          </thead>
          <tbody id="opsRows"></tbody>
        </table>
      </article>
    </section>

    <section class="grid grid-files">
      <article class="panel">
        <h2>Workspace Tree</h2>
        <div class="panel-note">Root: <span id="treeRoot" class="mono">-</span> | entries: <span id="treeCount">0</span></div>
        <ul id="treeRows" class="tree"></ul>
      </article>

      <article class="panel">
        <h2>File Preview</h2>
        <p id="fileMeta" class="file-meta">Select a file entry to preview content.</p>
        <pre id="filePreview" class="preview mono"></pre>
      </article>
    </section>
  </main>

  <script>
    (function () {
      const store = {
        timer: null,
        intervalMs: 5000,
        paused: false,
        selectedFilePath: "",
        treeEntries: [],
        treeRoot: "/",
      };

      const dom = {
        token: document.getElementById("token"),
        workspace: document.getElementById("workspace"),
        treePath: document.getElementById("treePath"),
        refresh: document.getElementById("refresh"),
        toggle: document.getElementById("toggle"),
        apiBase: document.getElementById("apiBase"),
        lastUpdated: document.getElementById("lastUpdated"),
        statusMessage: document.getElementById("statusMessage"),
        topBar: document.getElementById("topBar"),
        profile: document.getElementById("profile"),
        stateBackend: document.getElementById("stateBackend"),
        envelopeQueue: document.getElementById("envelopeQueue"),
        writebackQueue: document.getElementById("writebackQueue"),
        ingressAlertCount: document.getElementById("ingressAlertCount"),
        syncAlertCount: document.getElementById("syncAlertCount"),
        ingressAlerts: document.getElementById("ingressAlerts"),
        syncAlerts: document.getElementById("syncAlerts"),
        opsRows: document.getElementById("opsRows"),
        treeRows: document.getElementById("treeRows"),
        treeRoot: document.getElementById("treeRoot"),
        treeCount: document.getElementById("treeCount"),
        fileMeta: document.getElementById("fileMeta"),
        filePreview: document.getElementById("filePreview"),
      };

      function getBase() {
        return window.location.origin;
      }

      function getToken() {
        return dom.token.value.trim();
      }

      function getWorkspace() {
        return dom.workspace.value.trim();
      }

      function normalizePath(raw) {
        const trimmed = String(raw || "").trim();
        if (!trimmed) {
          return "/notion";
        }
        return trimmed.startsWith("/") ? trimmed : ("/" + trimmed);
      }

      function getTreePath() {
        return normalizePath(dom.treePath.value);
      }

      function cid(prefix) {
        return prefix + "_" + Date.now() + "_" + Math.random().toString(16).slice(2, 8);
      }

      async function request(path) {
        const token = getToken();
        if (!token) {
          throw new Error("missing token");
        }
        const response = await fetch(getBase() + path, {
          headers: {
            "Authorization": "Bearer " + token,
            "X-Correlation-Id": cid("dash"),
          },
        });
        const text = await response.text();
        let data;
        try {
          data = JSON.parse(text);
        } catch (err) {
          throw new Error("non-json response: " + text.slice(0, 220));
        }
        if (!response.ok) {
          const code = data.code ? String(data.code) : "error";
          const msg = data.message ? String(data.message) : response.statusText;
          throw new Error(response.status + " " + code + ": " + msg);
        }
        return data;
      }

      function formatQueue(name, depth, cap) {
        return String(name || "-") + "\n" + String(depth || 0) + "/" + String(cap || 0);
      }

      function setStatus(text, cls) {
        dom.statusMessage.textContent = text;
        dom.statusMessage.className = cls || "";
      }

      function renderAlerts(target, list) {
        target.innerHTML = "";
        if (!Array.isArray(list) || list.length === 0) {
          const item = document.createElement("li");
          item.textContent = "No alerts";
          target.appendChild(item);
          return;
        }
        list.forEach((alert) => {
          const li = document.createElement("li");
          const severity = String(alert.severity || "warning");
          li.classList.add(severity === "critical" ? "critical" : "warning");
          const scope = alert.workspaceId ? String(alert.workspaceId) : (alert.workspaceID ? String(alert.workspaceID) : "-");
          const kind = String(alert.type || "alert");
          const msg = String(alert.message || "");
          const value = (alert.value !== undefined && alert.value !== null) ? String(alert.value) : "-";
          const threshold = (alert.threshold !== undefined && alert.threshold !== null) ? String(alert.threshold) : "-";
          li.textContent = "[" + severity + "] " + kind + " | ws=" + scope + " | value=" + value + " | threshold=" + threshold + (msg ? " | " + msg : "");
          target.appendChild(li);
        });
      }

      function renderOps(items) {
        dom.opsRows.innerHTML = "";
        if (!Array.isArray(items) || items.length === 0) {
          const tr = document.createElement("tr");
          tr.innerHTML = "<td colspan=\"5\">No operation data</td>";
          dom.opsRows.appendChild(tr);
          return;
        }
        items.slice(0, 30).forEach((op) => {
          const tr = document.createElement("tr");
          const status = String(op.status || "-");
          const statusClass = status === "succeeded" ? "ok" : (status === "dead_lettered" ? "err" : "warn");
          tr.innerHTML =
            "<td class=\"mono\">" + String(op.opId || "-") + "</td>" +
            "<td class=\"" + statusClass + "\">" + status + "</td>" +
            "<td>" + String(op.action || "-") + "</td>" +
            "<td class=\"mono\">" + String(op.path || "-") + "</td>" +
            "<td>" + String(op.provider || "-") + "</td>";
          dom.opsRows.appendChild(tr);
        });
      }

      function clearFilePreview(message) {
        dom.fileMeta.textContent = message || "Select a file entry to preview content.";
        dom.filePreview.textContent = "";
      }

      async function loadFile(path) {
        const workspace = getWorkspace();
        if (!workspace || !path) {
          clearFilePreview("workspace or file path is missing");
          return;
        }
        dom.fileMeta.textContent = "loading " + path + "...";
        dom.filePreview.textContent = "";
        try {
          const file = await request("/v1/workspaces/" + encodeURIComponent(workspace) + "/fs/file?path=" + encodeURIComponent(path));
          store.selectedFilePath = path;
          const provider = file.provider ? String(file.provider) : "-";
          const revision = file.revision ? String(file.revision) : "-";
          const edited = file.lastEditedAt ? String(file.lastEditedAt) : "-";
          dom.fileMeta.textContent = path + " | rev=" + revision + " | provider=" + provider + " | edited=" + edited;
          dom.filePreview.textContent = String(file.content || "");
          renderTree(store.treeEntries);
        } catch (err) {
          clearFilePreview("failed to load file: " + String(err && err.message ? err.message : err));
        }
      }

      function renderTree(entries) {
        dom.treeRows.innerHTML = "";
        if (!Array.isArray(entries) || entries.length === 0) {
          const li = document.createElement("li");
          const btn = document.createElement("button");
          btn.type = "button";
          btn.disabled = true;
          btn.textContent = "No tree entries";
          li.appendChild(btn);
          dom.treeRows.appendChild(li);
          dom.treeCount.textContent = "0";
          return;
        }

        const sorted = entries.slice().sort((a, b) => {
          const typeA = String(a.type || "");
          const typeB = String(b.type || "");
          if (typeA !== typeB) {
            if (typeA === "dir") {
              return -1;
            }
            if (typeB === "dir") {
              return 1;
            }
          }
          return String(a.path || "").localeCompare(String(b.path || ""));
        });

        dom.treeCount.textContent = String(sorted.length);
        sorted.slice(0, 400).forEach((entry) => {
          const path = String(entry.path || "");
          const type = String(entry.type || "file");
          const li = document.createElement("li");
          const btn = document.createElement("button");
          btn.type = "button";
          btn.className = type === "dir" ? "dir" : "file";
          if (path === store.selectedFilePath) {
            btn.classList.add("active");
          }
          const size = entry.size !== undefined && entry.size !== null ? String(entry.size) : "-";
          const pathSpan = document.createElement("span");
          pathSpan.className = "mono";
          pathSpan.textContent = path;
          btn.appendChild(pathSpan);
          const metaSpan = document.createElement("span");
          metaSpan.className = "meta";
          metaSpan.textContent = type + (type === "file" ? (" | size=" + size) : "");
          btn.appendChild(metaSpan);
          if (type === "file") {
            btn.addEventListener("click", function () {
              loadFile(path);
            });
          } else {
            btn.disabled = true;
          }
          li.appendChild(btn);
          dom.treeRows.appendChild(li);
        });
      }

      async function refresh() {
        const workspace = getWorkspace();
        const treePath = getTreePath();
        setStatus("refreshing...", "warn");
        dom.topBar.classList.remove("pulse");
        void dom.topBar.offsetWidth;
        dom.topBar.classList.add("pulse");

        try {
          const [backends, ingress, sync] = await Promise.all([
            request("/v1/admin/backends"),
            request("/v1/admin/ingress?includeWorkspaces=false&includeAlerts=true&maxAlerts=30"),
            request("/v1/admin/sync?includeWorkspaces=false&includeAlerts=true&maxAlerts=30"),
          ]);

          let ops = { items: [] };
          let tree = { path: treePath, entries: [] };
          const partialErrors = [];
          if (workspace) {
            const [opsResult, treeResult] = await Promise.allSettled([
              request("/v1/workspaces/" + encodeURIComponent(workspace) + "/ops?limit=30"),
              request("/v1/workspaces/" + encodeURIComponent(workspace) + "/fs/tree?path=" + encodeURIComponent(treePath) + "&depth=3"),
            ]);
            if (opsResult.status === "fulfilled") {
              ops = opsResult.value;
            } else {
              partialErrors.push("ops: " + String(opsResult.reason && opsResult.reason.message ? opsResult.reason.message : opsResult.reason));
            }
            if (treeResult.status === "fulfilled") {
              tree = treeResult.value;
            } else {
              partialErrors.push("tree: " + String(treeResult.reason && treeResult.reason.message ? treeResult.reason.message : treeResult.reason));
            }
          } else {
            store.treeEntries = [];
            store.treeRoot = treePath;
            renderTree([]);
            dom.treeRoot.textContent = treePath;
            clearFilePreview("enter workspace id to load filesystem tree");
          }

          dom.profile.textContent = String(backends.backendProfile || "custom");
          dom.stateBackend.textContent = String(backends.stateBackend || "none");
          dom.envelopeQueue.textContent = formatQueue(backends.envelopeQueue, backends.envelopeQueueDepth, backends.envelopeQueueCapacity);
          dom.writebackQueue.textContent = formatQueue(backends.writebackQueue, backends.writebackQueueDepth, backends.writebackQueueCapacity);

          const ingressTotal = ingress.alertTotals && ingress.alertTotals.total ? ingress.alertTotals.total : 0;
          const syncTotal = sync.alertTotals && sync.alertTotals.total ? sync.alertTotals.total : 0;
          dom.ingressAlertCount.textContent = String(ingressTotal);
          dom.syncAlertCount.textContent = String(syncTotal);

          renderAlerts(dom.ingressAlerts, ingress.alerts || []);
          renderAlerts(dom.syncAlerts, sync.alerts || []);
          renderOps(ops.items || []);
          store.treeEntries = Array.isArray(tree.entries) ? tree.entries : [];
          store.treeRoot = String(tree.path || treePath || "/");
          dom.treeRoot.textContent = store.treeRoot;
          renderTree(store.treeEntries);

          const filePaths = store.treeEntries.filter((entry) => String(entry.type || "") === "file").map((entry) => String(entry.path || ""));
          if (filePaths.length === 0) {
            clearFilePreview("no files under " + store.treeRoot);
            store.selectedFilePath = "";
          } else if (!store.selectedFilePath || filePaths.indexOf(store.selectedFilePath) === -1) {
            await loadFile(filePaths[0]);
          }

          dom.lastUpdated.textContent = new Date().toLocaleTimeString();
          if (partialErrors.length > 0) {
            setStatus("partial: " + partialErrors[0], "warn");
          } else {
            setStatus("ok", "ok");
          }
          window.localStorage.setItem("relayfile_dashboard_token", getToken());
          window.localStorage.setItem("relayfile_dashboard_workspace", workspace);
          window.localStorage.setItem("relayfile_dashboard_tree_path", treePath);
        } catch (err) {
          setStatus(String(err && err.message ? err.message : err), "err");
        }
      }

      function ensureTimer() {
        if (store.timer) {
          clearInterval(store.timer);
          store.timer = null;
        }
        if (!store.paused) {
          store.timer = setInterval(refresh, store.intervalMs);
        }
      }

      dom.refresh.addEventListener("click", refresh);
      dom.toggle.addEventListener("click", function () {
        store.paused = !store.paused;
        dom.toggle.textContent = store.paused ? "Resume Auto" : "Pause Auto";
        ensureTimer();
      });
      dom.token.addEventListener("change", refresh);
      dom.workspace.addEventListener("change", refresh);
      dom.treePath.addEventListener("change", refresh);

      const savedToken = window.localStorage.getItem("relayfile_dashboard_token") || "";
      const savedWorkspace = window.localStorage.getItem("relayfile_dashboard_workspace") || "ws_live";
      const savedTreePath = window.localStorage.getItem("relayfile_dashboard_tree_path") || "/notion";
      dom.token.value = savedToken;
      dom.workspace.value = savedWorkspace;
      dom.treePath.value = savedTreePath;
      dom.apiBase.textContent = getBase();

      ensureTimer();
      if (savedToken) {
        refresh();
      } else {
        setStatus("enter token to start", "warn");
      }
    })();
  </script>
</body>
</html>`

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusNotFound, "not_found", "route not found", getCorrelationID(r))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprint(w, dashboardHTML)
}
