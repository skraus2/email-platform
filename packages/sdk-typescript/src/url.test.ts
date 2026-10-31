import assert from "node:assert/strict";
import { test } from "node:test";

import { buildUrl, parseBaseUrl } from "./url.ts";

test("accepts https and plain http only on loopback", () => {
  for (const [raw, origin, prefix] of [
    ["https://api.example.com", "https://api.example.com", ""],
    ["https://api.example.com/", "https://api.example.com", ""],
    ["https://API.example.com:8443/", "https://api.example.com:8443", ""],
    ["https://api.example.com/prefix/", "https://api.example.com", "/prefix"],
    ["https://api.example.com//a//b//", "https://api.example.com", "/a/b"],
    ["http://localhost:8080", "http://localhost:8080", ""],
    ["http://127.0.0.1:8080", "http://127.0.0.1:8080", ""],
    ["http://[::1]:8080", "http://[::1]:8080", ""],
  ] as const) {
    assert.deepEqual(parseBaseUrl(raw), { origin, prefix }, raw);
  }
});

test("rejects other schemes, look-alike hosts, credentials, query and fragment without echoing the value", () => {
  for (const raw of [
    "http://api.example.com",
    "http://localhost.evil.com",
    "http://localhost@evil.com",
    "http://127.0.0.1.nip.io",
    "http://localhost.",
    "http://0.0.0.0:8080",
    "https://user:secret-pw@api.example.com",
    "https://:secret-pw@api.example.com",
    "https://api.example.com/?a=1",
    "https://api.example.com/?",
    "https://api.example.com/#x",
    "https://api.example.com/#",
    "ftp://api.example.com",
    "not a url",
    "",
  ]) {
    assert.throws(
      () => parseBaseUrl(raw),
      (err: unknown) => {
        assert.ok(err instanceof TypeError, raw);
        assert.match(err.message, /^baseUrl /);
        if (raw !== "") {
          assert.ok(!err.message.includes(raw), raw);
        }
        assert.ok(!err.message.includes("secret-pw"), raw);
        return true;
      },
      raw,
    );
  }
});

test("rejects a baseUrl that is not a string", () => {
  assert.throws(() => parseBaseUrl(undefined), TypeError);
  assert.throws(() => parseBaseUrl(42), TypeError);
});

test("joins the prefix and the operation path without a double slash", () => {
  const base = parseBaseUrl("https://api.example.com/prefix/");
  assert.equal(
    buildUrl(base, "/v1/emails", {}, {}),
    "https://api.example.com/prefix/v1/emails",
  );
  assert.equal(
    buildUrl(parseBaseUrl("https://api.example.com/"), "/v1/emails", {}, {}),
    "https://api.example.com/v1/emails",
  );
});

test("percent-encodes path parameters", () => {
  const base = parseBaseUrl("https://api.example.com");
  assert.equal(
    buildUrl(
      base,
      "/v1/audiences/{id}/contacts/{contact_id}",
      { id: "a/b", contact_id: "c?d#e f" },
      {},
    ),
    "https://api.example.com/v1/audiences/a%2Fb/contacts/c%3Fd%23e%20f",
  );
  assert.equal(
    buildUrl(
      base,
      "/v1/templates/{id}/versions/{version}",
      { id: "t", version: 3 },
      {},
    ),
    "https://api.example.com/v1/templates/t/versions/3",
  );
});

test("a missing or empty path parameter throws without a request", () => {
  const base = parseBaseUrl("https://api.example.com");
  assert.throws(() => buildUrl(base, "/v1/emails/{id}", {}, {}), {
    name: "TypeError",
    message: "missing path parameter id",
  });
  assert.throws(() => buildUrl(base, "/v1/emails/{id}", { id: "" }, {}), {
    name: "TypeError",
    message: "missing path parameter id",
  });
});

test("a dot-segment path parameter throws: it would reroute to another operation", () => {
  const base = parseBaseUrl("https://api.example.com");
  for (const id of [".", ".."]) {
    // DELETE /v1/audiences/../contacts/C would reach deleteContact.
    assert.throws(
      () =>
        buildUrl(
          base,
          "/v1/audiences/{id}/contacts/{contact_id}",
          { id, contact_id: "C" },
          {},
        ),
      { name: "TypeError", message: "path parameter id must not be . or .." },
    );
  }
  // Percent signs are encoded, so an encoded dot stays a literal segment.
  const url = buildUrl(
    base,
    "/v1/audiences/{id}/contacts/{contact_id}",
    { id: "%2e%2E", contact_id: "C" },
    {},
  );
  assert.equal(
    url,
    "https://api.example.com/v1/audiences/%252e%252E/contacts/C",
  );
  assert.equal(new URL(url).pathname, "/v1/audiences/%252e%252E/contacts/C");
  assert.equal(
    new URL(buildUrl(base, "/v1/emails/{id}", { id: "a.b" }, {})).pathname,
    "/v1/emails/a.b",
  );
});

test("query: undefined left out, arrays repeated, scalars stringified", () => {
  const base = parseBaseUrl("https://api.example.com");
  assert.equal(
    buildUrl(
      base,
      "/v1/emails",
      {},
      {
        status: ["queued", "sent"],
        recipient: "a+b@example.com",
        limit: 10,
        template_id: undefined,
        flag: true,
      },
    ),
    "https://api.example.com/v1/emails?status=queued&status=sent&recipient=a%2Bb%40example.com&limit=10&flag=true",
  );
  assert.equal(
    buildUrl(base, "/v1/emails", {}, { cursor: undefined }),
    "https://api.example.com/v1/emails",
  );
});
