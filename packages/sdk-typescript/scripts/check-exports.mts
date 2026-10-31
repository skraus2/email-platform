// Fails unless every file the package's `exports` map points to exists, so a
// changed build setting cannot ship a package whose entry points are missing.
import { access, readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const packageDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);

/** Collects every file path in an `exports` map. */
export function exportTargets(exports: unknown): string[] {
  if (typeof exports === "string") {
    return [exports];
  }
  if (exports === null || typeof exports !== "object") {
    return [];
  }
  return Object.values(exports).flatMap(exportTargets);
}

/** Returns the export targets below `dir` that do not exist. */
export async function missingExports(dir: string): Promise<string[]> {
  const manifest = JSON.parse(
    await readFile(path.join(dir, "package.json"), "utf8"),
  ) as { exports?: unknown };
  const targets = exportTargets(manifest.exports);
  if (targets.length === 0) {
    return ["(package.json has no exports)"];
  }
  const missing: string[] = [];
  for (const target of targets) {
    await access(path.join(dir, target)).catch(() => missing.push(target));
  }
  return missing;
}

if (import.meta.main) {
  const missing = await missingExports(packageDir);
  if (missing.length > 0) {
    console.error(`package exports missing after build: ${missing.join(", ")}`);
    process.exitCode = 1;
  }
}
