import assert from "node:assert/strict";
import { test } from "node:test";

import { EmailPlatform } from "./client.ts";
import { ApiError, TransportError } from "./errors.ts";
import { METHODS } from "./methods.ts";
import type { components } from "./generated/openapi.ts";
import type * as entry from "./index.ts";
import type { OperationId } from "./operations.ts";
import type { Body, Contacts, Emails, Item, Result } from "./resources.ts";

const apiKey = "ep_live_resource-key-789";
const baseUrl = "https://api.example.com/prefix";

type Call = {
  url: URL;
  method: string;
  headers: Record<string, string>;
  body: string | undefined;
};

function stub(reply: (call: Call) => Response) {
  const calls: Call[] = [];
  const fetch = (input: string | URL | Request, init?: RequestInit) => {
    const call: Call = {
      url: new URL(input instanceof Request ? input.url : input.toString()),
      method: init?.method ?? "GET",
      headers: (init?.headers ?? {}) as Record<string, string>,
      body: init?.body as string | undefined,
    };
    calls.push(call);
    return Promise.resolve(reply(call));
  };
  return { calls, client: new EmailPlatform({ apiKey, baseUrl, fetch }) };
}

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

type Case = {
  /** Arguments before the options. */
  args: unknown[];
  method: string;
  path: string;
  query?: Record<string, string | string[]>;
  body?: unknown;
  status?: number;
};

const page = { data: [], next_cursor: null };
const contact = { email: "a@example.com", attributes: { plan: "pro" } };

// One call per operation, with the request it must produce. Bodies and
// queries use wire names on purpose: the SDK must not rename them.
const cases: Record<OperationId, Case> = {
  createEmail: {
    args: [{ from: "app@example.com", to: "u@example.com", text: "hi" }],
    method: "POST",
    path: "/v1/emails",
    body: { from: "app@example.com", to: "u@example.com", text: "hi" },
    status: 202,
  },
  listEmails: {
    args: [{ status: ["failed", "bounced"], recipient: "u@example.com" }],
    method: "GET",
    path: "/v1/emails",
    query: { status: ["failed", "bounced"], recipient: ["u@example.com"] },
  },
  getEmail: { args: ["e 1/x"], method: "GET", path: "/v1/emails/e%201%2Fx" },
  listEmailEvents: {
    args: ["e1", { limit: 5 }],
    method: "GET",
    path: "/v1/emails/e1/events",
    query: { limit: ["5"] },
  },
  cancelEmail: { args: ["e1"], method: "POST", path: "/v1/emails/e1/cancel" },
  createContact: {
    args: [contact],
    method: "POST",
    path: "/v1/contacts",
    body: contact,
    status: 201,
  },
  listContacts: {
    args: [{ email: "a@example.com", status: "subscribed" }],
    method: "GET",
    path: "/v1/contacts",
    query: { email: ["a@example.com"], status: ["subscribed"] },
  },
  getContact: { args: ["c1"], method: "GET", path: "/v1/contacts/c1" },
  updateContact: {
    args: ["c1", { attributes: { plan: "free" } }],
    method: "PATCH",
    path: "/v1/contacts/c1",
    body: { attributes: { plan: "free" } },
  },
  deleteContact: {
    args: ["c1"],
    method: "DELETE",
    path: "/v1/contacts/c1",
    status: 204,
  },
  createAudience: {
    args: [{ name: "Beta" }],
    method: "POST",
    path: "/v1/audiences",
    body: { name: "Beta" },
    status: 201,
  },
  listAudiences: {
    args: [{ limit: 10 }],
    method: "GET",
    path: "/v1/audiences",
    query: { limit: ["10"] },
  },
  getAudience: { args: ["a1"], method: "GET", path: "/v1/audiences/a1" },
  addAudienceContacts: {
    args: ["a1", { contact_ids: ["c1", "c2"] }],
    method: "POST",
    path: "/v1/audiences/a1/contacts",
    body: { contact_ids: ["c1", "c2"] },
  },
  listAudienceContacts: {
    args: ["a1", { cursor: "k1" }],
    method: "GET",
    path: "/v1/audiences/a1/contacts",
    query: { cursor: ["k1"] },
  },
  addAudienceContact: {
    args: ["a1", "c1"],
    method: "PUT",
    path: "/v1/audiences/a1/contacts/c1",
    status: 204,
  },
  removeAudienceContact: {
    args: ["a1", "c1"],
    method: "DELETE",
    path: "/v1/audiences/a1/contacts/c1",
    status: 204,
  },
  reportUnsubscribe: {
    args: [{ token: "t1" }],
    method: "POST",
    path: "/v1/unsubscribes",
    body: { token: "t1" },
  },
  createBroadcast: {
    args: [{ audience_id: "a1", template_version: "published" }],
    method: "POST",
    path: "/v1/broadcasts",
    body: { audience_id: "a1", template_version: "published" },
    status: 201,
  },
  listBroadcasts: {
    args: [{ status: "draft" }],
    method: "GET",
    path: "/v1/broadcasts",
    query: { status: ["draft"] },
  },
  getBroadcast: { args: ["b1"], method: "GET", path: "/v1/broadcasts/b1" },
  scheduleBroadcast: {
    args: ["b1", { scheduled_at: "2026-11-01T08:00:00Z" }],
    method: "POST",
    path: "/v1/broadcasts/b1/schedule",
    body: { scheduled_at: "2026-11-01T08:00:00Z" },
  },
  cancelBroadcast: {
    args: ["b1"],
    method: "POST",
    path: "/v1/broadcasts/b1/cancel",
  },
  createAutomation: {
    args: [{ name: "Welcome", trigger: { event: "signed_up" } }],
    method: "POST",
    path: "/v1/automations",
    body: { name: "Welcome", trigger: { event: "signed_up" } },
    status: 201,
  },
  listAutomations: {
    args: [{ status: "active" }],
    method: "GET",
    path: "/v1/automations",
    query: { status: ["active"] },
  },
  getAutomation: { args: ["m1"], method: "GET", path: "/v1/automations/m1" },
  updateAutomation: {
    args: ["m1", { status: "paused" }],
    method: "PATCH",
    path: "/v1/automations/m1",
    body: { status: "paused" },
  },
  listAutomationRuns: {
    args: ["m1", { status: "waiting", contact_id: "c1" }],
    method: "GET",
    path: "/v1/automations/m1/runs",
    query: { status: ["waiting"], contact_id: ["c1"] },
  },
  getAutomationRun: {
    args: ["r1"],
    method: "GET",
    path: "/v1/automation-runs/r1",
  },
  cancelAutomationRun: {
    args: ["r1"],
    method: "POST",
    path: "/v1/automation-runs/r1/cancel",
  },
  createContactEvent: {
    args: [{ name: "signed_up", contact_id: "c1" }],
    method: "POST",
    path: "/v1/contact-events",
    body: { name: "signed_up", contact_id: "c1" },
    status: 202,
  },
  getUsage: {
    args: [{ from: "2026-10-01", to: "2026-10-31" }],
    method: "GET",
    path: "/v1/usage",
    query: { from: ["2026-10-01"], to: ["2026-10-31"] },
  },
  createTemplate: {
    args: [{ name: "receipt" }],
    method: "POST",
    path: "/v1/templates",
    body: { name: "receipt" },
    status: 201,
  },
  getTemplate: { args: ["t1"], method: "GET", path: "/v1/templates/t1" },
  publishTemplateVersion: {
    args: ["t1", { subject: "Hi {{.name}}" }],
    method: "POST",
    path: "/v1/templates/t1/versions",
    body: { subject: "Hi {{.name}}" },
    status: 201,
  },
  getTemplateVersion: {
    args: ["t1", 3],
    method: "GET",
    path: "/v1/templates/t1/versions/3",
  },
  createWebhookEndpoint: {
    args: [{ url: "https://app.example.com/hooks" }],
    method: "POST",
    path: "/v1/webhook-endpoints",
    body: { url: "https://app.example.com/hooks" },
    status: 201,
  },
};

type AnyMethod = (...args: unknown[]) => Promise<unknown>;

function methodOf(client: EmailPlatform, operationId: OperationId) {
  const [resource, name] = METHODS[operationId];
  const target = client[resource] as unknown as Record<string, AnyMethod>;
  const fn = target[name];
  if (typeof fn !== "function") {
    assert.fail(`${resource}.${name} is not a method`);
  }
  return (...args: unknown[]) => fn.apply(target, args);
}

function queryOf(url: URL): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const name of new Set(url.searchParams.keys())) {
    out[name] = url.searchParams.getAll(name);
  }
  return out;
}

test("the method map names 37 distinct client methods", () => {
  const entries = Object.values(METHODS).map(([r, m]) => `${r}.${m}`);
  assert.equal(entries.length, 37);
  assert.equal(new Set(entries).size, 37);
});

for (const operationId of Object.keys(METHODS) as OperationId[]) {
  const c = cases[operationId];
  test(`${METHODS[operationId].join(".")} sends ${operationId} as on the wire`, async () => {
    const status = c.status ?? 200;
    const reply = { id: "x1", created_at: "2026-10-31T00:00:00Z", ...page };
    const { calls, client } = stub(() =>
      status === 204 ? new Response(null, { status }) : json(status, reply),
    );
    const result = await methodOf(client, operationId)(...c.args);

    assert.equal(calls.length, 1);
    const [call] = calls;
    assert.ok(call);
    assert.equal(call.method, c.method);
    assert.equal(call.url.pathname, `/prefix${c.path}`);
    assert.deepEqual(queryOf(call.url), c.query ?? {});
    if (c.body === undefined) {
      assert.equal(call.body, undefined);
      assert.equal(call.headers["Content-Type"], undefined);
    } else {
      assert.deepEqual(JSON.parse(call.body ?? ""), c.body);
      assert.equal(call.headers["Content-Type"], "application/json");
    }
    assert.deepEqual(result, status === 204 ? undefined : reply);

    // The method passes its call options on: an aborted signal stops it
    // before a second request.
    await assert.rejects(
      methodOf(client, operationId)(...c.args, {
        signal: AbortSignal.abort(),
      }),
      (err: unknown) =>
        err instanceof TransportError && err.reason === "aborted",
    );
    assert.equal(calls.length, 1);
  });
}

test("a broadcast is scheduled for now without a body", async () => {
  const { calls, client } = stub(() => json(200, { id: "b1" }));
  await client.broadcasts.schedule("b1");
  assert.equal(calls[0]?.body, undefined);
});

test("emails.send and contactEvents.create pass the caller's idempotency key", async () => {
  const { calls, client } = stub(() => json(202, { id: "e1" }));
  await client.emails.send(
    { from: "app@example.com", to: "u@example.com", subject: "Hi", text: "…" },
    { idempotencyKey: "order-42:receipt" },
  );
  await client.contactEvents.create(
    { name: "signed_up", email: "u@example.com" },
    { idempotencyKey: "signup-42" },
  );
  assert.deepEqual(
    calls.map((c) => c.headers["Idempotency-Key"]),
    ["order-42:receipt", "signup-42"],
  );
});

test("without a key, each send gets its own generated one", async () => {
  const { calls, client } = stub(() => json(202, { id: "e1" }));
  const body = { from: "app@example.com", to: "u@example.com", text: "x" };
  await client.emails.send(body);
  await client.emails.send(body);
  await client.contactEvents.create({ name: "signed_up", contact_id: "c1" });
  const keys = calls.map((c) => c.headers["Idempotency-Key"]);
  for (const key of keys) {
    assert.match(key ?? "", /^[0-9a-f-]{36}$/);
  }
  assert.equal(new Set(keys).size, 3);
});

test("a generated key is reused on the retry of a send", async () => {
  let n = 0;
  const { calls, client } = stub(() =>
    n++ === 0
      ? new Response('{"error":{"code":"service_unavailable","message":"x"}}', {
          status: 503,
          headers: { "Retry-After": "1" },
        })
      : json(202, { id: "e1" }),
  );
  await client.emails.send(
    { from: "app@example.com", to: "u@example.com", text: "x" },
    { timeout: 10_000 },
  );
  assert.equal(calls.length, 2);
  assert.equal(
    calls[0]?.headers["Idempotency-Key"],
    calls[1]?.headers["Idempotency-Key"],
  );
  assert.equal(calls[0]?.body, calls[1]?.body);
});

test("no other method takes an idempotency key", async () => {
  const { calls, client } = stub(() => json(200, { id: "x1" }));
  const keyed = new Set<OperationId>(["createEmail", "createContactEvent"]);
  const others = (Object.keys(METHODS) as OperationId[]).filter(
    (operationId) => !keyed.has(operationId),
  );
  assert.equal(others.length, 35);
  for (const operationId of others) {
    await assert.rejects(
      methodOf(client, operationId)(...cases[operationId].args, {
        idempotencyKey: "k1",
      }),
      { name: "TypeError", message: /^idempotencyKey is accepted only/ },
      operationId,
    );
  }
  assert.equal(calls.length, 0);
});

test("the option is not in the types of other methods", async () => {
  const { client } = stub(() => json(201, { id: "b1" }));
  const body = {
    audience_id: "a1",
    from: "app@example.com",
    template_id: "t1",
    template_version: "published",
    unsubscribe_url: "https://app.example.com/u?t={token}",
  } as const;
  await assert.rejects(
    // @ts-expect-error: only emails.send and contactEvents.create take one
    client.broadcasts.create(body, { idempotencyKey: "k1" }),
    { message: /^idempotencyKey is accepted only/ },
  );
  await assert.rejects(
    client.webhookEndpoints.create(
      {
        url: "https://app.example.com/hooks",
        event_types: ["email.delivered"],
      },
      // @ts-expect-error: only emails.send and contactEvents.create take one
      { idempotencyKey: "k1" },
    ),
    { message: /^idempotencyKey is accepted only/ },
  );
});

test("a keyless create is sent once when its outcome is unknown", async () => {
  const { calls, client } = stub(
    () =>
      new Response('{"error":{"code":"service_unavailable","message":"x"}}', {
        status: 503,
        headers: { "Retry-After": "1" },
      }),
  );
  await assert.rejects(
    client.webhookEndpoints.create({ url: "https://app.example.com/hooks" }),
    (err: unknown) => err instanceof ApiError && err.status === 503,
  );
  await assert.rejects(
    client.broadcasts.create({
      audience_id: "a1",
      from: "app@example.com",
      template_id: "t1",
      template_version: "published",
      unsubscribe_url: "https://app.example.com/u?t={token}",
    }),
    (err: unknown) => err instanceof ApiError && err.status === 503,
  );
  assert.equal(calls.length, 2);
});

test("an id of . or .. cannot reach another operation", async () => {
  const { calls, client } = stub(() => json(200, {}));
  await assert.rejects(client.contacts.get(".."), {
    name: "TypeError",
    message: "id must not be . or ..",
  });
  await assert.rejects(client.audiences.removeContact("a1", "."), {
    name: "TypeError",
    message: "contactId must not be . or ..",
  });
  await assert.rejects(client.templates.get(".."), {
    name: "TypeError",
    message: "id must not be . or ..",
  });
  assert.equal(calls.length, 0);
});

test("resources expose only their documented methods", () => {
  const { client } = stub(() => json(200, {}));
  const expected = new Map<string, Set<string>>();
  for (const [resource, method] of Object.values(METHODS)) {
    const names = expected.get(resource) ?? new Set<string>();
    names.add(method);
    if (method.startsWith("list")) {
      names.add(method.replace(/^list/, "listAll"));
    }
    expected.set(resource, names);
  }
  for (const [resource, names] of expected) {
    const object = (client as unknown as Record<string, object>)[resource];
    assert.ok(object !== undefined, resource);
    const prototype = Reflect.getPrototypeOf(object);
    assert.ok(prototype !== null);
    assert.deepEqual(
      Reflect.ownKeys(prototype)
        .filter((key) => key !== "constructor")
        .sort(),
      [...names].sort(),
      resource,
    );
    // The shared base class adds nothing callable.
    const base = Reflect.getPrototypeOf(prototype);
    assert.ok(base !== null);
    assert.deepEqual(Reflect.ownKeys(base), ["constructor"], resource);
    assert.equal(Reflect.getPrototypeOf(base), Object.prototype);
  }
});

test("per-call options reach the request", async () => {
  const unavailable = () =>
    new Response('{"error":{"code":"service_unavailable","message":"x"}}', {
      status: 503,
      headers: { "Retry-After": "1" },
    });
  const { calls, client } = stub(unavailable);
  await assert.rejects(
    client.emails.get("e1", { maxRetries: 0 }),
    (err: unknown) => err instanceof ApiError && err.status === 503,
  );
  assert.equal(calls.length, 1);

  await assert.rejects(
    client.contacts.list(undefined, { signal: AbortSignal.abort() }),
    (err: unknown) => err instanceof TransportError && err.reason === "aborted",
  );
  await assert.rejects(client.usage.get({}, { timeout: 0 }), RangeError);
  assert.equal(calls.length, 1);
});

test("bad path parameters reject before any request; the message names, not echoes", async () => {
  const { calls, client } = stub(() => json(200, {}));
  const first = (items: AsyncIterator<unknown>) => items.next();
  const bad: [string, () => Promise<unknown>][] = [
    ["id", () => client.emails.get("")],
    ["id", () => client.contacts.get(12345 as unknown as string)],
    ["id", () => client.contacts.delete("")],
    ["contactId", () => client.audiences.addContact("a1", "")],
    ["contactId", () => client.audiences.removeContact("a1", "")],
    ["id", () => first(client.emails.listAllEvents(""))],
    [
      "id",
      () => first(client.audiences.listAllContacts(98765 as unknown as string)),
    ],
    ["id", () => first(client.automations.listAllRuns(""))],
    ["version", () => client.templates.getVersion("t1", 0)],
    ["version", () => client.templates.getVersion("t1", 1.5)],
    [
      "version",
      () => client.templates.getVersion("t1", "4" as unknown as number),
    ],
  ];
  for (const [name, call] of bad) {
    await assert.rejects(call(), (err: unknown) => {
      assert.ok(err instanceof TypeError);
      assert.match(err.message, new RegExp(`^${name} must be`));
      assert.doesNotMatch(err.message, /12345|98765|1\.5/);
      return true;
    });
  }
  assert.equal(calls.length, 0);
});

test("listAll walks every page with the same filters", async () => {
  const pages: Record<string, unknown> = {
    "": { data: [{ id: "e1" }, { id: "e2" }], next_cursor: "k2" },
    k2: { data: [{ id: "e3" }], next_cursor: "k3" },
    k3: { data: [], next_cursor: null },
  };
  const { calls, client } = stub((call) =>
    json(200, pages[call.url.searchParams.get("cursor") ?? ""]),
  );
  const items = client.emails.listAll({ status: ["failed"], limit: 2 });
  assert.equal(calls.length, 0, "no request before the first next()");

  const ids: string[] = [];
  for await (const email of items) {
    ids.push(email.id);
  }
  assert.deepEqual(ids, ["e1", "e2", "e3"]);
  assert.deepEqual(
    calls.map((c) => queryOf(c.url)),
    [
      { status: ["failed"], limit: ["2"] },
      { status: ["failed"], limit: ["2"], cursor: ["k2"] },
      { status: ["failed"], limit: ["2"], cursor: ["k3"] },
    ],
  );
});

test("listAll starts at the first page even when given a cursor", async () => {
  const { calls, client } = stub(() => json(200, page));
  const filters = { cursor: "stale" } as { limit?: number };
  const items: unknown[] = [];
  for await (const item of client.contacts.listAll(filters)) {
    items.push(item);
  }
  assert.deepEqual(items, []);
  assert.deepEqual(queryOf(calls[0]?.url ?? new URL(baseUrl)), {});
});

test("every list iterator keeps its filters and options on every page", async () => {
  const { calls, client } = stub((call) =>
    json(
      200,
      call.url.searchParams.has("cursor")
        ? { data: [{ id: "i2" }], next_cursor: null }
        : { data: [{ id: "i1" }], next_cursor: "k2" },
    ),
  );
  type Options = { signal?: AbortSignal };
  const iterators: [
    string,
    (options?: Options) => AsyncGenerator<{ id: string }, void, undefined>,
  ][] = [
    ["/v1/emails", (o) => client.emails.listAll({ limit: 7 }, o)],
    [
      "/v1/emails/e1/events",
      (o) => client.emails.listAllEvents("e1", { limit: 7 }, o),
    ],
    ["/v1/contacts", (o) => client.contacts.listAll({ limit: 7 }, o)],
    ["/v1/audiences", (o) => client.audiences.listAll({ limit: 7 }, o)],
    [
      "/v1/audiences/a1/contacts",
      (o) => client.audiences.listAllContacts("a1", { limit: 7 }, o),
    ],
    ["/v1/broadcasts", (o) => client.broadcasts.listAll({ limit: 7 }, o)],
    ["/v1/automations", (o) => client.automations.listAll({ limit: 7 }, o)],
    [
      "/v1/automations/m1/runs",
      (o) => client.automations.listAllRuns("m1", { limit: 7 }, o),
    ],
  ];
  for (const [path, iterate] of iterators) {
    calls.length = 0;
    const ids: string[] = [];
    for await (const item of iterate()) {
      ids.push(item.id);
    }
    assert.deepEqual(ids, ["i1", "i2"], path);
    assert.deepEqual(
      calls.map((c) => [c.url.pathname, queryOf(c.url)]),
      [
        [`/prefix${path}`, { limit: ["7"] }],
        [`/prefix${path}`, { limit: ["7"], cursor: ["k2"] }],
      ],
      path,
    );

    // The options reach the second page too.
    calls.length = 0;
    const controller = new AbortController();
    const items = iterate({ signal: controller.signal });
    assert.equal((await items.next()).done, false, path);
    controller.abort();
    await assert.rejects(
      items.next(),
      (err: unknown) =>
        err instanceof TransportError && err.reason === "aborted",
      path,
    );
    assert.equal(calls.length, 1, path);
  }
});

test("listAll applies the call options to every page", async () => {
  const controller = new AbortController();
  const { calls, client } = stub(() =>
    json(200, { data: [{ id: "e1" }], next_cursor: "k2" }),
  );
  const items = client.emails.listAll({}, { signal: controller.signal });
  const first = await items.next();
  assert.equal(first.done, false);
  controller.abort();
  await assert.rejects(
    items.next(),
    (err: unknown) => err instanceof TransportError && err.reason === "aborted",
  );
  assert.equal(calls.length, 1);
});

// Compile-time: methods take and return the generated schema types unchanged.
type S = components["schemas"];
type Same<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false;
type Expect<T extends true> = T;
export type SchemaChecks = [
  Expect<Same<Body<"createEmail">, S["CreateEmailRequest"]>>,
  Expect<Same<Result<"createEmail">, S["CreateEmailResponse"]>>,
  Expect<Same<Result<"getEmail">, S["Email"]>>,
  Expect<Same<Result<"listEmails">, S["EmailList"]>>,
  Expect<Same<Item<"listEmails">, S["Email"]>>,
  Expect<Same<Item<"listEmailEvents">, S["EmailEvent"]>>,
  Expect<Same<Body<"createBroadcast">, S["CreateBroadcastRequest"]>>,
  Expect<Same<Result<"createContactEvent">, S["ContactEvent"]>>,
  Expect<Same<Result<"getUsage">, S["Usage"]>>,
  Expect<Same<Result<"createWebhookEndpoint">, S["WebhookEndpointCreated"]>>,
  Expect<Same<Result<"deleteContact">, undefined>>,
  Expect<Same<Awaited<ReturnType<Emails["get"]>>, S["Email"]>>,
  Expect<Same<Awaited<ReturnType<Contacts["delete"]>>, void>>,
];

// Compile-time: the entry exports the resource and option types.
export type EntryTypes = [
  entry.Emails,
  entry.Contacts,
  entry.Audiences,
  entry.Unsubscribes,
  entry.Broadcasts,
  entry.Automations,
  entry.AutomationRuns,
  entry.ContactEvents,
  entry.Usage,
  entry.Templates,
  entry.WebhookEndpoints,
  entry.RequestOptions,
  entry.IdempotentRequestOptions,
];
