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
export class BlobCodec {
  constructor(
    private readonly store: BlobStore | null,
    private readonly prefix: string,
    readonly inlineMaxBytes: number,
  ) {}

  get enabled(): boolean {
    return this.store !== null;
  }

  /** Offload one file-shaped object in place. Returns the decoded size. */
  async offloadFile(file: Record<string, unknown>): Promise<void> {
    if (typeof file.content !== "string") return;
    const encoding = file.encoding === "base64" ? "base64" : "utf-8";
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
  async offloadBytes(bytes: Uint8Array, encoding: "utf-8" | "base64"): Promise<Record<string, unknown>> {
    const sha256 = sha256Hex(bytes);
    if (this.store && bytes.byteLength > this.inlineMaxBytes) {
      await this.store.put(blobKey(this.prefix, sha256), bytes);
      return { content: "", contentBlob: { sha256, size: bytes.byteLength }, contentHash: sha256, contentSize: bytes.byteLength, encoding };
    }
    const content = encoding === "base64" ? Buffer.from(bytes).toString("base64") : Buffer.from(bytes).toString("utf8");
    return { content, contentHash: sha256, contentSize: bytes.byteLength, encoding };
  }

  /** Replace every `contentBlob` ref in a parsed JSON value with its content. */
  async hydrate(value: unknown): Promise<void> {
    const pending: Array<Promise<void>> = [];
    const visit = (node: unknown) => {
      if (Array.isArray(node)) {
        for (const item of node) visit(item);
        return;
      }
      if (!node || typeof node !== "object") return;
      const record = node as Record<string, unknown>;
      const ref = record.contentBlob as ContentBlobRef | undefined;
      if (ref && typeof ref === "object" && typeof ref.sha256 === "string") {
        pending.push(
          (async () => {
            if (!this.store) throw new Error("file content is in object storage but no blob store is configured");
            const bytes = await this.store.get(blobKey(this.prefix, ref.sha256));
            if (!bytes) throw new Error(`blob ${ref.sha256} is missing from object storage`);
            record.content =
              record.encoding === "base64" ? Buffer.from(bytes).toString("base64") : Buffer.from(bytes).toString("utf8");
            delete record.contentBlob;
          })(),
        );
        return;
      }
      for (const child of Object.values(record)) visit(child);
    };
    visit(value);
    await Promise.all(pending);
  }

  async rawBytes(ref: ContentBlobRef): Promise<Uint8Array> {
    if (!this.store) throw new Error("no blob store configured");
    const bytes = await this.store.get(blobKey(this.prefix, ref.sha256));
    if (!bytes) throw new Error(`blob ${ref.sha256} is missing from object storage`);
    return bytes;
  }
}

function decode(content: string, encoding: "utf-8" | "base64"): Uint8Array | null {
  if (encoding === "utf-8") return Buffer.from(content, "utf8");
  if (!/^[A-Za-z0-9+/]*={0,2}$/.test(content.replace(/\s+/g, ""))) return null;
  return Buffer.from(content, "base64");
}
