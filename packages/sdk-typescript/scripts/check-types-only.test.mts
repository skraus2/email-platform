import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { notTypesOnly } from "./check-types-only.mts";
import { outputPath } from "./generate.mts";

test("the generated file is types only", async () => {
  assert.equal(notTypesOnly(await readFile(outputPath, "utf8")), undefined);
});

const typesOnly =
  "export interface A {\n  a: string;\n}\nexport type B = A | null;\n";

test("plain interfaces and type aliases pass", () => {
  assert.equal(notTypesOnly(typesOnly), undefined);
});

for (const [name, source] of [
  ["a constant", 'export const url = "https://example.com";'],
  ["a statement", 'globalThis.fetch = () => { throw new Error("x"); };'],
  ["an enum", "export enum E { A }"],
  ["an import with side effects", 'import "node:child_process";'],
  ["declare global", "declare global {\n  var x: string;\n}"],
  ["declare module", 'declare module "node:http" {\n  interface X {}\n}'],
  [
    "a value import used only as a type",
    'import { x } from "node:fs";\nexport type T = typeof x;',
  ],
  [
    "a type-only import",
    'import type { X } from "./x.ts";\nexport type T = X;',
  ],
  [
    "declare global behind a comment",
    "declare /**/ global {\n  var x: string;\n}",
  ],
  ["a namespace", "export namespace N {\n  export type X = string;\n}"],
  ["an unexported interface", "interface Hidden {\n  a: string;\n}"],
  ["export =", "export = {};"],
  ["an import type", 'export type T = import("node:http").Server;'],
  ["a typeof import type", 'export type T = typeof import("node:fs");'],
] as const) {
  test(`rejects ${name}`, () => {
    assert.notEqual(notTypesOnly(typesOnly + source + "\n"), undefined);
  });
}

for (const directive of [
  '/// <reference types="node" />',
  '/// <reference path="./x.d.ts" />',
  '/// <reference lib="dom" />',
  '/// <reference no-default-lib="true" />',
  '/// <amd-dependency path="x" />',
]) {
  test(`rejects the triple-slash directive ${directive}`, () => {
    assert.notEqual(notTypesOnly(directive + "\n" + typesOnly), undefined);
  });
}
