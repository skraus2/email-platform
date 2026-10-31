import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { after, test } from "node:test";
import { exportTargets, missingExports } from "./check-exports.mts";

const dir = await mkdtemp(path.join(tmpdir(), "sdk-exports-"));
after(() => rm(dir, { recursive: true, force: true }));

test("collects nested condition targets", () => {
  assert.deepEqual(
    exportTargets({
      ".": { types: "./dist/index.d.ts", default: "./dist/index.js" },
    }),
    ["./dist/index.d.ts", "./dist/index.js"],
  );
});

test("reports targets that were not built", async () => {
  await mkdir(path.join(dir, "dist"));
  await writeFile(path.join(dir, "dist/index.js"), "export {};\n");
  await writeFile(
    path.join(dir, "package.json"),
    JSON.stringify({
      exports: {
        ".": { types: "./dist/index.d.ts", default: "./dist/index.js" },
      },
    }),
  );
  assert.deepEqual(await missingExports(dir), ["./dist/index.d.ts"]);
});

test("a manifest without exports is refused", async () => {
  const empty = path.join(dir, "empty");
  await mkdir(empty);
  await writeFile(path.join(empty, "package.json"), "{}");
  assert.equal((await missingExports(empty)).length, 1);
});
