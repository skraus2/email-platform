import assert from "node:assert/strict";
import { test } from "node:test";
import { inspect } from "node:util";

import { EmailPlatform, type EmailPlatformOptions } from "./client.ts";
import { ApiError, TransportError } from "./errors.ts";

const apiKey = "ep_live_secret-key-456";
const baseUrl = "https://api.example.com";

function stubFetch(reply: () => Response | Promise<Response>) {
  const calls: { url: string; init: RequestInit }[] = [];
  const fetch = (input: string | URL | Request, init?: RequestInit) => {
    calls.push({
      url: input instanceof Request ? input.url : input.toString(),
      init: init ?? {},
    });
    return Promise.resolve(reply());
  };
  return { calls, fetch };
}

test("defaults: 30 s timeout, 2 retries", async () => {
  let attempts = 0;
  const { fetch } = stubFetch(() => {
    attempts++;
    return new Response("{}", { status: 404 });
  });
  const client = new EmailPlatform({ apiKey, baseUrl, fetch });
  assert.deepEqual(JSON.parse(JSON.stringify(client)), {
    baseUrl,
    timeout: 30_000,
    maxRetries: 2,
    apiKey: "[redacted]",
  });
  await assert.rejects(client.emails.get("e1"), ApiError);
  assert.equal(attempts, 1);
});

test("the injected fetch is used and receives the key", async () => {
  const { calls, fetch } = stubFetch(
    () => new Response('{"id":"e1"}', { status: 200 }),
  );
  const client = new EmailPlatform({ apiKey, baseUrl: `${baseUrl}/`, fetch });
  const result = await client.emails.get("e1");
  assert.deepEqual(result, { id: "e1" });
  const [call] = calls;
  assert.equal(call?.url, `${baseUrl}/v1/emails/e1`);
  assert.equal(
    (call.init.headers as Record<string, string>)["Authorization"],
    `Bearer ${apiKey}`,
  );
});

test("invalid options throw, naming the option but never its value", () => {
  const cases: [
    Partial<EmailPlatformOptions> | undefined,
    string,
    ErrorConstructor,
  ][] = [
    [undefined, "options", TypeError],
    [{ baseUrl }, "apiKey", TypeError],
    [{ apiKey: "", baseUrl }, "apiKey", TypeError],
    [{ apiKey: 42 as unknown as string, baseUrl }, "apiKey", TypeError],
    [{ apiKey: "has space", baseUrl }, "apiKey", TypeError],
    [{ apiKey: "line\nbreak", baseUrl }, "apiKey", TypeError],
    [{ apiKey }, "baseUrl", TypeError],
    [{ apiKey, baseUrl: "http://api.example.com" }, "baseUrl", TypeError],
    [{ apiKey, baseUrl, timeout: 0 }, "timeout", RangeError],
    [{ apiKey, baseUrl, timeout: Infinity }, "timeout", RangeError],
    [{ apiKey, baseUrl, timeout: 2 ** 31 }, "timeout", RangeError],
    [{ apiKey, baseUrl, maxRetries: -1 }, "maxRetries", RangeError],
    [{ apiKey, baseUrl, maxRetries: 1.5 }, "maxRetries", RangeError],
    [
      { apiKey, baseUrl, fetch: "x" as unknown as typeof fetch },
      "fetch",
      TypeError,
    ],
  ];
  for (const [options, name, type] of cases) {
    assert.throws(
      () => new EmailPlatform(options as EmailPlatformOptions),
      (err: unknown) => {
        assert.ok(err instanceof type, `${name}: ${String(err)}`);
        assert.match(err.message, new RegExp(`^${name}\\b`));
        assert.ok(!err.message.includes("has space"));
        assert.ok(!err.message.includes(apiKey));
        return true;
      },
    );
  }
});

test("maxRetries above 5 is clamped", async () => {
  let attempts = 0;
  const { fetch } = stubFetch(() => {
    attempts++;
    return new Response(
      JSON.stringify({ error: { code: "rate_limited", message: "x" } }),
      {
        status: 429,
        headers: { "Retry-After": "1" },
      },
    );
  });
  const client = new EmailPlatform({
    apiKey,
    baseUrl,
    fetch,
    maxRetries: 9,
    timeout: 1500,
  });
  assert.equal((client.toJSON() as { maxRetries: number }).maxRetries, 5);
  // The 1 s wait plus jitter fits the 1.5 s deadline at most once.
  await assert.rejects(client.emails.get("e1"), ApiError);
  assert.ok(attempts >= 1 && attempts <= 2);
});

test("the default runtime aborts at the deadline with reason timeout", async () => {
  const client = new EmailPlatform({
    apiKey,
    baseUrl,
    timeout: 20,
    fetch: (_input, init) =>
      new Promise((_resolve, reject) => {
        init?.signal?.addEventListener("abort", () => {
          reject(init.signal?.reason as Error);
        });
      }),
  });
  const err = await client.emails.get("e1").catch((e: unknown) => e);
  assert.ok(err instanceof TransportError);
  assert.equal(err.reason, "timeout");
});

test("printing or serialising the client never shows the key", () => {
  const client = new EmailPlatform({ apiKey, baseUrl: `${baseUrl}/prefix` });
  const texts = [
    inspect(client),
    inspect(client, { showHidden: true, depth: 10 }),
    JSON.stringify(client),
    String(client),
    client.toString(),
    JSON.stringify(Object.entries(client)),
  ];
  for (const text of texts) {
    assert.ok(!text.includes(apiKey), text);
  }
  assert.equal(String(client), "EmailPlatform(https://api.example.com/prefix)");
  assert.deepEqual(Object.keys(client), resourceNames);
  assert.deepEqual(Object.getOwnPropertyNames(client), resourceNames);
  assert.deepEqual(Object.getOwnPropertySymbols(client), []);
});

const resourceNames = [
  "emails",
  "contacts",
  "audiences",
  "unsubscribes",
  "broadcasts",
  "automations",
  "automationRuns",
  "contactEvents",
  "usage",
  "templates",
  "webhookEndpoints",
];

// Every value reachable through own properties (string and symbol keys,
// enumerable or not, getters not invoked) and prototypes, stopping at the
// built-in prototypes.
function reachable(root: object): unknown[] {
  const builtIns = new Set<unknown>([Object.prototype, Function.prototype]);
  const seen = new Set<unknown>();
  const values: unknown[] = [];
  const visit = (value: unknown) => {
    values.push(value);
    if (
      (typeof value !== "object" && typeof value !== "function") ||
      value === null ||
      builtIns.has(value) ||
      seen.has(value)
    ) {
      return;
    }
    seen.add(value);
    for (const key of Reflect.ownKeys(value)) {
      const descriptor = Reflect.getOwnPropertyDescriptor(value, key);
      if (descriptor !== undefined && "value" in descriptor) {
        visit(descriptor.value);
      }
    }
    visit(Reflect.getPrototypeOf(value));
  };
  visit(root);
  return values;
}

test("no property of the client or its resources leads to the key", () => {
  const client = new EmailPlatform({ apiKey, baseUrl });
  const values = reachable(client);
  // The resources and their methods are part of the walk.
  assert.ok(values.includes(client.emails));
  assert.ok(values.includes(Reflect.get(client.emails, "send")));
  for (const value of values) {
    if (typeof value === "string") {
      assert.ok(!value.includes(apiKey), value);
    }
    if (typeof value === "object" && value !== null) {
      assert.ok(!("apiKey" in value), "settings object");
    }
  }
  for (const name of resourceNames) {
    const resource = (client as unknown as Record<string, object>)[name];
    assert.ok(resource !== undefined, name);
    assert.deepEqual(Reflect.ownKeys(resource), [], name);
    for (const text of [
      inspect(resource, { showHidden: true, depth: Infinity }),
      JSON.stringify(resource),
    ]) {
      assert.ok(!text.includes(apiKey), `${name}: ${text}`);
    }
  }
  const text = inspect(client, { showHidden: true, depth: Infinity });
  assert.ok(!text.includes(apiKey), text);
});

test("errors from a client call never show the key, the query or the body", async () => {
  const { fetch } = stubFetch(
    () =>
      new Response(
        JSON.stringify({
          error: { code: "validation_failed", message: "bad" },
        }),
        {
          status: 422,
        },
      ),
  );
  const client = new EmailPlatform({ apiKey, baseUrl, fetch });
  const err = await client.emails
    .list({ recipient: "user@example.com" })
    .catch((e: unknown) => e);
  assert.ok(err instanceof ApiError);
  for (const text of [
    err.message,
    err.stack ?? "",
    inspect(err, { showHidden: true }),
    JSON.stringify(err),
  ]) {
    assert.ok(!text.includes(apiKey), text);
    assert.ok(!text.includes("user@example.com"), text);
  }
});
