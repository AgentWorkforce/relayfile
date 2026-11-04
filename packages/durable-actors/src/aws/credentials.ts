import { readFile } from "node:fs/promises";

/**
 * A small, dependency-free version of the AWS SDK default credential chain,
 * for the actor deployer (no static keys in config). Sources, in order:
 *
 * 1. environment: AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / AWS_SESSION_TOKEN
 * 2. container credentials: EKS Pod Identity and ECS
 *    (AWS_CONTAINER_CREDENTIALS_FULL_URI or _RELATIVE_URI, with
 *    AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE or _TOKEN)
 * 3. web identity: IRSA (AWS_WEB_IDENTITY_TOKEN_FILE + AWS_ROLE_ARN via STS)
 * 4. EC2 instance metadata (IMDSv2)
 *
 * Credentials are cached until five minutes before they expire. Credential
 * requests never follow redirects, so a token cannot leave the checked host.
 */

export interface AwsCredentials {
  accessKeyId: string;
  secretAccessKey: string;
  sessionToken?: string;
  expiration?: Date;
}

export type CredentialProvider = () => Promise<AwsCredentials>;

export interface CredentialChainOptions {
  env?: NodeJS.ProcessEnv;
  fetch?: typeof fetch;
  readFile?: (path: string) => Promise<string>;
  now?: () => Date;
}

const ECS_CONTAINER_HOST = "169.254.170.2";
// EKS Pod Identity agent (IPv4 and IPv6) plus ECS; plain http is only allowed to these or loopback.
const ALLOWED_HTTP_HOSTS = new Set([ECS_CONTAINER_HOST, "169.254.170.23", "[fd00:ec2::23]", "localhost", "127.0.0.1", "[::1]"]);
const IMDS = "http://169.254.169.254";
const REFRESH_MARGIN_MS = 5 * 60_000;

export function defaultCredentialProvider(options: CredentialChainOptions = {}): CredentialProvider {
  const env = options.env ?? process.env;
  const fetchImpl = options.fetch ?? fetch;
  const read = options.readFile ?? ((path: string) => readFile(path, "utf8"));
  const now = options.now ?? (() => new Date());
  let cached: AwsCredentials | undefined;
  let pending: Promise<AwsCredentials> | undefined;

  return async () => {
    if (cached && (!cached.expiration || cached.expiration.getTime() - REFRESH_MARGIN_MS > now().getTime())) return cached;
    // One refresh at a time: concurrent requests share it. The shared promise
    // is cleared only after the result is cached (or the refresh failed, so the
    // next call retries).
    const refresh = (pending ??= resolve());
    try {
      cached = await refresh;
      return cached;
    } finally {
      if (pending === refresh) pending = undefined;
    }
  };

  async function resolve(): Promise<AwsCredentials> {
    const get = (name: string) => env[name]?.trim() || undefined;

    const accessKeyId = get("AWS_ACCESS_KEY_ID");
    const secretAccessKey = get("AWS_SECRET_ACCESS_KEY");
    // A half-configured static pair is a mistake: never fall through to a different identity.
    if (!accessKeyId !== !secretAccessKey) {
      throw new Error("AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY must be set together");
    }
    if (accessKeyId && secretAccessKey) return { accessKeyId, secretAccessKey, sessionToken: get("AWS_SESSION_TOKEN") };

    const fullUri = get("AWS_CONTAINER_CREDENTIALS_FULL_URI");
    const relativeUri = get("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI");
    if (fullUri || relativeUri) {
      const url = new URL(fullUri ?? `http://${ECS_CONTAINER_HOST}${relativeUri}`);
      if (url.protocol !== "https:" && !(url.protocol === "http:" && ALLOWED_HTTP_HOSTS.has(url.hostname))) {
        throw new Error(`AWS container credentials URI host ${url.host} is not allowed over ${url.protocol}`);
      }
      const tokenFile = get("AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE");
      const token = tokenFile ? (await read(tokenFile)).trim() : get("AWS_CONTAINER_AUTHORIZATION_TOKEN");
      const response = await fetchImpl(url, {
        headers: token ? { authorization: token } : {},
        redirect: "error", signal: AbortSignal.timeout(5_000),
      });
      if (!response.ok) throw new Error(`AWS container credentials request failed: HTTP ${response.status}`);
      return fromJson(await response.json(), "container credentials", now());
    }

    const tokenFile = get("AWS_WEB_IDENTITY_TOKEN_FILE");
    const roleArn = get("AWS_ROLE_ARN");
    if (tokenFile && roleArn) {
      const region = get("AWS_REGION") ?? get("AWS_DEFAULT_REGION");
      const url = new URL(region ? `https://sts.${region}.amazonaws.com/` : "https://sts.amazonaws.com/");
      // POST form body: the bearer token stays out of the URL (and its length limits and logs).
      const body = new URLSearchParams({
        Action: "AssumeRoleWithWebIdentity",
        Version: "2011-06-15",
        RoleArn: roleArn,
        RoleSessionName: get("AWS_ROLE_SESSION_NAME") ?? `relayfile-actors-${now().getTime()}`,
        WebIdentityToken: (await read(tokenFile)).trim(),
      });
      const response = await fetchImpl(url, {
        method: "POST",
        headers: { "content-type": "application/x-www-form-urlencoded" },
        body: body.toString(),
        redirect: "error",
        signal: AbortSignal.timeout(10_000),
      });
      if (!response.ok) throw new Error(`STS AssumeRoleWithWebIdentity failed: HTTP ${response.status}`);
      const xml = await response.text();
      const tag = (name: string) => xml.match(new RegExp(`<${name}>([^<]+)</${name}>`))?.[1];
      return fromJson(
        { AccessKeyId: tag("AccessKeyId"), SecretAccessKey: tag("SecretAccessKey"), Token: tag("SessionToken"), Expiration: tag("Expiration") },
        "STS web identity",
        now(),
      );
    }

    if (get("AWS_EC2_METADATA_DISABLED")?.toLowerCase() !== "true") {
      const tokenResponse = await fetchImpl(`${IMDS}/latest/api/token`, {
        method: "PUT",
        headers: { "x-aws-ec2-metadata-token-ttl-seconds": "21600" },
        redirect: "error", signal: AbortSignal.timeout(1_000),
      }).catch(() => null);
      if (tokenResponse?.ok) {
        const headers = { "x-aws-ec2-metadata-token": await tokenResponse.text() };
        const base = `${IMDS}/latest/meta-data/iam/security-credentials/`;
        const role = await fetchImpl(base, { headers, redirect: "error", signal: AbortSignal.timeout(1_000) });
        if (role.ok) {
          const name = (await role.text()).split("\n")[0]!.trim();
          const creds = await fetchImpl(`${base}${encodeURIComponent(name)}`, { headers, redirect: "error", signal: AbortSignal.timeout(1_000) });
          if (creds.ok) return fromJson(await creds.json(), "instance metadata", now());
        }
      }
    }

    throw new Error(
      "no AWS credentials found: expected EKS Pod Identity, IRSA, instance metadata, or AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY",
    );
  }
}

/** Temporary credentials without a usable Expiration are refreshed after this long. */
const DEFAULT_LIFETIME_MS = 15 * 60_000;

function fromJson(body: unknown, source: string, now: Date): AwsCredentials {
  const value = (body ?? {}) as Record<string, unknown>;
  const accessKeyId = typeof value.AccessKeyId === "string" ? value.AccessKeyId : "";
  const secretAccessKey = typeof value.SecretAccessKey === "string" ? value.SecretAccessKey : "";
  if (!accessKeyId || !secretAccessKey) throw new Error(`AWS ${source} response had no access key`);
  const parsed = typeof value.Expiration === "string" ? new Date(value.Expiration) : undefined;
  // Never cache temporary credentials forever: an absent or bad Expiration gets a bounded lifetime.
  const expiration =
    parsed && !Number.isNaN(parsed.getTime()) ? parsed : new Date(now.getTime() + REFRESH_MARGIN_MS + DEFAULT_LIFETIME_MS);
  return {
    accessKeyId,
    secretAccessKey,
    sessionToken: typeof value.Token === "string" && value.Token ? value.Token : undefined,
    expiration,
  };
}
