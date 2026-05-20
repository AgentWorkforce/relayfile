import { readFile } from "node:fs/promises"
import path from "node:path"
import * as ts from "typescript"
import { describe, expect, it } from "vitest"

const SDK_SRC_ROOT = path.resolve(import.meta.dirname)
const FORBIDDEN_DEFAULT_ENTRY_IMPORTS = new Set([
  "node:child_process",
  "node:fs",
  "node:fs/promises",
  "node:http",
  "node:path",
  "node:process",
  "node:url"
])

describe("default entry import safety", () => {
  it("does not statically pull CLI-only Node modules into @relayfile/sdk", async () => {
    const graph = await collectStaticImportGraph(path.join(SDK_SRC_ROOT, "index.ts"))

    expect([...graph.files].sort()).not.toContain(
      path.join(SDK_SRC_ROOT, "mount-launcher.ts")
    )
    expect([...graph.files].sort()).not.toContain(
      path.join(SDK_SRC_ROOT, "cloud-login.ts")
    )
    expect([...graph.nodeSpecifiers].sort()).toEqual([])
  })
})

async function collectStaticImportGraph(entry: string): Promise<{
  files: Set<string>
  nodeSpecifiers: Set<string>
}> {
  const files = new Set<string>()
  const nodeSpecifiers = new Set<string>()
  const pending = [entry]

  while (pending.length > 0) {
    const file = pending.pop()!
    if (files.has(file)) {
      continue
    }
    files.add(file)

    const source = await readFile(file, "utf8")
    const sourceFile = ts.createSourceFile(
      file,
      source,
      ts.ScriptTarget.Latest,
      true,
      ts.ScriptKind.TS
    )

    for (const specifier of readRuntimeModuleSpecifiers(sourceFile)) {
      if (FORBIDDEN_DEFAULT_ENTRY_IMPORTS.has(specifier)) {
        nodeSpecifiers.add(specifier)
      }
      if (specifier.startsWith(".")) {
        pending.push(resolveTypeScriptSource(file, specifier))
      }
    }
  }

  return { files, nodeSpecifiers }
}

function readRuntimeModuleSpecifiers(sourceFile: ts.SourceFile): string[] {
  const specifiers: string[] = []
  for (const statement of sourceFile.statements) {
    if (ts.isImportDeclaration(statement)) {
      if (statement.importClause?.isTypeOnly) {
        continue
      }
      specifiers.push(readModuleSpecifier(statement.moduleSpecifier))
      continue
    }
    if (ts.isExportDeclaration(statement) && statement.moduleSpecifier) {
      if (statement.isTypeOnly || exportClauseIsTypeOnly(statement.exportClause)) {
        continue
      }
      specifiers.push(readModuleSpecifier(statement.moduleSpecifier))
    }
  }
  return specifiers
}

function exportClauseIsTypeOnly(exportClause: ts.NamedExportBindings | undefined): boolean {
  if (!exportClause || !ts.isNamedExports(exportClause)) {
    return false
  }
  return exportClause.elements.every((element) => element.isTypeOnly)
}

function readModuleSpecifier(specifier: ts.Expression): string {
  if (!ts.isStringLiteral(specifier)) {
    throw new Error(`Unsupported non-literal module specifier in ${specifier.getText()}`)
  }
  return specifier.text
}

function resolveTypeScriptSource(fromFile: string, specifier: string): string {
  const resolved = path.resolve(path.dirname(fromFile), specifier)
  if (resolved.endsWith(".js")) {
    return `${resolved.slice(0, -3)}.ts`
  }
  return `${resolved}.ts`
}
