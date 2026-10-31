import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";

type Manifest = {
  dependencies?: Record<string, string>;
  optionalDependencies?: Record<string, string>;
  peerDependencies?: Record<string, string>;
  scripts?: Record<string, string>;
};

const manifest = JSON.parse(
  await readFile(new URL("../package.json", import.meta.url), "utf8"),
) as Manifest;

test("the package has no runtime dependencies", () => {
  assert.equal(manifest.dependencies, undefined);
  assert.equal(manifest.optionalDependencies, undefined);
  assert.equal(manifest.peerDependencies, undefined);
});

test("installing the package runs no scripts", () => {
  for (const hook of [
    "preinstall",
    "install",
    "postinstall",
    "prepare",
    "prepack",
  ]) {
    assert.equal(manifest.scripts?.[hook], undefined, hook);
  }
});
