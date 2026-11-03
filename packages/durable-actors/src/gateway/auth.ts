import { createHmac, createPublicKey, timingSafeEqual, verify as verifySignature, type KeyObject } from "node:crypto";
import { parseScopes } from "../runtime/scopes.js";

/**
 * Bearer verification, ported from internal/httpapi/auth.go:
 * - RS256 tokens verified against relayauth's JWKS (RELAYAUTH_JWKS_URL), with
 *   relayauth's `relay_pa_` / `relay_ws_` / `relay_id_` / `relay_ag_` prefixes stripped.
 * - Optionally HS256 tokens signed with RELAYFILE_JWT_SECRET (self-host
 *   installs without relayauth, local tests).
 * Claims: wks|workspace_id, sub|agent_name, exp, aud must include "relayfile",
 * non-empty scopes.
 */
export interface Principal {
  workspaceId: string;
  agentName: string;
  scopes: string[];
  exp: number;
}

export class AuthError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

const RELAYAUTH_PREFIXES = ["relay_pa_", "relay_ws_", "relay_id_", "relay_ag_"];
const JWKS_TTL_MS = 5 * 60_000;
const JWKS_STALE_GRACE_MS = 60_000;

export interface VerifierOptions {
  jwksUrl?: string;
  hs256Secret?: string;
  fetch?: typeof fetch;
  now?: () => number;
}

export class BearerVerifier {
  private jwks: { keys: Map<string, KeyObject>; fetchedAt: number } | undefined;
  private readonly fetchImpl: typeof fetch;
  private readonly now: () => number;
  constructor(private readonly options: VerifierOptions) {
    if (!options.jwksUrl && !options.hs256Secret) {
      throw new Error("RELAYAUTH_JWKS_URL or RELAYFILE_JWT_SECRET is required");
    }
    this.fetchImpl = options.fetch ?? fetch;
    this.now = options.now ?? Date.now;
  }

  async verify(authorization: string | undefined | null): Promise<Principal> {
    if (!authorization || !authorization.startsWith("Bearer ")) {
      throw new AuthError(401, "unauthorized", "missing or invalid bearer token");
    }
    return this.verifyToken(authorization.slice(7).trim());
  }

  async verifyToken(rawToken: string): Promise<Principal> {
    let raw = rawToken;
    for (const prefix of RELAYAUTH_PREFIXES) {
      if (raw.startsWith(prefix)) {
        raw = raw.slice(prefix.length);
        break;
      }
    }
    const parts = raw.split(".");
    if (parts.length !== 3) throw new AuthError(401, "unauthorized", "invalid jwt format");
    const [h, p, s] = parts as [string, string, string];
    let header: { alg?: string; kid?: string };
    let payload: Record<string, unknown>;
    try {
      header = JSON.parse(Buffer.from(h, "base64url").toString("utf8"));
    } catch {
      throw new AuthError(401, "unauthorized", "invalid jwt header");
    }
    try {
      payload = JSON.parse(Buffer.from(p, "base64url").toString("utf8"));
    } catch {
      throw new AuthError(401, "unauthorized", "invalid jwt payload");
    }
    const signature = Buffer.from(s, "base64url");
    const signingInput = Buffer.from(`${h}.${p}`);
    if (header.alg === "RS256" && this.options.jwksUrl) {
      const key = await this.rsaKey(header.kid ?? "");
      if (!verifySignature("RSA-SHA256", signingInput, key, signature)) {
        throw new AuthError(401, "unauthorized", "jwt signature mismatch");
      }
    } else if (header.alg === "HS256" && this.options.hs256Secret) {
      const expected = createHmac("sha256", this.options.hs256Secret).update(signingInput).digest();
      if (expected.length !== signature.length || !timingSafeEqual(expected, signature)) {
        throw new AuthError(401, "unauthorized", "jwt signature mismatch");
      }
    } else {
      throw new AuthError(401, "unauthorized", "unsupported jwt algorithm");
    }

    const workspaceId = firstString(payload, "wks", "workspace_id");
    if (!workspaceId) throw new AuthError(401, "unauthorized", "missing workspace claim");
    const agentName = firstString(payload, "sub", "agent_name");
    if (!agentName) throw new AuthError(401, "unauthorized", "missing subject claim");
    const exp = payload.exp;
    if (typeof exp !== "number") throw new AuthError(401, "unauthorized", "invalid exp claim");
    if (Math.floor(this.now() / 1000) >= exp) throw new AuthError(401, "unauthorized", "token expired");
    if (!hasAudience(payload.aud, "relayfile")) throw new AuthError(401, "unauthorized", "invalid aud claim");
    const scopes = parseScopes(payload.scopes);
    if (scopes.size === 0) throw new AuthError(403, "forbidden", "no scopes granted");
    return { workspaceId, agentName, scopes: [...scopes], exp };
  }

  private async rsaKey(kid: string): Promise<KeyObject> {
    const now = this.now();
    if (!this.jwks || now - this.jwks.fetchedAt >= JWKS_TTL_MS || !this.jwks.keys.has(kid)) {
      try {
        this.jwks = { keys: await this.fetchJwks(), fetchedAt: now };
      } catch {
        // Serve a slightly stale key set through a brief JWKS outage, like the Go server.
        if (!this.jwks || now - this.jwks.fetchedAt > JWKS_TTL_MS + JWKS_STALE_GRACE_MS) {
          throw new AuthError(503, "unavailable", "jwks unreachable");
        }
      }
    }
    const key = this.jwks?.keys.get(kid);
    if (!key) throw new AuthError(401, "unauthorized", "unknown jwt key id");
    return key;
  }

  private async fetchJwks(): Promise<Map<string, KeyObject>> {
    const response = await this.fetchImpl(this.options.jwksUrl!, { signal: AbortSignal.timeout(5_000) });
    if (!response.ok) throw new Error(`jwks HTTP ${response.status}`);
    const document = (await response.json()) as { keys?: Array<Record<string, unknown>> };
    const keys = new Map<string, KeyObject>();
    for (const jwk of document.keys ?? []) {
      if (jwk.kty !== "RSA" || typeof jwk.n !== "string" || typeof jwk.e !== "string") continue;
      keys.set(String(jwk.kid ?? ""), createPublicKey({ key: { kty: "RSA", n: jwk.n, e: jwk.e }, format: "jwk" }));
    }
    return keys;
  }
}

function firstString(payload: Record<string, unknown>, ...keys: string[]): string {
  for (const key of keys) {
    const value = payload[key];
    if (typeof value === "string" && value.trim() !== "") return value;
  }
  return "";
}

function hasAudience(value: unknown, required: string): boolean {
  if (typeof value === "string") return value.trim() === required;
  if (Array.isArray(value)) return value.some((item) => typeof item === "string" && item.trim() === required);
  return false;
}

/** HS256 token minting for tests and local tooling. */
export function mintHs256(secret: string, claims: Record<string, unknown>): string {
  const header = Buffer.from(JSON.stringify({ alg: "HS256", typ: "JWT" })).toString("base64url");
  const payload = Buffer.from(JSON.stringify(claims)).toString("base64url");
  const signature = createHmac("sha256", secret).update(`${header}.${payload}`).digest("base64url");
  return `${header}.${payload}.${signature}`;
}

/**
 * Internal webhook HMAC, matching verifyInternalHMAC in internal/httpapi/auth.go:
 * X-Relay-Timestamp is RFC3339 within +/- maxSkew, X-Relay-Signature is
 * lowercase hex HMAC-SHA256(secret, timestamp + "\n" + rawBody).
 */
export function verifyInternalHmac(
  secret: string,
  timestamp: string,
  signature: string,
  body: Uint8Array,
  nowMs: number,
  maxSkewMs = 5 * 60_000,
): AuthError | null {
  if (!secret) return new AuthError(503, "unavailable", "internal webhook ingestion is not configured");
  if (!timestamp || !signature) return new AuthError(401, "unauthorized", "missing signature headers");
  const when = Date.parse(timestamp);
  if (!Number.isFinite(when)) return new AuthError(401, "unauthorized", "invalid timestamp");
  if (Math.abs(nowMs - when) > maxSkewMs) return new AuthError(401, "unauthorized", "timestamp outside allowed window");
  const mac = createHmac("sha256", secret).update(timestamp).update("\n").update(body).digest("hex");
  const a = Buffer.from(mac);
  const b = Buffer.from(signature.toLowerCase());
  if (a.length !== b.length || !timingSafeEqual(a, b)) return new AuthError(401, "unauthorized", "invalid signature");
  return null;
}
