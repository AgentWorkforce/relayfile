const http = require("node:http");
const crypto = require("node:crypto");

const PORT = 9091;
const KEY_ID = process.env.RELAYAUTH_KEY_ID || "dev-relayauth-key";
const { privateKey, publicKey } = crypto.generateKeyPairSync("rsa", {
  modulusLength: 2048,
});
const jwk = publicKey.export({ format: "jwk" });

function b64url(buf) {
  return Buffer.from(buf)
    .toString("base64")
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

function sign(payload) {
  const header = b64url(JSON.stringify({ alg: "RS256", typ: "JWT", kid: KEY_ID }));
  const body = b64url(JSON.stringify(payload));
  const signer = crypto.createSign("RSA-SHA256");
  signer.update(`${header}.${body}`);
  signer.end();
  const sig = b64url(signer.sign(privateKey));
  return `${header}.${body}.${sig}`;
}

const server = http.createServer((req, res) => {
  if (req.method === "GET" && req.url === "/health") {
    res.writeHead(200, { "Content-Type": "application/json" });
    return res.end('{"status":"ok"}');
  }

  if (req.method === "GET" && req.url === "/.well-known/jwks.json") {
    res.writeHead(200, { "Content-Type": "application/json" });
    return res.end(
      JSON.stringify({
        keys: [{ ...jwk, kid: KEY_ID, alg: "RS256", use: "sig" }],
      })
    );
  }

  if (req.method === "POST" && req.url === "/sign") {
    let data = "";
    req.on("data", (c) => (data += c));
    req.on("end", () => {
      try {
        const { workspace_id, agent_name, scopes } = JSON.parse(data);
        const token = sign({
          workspace_id: workspace_id || "ws_demo",
          agent_name: agent_name || "dev-agent",
          scopes: scopes || [
            "fs:read",
            "fs:write",
            "sync:read",
            "ops:read",
          ],
          exp: 4102444800,
          aud: "relayfile",
        });
        res.writeHead(200, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ token }));
      } catch {
        res.writeHead(400, { "Content-Type": "application/json" });
        res.end('{"error":"invalid json body"}');
      }
    });
    return;
  }

  res.writeHead(404);
  res.end("Not found");
});

server.listen(PORT, () => console.log(`relayauth listening on :${PORT}`));
