interface Env {
  FILE_OBSERVER_ORIGIN?: string;
}

const DEFAULT_FILE_OBSERVER_ORIGIN = "https://relayfile-file-observer.pages.dev";

function resolveOrigin(env: Env): URL {
  const raw = env.FILE_OBSERVER_ORIGIN?.trim() || DEFAULT_FILE_OBSERVER_ORIGIN;
  const url = new URL(raw);
  if (url.protocol !== "https:") {
    throw new Error("FILE_OBSERVER_ORIGIN must use https://");
  }
  return url;
}

export default {
  async fetch(request: Request, env: Env): Promise<Response> {
    const incoming = new URL(request.url);
    const origin = resolveOrigin(env);
    const upstream = new URL(origin.toString());
    upstream.pathname = incoming.pathname;
    upstream.search = incoming.search;

    const headers = new Headers(request.headers);
    headers.set("x-relayfile-observer-host", incoming.host);
    headers.set("x-forwarded-host", incoming.host);
    headers.set("x-forwarded-proto", incoming.protocol.replace(":", ""));
    headers.delete("host");

    const forwardedFor = request.headers.get("cf-connecting-ip");
    if (forwardedFor) {
      headers.set("x-forwarded-for", forwardedFor);
    }

    const upstreamRequest = new Request(upstream.toString(), {
      method: request.method,
      headers,
      body: request.body,
      redirect: "manual",
    });

    return fetch(upstreamRequest);
  },
};
