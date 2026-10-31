import assert from "node:assert/strict";
import { copyFile, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { after, test } from "node:test";
import {
  checkDrift,
  contractPath,
  generate,
  generateUnchecked,
  nonLocalReference,
  outputPath,
} from "./generate.mts";

const dir = await mkdtemp(path.join(tmpdir(), "sdk-generate-"));
after(() => rm(dir, { recursive: true, force: true }));

async function contractWith(
  name: string,
  edit: (text: string) => string,
): Promise<string> {
  const file = path.join(dir, name);
  await writeFile(file, edit(await readFile(contractPath, "utf8")));
  return file;
}

const addSchema = (text: string): string =>
  text.replace(
    "    EventName:\n",
    "    DriftProbe:\n      type: object\n      properties:\n        probe:\n          type: string\n    EventName:\n",
  );

test("the committed file matches the contract", async () => {
  assert.equal(await checkDrift(contractPath, outputPath), undefined);
});

test("a contract change without regeneration is reported as drift", async () => {
  const changed = await contractWith("changed.yaml", addSchema);
  const output = path.join(dir, "openapi.ts");
  await copyFile(outputPath, output);
  const drift = await checkDrift(changed, output);
  assert.match(drift ?? "", /openapi\.ts is out of date with changed\.yaml/);
  assert.match(await generate(changed, output), /DriftProbe/);
});

test("a missing generated file is reported as drift", async () => {
  const drift = await checkDrift(contractPath, path.join(dir, "missing.ts"));
  assert.match(drift ?? "", /missing\.ts is out of date/);
});

test("the contract itself has only local references", async () => {
  assert.equal(
    nonLocalReference(await readFile(contractPath, "utf8")),
    undefined,
  );
});

const schemas = (b: string): string =>
  `components:\n  schemas:\n    A:\n      $ref: "#/components/schemas/B"\n    B:\n${b}\n`;

for (const [name, text] of [
  ["http", schemas('      $ref: "http://127.0.0.1:1/x.yaml#/Foo"')],
  ["https", schemas("      $ref: 'https://example.com/schema.json'")],
  [
    "relative file",
    schemas('      $ref: "./other.yaml#/components/schemas/X"'),
  ],
  ["unquoted", schemas("      $ref: other.yaml")],
  ["flow mapping", schemas('      { "$ref": "other.json" }')],
  ["escaped key", schemas('      "$r\\x65f": "https://example.com/x.yaml"')],
  ["$dynamicRef", schemas('      $dynamicRef: "#meta"')],
  ["$id", schemas('      "\\x24id": "https://example.com/s"')],
  [
    "externalValue",
    "paths:\n  /x:\n    get:\n      responses:\n        '200':\n          description: x\n          content:\n            application/json:\n              examples:\n                e:\n                  externalValue: https://example.com/e.json\n",
  ],
  [
    "discriminator mapping to a URL",
    schemas(
      "      oneOf: []\n      discriminator:\n        propertyName: type\n        mapping:\n          a: https://example.com/x.yaml",
    ),
  ],
  [
    "discriminator mapping to a file",
    schemas(
      "      oneOf: []\n      discriminator:\n        propertyName: type\n        mapping:\n          a: ../../../.config/gh/hosts.yml",
    ),
  ],
] as const) {
  test(`refuses a ${name} reference`, () => {
    assert.match(
      nonLocalReference(text) ?? "",
      /not (allowed|a local reference)/,
    );
  });
}

for (const [name, text] of [
  [
    "a discriminator behind a $ref",
    'components:\n  schemas:\n    A:\n      oneOf: []\n      discriminator:\n        $ref: "#/x-d"\nx-d:\n  propertyName: t\n  mapping:\n    a: ../../../.config/gh/hosts.yml\n',
  ],
  [
    "a mapping behind a $ref",
    'components:\n  schemas:\n    A:\n      oneOf: []\n      discriminator:\n        propertyName: t\n        mapping:\n          $ref: "#/x-m"\nx-m:\n  a: /etc/hosts.yaml\n',
  ],
] as const) {
  test(`refuses ${name}`, () => {
    assert.match(nonLocalReference(text) ?? "", /must not use \$ref/);
  });
}

test("a self-referencing alias does not hang the check", () => {
  assert.equal(nonLocalReference("x-a: &a\n  b: *a\n"), undefined);
});

test("the resolver may load only the checked contract", async () => {
  const text = await readFile(contractPath, "utf8");
  const leaking = text.replace(
    "    EventName:\n",
    "    Leak:\n      oneOf: []\n      discriminator:\n        propertyName: t\n        mapping:\n          a: ./other.yaml\n    EventName:\n",
  );
  const file = path.join(dir, "leaking.yaml");
  await writeFile(file, leaking);
  await writeFile(path.join(dir, "other.yaml"), "type: object\n");
  await assert.rejects(generateUnchecked(file, leaking), /refusing to load/);
  assert.ok(await generateUnchecked(contractPath, text));
});

test("an anchored key reused through an alias is seen as decoded", () => {
  const text = schemas(
    '      allOf:\n        - &r $ref: "#/components/schemas/A"\n        - *r : "https://example.com/x.yaml"',
  );
  // Either the parser refuses the document or the decoded remote $ref is found.
  let reason: string | undefined;
  try {
    reason = nonLocalReference(text);
  } catch {
    reason = "parse error";
  }
  assert.notEqual(reason, undefined);
});

test("local references and a $ref inside a description pass", () => {
  const text = schemas(
    '      description: "see $ref: http://example.com"\n      discriminator:\n        propertyName: type\n        mapping:\n          a: "#/components/schemas/A"',
  );
  assert.equal(nonLocalReference(text), undefined);
});

test("refuses a remote reference before the generator runs", async () => {
  const remote = await contractWith("remote.yaml", (text) =>
    text.replace(
      "    EventName:\n",
      '    Remote:\n      $ref: "http://127.0.0.1:1/x.yaml#/Foo"\n    EventName:\n',
    ),
  );
  await assert.rejects(
    generate(remote, outputPath),
    /only "#\/\.\.\." references are allowed/,
  );
});
