import { afterEach, describe, expect, it, vi } from "vitest";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import * as path from "node:path";

import type { RelayFileClient } from "./client.js";
import { IntegrationAdapter, type IngestResult } from "./adapter.js";
import {
  discoverInstalledAdapters,
  loadAdapterModule,
  type InstalledAdapterDescriptor,
  type LoadAdapterModuleResult,
  type LoadAdapterModuleSuccess,
} from "./plugin-loader.js";
import type { ConnectionProvider, NormalizedWebhook } from "./provider.js";
import { AdapterRegistry, registerDiscoveredAdapters } from "./registry.js";
import type { FileSemantics } from "./types.js";

const tempRoots: string[] = [];

const EMPTY_INGEST_RESULT: IngestResult = {
  filesWritten: 0,
  filesUpdated: 0,
  filesDeleted: 0,
  paths: [],
  errors: [],
};

async function createWorkspace(): Promise<string> {
  const root = await mkdtemp(path.join(tmpdir(), "relayfile-plugin-loader-"));
  tempRoots.push(root);
  return root;
}

async function writePackageFixture(
  workspaceRoot: string,
  packageName: string,
  options: {
    manifest?: Record<string, unknown>;
    files?: Record<string, string>;
  } = {},
): Promise<string> {
  const packageDir = path.join(workspaceRoot, "node_modules", ...packageName.split("/"));
  await mkdir(packageDir, { recursive: true });

  const packageJson = {
    name: packageName,
    version: "1.0.0",
    ...options.manifest,
  };

  await writeFile(
    path.join(packageDir, "package.json"),
    `${JSON.stringify(packageJson, null, 2)}\n`,
    "utf8",
  );

  for (const [relativePath, contents] of Object.entries(options.files ?? {})) {
    const filePath = path.join(packageDir, relativePath);
    await mkdir(path.dirname(filePath), { recursive: true });
    await writeFile(filePath, contents, "utf8");
  }

  return packageDir;
}

function expectLoadSuccess(
  result: LoadAdapterModuleResult,
): LoadAdapterModuleSuccess {
  expect(result.ok).toBe(true);

  if (!result.ok) {
    throw new Error(`Expected module load to succeed for ${result.packageName}.`);
  }

  return result;
}

function makeDescriptor(packageName: string): InstalledAdapterDescriptor {
  const adapterName = packageName.replace(/^@relayfile\/adapter-/, "");
  const packageRoot = path.join("/virtual", adapterName);

  return {
    packageName,
    adapterName,
    version: "1.0.0",
    packageRoot,
    packageJsonPath: path.join(packageRoot, "package.json"),
    discoveredFrom: "/virtual",
  };
}

function makeProvider(name: string): ConnectionProvider {
  return {
    name,
    proxy: vi.fn(),
    healthCheck: vi.fn(),
  };
}

class RegistryTestAdapter extends IntegrationAdapter {
  constructor(
    readonly name: string,
    readonly version: string,
  ) {
    super({} as RelayFileClient, makeProvider(name));
  }

  async ingestWebhook(
    _workspaceId: string,
    _event: NormalizedWebhook,
  ): Promise<IngestResult> {
    return EMPTY_INGEST_RESULT;
  }

  computePath(objectType: string, objectId: string): string {
    return `/${this.name}/${objectType}/${objectId}.json`;
  }

  computeSemantics(
    _objectType: string,
    _objectId: string,
    _payload: Record<string, unknown>,
  ): FileSemantics {
    return {};
  }
}

afterEach(async () => {
  vi.restoreAllMocks();
  await Promise.all(tempRoots.splice(0).map((root) => rm(root, { recursive: true, force: true })));
});

describe("plugin loader discovery", () => {
  it("discovers matching adapter packages and ignores non-plugin packages", async () => {
    const workspaceRoot = await createWorkspace();

    await Promise.all([
      writePackageFixture(workspaceRoot, "@relayfile/adapter-github"),
      writePackageFixture(workspaceRoot, "@relayfile/adapter-slack"),
      writePackageFixture(workspaceRoot, "@relayfile/sdk"),
      writePackageFixture(workspaceRoot, "@relayfile/adapter-"),
      writePackageFixture(workspaceRoot, "@acme/adapter-not-relayfile"),
    ]);

    const nestedCwd = path.join(workspaceRoot, "apps", "web");
    await mkdir(nestedCwd, { recursive: true });

    const result = await discoverInstalledAdapters(nestedCwd);

    expect(result.adapters.map((adapter) => adapter.packageName)).toEqual([
      "@relayfile/adapter-github",
      "@relayfile/adapter-slack",
    ]);
    expect(result.adapters.map((adapter) => adapter.adapterName)).toEqual([
      "github",
      "slack",
    ]);
    expect(result.diagnostics).toEqual([]);
  });
});

describe("plugin loader module loading", () => {
  it.each([
    {
      packageName: "@relayfile/adapter-github",
      manifest: {
        exports: "./factory.js",
        version: "1.2.3",
      },
      files: {
        "factory.js": [
          'export const adapterName = "github";',
          'export const adapterVersion = "1.2.3";',
          "export function createAdapter() {",
          '  return { provider: "github" };',
          "}",
          "",
        ].join("\n"),
      },
      expected: {
        adapterName: "github",
        version: "1.2.3",
        exportName: "createAdapter",
        exportKind: "factory",
      },
    },
    {
      packageName: "@relayfile/adapter-stripe",
      manifest: {
        main: "./class.js",
        version: "2.0.0",
      },
      files: {
        "class.js": [
          "export default class StripeAdapter {",
          "  ping() {",
          '    return "pong";',
          "  }",
          "}",
          "",
        ].join("\n"),
      },
      expected: {
        adapterName: "stripe",
        version: "2.0.0",
        exportName: "default",
        exportKind: "class",
      },
    },
    {
      packageName: "@relayfile/adapter-linear",
      manifest: {
        exports: "./nested.js",
        version: "3.0.0",
      },
      files: {
        "nested.js": [
          "function createIntegrationAdapter() {",
          '  return { provider: "linear" };',
          "}",
          "",
          "export default {",
          '  adapterName: "linear",',
          '  version: "3.1.0",',
          "  createIntegrationAdapter,",
          "};",
          "",
        ].join("\n"),
      },
      expected: {
        adapterName: "linear",
        version: "3.1.0",
        exportName: "default.createIntegrationAdapter",
        exportKind: "factory",
      },
    },
  ])(
    "loads supported export shapes from $packageName",
    async ({ packageName, manifest, files, expected }) => {
      const workspaceRoot = await createWorkspace();
      await writePackageFixture(workspaceRoot, packageName, { manifest, files });
      vi.spyOn(process, "cwd").mockReturnValue(workspaceRoot);

      const result = expectLoadSuccess(await loadAdapterModule(packageName));

      expect(result.metadata.adapterName).toBe(expected.adapterName);
      expect(result.metadata.version).toBe(expected.version);
      expect(result.metadata.exportName).toBe(expected.exportName);
      expect(result.metadata.exportKind).toBe(expected.exportKind);
      expect(result.metadata.exportValue).toBeTypeOf("function");
      expect(result.diagnostics).toEqual([]);
    },
  );

  it("returns diagnostics when package entrypoints are missing", async () => {
    const workspaceRoot = await createWorkspace();
    const packageName = "@relayfile/adapter-broken";

    await writePackageFixture(workspaceRoot, packageName, {
      manifest: {
        exports: "./dist/index.js",
        module: "./dist/module.js",
        main: "./dist/main.js",
      },
    });

    vi.spyOn(process, "cwd").mockReturnValue(workspaceRoot);

    const result = await loadAdapterModule(packageName);

    expect(result.ok).toBe(false);
    expect(result.diagnostics).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          severity: "warning",
          code: "entrypoint_missing",
          packageName,
        }),
        expect.objectContaining({
          severity: "error",
          code: "entrypoint_not_exported",
          packageName,
        }),
      ]),
    );
    expect(
      result.diagnostics.filter((diagnostic) => diagnostic.code === "entrypoint_missing").length,
    ).toBeGreaterThan(0);
  });
});

describe("registry bulk registration", () => {
  it("registers discovered adapters, preserves failures, and aggregates diagnostics", async () => {
    const registry = new AdapterRegistry<RegistryTestAdapter>();
    const githubDescriptor = makeDescriptor("@relayfile/adapter-github");
    const brokenDescriptor = makeDescriptor("@relayfile/adapter-broken");

    const discovery = {
      cwd: "/virtual/workspace",
      searchRoots: ["/virtual/workspace"],
      adapters: [githubDescriptor, brokenDescriptor],
      diagnostics: [
        {
          severity: "info" as const,
          code: "shadowed_adapter_package" as const,
          message: "Ignoring a shadowed adapter package.",
          packageName: githubDescriptor.packageName,
        },
      ],
    };

    const githubLoad: LoadAdapterModuleSuccess = {
      ok: true,
      packageName: githubDescriptor.packageName,
      module: {},
      metadata: {
        packageName: githubDescriptor.packageName,
        adapterName: "github",
        version: "1.0.0",
        packageRoot: githubDescriptor.packageRoot,
        packageJsonPath: githubDescriptor.packageJsonPath,
        entrypoint: path.join(githubDescriptor.packageRoot, "index.js"),
        exportName: "createAdapter",
        exportKind: "factory",
        exportValue: () => ({ provider: "github" }),
      },
      diagnostics: [
        {
          severity: "warning",
          code: "ambiguous_adapter_export",
          message: "Selected createAdapter from multiple candidates.",
          packageName: githubDescriptor.packageName,
        },
      ],
    };

    const brokenLoad = {
      ok: false,
      packageName: brokenDescriptor.packageName,
      diagnostics: [
        {
          severity: "error" as const,
          code: "entrypoint_not_exported" as const,
          message: "No importable entrypoint could be resolved.",
          packageName: brokenDescriptor.packageName,
        },
      ],
    };

    const discoverInstalledAdapters = vi.fn().mockResolvedValue(discovery);
    const loadAdapterModule = vi
      .fn()
      .mockImplementation(async (packageName: string) =>
        packageName === githubDescriptor.packageName ? githubLoad : brokenLoad,
      );
    const createAdapter = vi.fn(
      async (
        result: LoadAdapterModuleSuccess,
        context: { registry?: AdapterRegistry<RegistryTestAdapter> },
      ) => {
        expect(context.registry).toBe(registry);
        return new RegistryTestAdapter(
          result.metadata.adapterName,
          result.metadata.version ?? "0.0.0",
        );
      },
    );

    const result = await registerDiscoveredAdapters({
      cwd: "/virtual/workspace",
      registry,
      createAdapter,
      discoverInstalledAdapters,
      loadAdapterModule,
    });

    expect(discoverInstalledAdapters).toHaveBeenCalledWith("/virtual/workspace");
    expect(loadAdapterModule).toHaveBeenNthCalledWith(1, githubDescriptor.packageName);
    expect(loadAdapterModule).toHaveBeenNthCalledWith(2, brokenDescriptor.packageName);
    expect(createAdapter).toHaveBeenCalledTimes(1);
    expect(result.registry).toBe(registry);
    expect(result.loaded).toEqual([githubLoad]);
    expect(result.failures).toEqual([brokenLoad]);
    expect(result.registered).toHaveLength(1);
    expect(result.registered[0]).toBe(registry.getAdapter("github"));
    expect(result.diagnostics.map((diagnostic) => diagnostic.code)).toEqual([
      "shadowed_adapter_package",
      "ambiguous_adapter_export",
      "entrypoint_not_exported",
    ]);
  });
});
