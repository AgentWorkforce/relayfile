import { readFile } from "node:fs/promises";
import Ajv2020, { type ErrorObject, type ValidateFunction } from "ajv/dist/2020.js";
import { parse } from "yaml";

interface OpenApiDocument {
  paths: Record<string, Record<string, unknown>>;
  components?: Record<string, unknown>;
}

interface OpenApiOperation {
  responses?: Record<string, OpenApiResponse | { $ref: string }>;
}

interface OpenApiResponse {
  content?: Record<string, { schema?: unknown }>;
}

interface Route {
  template: string;
  pathItem: Record<string, unknown>;
  expression: RegExp;
}

let documentPromise: Promise<OpenApiDocument> | undefined;
const validators = new Map<string, ValidateFunction>();

export async function validateOpenApiResponse(
  method: string,
  requestPath: string,
  status: number,
  data: unknown,
  contentType: string | null,
): Promise<void> {
  const document = await loadDocument();
  const pathname = new URL(requestPath, "http://conformance.invalid").pathname;
  const route = routes(document).find((candidate) => candidate.expression.test(pathname));
  if (!route) throw new Error(`OpenAPI has no path matching ${method} ${pathname}`);
  const operation = route.pathItem[method.toLowerCase()] as OpenApiOperation | undefined;
  if (!operation?.responses) throw new Error(`OpenAPI has no operation matching ${method} ${route.template}`);
  const rawResponse = operation.responses[String(status)] ?? operation.responses.default;
  if (!rawResponse) {
    throw new Error(`OpenAPI does not declare ${status} for ${method} ${route.template}`);
  }
  const response = resolveResponse(document, rawResponse);
  const declaredJson = response.content?.["application/json"] ?? response.content?.["application/problem+json"];
  if (!declaredJson?.schema) return;
  if (!contentType?.toLowerCase().includes("json")) {
    throw new Error(`OpenAPI expects JSON for ${method} ${route.template} ${status}, got ${contentType ?? "no Content-Type"}`);
  }

  const key = `${method.toUpperCase()} ${route.template} ${status}`;
  let validate = validators.get(key);
  if (!validate) {
    const ajv = new Ajv2020({ allErrors: true, strict: false, validateFormats: false });
    validate = ajv.compile({
      $ref: "#/$defs/response",
      $defs: { response: declaredJson.schema },
      components: document.components ?? {},
    });
    validators.set(key, validate);
  }
  if (!validate(data)) {
    throw new Error(`OpenAPI response mismatch for ${key}: ${formatErrors(validate.errors)}`);
  }
}

async function loadDocument(): Promise<OpenApiDocument> {
  documentPromise ??= readFile("openapi/relayfile-v1.openapi.yaml", "utf8").then((source) => {
    const parsed = parse(source) as OpenApiDocument;
    if (!parsed?.paths) throw new Error("OpenAPI document has no paths");
    return parsed;
  });
  return documentPromise;
}

const routeCache = new WeakMap<OpenApiDocument, Route[]>();
function routes(document: OpenApiDocument): Route[] {
  const cached = routeCache.get(document);
  if (cached) return cached;
  const compiled = Object.entries(document.paths)
    .map(([template, pathItem]) => ({
      template,
      pathItem,
      expression: new RegExp(`^${escapeRegex(template).replace(/\\\{[^}]+\\\}/gu, "[^/]+")}$`, "u"),
    }))
    .sort((left, right) => right.template.length - left.template.length);
  routeCache.set(document, compiled);
  return compiled;
}

function resolveResponse(
  document: OpenApiDocument,
  response: OpenApiResponse | { $ref: string },
): OpenApiResponse {
  if (!("$ref" in response)) return response;
  if (!response.$ref.startsWith("#/")) throw new Error(`unsupported external OpenAPI ref ${response.$ref}`);
  let current: unknown = document;
  for (const token of response.$ref.slice(2).split("/")) {
    const key = token.replaceAll("~1", "/").replaceAll("~0", "~");
    current = (current as Record<string, unknown>)?.[key];
  }
  if (!current || typeof current !== "object") throw new Error(`unresolved OpenAPI ref ${response.$ref}`);
  return current as OpenApiResponse;
}

function escapeRegex(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/gu, "\\$&");
}

function formatErrors(errors: ErrorObject[] | null | undefined): string {
  return (errors ?? [])
    .slice(0, 5)
    .map((error) => `${error.instancePath || "/"} ${error.message ?? error.keyword}`)
    .join("; ");
}
