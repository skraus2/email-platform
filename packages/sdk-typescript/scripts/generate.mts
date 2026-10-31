// Generates src/generated/openapi.ts from the API contract.
//
//   node scripts/generate.mts          write the file
//   node scripts/generate.mts --check  fail if the committed file differs
//
// The generator follows remote and file references and cannot be told not
// to, so the parsed contract is checked for anything that would make it
// load another document first, and while it runs its resolver may load the
// checked contract text and nothing else.
import { readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { BaseResolver, Source, parseYaml } from "@redocly/openapi-core";
import openapiTS, { astToString } from "openapi-typescript";
import * as prettier from "prettier";

const packageDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);
export const contractPath = path.resolve(packageDir, "../../api/openapi.yaml");
export const outputPath = path.join(packageDir, "src/generated/openapi.ts");

const header = `// Code generated from api/openapi.yaml by scripts/generate.mts. DO NOT EDIT.
// Regenerate with \`pnpm --filter @email-platform/sdk generate\`.

`;

// Keys that make a resolver load another document whatever their value.
const loadingKeys = new Set(["$id", "$dynamicRef", "externalValue"]);

/**
 * Returns why the contract may make the generator load another document, or
 * undefined. It inspects the parsed document (the same YAML parser the
 * generator uses), so escaped keys, anchors and aliases are seen as decoded.
 * Allowed: `$ref` and discriminator mapping values that point into this
 * document ("#/..."); a discriminator itself must be written inline, since
 * the resolver treats whatever a `$ref` there points to as a mapping.
 */
export function nonLocalReference(text: string): string | undefined {
  let found: string | undefined;
  const seen = new WeakSet<object>();
  const visit = (node: unknown, at: string, inDiscriminator: boolean): void => {
    if (found !== undefined || node === null || typeof node !== "object") {
      return;
    }
    if (seen.has(node)) {
      return;
    }
    seen.add(node);
    if (Array.isArray(node)) {
      node.forEach((item, i) => {
        visit(item, `${at}/${String(i)}`, inDiscriminator);
      });
      return;
    }
    for (const [key, value] of Object.entries(node)) {
      const where = `${at}/${key}`;
      if (loadingKeys.has(key)) {
        found = `${where} is not allowed`;
      } else if (key === "$ref" && inDiscriminator) {
        found = `${where}: a discriminator must not use $ref`;
      } else if (key === "$ref" && !isLocal(value)) {
        found = `${where} is not a local reference`;
      } else if (key === "mapping" && at.endsWith("/discriminator")) {
        for (const [name, target] of Object.entries(asObject(value))) {
          if (!isLocal(target)) {
            found = `${where}/${name} is not a local reference`;
          }
        }
      }
      visit(value, where, inDiscriminator || key === "discriminator");
    }
  };
  visit(parseYaml(text), "#", false);
  return found;
}

function isLocal(value: unknown): boolean {
  return typeof value === "string" && value.startsWith("#/");
}

function asObject(value: unknown): object {
  return value !== null && typeof value === "object" ? value : {};
}

/** Generates the formatted TypeScript source for the contract at `contract`. */
export async function generate(
  contract: string,
  target: string,
): Promise<string> {
  const text = await readFile(contract, "utf8");
  const reason = nonLocalReference(text);
  if (reason !== undefined) {
    throw new Error(
      `${contract}: ${reason}; only "#/..." references are allowed`,
    );
  }
  const ast = await loadingOnly(contract, text, () =>
    openapiTS(pathToFileURL(contract)),
  );
  const config = await prettier.resolveConfig(target);
  return prettier.format(header + astToString(ast), {
    ...config,
    filepath: target,
  });
}

/**
 * Runs `fn` while the generator's resolver can load only `contract`, served
 * from the already checked `text`: every file or URL load goes through
 * BaseResolver.loadExternalRef, so anything nonLocalReference missed fails
 * here instead of being read.
 */
async function loadingOnly<T>(
  contract: string,
  text: string,
  fn: () => Promise<T>,
): Promise<T> {
  const original = Object.getOwnPropertyDescriptor(
    BaseResolver.prototype,
    "loadExternalRef",
  );
  if (original === undefined) {
    throw new Error("BaseResolver.loadExternalRef not found");
  }
  const allowed = path.resolve(contract);
  const guarded: BaseResolver["loadExternalRef"] = (absoluteRef) =>
    absoluteRef === allowed
      ? Promise.resolve(new Source(absoluteRef, text.replace(/\r\n/g, "\n")))
      : Promise.reject(
          new Error(`refusing to load ${absoluteRef} during generation`),
        );
  Object.defineProperty(BaseResolver.prototype, "loadExternalRef", {
    ...original,
    value: guarded,
  });
  try {
    return await fn();
  } finally {
    Object.defineProperty(BaseResolver.prototype, "loadExternalRef", original);
  }
}

/** Test hook: the resolver guard without the static check. */
export function generateUnchecked(
  contract: string,
  text: string,
): Promise<unknown> {
  return loadingOnly(contract, text, () => openapiTS(pathToFileURL(contract)));
}

/** Returns a message if `output` differs from what `contract` generates. */
export async function checkDrift(
  contract: string,
  output: string,
): Promise<string | undefined> {
  const source = await generate(contract, output);
  const committed = await readFile(output, "utf8").catch(() => "");
  if (committed === source) {
    return undefined;
  }
  return `${path.relative(process.cwd(), output)} is out of date with ${path.basename(contract)}; run: pnpm --filter @email-platform/sdk generate`;
}

async function main(): Promise<void> {
  if (!process.argv.includes("--check")) {
    await writeFile(outputPath, await generate(contractPath, outputPath));
    return;
  }
  const drift = await checkDrift(contractPath, outputPath);
  if (drift !== undefined) {
    console.error(drift);
    process.exitCode = 1;
  }
}

if (import.meta.main) {
  await main();
}
