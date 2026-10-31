import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

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

type Exports = { ".": Record<string, string> };

test("browsers get a module that refuses to load, before the default entry", async () => {
  const exports = (manifest as Manifest & { exports: Exports }).exports;
  assert.deepEqual(Object.keys(exports["."]), ["types", "browser", "default"]);
  assert.equal(exports["."]["browser"], "./dist/browser.js");
  await assert.rejects(import("./browser.ts"), {
    name: "Error",
    message: /server-side only/,
  });
});

test("the client imports node:crypto at the top level, so browser bundles fail", async () => {
  const index = await readFile(new URL("./index.ts", import.meta.url), "utf8");
  assert.match(
    index,
    /^export \{[^}]*\bEmailPlatform\b[^}]*\} from "\.\/client\.ts";$/m,
  );
  const client = await readFile(
    new URL("./client.ts", import.meta.url),
    "utf8",
  );
  assert.match(client, /^import \{[^}]*\} from "node:crypto";$/m);
});

test("the webhook verifier imports node:crypto at the top level", async () => {
  const webhook = await readFile(
    new URL("./webhook.ts", import.meta.url),
    "utf8",
  );
  assert.match(webhook, /^import \{[^}]*\} from "node:crypto";$/m);
});

test("the entry exports the client, its errors, the page iterator and the webhook verifier only", async () => {
  const entry = (await import("./index.ts")) as Record<string, unknown>;
  // The request function, its accessor and the operation table stay
  // internal; everything else is types.
  assert.deepEqual(Object.keys(entry).sort(), [
    "ApiError",
    "EmailPlatform",
    "TransportError",
    "WebhookVerificationError",
    "paginate",
    "verifyWebhook",
  ]);
});

test("the SDK's own code may not log", async () => {
  const { ESLint } = await import("eslint");
  const eslint = new ESLint({
    cwd: fileURLToPath(new URL("..", import.meta.url)),
  });
  const config = (await eslint.calculateConfigForFile("src/client.ts")) as {
    rules: Record<string, unknown[]>;
  };
  assert.equal(config.rules["no-console"]?.[0], 2);
});
