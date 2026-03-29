import { mkdir, readdir, stat, writeFile } from "node:fs/promises";
import path from "node:path";

export type ProjectKind = "adapter" | "provider";
export type TemplateKind = ProjectKind;

export interface ParsedArgs {
  help: boolean;
  name?: string;
  kind: ProjectKind;
  provider?: string;
  targetDirectory?: string;
}

export interface ScaffoldOptions {
  name: string;
  kind?: ProjectKind;
  template?: TemplateKind;
  provider?: string;
  targetDirectory?: string;
  cwd?: string;
}

export interface ScaffoldResult {
  kind: ProjectKind;
  template: TemplateKind;
  packageName: string;
  provider: string;
  destination: string;
  files: string[];
  nextSteps: string[];
}

const VALID_KINDS: readonly ProjectKind[] = ["adapter", "provider"];
const NODE_VERSION = ">=18";
const SDK_VERSION = "^0.1.3";
const TYPESCRIPT_VERSION = "^5.7.3";
const VITEST_VERSION = "^3.0.0";
const NODE_TYPES_VERSION = "^22.14.1";

function isProjectKind(value: string): value is ProjectKind {
  return VALID_KINDS.includes(value as ProjectKind);
}

function printHelp(): void {
  console.log(`Usage: create-relayfile-adapter --name <package-name> --provider <provider> [options]

Required:
  --name, -n        Package name to generate.
  --provider, -p    Provider slug used in code and docs.

Options:
  --kind, -k        Project kind: adapter | provider. Defaults to "adapter".
  --target-dir, -d  Output directory. Defaults to the package name.
  --help, -h        Show this help message.

Examples:
  create-relayfile-adapter --name @acme/relayfile-github-adapter --provider github
  create-relayfile-adapter --name relayfile-linear-provider --provider linear --kind provider --target-dir ./integrations/linear-provider
`);
}

function readOptionValue(
  argument: string,
  argv: readonly string[],
  index: number
): { value: string; nextIndex: number } {
  const equalsIndex = argument.indexOf("=");
  if (equalsIndex >= 0) {
    return {
      value: argument.slice(equalsIndex + 1),
      nextIndex: index
    };
  }

  const value = argv[index + 1];
  if (!value || value.startsWith("-")) {
    throw new Error(`Expected a value after "${argument}".`);
  }

  return {
    value,
    nextIndex: index + 1
  };
}

export function parseArgs(argv: readonly string[]): ParsedArgs {
  let help = false;
  let kind: ProjectKind = "adapter";
  let name: string | undefined;
  let provider: string | undefined;
  let targetDirectory: string | undefined;
  const positionals: string[] = [];

  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];

    if (argument === "--help" || argument === "-h") {
      help = true;
      continue;
    }

    if (
      argument === "--name" ||
      argument === "-n" ||
      argument.startsWith("--name=")
    ) {
      const option = readOptionValue(argument, argv, index);
      name = option.value;
      index = option.nextIndex;
      continue;
    }

    if (
      argument === "--provider" ||
      argument === "-p" ||
      argument.startsWith("--provider=")
    ) {
      const option = readOptionValue(argument, argv, index);
      provider = option.value;
      index = option.nextIndex;
      continue;
    }

    if (
      argument === "--kind" ||
      argument === "-k" ||
      argument.startsWith("--kind=")
    ) {
      const option = readOptionValue(argument, argv, index);
      if (!isProjectKind(option.value)) {
        throw new Error(`Unsupported kind "${option.value}". Use "adapter" or "provider".`);
      }
      kind = option.value;
      index = option.nextIndex;
      continue;
    }

    if (
      argument === "--target-dir" ||
      argument === "-d" ||
      argument.startsWith("--target-dir=")
    ) {
      const option = readOptionValue(argument, argv, index);
      targetDirectory = option.value;
      index = option.nextIndex;
      continue;
    }

    if (argument.startsWith("-")) {
      throw new Error(`Unknown option "${argument}".`);
    }

    positionals.push(argument);
  }

  if (!name && positionals[0]) {
    name = positionals[0];
  }

  if (!targetDirectory && positionals[1]) {
    targetDirectory = positionals[1];
  }

  return {
    help,
    name,
    kind,
    provider,
    targetDirectory
  };
}

function sanitizePackageName(name: string): string {
  const trimmed = name.trim();
  if (!trimmed) {
    throw new Error("A package name is required.");
  }

  const scopedMatch = trimmed.match(/^@([a-z0-9][a-z0-9._-]*)\/([a-z0-9][a-z0-9._-]*)$/i);
  const unscopedMatch = trimmed.match(/^[a-z0-9][a-z0-9._-]*$/i);

  if (!scopedMatch && !unscopedMatch) {
    throw new Error(
      `Invalid package name "${trimmed}". Use an npm-compatible package name, optionally scoped.`
    );
  }

  return trimmed;
}

function sanitizeProvider(provider: string): string {
  const normalized = provider
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");

  if (!normalized) {
    throw new Error("A provider slug is required.");
  }

  return normalized;
}

function inferProviderFromPackageName(packageName: string): string {
  const basename = packageDirectoryName(packageName);
  const withoutRelayfilePrefix = basename.replace(/^relayfile-/, "");
  const withoutKindSuffix = withoutRelayfilePrefix.replace(/-(adapter|provider)$/, "");

  return sanitizeProvider(withoutKindSuffix || basename);
}

function toPascalCase(value: string): string {
  const pieces = value.match(/[A-Za-z0-9]+/g) ?? [];
  const pascal = pieces
    .map((piece) => {
      const lower = piece.toLowerCase();
      return lower.charAt(0).toUpperCase() + lower.slice(1);
    })
    .join("");

  return pascal || "Relayfile";
}

function packageDirectoryName(packageName: string): string {
  return packageName.replace(/^@[^/]+\//, "");
}

function formatOutputPath(cwd: string, destination: string): string {
  const relativePath = path.relative(cwd, destination) || ".";
  if (relativePath === "." || (!relativePath.startsWith("..") && !path.isAbsolute(relativePath))) {
    return relativePath;
  }
  return destination;
}

function renderJson(value: unknown): string {
  return `${JSON.stringify(value, null, 2)}\n`;
}

function renderTsconfig(): string {
  return `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "NodeNext",
    "moduleResolution": "NodeNext",
    "outDir": "dist",
    "rootDir": "src",
    "declaration": true,
    "strict": true,
    "skipLibCheck": true,
    "types": ["node"]
  },
  "include": ["src"]
}
`;
}

function renderGitignore(): string {
  return `node_modules
dist
coverage
`;
}

function renderWorkflow(): string {
  return `name: ci

on:
  push:
    branches:
      - main
  pull_request:

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: 20
          cache: npm
      - run: npm ci
      - run: npm test
      - run: npm run build
`;
}

function renderPackageJson(options: {
  packageName: string;
  kind: ProjectKind;
  provider: string;
}): string {
  const { packageName, kind, provider } = options;
  const kindLabel = kind === "adapter" ? "adapter" : "provider";

  return renderJson({
    name: packageName,
    version: "0.1.0",
    description: `Relayfile ${provider} ${kindLabel} package`,
    type: "module",
    main: "dist/index.js",
    types: "dist/index.d.ts",
    exports: {
      ".": {
        types: "./dist/index.d.ts",
        import: "./dist/index.js",
        default: "./dist/index.js"
      },
      "./package.json": "./package.json"
    },
    files: ["dist", "README.md"],
    scripts: {
      build: "tsc -p tsconfig.json",
      test: "vitest run",
      typecheck: "tsc --noEmit -p tsconfig.json"
    },
    dependencies: {
      "@relayfile/sdk": SDK_VERSION
    },
    devDependencies: {
      "@types/node": NODE_TYPES_VERSION,
      typescript: TYPESCRIPT_VERSION,
      vitest: VITEST_VERSION
    },
    license: "MIT",
    keywords: ["relayfile", provider, kindLabel, "integration"],
    engines: {
      node: NODE_VERSION
    }
  });
}

function renderAdapterSource(options: {
  className: string;
  provider: string;
}): string {
  const { className, provider } = options;

  return `import {
  computeCanonicalPath,
  type FileSemantics,
  type IngestResult,
  type NormalizedWebhook,
  IntegrationAdapter
} from "@relayfile/sdk";

export class ${className}Adapter extends IntegrationAdapter {
  readonly name = "${provider}";
  readonly version = "0.1.0";

  async ingestWebhook(
    workspaceId: string,
    event: NormalizedWebhook
  ): Promise<IngestResult> {
    const filePath = this.computePath(event.objectType, event.objectId);
    const semantics = this.computeSemantics(event.objectType, event.objectId, event.payload);

    await this.client.writeFile({
      workspaceId,
      path: filePath,
      baseRevision: "0",
      contentType: "application/json",
      content: renderWebhookContent(event),
      semantics
    });

    return {
      filesWritten: 1,
      filesUpdated: 0,
      filesDeleted: 0,
      paths: [filePath],
      errors: []
    };
  }

  computePath(objectType: string, objectId: string): string {
    return computeCanonicalPath(this.provider.name, objectType, objectId);
  }

  computeSemantics(
    objectType: string,
    objectId: string,
    payload: Record<string, unknown>
  ): FileSemantics {
    return {
      properties: {
        provider: this.provider.name,
        "provider.object_type": objectType,
        "provider.object_id": objectId,
        "provider.payload_keys": Object.keys(payload).join(",")
      }
    };
  }

  supportedEvents(): string[] {
    return ["created", "updated", "deleted"];
  }
}

export function renderWebhookContent(event: NormalizedWebhook): string {
  return JSON.stringify(
    {
      provider: event.provider,
      connectionId: event.connectionId,
      eventType: event.eventType,
      objectType: event.objectType,
      objectId: event.objectId,
      payload: event.payload,
      relations: event.relations ?? [],
      metadata: event.metadata ?? {}
    },
    null,
    2
  );
}
`;
}

function renderProviderSource(options: {
  className: string;
  provider: string;
}): string {
  const { className, provider } = options;

  return `import type {
  ConnectionProvider,
  NormalizedWebhook,
  ProxyRequest,
  ProxyResponse
} from "@relayfile/sdk";

export interface ${className}ProviderOptions {
  apiKey: string;
  baseUrl?: string;
  fetch?: typeof fetch;
}

export class ${className}Provider implements ConnectionProvider {
  readonly name = "${provider}";

  private readonly apiKey: string;
  private readonly baseUrl: string;
  private readonly fetchImpl: typeof fetch;

  constructor(options: ${className}ProviderOptions) {
    this.apiKey = options.apiKey;
    this.baseUrl = options.baseUrl ?? "https://api.${provider}.com";
    this.fetchImpl = options.fetch ?? fetch;
  }

  async proxy(request: ProxyRequest): Promise<ProxyResponse> {
    const url = buildUrl(request.baseUrl || this.baseUrl, request.endpoint, request.query);
    const response = await this.fetchImpl(url, {
      method: request.method,
      headers: {
        authorization: \`Bearer \${this.apiKey}\`,
        "content-type": "application/json",
        ...request.headers
      },
      body: request.body === undefined ? undefined : JSON.stringify(request.body)
    });

    return {
      status: response.status,
      headers: headersToRecord(response.headers),
      data: await parseResponseBody(response)
    };
  }

  async healthCheck(connectionId: string): Promise<boolean> {
    const response = await this.proxy({
      method: "GET",
      baseUrl: this.baseUrl,
      endpoint: "/health",
      connectionId
    });

    return response.status >= 200 && response.status < 400;
  }

  async handleWebhook(rawPayload: unknown): Promise<NormalizedWebhook> {
    const payload = assertRecord(rawPayload, "Webhook payload must be an object.");
    const objectType = readRequiredString(payload, "objectType");
    const objectId = readRequiredString(payload, "objectId");
    const eventType = readRequiredString(payload, "eventType");
    const connectionId = readRequiredString(payload, "connectionId");
    const eventPayload = readOptionalRecord(payload.payload, "payload");
    const relations = readOptionalStringArray(payload.relations, "relations");
    const metadata = readOptionalStringRecord(payload.metadata, "metadata");

    return {
      provider: this.name,
      objectType,
      objectId,
      eventType,
      connectionId,
      payload: eventPayload,
      relations,
      metadata
    };
  }
}

function buildUrl(baseUrl: string, endpoint: string, query?: Record<string, string>): string {
  const url = new URL(endpoint, baseUrl);
  for (const [key, value] of Object.entries(query ?? {})) {
    url.searchParams.set(key, value);
  }
  return url.toString();
}

async function parseResponseBody(response: Response): Promise<unknown> {
  const contentType = response.headers.get("content-type") ?? "";

  if (contentType.includes("application/json")) {
    return response.json();
  }

  const text = await response.text();
  return text.length > 0 ? text : null;
}

function headersToRecord(headers: Headers): Record<string, string> {
  const result: Record<string, string> = {};
  headers.forEach((value, key) => {
    result[key] = value;
  });
  return result;
}

function assertRecord(value: unknown, message: string): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(message);
  }
  return value as Record<string, unknown>;
}

function readRequiredString(record: Record<string, unknown>, key: string): string {
  const value = record[key];
  if (typeof value !== "string" || value.trim().length === 0) {
    throw new Error(\`Webhook payload is missing a valid "\${key}" field.\`);
  }
  return value;
}

function readOptionalRecord(
  value: unknown,
  key: string
): Record<string, unknown> {
  if (value === undefined) {
    return {};
  }
  return assertRecord(value, \`Webhook payload field "\${key}" must be an object.\`);
}

function readOptionalStringArray(
  value: unknown,
  key: string
): string[] | undefined {
  if (value === undefined) {
    return undefined;
  }
  if (!Array.isArray(value) || value.some((item) => typeof item !== "string")) {
    throw new Error(\`Webhook payload field "\${key}" must be a string array.\`);
  }
  return [...value];
}

function readOptionalStringRecord(
  value: unknown,
  key: string
): Record<string, string> | undefined {
  if (value === undefined) {
    return undefined;
  }

  const record = assertRecord(value, \`Webhook payload field "\${key}" must be an object.\`);
  const metadata: Record<string, string> = {};
  for (const [entryKey, entryValue] of Object.entries(record)) {
    if (typeof entryValue !== "string") {
      throw new Error(\`Webhook payload field "\${key}.\${entryKey}" must be a string.\`);
    }
    metadata[entryKey] = entryValue;
  }
  return metadata;
}
`;
}

function renderAdapterTest(options: {
  className: string;
  provider: string;
}): string {
  const { className, provider } = options;

  return `import { describe, expect, it } from "vitest";
import {
  createMockConnectionProvider,
  createMockNormalizedWebhook,
  createMockRelayFileClient
} from "@relayfile/sdk/testing";
import { ${className}Adapter } from "../src/index.js";

describe("${className}Adapter", () => {
  it("computes canonical paths for ${provider} objects", () => {
    const adapter = new ${className}Adapter(
      createMockRelayFileClient() as never,
      createMockConnectionProvider({ name: "${provider}" })
    );

    expect(adapter.computePath("issues", "42")).toBe("/${provider}/issues/42.json");
  });

  it("writes normalized webhook content into Relayfile", async () => {
    const client = createMockRelayFileClient();
    const adapter = new ${className}Adapter(
      client as never,
      createMockConnectionProvider({ name: "${provider}" })
    );
    const event = createMockNormalizedWebhook({
      provider: "${provider}",
      objectType: "issues",
      objectId: "42",
      eventType: "updated"
    });

    const result = await adapter.ingestWebhook("ws_test", event);

    expect(result.paths).toEqual(["/${provider}/issues/42.json"]);
    expect(client.listWrittenPaths("ws_test")).toContain("/${provider}/issues/42.json");
  });
});
`;
}

function renderProviderTest(options: {
  className: string;
  provider: string;
}): string {
  const { className, provider } = options;

  return `import { describe, expect, it, vi } from "vitest";
import { ${className}Provider } from "../src/index.js";

describe("${className}Provider", () => {
  it("forwards proxy requests with provider auth", async () => {
    const fetchMock = vi.fn(async () => {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: {
          "content-type": "application/json",
          "x-provider": "${provider}"
        }
      });
    });

    const provider = new ${className}Provider({
      apiKey: "test-token",
      baseUrl: "https://api.${provider}.com",
      fetch: fetchMock as typeof fetch
    });

    const response = await provider.proxy({
      method: "GET",
      baseUrl: "https://api.${provider}.com",
      endpoint: "/items",
      connectionId: "conn_test",
      query: { state: "open" }
    });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe("https://api.${provider}.com/items?state=open");
    expect(new Headers(fetchMock.mock.calls[0]?.[1]?.headers).get("authorization")).toBe(
      "Bearer test-token"
    );
    expect(response.data).toEqual({ ok: true });
  });

  it("normalizes webhook payloads", async () => {
    const provider = new ${className}Provider({
      apiKey: "test-token",
      fetch: vi.fn()
    });

    const event = await provider.handleWebhook?.({
      objectType: "issues",
      objectId: "42",
      eventType: "updated",
      connectionId: "conn_test",
      payload: { title: "Example" },
      metadata: { source: "test" }
    });

    expect(event).toMatchObject({
      provider: "${provider}",
      objectType: "issues",
      objectId: "42",
      eventType: "updated",
      connectionId: "conn_test"
    });
  });
});
`;
}

function renderReadme(options: {
  packageName: string;
  kind: ProjectKind;
  provider: string;
  className: string;
}): string {
  const { packageName, kind, provider, className } = options;
  const classSuffix = kind === "adapter" ? "Adapter" : "Provider";
  const buildTarget =
    kind === "adapter"
      ? `${className}Adapter`
      : `${className}Provider`;

  return `# ${packageName}

Starter ${kind} package for the ${provider} integration.

## What You Get

- TypeScript package metadata wired for build, test, and typecheck
- \`src/index.ts\` with a ${kind} skeleton
- Vitest coverage stub in \`test/index.test.ts\`
- GitHub Actions workflow stub in \`.github/workflows/ci.yml\`

## Usage

\`\`\`ts
import { ${buildTarget} } from "${packageName}";
\`\`\`

The generated ${classSuffix.toLowerCase()} is intentionally minimal. Replace the placeholder logic with real ${provider} API calls, webhook normalization, and Relayfile mapping rules.

## Development

\`\`\`bash
npm install
npm test
npm run build
\`\`\`
`;
}

function createFileMap(options: {
  packageName: string;
  kind: ProjectKind;
  provider: string;
}): Record<string, string> {
  const { packageName, kind, provider } = options;
  const className = toPascalCase(provider);

  return {
    "package.json": renderPackageJson({ packageName, kind, provider }),
    "tsconfig.json": renderTsconfig(),
    ".gitignore": renderGitignore(),
    ".github/workflows/ci.yml": renderWorkflow(),
    "README.md": renderReadme({ packageName, kind, provider, className }),
    "src/index.ts":
      kind === "adapter"
        ? renderAdapterSource({ className, provider })
        : renderProviderSource({ className, provider }),
    "test/index.test.ts":
      kind === "adapter"
        ? renderAdapterTest({ className, provider })
        : renderProviderTest({ className, provider })
  };
}

async function ensureDestinationIsWritable(destination: string): Promise<void> {
  try {
    const destinationStats = await stat(destination);

    if (!destinationStats.isDirectory()) {
      throw new Error(`Target path exists and is not a directory: ${destination}`);
    }

    const existingEntries = await readdir(destination);
    if (existingEntries.length > 0) {
      throw new Error(`Target directory already exists and is not empty: ${destination}`);
    }
  } catch (error) {
    const isMissingDirectory =
      error instanceof Error &&
      "code" in error &&
      typeof error.code === "string" &&
      error.code === "ENOENT";

    if (!isMissingDirectory) {
      throw error;
    }
  }
}

async function writeGeneratedFiles(
  destination: string,
  files: Record<string, string>
): Promise<string[]> {
  const writtenFiles: string[] = [];

  for (const [relativePath, content] of Object.entries(files)) {
    const absolutePath = path.join(destination, relativePath);
    await mkdir(path.dirname(absolutePath), { recursive: true });
    await writeFile(absolutePath, content, "utf8");
    writtenFiles.push(absolutePath);
  }

  return writtenFiles;
}

export async function scaffoldProject(options: ScaffoldOptions): Promise<ScaffoldResult> {
  const packageName = sanitizePackageName(options.name);
  const provider = options.provider
    ? sanitizeProvider(options.provider)
    : inferProviderFromPackageName(packageName);
  const kind = options.kind ?? options.template ?? "adapter";
  const cwd = options.cwd ?? process.cwd();
  const destination = path.resolve(
    cwd,
    options.targetDirectory ?? packageDirectoryName(packageName)
  );

  await ensureDestinationIsWritable(destination);
  await mkdir(destination, { recursive: true });

  const files = await writeGeneratedFiles(
    destination,
    createFileMap({
      packageName,
      kind,
      provider
    })
  );

  return {
    kind,
    template: kind,
    packageName,
    provider,
    destination,
    files,
    nextSteps: [
      `cd ${formatOutputPath(cwd, destination)}`,
      "npm install",
      "npm test",
      "npm run build"
    ]
  };
}

export async function run(argv: readonly string[]): Promise<void> {
  const parsed = parseArgs(argv);

  if (parsed.help) {
    printHelp();
    return;
  }

  if (!parsed.name) {
    throw new Error("Missing required --name flag.");
  }

  if (!parsed.provider) {
    throw new Error("Missing required --provider flag.");
  }

  const result = await scaffoldProject({
    name: parsed.name,
    kind: parsed.kind,
    provider: parsed.provider,
    targetDirectory: parsed.targetDirectory
  });

  const relativeDestination = formatOutputPath(process.cwd(), result.destination);

  console.log(`Created ${result.kind} package "${result.packageName}" in ${relativeDestination}`);
  console.log("");
  console.log("Files:");
  for (const file of result.files) {
    console.log(`- ${formatOutputPath(process.cwd(), file)}`);
  }
  console.log("");
  console.log("Next steps:");
  for (const step of result.nextSteps) {
    console.log(`- ${step}`);
  }
}
