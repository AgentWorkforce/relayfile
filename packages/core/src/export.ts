/**
 * Workspace export logic.
 *
 * Extract from workspace.ts:
 *   - JSON export (file list with metadata)
 *   - tar export (gzipped archive)
 *   - patch export
 */

import type { StorageAdapter, FileRow } from "./storage.js";
import type { TokenClaims } from "./acl.js";
import { filePermissionAllows, resolveFilePermissions } from "./acl.js";

export type ExportFormat = "json" | "tar" | "patch";

export function exportWorkspaceJson(
  storage: StorageAdapter,
  claims: TokenClaims | null,
): FileRow[] {
  const workspaceId = storage.getWorkspaceId();

  return storage
    .listFiles()
    .slice()
    .sort((left, right) => left.path.localeCompare(right.path))
    .filter((row) =>
      filePermissionAllows(
        resolveFilePermissions(storage, row.path, true),
        workspaceId,
        claims,
      ),
    )
    .map((row) => materializeFile(storage, row));
}

export function exportWorkspacePatch(
  storage: StorageAdapter,
  claims: TokenClaims | null,
): string {
  return buildUnifiedPatch(exportWorkspaceJson(storage, claims));
}

export async function exportWorkspaceTarGzip(
  storage: StorageAdapter,
  claims: TokenClaims | null,
): Promise<ArrayBuffer> {
  return buildTarGzip(exportWorkspaceJson(storage, claims));
}

export function buildUnifiedPatch(files: FileRow[]): string {
  if (files.length === 0) {
    return "";
  }

  return files
    .map((file) => {
      const lines = file.content.split("\n");
      return [
        `--- ${file.path}`,
        `+++ ${file.path}`,
        "@@",
        ...lines.map((line) => `+${line}`),
      ].join("\n");
    })
    .join("\n");
}

export async function buildTarGzip(files: FileRow[]): Promise<ArrayBuffer> {
  const chunks: Uint8Array[] = [];
  for (const file of files) {
    const content =
      file.encoding === "base64"
        ? Uint8Array.from(atob(file.content), (char) => char.charCodeAt(0))
        : new TextEncoder().encode(file.content);
    const header = buildTarHeader(
      file.path.replace(/^\/+/, ""),
      content.byteLength,
      file.lastEditedAt,
    );
    chunks.push(header);
    chunks.push(content);

    const remainder = content.byteLength % 512;
    if (remainder > 0) {
      chunks.push(new Uint8Array(512 - remainder));
    }
  }
  chunks.push(new Uint8Array(1024));

  const tar = concatBytes(chunks);
  const sourceBody = new Response(toArrayBuffer(tar)).body;
  if (!sourceBody) {
    throw new Error("failed to create readable stream for tar archive");
  }
  const compressed = new Response(
    sourceBody.pipeThrough(new CompressionStream("gzip")),
  );
  return compressed.arrayBuffer();
}

function cloneSemantics(file: FileRow): FileRow["semantics"] {
  return {
    properties: file.semantics.properties
      ? { ...file.semantics.properties }
      : undefined,
    relations: file.semantics.relations
      ? [...file.semantics.relations]
      : undefined,
    permissions: file.semantics.permissions
      ? [...file.semantics.permissions]
      : undefined,
    comments: file.semantics.comments ? [...file.semantics.comments] : undefined,
  };
}

function materializeFile(storage: StorageAdapter, file: FileRow): FileRow {
  const loaded = storage.loadFileContent?.(file);
  const contentState =
    typeof loaded === "string"
      ? { content: loaded, encoding: file.encoding }
      : loaded ?? { content: file.content, encoding: file.encoding };

  return {
    ...file,
    content: contentState.content,
    encoding: contentState.encoding ?? file.encoding,
    semantics: cloneSemantics(file),
  };
}

function buildTarHeader(
  name: string,
  size: number,
  updatedAt?: string,
): Uint8Array {
  const header = new Uint8Array(512);
  writeTarString(header, 0, 100, name.slice(0, 100));
  writeTarOctal(header, 100, 8, 0o644);
  writeTarOctal(header, 108, 8, 0);
  writeTarOctal(header, 116, 8, 0);
  writeTarOctal(header, 124, 12, size);
  writeTarOctal(
    header,
    136,
    12,
    Math.floor((updatedAt ? Date.parse(updatedAt) : Date.now()) / 1000),
  );

  for (let index = 148; index < 156; index += 1) {
    header[index] = 0x20;
  }
  header[156] = "0".charCodeAt(0);
  writeTarString(header, 257, 6, "ustar");
  writeTarString(header, 263, 2, "00");

  const checksum = header.reduce((sum, byte) => sum + byte, 0);
  writeTarChecksum(header, checksum);
  return header;
}

function writeTarString(
  buffer: Uint8Array,
  offset: number,
  length: number,
  value: string,
): void {
  const bytes = new TextEncoder().encode(value);
  buffer.set(bytes.slice(0, length), offset);
}

function writeTarOctal(
  buffer: Uint8Array,
  offset: number,
  length: number,
  value: number,
): void {
  const octal = value.toString(8).padStart(length - 1, "0");
  writeTarString(buffer, offset, length - 1, octal.slice(-length + 1));
  buffer[offset + length - 1] = 0;
}

function writeTarChecksum(buffer: Uint8Array, checksum: number): void {
  const octal = checksum.toString(8).padStart(6, "0");
  writeTarString(buffer, 148, 6, octal);
  buffer[154] = 0;
  buffer[155] = 0x20;
}

function concatBytes(chunks: Uint8Array[]): Uint8Array {
  const total = chunks.reduce((sum, chunk) => sum + chunk.byteLength, 0);
  const out = new Uint8Array(total);
  let offset = 0;

  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }

  return out;
}

function toArrayBuffer(bytes: Uint8Array): ArrayBuffer {
  const copy = new Uint8Array(bytes.byteLength);
  copy.set(bytes);
  return copy.buffer;
}
