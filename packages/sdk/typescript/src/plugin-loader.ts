type UnknownRecord = Record<string, unknown>;

const RELAYFILE_SCOPE = "@relayfile";
export const ADAPTER_PACKAGE_PREFIX = `${RELAYFILE_SCOPE}/adapter-` as const;

async function dynamicImport(specifier: string): Promise<unknown> {
  return import(specifier);
}

interface FsDirentLike {
  name: string;
  isDirectory(): boolean;
  isSymbolicLink(): boolean;
}

interface FsPromisesLike {
  access(path: string): Promise<void>;
  readFile(path: string, encoding: string): Promise<string>;
  readdir(
    path: string,
    options?: {
      withFileTypes?: boolean;
    }
  ): Promise<FsDirentLike[]>;
  realpath(path: string): Promise<string>;
}

interface PathLike {
  dirname(path: string): string;
  join(...paths: string[]): string;
  resolve(...paths: string[]): string;
}

interface UrlLike {
  pathToFileURL(path: string): {
    href: string;
  };
}

interface PackageJsonShape {
  name?: unknown;
  version?: unknown;
  main?: unknown;
  module?: unknown;
  exports?: unknown;
}

export type AdapterLoaderDiagnosticSeverity = "info" | "warning" | "error";

export type AdapterLoaderDiagnosticCode =
  | "adapter_not_found"
  | "adapter_instantiation_failed"
  | "adapter_registration_failed"
  | "adapter_validation_failed"
  | "ambiguous_adapter_export"
  | "entrypoint_missing"
  | "entrypoint_not_exported"
  | "invalid_adapter_package_name"
  | "invalid_package_json"
  | "manifest_name_mismatch"
  | "manifest_read_failed"
  | "module_import_failed"
  | "shadowed_adapter_package";

export interface AdapterLoaderDiagnostic {
  severity: AdapterLoaderDiagnosticSeverity;
  code: AdapterLoaderDiagnosticCode;
  message: string;
  packageName?: string;
  path?: string;
  detail?: string;
}

export interface InstalledAdapterDescriptor {
  packageName: string;
  adapterName: string;
  version: string | null;
  packageRoot: string;
  packageJsonPath: string;
  discoveredFrom: string;
}

export interface DiscoverInstalledAdaptersResult {
  cwd: string;
  searchRoots: string[];
  adapters: InstalledAdapterDescriptor[];
  diagnostics: AdapterLoaderDiagnostic[];
}

export type AdapterFactory<TAdapter = unknown> = (...args: unknown[]) => TAdapter;
export type AdapterClass<TAdapter = unknown> = abstract new (...args: unknown[]) => TAdapter;
export type AdapterModuleExport<TAdapter = unknown> = AdapterFactory<TAdapter> | AdapterClass<TAdapter>;
export type AdapterModuleExportKind = "factory" | "class";

export interface AdapterModuleNamespace extends UnknownRecord {
  default?: unknown;
}

export interface LoadedAdapterMetadata<TExport extends AdapterModuleExport = AdapterModuleExport> {
  packageName: string;
  adapterName: string;
  version: string | null;
  packageRoot: string;
  packageJsonPath: string;
  entrypoint: string;
  exportName: string;
  exportKind: AdapterModuleExportKind;
  exportValue: TExport;
}

export interface LoadAdapterModuleSuccess<
  TModule extends AdapterModuleNamespace = AdapterModuleNamespace,
  TExport extends AdapterModuleExport = AdapterModuleExport
> {
  ok: true;
  packageName: string;
  module: TModule;
  metadata: LoadedAdapterMetadata<TExport>;
  diagnostics: AdapterLoaderDiagnostic[];
}

export interface LoadAdapterModuleFailure {
  ok: false;
  packageName: string;
  diagnostics: AdapterLoaderDiagnostic[];
}

export type LoadAdapterModuleResult = LoadAdapterModuleSuccess | LoadAdapterModuleFailure;

interface AdapterManifestRecord extends InstalledAdapterDescriptor {
  manifest: PackageJsonShape;
}

interface CandidateExport {
  exportName: string;
  exportKind: AdapterModuleExportKind;
  exportValue: AdapterModuleExport;
  score: number;
}

let fsPromise: Promise<FsPromisesLike> | undefined;
let pathPromise: Promise<PathLike> | undefined;
let urlPromise: Promise<UrlLike> | undefined;

function getFs(): Promise<FsPromisesLike> {
  fsPromise ??= dynamicImport("node:fs/promises") as Promise<FsPromisesLike>;
  return fsPromise;
}

function getPath(): Promise<PathLike> {
  pathPromise ??= dynamicImport("node:path") as Promise<PathLike>;
  return pathPromise;
}

function getUrl(): Promise<UrlLike> {
  urlPromise ??= dynamicImport("node:url") as Promise<UrlLike>;
  return urlPromise;
}

function getProcessCwd(): string {
  const runtimeProcess = globalThis as typeof globalThis & {
    process?: {
      cwd?: () => string;
    };
  };

  return runtimeProcess.process?.cwd?.() ?? ".";
}

function createDiagnostic(
  severity: AdapterLoaderDiagnosticSeverity,
  code: AdapterLoaderDiagnosticCode,
  message: string,
  details: Partial<Omit<AdapterLoaderDiagnostic, "severity" | "code" | "message">> = {}
): AdapterLoaderDiagnostic {
  return {
    severity,
    code,
    message,
    ...details,
  };
}

function isRecord(value: unknown): value is UnknownRecord {
  return typeof value === "object" && value !== null;
}

function isAdapterPackageName(packageName: string): boolean {
  return packageName.startsWith(ADAPTER_PACKAGE_PREFIX) && packageName.length > ADAPTER_PACKAGE_PREFIX.length;
}

function packageNameToAdapterName(packageName: string): string {
  return packageName.slice(ADAPTER_PACKAGE_PREFIX.length);
}

function normalizeErrorDetail(error: unknown): string | undefined {
  if (error instanceof Error) {
    return error.message;
  }
  return typeof error === "string" ? error : undefined;
}

function getErrorCode(error: unknown): string | undefined {
  if (!isRecord(error)) {
    return undefined;
  }

  return typeof error.code === "string" ? error.code : undefined;
}

async function resolveCwd(cwd?: string): Promise<string> {
  const path = await getPath();
  const fs = await getFs();
  const resolved = path.resolve(cwd ?? getProcessCwd());

  try {
    return await fs.realpath(resolved);
  } catch {
    return resolved;
  }
}

function buildSearchRoots(cwd: string, path: PathLike): string[] {
  const roots: string[] = [];
  let current = cwd;

  for (;;) {
    roots.push(current);
    const parent = path.dirname(current);
    if (parent === current) {
      return roots;
    }
    current = parent;
  }
}

async function listAdapterEntries(scopeDirectory: string): Promise<FsDirentLike[] | null> {
  const fs = await getFs();

  try {
    const entries = await fs.readdir(scopeDirectory, { withFileTypes: true });
    return entries
      .filter((entry) => (entry.isDirectory() || entry.isSymbolicLink()) && entry.name.startsWith("adapter-"))
      .sort((left, right) => left.name.localeCompare(right.name));
  } catch (error) {
    if (getErrorCode(error) === "ENOENT") {
      return null;
    }
    throw error;
  }
}

async function readAdapterManifest(
  packageDirectory: string,
  packageName: string,
  discoveredFrom: string
): Promise<{ adapter?: AdapterManifestRecord; diagnostics: AdapterLoaderDiagnostic[] }> {
  const fs = await getFs();
  const path = await getPath();
  const packageJsonPath = path.join(packageDirectory, "package.json");
  const diagnostics: AdapterLoaderDiagnostic[] = [];

  let fileContents: string;
  try {
    fileContents = await fs.readFile(packageJsonPath, "utf8");
  } catch (error) {
    diagnostics.push(
      createDiagnostic(
        "error",
        "manifest_read_failed",
        `Failed to read package manifest for ${packageName}.`,
        {
          detail: normalizeErrorDetail(error),
          packageName,
          path: packageJsonPath,
        }
      )
    );
    return { diagnostics };
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(fileContents);
  } catch (error) {
    diagnostics.push(
      createDiagnostic(
        "error",
        "invalid_package_json",
        `Invalid JSON in package manifest for ${packageName}.`,
        {
          detail: normalizeErrorDetail(error),
          packageName,
          path: packageJsonPath,
        }
      )
    );
    return { diagnostics };
  }

  if (!isRecord(parsed)) {
    diagnostics.push(
      createDiagnostic(
        "error",
        "invalid_package_json",
        `Package manifest for ${packageName} must be a JSON object.`,
        {
          packageName,
          path: packageJsonPath,
        }
      )
    );
    return { diagnostics };
  }

  if (parsed.name !== packageName) {
    diagnostics.push(
      createDiagnostic(
        "error",
        "manifest_name_mismatch",
        `Package manifest name does not match discovered adapter package ${packageName}.`,
        {
          detail: typeof parsed.name === "string" ? `Found ${parsed.name}.` : undefined,
          packageName,
          path: packageJsonPath,
        }
      )
    );
    return { diagnostics };
  }

  let packageRoot = packageDirectory;
  try {
    packageRoot = await fs.realpath(packageDirectory);
  } catch {
    packageRoot = packageDirectory;
  }

  return {
    adapter: {
      packageName,
      adapterName: packageNameToAdapterName(packageName),
      version: typeof parsed.version === "string" ? parsed.version : null,
      packageRoot,
      packageJsonPath,
      discoveredFrom,
      manifest: parsed as PackageJsonShape,
    },
    diagnostics,
  };
}

function resolveExportsTarget(value: unknown): string | null {
  if (typeof value === "string") {
    return value;
  }

  if (!isRecord(value)) {
    return null;
  }

  const priorityKeys = [".", "import", "default", "node", "module", "require"];
  for (const key of priorityKeys) {
    if (key in value) {
      const resolved = resolveExportsTarget(value[key]);
      if (resolved) {
        return resolved;
      }
    }
  }

  for (const nestedValue of Object.values(value)) {
    const resolved = resolveExportsTarget(nestedValue);
    if (resolved) {
      return resolved;
    }
  }

  return null;
}

async function resolveEntrypoint(
  manifestRecord: AdapterManifestRecord
): Promise<{ entrypoint?: string; diagnostics: AdapterLoaderDiagnostic[] }> {
  const fs = await getFs();
  const path = await getPath();
  const diagnostics: AdapterLoaderDiagnostic[] = [];

  const candidates = [
    resolveExportsTarget(manifestRecord.manifest.exports),
    typeof manifestRecord.manifest.module === "string" ? manifestRecord.manifest.module : null,
    typeof manifestRecord.manifest.main === "string" ? manifestRecord.manifest.main : null,
    "./index.js",
  ];

  for (const candidate of candidates) {
    if (!candidate) {
      continue;
    }

    const resolvedPath = path.resolve(manifestRecord.packageRoot, candidate);
    try {
      await fs.access(resolvedPath);
      return { entrypoint: resolvedPath, diagnostics };
    } catch {
      diagnostics.push(
        createDiagnostic(
          "warning",
          "entrypoint_missing",
          `Entrypoint candidate was not found for ${manifestRecord.packageName}.`,
          {
            packageName: manifestRecord.packageName,
            path: resolvedPath,
          }
        )
      );
    }
  }

  diagnostics.push(
    createDiagnostic(
      "error",
      "entrypoint_not_exported",
      `No importable entrypoint could be resolved for ${manifestRecord.packageName}.`,
      {
        packageName: manifestRecord.packageName,
        path: manifestRecord.packageJsonPath,
      }
    )
  );

  return { diagnostics };
}

function getExportFunctionSource(value: unknown): string {
  if (typeof value !== "function") {
    return "";
  }

  try {
    return Function.prototype.toString.call(value);
  } catch {
    return "";
  }
}

function isLikelyClassExport(value: unknown, exportName: string): value is AdapterClass {
  if (typeof value !== "function") {
    return false;
  }

  if (getExportFunctionSource(value).startsWith("class ")) {
    return true;
  }

  if (exportName === "default" || exportName.endsWith("Adapter") || exportName.endsWith("Plugin")) {
    return true;
  }

  const prototype = (value as { prototype?: unknown }).prototype;
  if (!isRecord(prototype)) {
    return false;
  }

  return Object.getOwnPropertyNames(prototype).some((propertyName) => propertyName !== "constructor");
}

function scoreExportCandidate(exportName: string, exportValue: AdapterModuleExport): CandidateExport {
  const explicitFactoryNames = new Set([
    "create",
    "createAdapter",
    "createIntegrationAdapter",
    "adapterFactory",
    "createPlugin",
  ]);
  const explicitClassNames = new Set(["Adapter", "IntegrationAdapter"]);
  const terminalName = exportName.split(".").at(-1) ?? exportName;
  const likelyClass = isLikelyClassExport(exportValue, terminalName);

  if (explicitFactoryNames.has(terminalName)) {
    return { exportName, exportKind: "factory", exportValue, score: 120 };
  }

  if (terminalName.startsWith("create")) {
    return { exportName, exportKind: "factory", exportValue, score: 110 };
  }

  if (likelyClass && explicitClassNames.has(terminalName)) {
    return { exportName, exportKind: "class", exportValue, score: 105 };
  }

  if (likelyClass && terminalName.endsWith("Adapter")) {
    return { exportName, exportKind: "class", exportValue, score: 100 };
  }

  if (likelyClass && terminalName.endsWith("Plugin")) {
    return { exportName, exportKind: "class", exportValue, score: 95 };
  }

  if (likelyClass && terminalName === "default") {
    return { exportName, exportKind: "class", exportValue, score: 90 };
  }

  if (likelyClass) {
    return { exportName, exportKind: "class", exportValue, score: 80 };
  }

  return { exportName, exportKind: "factory", exportValue, score: 70 };
}

function collectExportCandidates(moduleNamespace: AdapterModuleNamespace): CandidateExport[] {
  const candidates: CandidateExport[] = [];

  for (const [exportName, exportValue] of Object.entries(moduleNamespace)) {
    if (typeof exportValue === "function") {
      candidates.push(scoreExportCandidate(exportName, exportValue as AdapterModuleExport));
      continue;
    }

    if (exportName === "default" && isRecord(exportValue)) {
      for (const nestedExportName of [
        "create",
        "createAdapter",
        "createIntegrationAdapter",
        "adapterFactory",
        "createPlugin",
        "Adapter",
      ]) {
        const nestedValue = exportValue[nestedExportName];
        if (typeof nestedValue === "function") {
          candidates.push(
            scoreExportCandidate(`default.${nestedExportName}`, nestedValue as AdapterModuleExport)
          );
        }
      }
    }
  }

  return candidates.sort((left, right) => {
    if (right.score !== left.score) {
      return right.score - left.score;
    }
    return left.exportName.localeCompare(right.exportName);
  });
}

function resolveAdapterName(
  packageName: string,
  moduleNamespace: AdapterModuleNamespace
): { adapterName: string; diagnostics: AdapterLoaderDiagnostic[] } {
  const diagnostics: AdapterLoaderDiagnostic[] = [];
  const fallbackName = packageNameToAdapterName(packageName);
  const candidates = [
    moduleNamespace.adapterName,
    moduleNamespace.ADAPTER_NAME,
    typeof moduleNamespace.default === "object" && moduleNamespace.default !== null
      ? (moduleNamespace.default as UnknownRecord).adapterName
      : undefined,
  ];

  const runtimeName = candidates.find((candidate): candidate is string => typeof candidate === "string" && candidate.trim().length > 0);

  if (runtimeName && runtimeName !== fallbackName) {
    diagnostics.push(
      createDiagnostic(
        "warning",
        "manifest_name_mismatch",
        `Runtime adapter name for ${packageName} differs from the package name suffix.`,
        {
          detail: `Using runtime name ${runtimeName}; package suffix is ${fallbackName}.`,
          packageName,
        }
      )
    );
  }

  return {
    adapterName: runtimeName ?? fallbackName,
    diagnostics,
  };
}

function resolveAdapterVersion(
  manifestRecord: AdapterManifestRecord,
  moduleNamespace: AdapterModuleNamespace
): string | null {
  const runtimeVersionCandidates = [
    moduleNamespace.adapterVersion,
    moduleNamespace.ADAPTER_VERSION,
    moduleNamespace.version,
    typeof moduleNamespace.default === "object" && moduleNamespace.default !== null
      ? (moduleNamespace.default as UnknownRecord).version
      : undefined,
  ];

  const runtimeVersion = runtimeVersionCandidates.find(
    (candidate): candidate is string => typeof candidate === "string" && candidate.trim().length > 0
  );

  return runtimeVersion ?? manifestRecord.version;
}

export async function discoverInstalledAdapters(cwd?: string): Promise<DiscoverInstalledAdaptersResult> {
  const path = await getPath();
  const resolvedCwd = await resolveCwd(cwd);
  const searchRoots = buildSearchRoots(resolvedCwd, path);
  const diagnostics: AdapterLoaderDiagnostic[] = [];
  const adaptersByPackageName = new Map<string, InstalledAdapterDescriptor>();

  for (const searchRoot of searchRoots) {
    const scopeDirectory = path.join(searchRoot, "node_modules", RELAYFILE_SCOPE);
    let entries: FsDirentLike[] | null;

    try {
      entries = await listAdapterEntries(scopeDirectory);
    } catch (error) {
      diagnostics.push(
        createDiagnostic(
          "warning",
          "manifest_read_failed",
          `Failed to inspect ${scopeDirectory} while discovering installed adapters.`,
          {
            detail: normalizeErrorDetail(error),
            path: scopeDirectory,
          }
        )
      );
      continue;
    }

    if (!entries) {
      continue;
    }

    for (const entry of entries) {
      const packageName = `${RELAYFILE_SCOPE}/${entry.name}`;
      const packageDirectory = path.join(scopeDirectory, entry.name);

      if (!isAdapterPackageName(packageName)) {
        continue;
      }

      if (adaptersByPackageName.has(packageName)) {
        diagnostics.push(
          createDiagnostic(
            "info",
            "shadowed_adapter_package",
            `Ignoring shadowed adapter package ${packageName} discovered higher in the filesystem tree.`,
            {
              packageName,
              path: packageDirectory,
            }
          )
        );
        continue;
      }

      const manifestResult = await readAdapterManifest(packageDirectory, packageName, searchRoot);
      diagnostics.push(...manifestResult.diagnostics);

      if (manifestResult.adapter) {
        const { manifest, ...descriptor } = manifestResult.adapter;
        void manifest;
        adaptersByPackageName.set(packageName, descriptor);
      }
    }
  }

  return {
    cwd: resolvedCwd,
    searchRoots,
    adapters: Array.from(adaptersByPackageName.values()).sort((left, right) =>
      left.packageName.localeCompare(right.packageName)
    ),
    diagnostics,
  };
}

export async function loadAdapterModule(packageName: string): Promise<LoadAdapterModuleResult> {
  if (!isAdapterPackageName(packageName)) {
    return {
      ok: false,
      packageName,
      diagnostics: [
        createDiagnostic(
          "error",
          "invalid_adapter_package_name",
          `Adapter package names must start with ${ADAPTER_PACKAGE_PREFIX}.`,
          { packageName }
        ),
      ],
    };
  }

  const discovery = await discoverInstalledAdapters();
  const diagnostics = discovery.diagnostics.filter(
    (diagnostic) => diagnostic.packageName === undefined || diagnostic.packageName === packageName
  );
  const discovered = discovery.adapters.find((adapter) => adapter.packageName === packageName);

  if (!discovered) {
    diagnostics.push(
      createDiagnostic("error", "adapter_not_found", `No installed adapter matched ${packageName}.`, {
        packageName,
      })
    );
    return { ok: false, packageName, diagnostics };
  }

  const manifestResult = await readAdapterManifest(
    discovered.packageRoot,
    discovered.packageName,
    discovered.discoveredFrom
  );
  diagnostics.push(...manifestResult.diagnostics);

  if (!manifestResult.adapter) {
    return { ok: false, packageName, diagnostics };
  }

  const entrypointResult = await resolveEntrypoint(manifestResult.adapter);
  diagnostics.push(...entrypointResult.diagnostics);

  if (!entrypointResult.entrypoint) {
    return { ok: false, packageName, diagnostics };
  }

  const url = await getUrl();
  let moduleNamespace: AdapterModuleNamespace;

  try {
    moduleNamespace = (await dynamicImport(
      url.pathToFileURL(entrypointResult.entrypoint).href
    )) as AdapterModuleNamespace;
  } catch (error) {
    diagnostics.push(
      createDiagnostic(
        "error",
        "module_import_failed",
        `Failed to import adapter module ${packageName}.`,
        {
          detail: normalizeErrorDetail(error),
          packageName,
          path: entrypointResult.entrypoint,
        }
      )
    );
    return { ok: false, packageName, diagnostics };
  }

  const exportCandidates = collectExportCandidates(moduleNamespace);
  const resolvedExport = exportCandidates[0];

  if (!resolvedExport) {
    diagnostics.push(
      createDiagnostic(
        "error",
        "entrypoint_not_exported",
        `No adapter factory or class export was found in ${packageName}.`,
        {
          packageName,
          path: entrypointResult.entrypoint,
        }
      )
    );
    return { ok: false, packageName, diagnostics };
  }

  if (
    exportCandidates.length > 1 &&
    exportCandidates[0].score === exportCandidates[1].score &&
    exportCandidates[0].exportName !== exportCandidates[1].exportName
  ) {
    diagnostics.push(
      createDiagnostic(
        "warning",
        "ambiguous_adapter_export",
        `Multiple exports in ${packageName} looked equally valid; selected ${resolvedExport.exportName}.`,
        {
          detail: `Competing export: ${exportCandidates[1].exportName}.`,
          packageName,
          path: entrypointResult.entrypoint,
        }
      )
    );
  }

  const resolvedName = resolveAdapterName(packageName, moduleNamespace);
  diagnostics.push(...resolvedName.diagnostics);

  return {
    ok: true,
    packageName,
    module: moduleNamespace,
    metadata: {
      packageName,
      adapterName: resolvedName.adapterName,
      version: resolveAdapterVersion(manifestResult.adapter, moduleNamespace),
      packageRoot: manifestResult.adapter.packageRoot,
      packageJsonPath: manifestResult.adapter.packageJsonPath,
      entrypoint: entrypointResult.entrypoint,
      exportName: resolvedExport.exportName,
      exportKind: resolvedExport.exportKind,
      exportValue: resolvedExport.exportValue,
    },
    diagnostics,
  };
}
