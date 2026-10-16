import assert from "node:assert/strict";
import { test } from "node:test";
import { buildAdminURL } from "./admin-url.ts";

test("builds URLs on the configured origin", () => {
  for (const [base, want] of [
    ["http://127.0.0.1:8081", "http://127.0.0.1:8081/admin/v1/me"],
    ["http://127.0.0.1:8081/", "http://127.0.0.1:8081/admin/v1/me"],
    ["http://h:1/pre/", "http://h:1/pre/admin/v1/me"],
    ["https://h:1/pre", "https://h:1/pre/admin/v1/me"],
    ["http://[::1]:8081", "http://[::1]:8081/admin/v1/me"],
  ]) {
    assert.equal(buildAdminURL(base, "/admin/v1/me").href, want);
  }
  const q = new URLSearchParams({ q: "a&b=c", status: "failed" });
  assert.equal(
    buildAdminURL("http://127.0.0.1:8081", "/admin/v1/emails", q).href,
    "http://127.0.0.1:8081/admin/v1/emails?q=a%26b%3Dc&status=failed",
  );
});

test("refuses bad bases and paths", () => {
  for (const base of [
    "ftp://h",
    "http://u:p@h",
    "http://h/?x=1",
    "http://h/#f",
    "not a url",
  ]) {
    assert.throws(() => buildAdminURL(base, "/admin/v1/me"), base);
  }
  for (const path of [
    "/admin/v2/me",
    "admin/v1/me",
    "/admin/v1/../x",
    "/admin/v1//evil.example/x",
    "/admin/v1/me?x=1",
    "/admin/v1/me#x",
    "/admin/v1/a\\b",
    "/admin/v1/%2e%2e/x",
    "/admin/v1/%2E./x",
    "/admin/v1/.%2e/x",
    "/admin/v1/.\t./x",
    "/admin/v1/me\n",
    "/admin/v1/a b",
  ]) {
    assert.throws(() => buildAdminURL("http://127.0.0.1:8081", path), path);
  }
});
