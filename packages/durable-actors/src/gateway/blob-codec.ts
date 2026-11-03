import { decodeContent } from "../runtime/storage.js";
import type { ContentBlobRef } from "../runtime/types.js";
import { blobKey, sha256Hex, type BlobStore } from "./blobs.js";

/**
 * Moves file bytes between client JSON and object storage.
 *
 * Outbound (client -> actor): a file whose decoded size exceeds the inline
 * threshold is uploaded to the blob store under its sha256 and its `content`
 * is replaced by `contentBlob: {sha256, size}`; smaller files stay inline and
 * get `contentHash` precomputed so the actor never hashes large strings.
 *
 * Inbound (actor -> client): every object carrying `contentBlob` is hydrated
 * back to `content` in its declared encoding.
 */
const HYDRATE_MAX_BYTES = 256 * 1024 * 1024;
const HYDRATE_CONCURRENCY = 8;

export class HydrateTooLarge extends Error {
  constructor(readonly bytes: number) {
    super(`response would hydrate ${bytes} bytes of file content`);
  }
}

export class BlobCodec {
  constructor(
    private readonly store: BlobStore | null,
    private readonly prefix: string,
    readonly inlineMaxBytes: number,
  ) {}

  get enabled(): boolean {
    return this.store !== null;
  }

  /**
   * Offload one file-shaped object in place.
   *
   * Byte references are minted here only: a client-supplied `contentBlob`
   * (or `contentHash`/`contentSize`) is dropped first, so a client can never
   * point a file at an object it did not upload (blob keys are shared across
   * workspaces) or record a hash that does not describe its bytes.
   */
  async offloadFile(file: Record<string, unknown>): Promise<void> {
    delete file.contentBlob;
    delete file.contentHash;
    delete file.contentSize;
    if (typeof file.content !== "string") return;
    // Same normalization as the actor (trim + lower-case), so both sides agree on the bytes.
    const encoding = normalizeEncoding(file.encoding) === "base64" ? "base64" : "utf-8";
    const bytes = decode(file.content, encoding);
    if (bytes === null) return; // let the actor reject invalid base64 with the canonical error
    const sha256 = sha256Hex(bytes);
    file.contentHash = sha256;
    file.contentSize = bytes.byteLength;
    if (!this.store || bytes.byteLength <= this.inlineMaxBytes) return;
    await this.store.put(blobKey(this.prefix, sha256), bytes);
    const ref: ContentBlobRef = { sha256, size: bytes.byteLength };
    file.contentBlob = ref;
    file.content = "";
  }

  /** Offload raw bytes (streaming PUT). */
  async offloadBytes(bytes: Uint8Array, requested: "utf-8" | "base64"): Promise<Record<string, unknown>> {
    // Bytes that are not valid UTF-8 cannot round-trip through a utf-8 string: store them as base64.
    const encoding = requested === "utf-8" && !isValidUtf8(bytes) ? "base64" : requested;
    const sha256 = sha256Hex(bytes);
    if (this.store && bytes.byteLength > this.inlineMaxBytes) {
      await this.store.put(blobKey(this.prefix, sha256), bytes);
      return { content: "", contentBlob: { sha256, size: bytes.byteLength }, contentHash: sha256, contentSize: bytes.byteLength, encoding };
    }
    const content = encoding === "base64" ? Buffer.from(bytes).toString("base64") : Buffer.from(bytes).toString("utf8");
    return { content, contentHash: sha256, contentSize: bytes.byteLength, encoding };
  }

  /**
   * Replace every `contentBlob` ref in a parsed JSON value with its content.
   * Bounded: at most HYDRATE_MAX_BYTES in one response (else HydrateTooLarge)
   * and HYDRATE_CONCURRENCY object-store GETs at a time.
   */
  async hydrate(value: unknown): Promise<void> {
    const pending: Array<() => Promise<void>> = [];
    let total = 0;
    const visit = (node: unknown) => {
      if (Array.isArray(node)) {
        for (const item of node) visit(item);
        return;
      }
      if (!node || typeof node !== "object") return;
      const record = node as Record<string, unknown>;
      const ref = record.contentBlob as ContentBlobRef | undefined;
      if (ref && typeof ref === "object" && typeof ref.sha256 === "string") {
        total += Number.isSafeInteger(ref.size) && ref.size > 0 ? ref.size : 0;
        pending.push(
          (async () => {
            if (!this.store) throw new Error("file content is in object storage but no blob store is configured");
            const bytes = await this.store.get(blobKey(this.prefix, ref.sha256));
            if (!bytes) throw new Error(`blob ${ref.sha256} is missing from object storage`);
            record.content =
              record.encoding === "base64" ? Buffer.from(bytes).toString("base64") : Buffer.from(bytes).toString("utf8");
            delete record.contentBlob;
          }),
        );
        return;
      }
      for (const child of Object.values(record)) visit(child);
    };
    visit(value);
    if (total > HYDRATE_MAX_BYTES) throw new HydrateTooLarge(total);
    let next = 0;
    const worker = async () => {
      while (next < pending.length) await pending[next++]!();
    };
    await Promise.all(Array.from({ length: Math.min(HYDRATE_CONCURRENCY, pending.length) }, worker));
  }

  async rawBytes(ref: ContentBlobRef): Promise<Uint8Array> {
    if (!this.store) throw new Error("no blob store configured");
    const bytes = await this.store.get(blobKey(this.prefix, ref.sha256));
    if (!bytes) throw new Error(`blob ${ref.sha256} is missing from object storage`);
    return bytes;
  }
}

export function normalizeEncoding(value: unknown): string {
  return typeof value === "string" ? value.trim().toLowerCase() : "";
}

const strictUtf8 = new TextDecoder("utf-8", { fatal: true });

function isValidUtf8(bytes: Uint8Array): boolean {
  try {
    strictUtf8.decode(bytes);
    return true;
  } catch {
    return false;
  }
}

/** Same validation as the actor (decodeContent), so small and large files get the same verdict. */
function decode(content: string, encoding: "utf-8" | "base64"): Uint8Array | null {
  return decodeContent(content, encoding);
}
