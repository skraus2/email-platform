// Fails unless src/generated/openapi.ts is types only: every top-level
// statement must be an exported interface or type alias, there must be no
// imports (also no `import("…")` types), module blocks or triple-slash
// comments, and compiling it with
// the build's module settings must emit no JavaScript. A generator that
// injected code would otherwise ship it to every consumer.
import { readFile } from "node:fs/promises";
import ts from "typescript";
import { outputPath } from "./generate.mts";

/** Returns why `source` is not a types-only module, or undefined. */
export function notTypesOnly(source: string): string | undefined {
  const file = ts.createSourceFile(
    "openapi.ts",
    source,
    ts.ScriptTarget.Latest,
  );
  // Any `///` comment: besides references, directives like no-default-lib
  // and amd-dependency change how consumers compile.
  if (source.includes("///")) {
    return "contains a triple-slash comment";
  }
  for (const statement of file.statements) {
    const allowed =
      (ts.isInterfaceDeclaration(statement) ||
        ts.isTypeAliasDeclaration(statement)) &&
      statement.modifiers?.length === 1 &&
      statement.modifiers[0]?.kind === ts.SyntaxKind.ExportKeyword;
    if (!allowed) {
      const { line } = file.getLineAndCharacterOfPosition(
        statement.getStart(file),
      );
      return `line ${String(line + 1)}: only exported interfaces and type aliases are allowed (${ts.SyntaxKind[statement.kind]})`;
    }
  }
  let importType: ts.Node | undefined;
  const visit = (node: ts.Node): void => {
    if (ts.isImportTypeNode(node)) {
      importType = node;
    }
    if (importType === undefined) {
      ts.forEachChild(node, visit);
    }
  };
  visit(file);
  if (importType !== undefined) {
    return "contains an import type";
  }
  // Same module settings as tsconfig.json, which the build extends.
  const emitted = ts.transpileModule(source, {
    compilerOptions: {
      module: ts.ModuleKind.NodeNext,
      moduleResolution: ts.ModuleResolutionKind.NodeNext,
      target: ts.ScriptTarget.ES2023,
      verbatimModuleSyntax: true,
      removeComments: true,
    },
    fileName: "openapi.mts",
  }).outputText;
  if (emitted.trim() !== "export {};") {
    return `emits JavaScript: ${emitted.trim().slice(0, 200)}`;
  }
  return undefined;
}

async function main(): Promise<void> {
  const reason = notTypesOnly(await readFile(outputPath, "utf8"));
  if (reason !== undefined) {
    console.error(`src/generated/openapi.ts is not types only: ${reason}`);
    process.exitCode = 1;
  }
}

if (import.meta.main) {
  await main();
}
