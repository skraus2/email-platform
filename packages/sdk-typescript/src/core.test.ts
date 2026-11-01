import assert from "node:assert/strict";
import { test } from "node:test";
import { inspect } from "node:util";

import { createSender, type Runtime, type Settings } from "./core.ts";
import { ApiError, TransportError } from "./errors.ts";
import { OPERATIONS, type OperationId } from "./operations.ts";
import { parseBaseUrl } from "./url.ts";

const apiKey = "ep_live_secret-key-123";

type Reply = Response | Error | ((init: RequestInit) => Promise<Response>);

type Sent = { url: string; init: RequestInit; at: number };

/** A stub transport and a clock that only moves when the core sleeps. */
function harness(
  replies: Reply[],
  overrides: Partial<Settings> & {
    deadlineFires?: boolean;
    /** Waits never end on their own; only their signal can end them. */
    pendingSleep?: boolean;
  } = {},
) {
  let now = 1_000_000;
  let uuid = 0;
  const sent: Sent[] = [];
  const waits: number[] = [];
  const deadlines: number[] = [];
  const runtime: Runtime = {
    fetch: (input, init) => {
      sent.push({ url: input, init, at: now });
      const reply = replies.shift();
      if (reply === undefined) {
        return Promise.reject(new Error("unexpected request"));
      }
      if (init.signal?.aborted === true) {
        return Promise.reject(init.signal.reason as Error);
      }
      if (reply instanceof Response) {
        return Promise.resolve(reply);
      }
      if (reply instanceof Error) {
        return Promise.reject(reply);
      }
      return reply(init);
    },
    now: () => now,
    sleep: (ms, signal) => {
      waits.push(ms);
      if (overrides.pendingSleep === true) {
        return new Promise((_resolve, reject) => {
          signal?.addEventListener("abort", () => {
            reject(signal.reason as Error);
          });
        });
      }
      now += ms;
      if (signal?.aborted === true) {
        return Promise.reject(signal.reason as Error);
      }
      return Promise.resolve();
    },
    deadlineSignal: (ms) => {
      deadlines.push(ms);
      return overrides.deadlineFires === true
        ? AbortSignal.abort(new DOMException("deadline", "TimeoutError"))
        : new AbortController().signal;
    },
    random: () => 0,
    randomUUID: () => `generated-key-${++uuid}`,
  };
  const settings: Settings = {
    apiKey,
    base: parseBaseUrl("https://api.example.com/prefix"),
    timeout: 30_000,
    maxRetries: 2,
    ...overrides,
  };
  return { send: createSender(settings, runtime), sent, waits, deadlines };
}

function json(
  status: number,
  body: unknown,
  headers: Record<string, string> = {},
) {
  return new Response(JSON.stringify(body), {
    status,
    headers: {
      "Content-Type": "application/json",
      "X-Request-Id": "req_1",
      ...headers,
    },
  });
}

function apiError(
  status: number,
  code: string,
  headers: Record<string, string> = {},
) {
  return json(
    status,
    { error: { code, message: `server says ${code}` } },
    headers,
  );
}

function networkError(code = "ECONNRESET") {
  return new TypeError(
    "fetch failed to https://api.example.com/prefix/v1/emails?recipient=x",
    {
      cause: Object.assign(new Error(`read ${code} 10.0.0.1:443`), { code }),
    },
  );
}

const email = {
  from: "app@example.com",
  to: "user@example.com",
  subject: "Hi",
  text: "body with whsec_request_secret",
};

function header(sent: Sent | undefined, name: string): string | undefined {
  return (sent?.init.headers as Record<string, string> | undefined)?.[name];
}

// --- request building (AC3) ---

test("sends the key, fixed headers and a JSON body; never follows redirects", async () => {
  const h = harness([json(202, { id: "e1" })]);
  const result = await h.send("createEmail", { body: email });
  assert.deepEqual(result, {
    status: 202,
    data: { id: "e1" },
    requestId: "req_1",
  });
  const req = h.sent.at(0);
  assert.equal(req?.url, "https://api.example.com/prefix/v1/emails");
  assert.equal(req.init.method, "POST");
  assert.equal(req.init.redirect, "manual");
  assert.equal(header(req, "Authorization"), `Bearer ${apiKey}`);
  assert.equal(header(req, "Accept"), "application/json");
  assert.equal(header(req, "User-Agent"), "email-platform-sdk-typescript");
  assert.equal(header(req, "Content-Type"), "application/json");
  assert.equal(req.init.body, JSON.stringify(email));
});

test("a request without body has no Content-Type; path and query are encoded", async () => {
  const h = harness([json(200, { data: [], next_cursor: null })]);
  await h.send("listEmails", {
    query: {
      recipient: "a+b@example.com",
      status: ["queued", "sent"],
      cursor: undefined,
    },
  });
  const req = h.sent.at(0);
  assert.equal(
    req?.url,
    "https://api.example.com/prefix/v1/emails?recipient=a%2Bb%40example.com&status=queued&status=sent",
  );
  assert.equal(req.init.body, undefined);
  assert.equal(header(req, "Content-Type"), undefined);
  assert.equal(header(req, "Idempotency-Key"), undefined);
});

test("a body on a read throws before any request", async () => {
  const h = harness([]);
  await assert.rejects(h.send("getUsage", { body: {} }), {
    name: "TypeError",
    message: "getUsage takes no request body",
  });
  assert.equal(h.sent.length, 0);
});

test("a missing or dot-segment path parameter throws before any request", async () => {
  const h = harness([]);
  await assert.rejects(h.send("getEmail", {}), TypeError);
  await assert.rejects(
    h.send("removeAudienceContact", { path: { id: "..", contact_id: "c1" } }),
    TypeError,
  );
  assert.equal(h.sent.length, 0);
});

// --- errors (AC4, AC5) ---

test("a JSON error gives ApiError with the server's code, message, Retry-After and request id", async () => {
  const h = harness([
    apiError(422, "validation_failed", { "Retry-After": "7" }),
  ]);
  const err = await h
    .send("createBroadcast", { body: {} })
    .catch((e: unknown) => e);
  assert.ok(err instanceof ApiError);
  assert.equal(err.name, "ApiError");
  assert.equal(err.status, 422);
  assert.equal(err.code, "validation_failed");
  assert.equal(err.message, "server says validation_failed");
  assert.equal(err.retryAfter, 7);
  assert.equal(err.requestId, "req_1");
  assert.equal(err.operationId, "createBroadcast");
  assert.equal(err.cause, undefined);
});

test("a body that is not the error envelope: http_error, or service_unavailable on 503; proxy text dropped", async () => {
  const cases: [Response, number, string][] = [
    [
      new Response("<html>bad gateway whsec_proxy</html>", { status: 502 }),
      502,
      "http_error",
    ],
    [new Response("", { status: 400 }), 400, "http_error"],
    [json(404, { error: { code: 5 } }), 404, "http_error"],
    [
      new Response("upstream down", { status: 503 }),
      503,
      "service_unavailable",
    ],
  ];
  for (const [reply, status, code] of cases) {
    const requestId = reply.headers.get("X-Request-Id") ?? undefined;
    const h = harness([reply], { maxRetries: 0 });
    const err = await h
      .send("getEmail", { path: { id: "e1" } })
      .catch((e: unknown) => e);
    assert.ok(err instanceof ApiError);
    assert.equal(err.status, status);
    assert.equal(err.code, code);
    assert.equal(err.message, `HTTP ${status}`);
    assert.equal(err.requestId, requestId);
  }
});

test("an error code that is not snake_case falls back; a long message is cut", async () => {
  const odd = harness(
    [json(400, { error: { code: "Not A Code <script>", message: "m" } })],
    { maxRetries: 0 },
  );
  const err = await odd
    .send("getEmail", { path: { id: "e1" } })
    .catch((e: unknown) => e);
  assert.ok(err instanceof ApiError);
  assert.equal(err.code, "http_error");
  assert.equal(err.message, "HTTP 400");

  const long = harness(
    [
      json(422, {
        error: { code: "validation_failed", message: "x".repeat(5000) },
      }),
    ],
    { maxRetries: 0 },
  );
  const cut = await long
    .send("getEmail", { path: { id: "e1" } })
    .catch((e: unknown) => e);
  assert.ok(cut instanceof ApiError);
  assert.equal(cut.code, "validation_failed");
  assert.equal(cut.message.length, 1024);
});

test("control characters in the API's message become spaces", async () => {
  const h = harness(
    [
      json(400, {
        error: {
          code: "bad",
          message: "a\r\nFAKE log line\u0000\t\u007f\u0085 b",
        },
      }),
    ],
    { maxRetries: 0 },
  );
  const err = await h
    .send("getEmail", { path: { id: "e1" } })
    .catch((e: unknown) => e);
  assert.ok(err instanceof ApiError);
  // C0, DEL and C1 are replaced; U+2028 is a separator, not a control.
  assert.equal(err.message, "a  FAKE log line     b");
});

test("an X-Request-Id that is not a server id is dropped", async () => {
  const h = harness([
    apiError(404, "not_found", { "X-Request-Id": "x".repeat(65) }),
  ]);
  const err = await h
    .send("getEmail", { path: { id: "e1" } })
    .catch((e: unknown) => e);
  assert.ok(err instanceof ApiError);
  assert.equal(err.requestId, undefined);
});

test("a redirect is an error, is not followed and its body is cancelled", async () => {
  for (const status of [301, 307]) {
    let canceled = false;
    const h = harness([
      new Response(
        streamOf(64, 1024, () => {
          canceled = true;
        }),
        { status, headers: { Location: "https://evil.example/" } },
      ),
    ]);
    const err = await h
      .send("getEmail", { path: { id: "e1" } })
      .catch((e: unknown) => e);
    assert.ok(err instanceof ApiError);
    assert.equal(err.status, status);
    assert.equal(err.code, "redirect");
    assert.equal(h.sent.length, 1);
    assert.ok(canceled, String(status));
  }
});

// --- transport errors (AC6, AC7) ---

test("a network failure gives TransportError with the path template and system code only", async () => {
  const h = harness([networkError("ECONNREFUSED")], { maxRetries: 0 });
  const err = await h
    .send("listEmails", { query: { recipient: "user@example.com" } })
    .catch((e: unknown) => e);
  assert.ok(err instanceof TransportError);
  assert.equal(err.reason, "network");
  assert.equal(err.method, "GET");
  assert.equal(err.path, "/v1/emails");
  assert.equal(err.operationId, "listEmails");
  assert.equal(err.systemCode, "ECONNREFUSED");
  assert.equal(err.cause, undefined);
  for (const text of [
    err.message,
    err.stack ?? "",
    inspect(err),
    JSON.stringify(err),
  ]) {
    assert.ok(!text.includes("user@example.com"), text);
    assert.ok(!text.includes("api.example.com"), text);
    assert.ok(!text.includes("10.0.0.1"), text);
  }
});

test("a system code that is not a plain code is dropped", async () => {
  const err = new TypeError("fetch failed", {
    cause: Object.assign(new Error("x"), { code: "user@example.com" }),
  });
  const h = harness([err], { maxRetries: 0 });
  const got = await h
    .send("getEmail", { path: { id: "e1" } })
    .catch((e: unknown) => e);
  assert.ok(got instanceof TransportError);
  assert.equal(got.systemCode, undefined);
});

test("the call deadline gives reason timeout, bounded by the configured timeout", async () => {
  const h = harness([networkError()], { deadlineFires: true, timeout: 5000 });
  const err = await h
    .send("getEmail", { path: { id: "e1" } })
    .catch((e: unknown) => e);
  assert.ok(err instanceof TransportError);
  assert.equal(err.reason, "timeout");
  assert.deepEqual(h.deadlines, [5000]);
  assert.equal(h.sent.length, 1);
});

test("an aborted signal gives reason aborted with the signal's reason as cause", async () => {
  const controller = new AbortController();
  const reason = new Error("shutting down");
  controller.abort(reason);
  const h = harness([json(200, {})]);
  const err = await h
    .send("getEmail", { path: { id: "e1" } }, { signal: controller.signal })
    .catch((e: unknown) => e);
  assert.ok(err instanceof TransportError);
  assert.equal(err.reason, "aborted");
  assert.equal(err.cause, reason);
  assert.equal(h.sent.length, 0);
});

/** A request that only ends when the signal fetch was given aborts. */
function hangingRequest(onStart: () => void): Reply {
  return (init) =>
    new Promise((_resolve, reject) => {
      init.signal?.addEventListener("abort", () => {
        reject(init.signal?.reason as Error);
      });
      onStart();
    });
}

test("an abort during a request reaches fetch and stops without a retry", async () => {
  const controller = new AbortController();
  const reason = new Error("stop");
  const h = harness([
    hangingRequest(() =>
      setImmediate(() => {
        controller.abort(reason);
      }),
    ),
    json(200, {}),
  ]);
  const err = await h
    .send("getEmail", { path: { id: "e1" } }, { signal: controller.signal })
    .catch((e: unknown) => e);
  assert.ok(err instanceof TransportError);
  assert.equal(err.reason, "aborted");
  assert.equal(err.cause, reason);
  assert.equal(h.sent.length, 1);
});

test("a 2xx body that is not JSON gives invalid_body without the parser's text", async () => {
  for (const body of ["whsec_response_secret not json", "", "￾\uD800"]) {
    const h = harness([new Response(body, { status: 201 })]);
    const err = await h
      .send("createWebhookEndpoint", { body: {} })
      .catch((e: unknown) => e);
    assert.ok(err instanceof TransportError, JSON.stringify(body));
    assert.equal(err.reason, "invalid_body");
    assert.equal(err.cause, undefined);
    for (const text of [
      err.message,
      err.stack ?? "",
      inspect(err),
      JSON.stringify(err),
    ]) {
      assert.ok(!text.includes("whsec_"), text);
    }
    assert.equal(h.sent.length, 1);
  }
});

test("invalid UTF-8 in a 2xx body gives invalid_body", async () => {
  const h = harness([
    new Response(new Uint8Array([0x7b, 0xff, 0x7d]), { status: 200 }),
  ]);
  const err = await h
    .send("getEmail", { path: { id: "e1" } })
    .catch((e: unknown) => e);
  assert.ok(err instanceof TransportError);
  assert.equal(err.reason, "invalid_body");
});

test("a 204 resolves without data", async () => {
  const h = harness([
    new Response(null, { status: 204, headers: { "X-Request-Id": "r2" } }),
  ]);
  assert.deepEqual(await h.send("deleteContact", { path: { id: "c1" } }), {
    status: 204,
    data: undefined,
    requestId: "r2",
  });
});

function streamOf(chunks: number, chunkSize: number, onCancel: () => void) {
  let sentChunks = 0;
  return new ReadableStream<Uint8Array>({
    pull(controller) {
      if (sentChunks === chunks) {
        controller.close();
        return;
      }
      sentChunks++;
      controller.enqueue(new Uint8Array(chunkSize).fill(0x20));
    },
    cancel() {
      onCancel();
    },
  });
}

test("a body above 10 MiB gives body_too_large and the stream is cancelled", async () => {
  let canceled = false;
  const body = streamOf(64, 1024 * 1024, () => {
    canceled = true;
  });
  const h = harness([new Response(body, { status: 200 })]);
  const err = await h
    .send("getEmail", { path: { id: "e1" } })
    .catch((e: unknown) => e);
  assert.ok(err instanceof TransportError);
  assert.equal(err.reason, "body_too_large");
  assert.ok(canceled);
  assert.equal(h.sent.length, 1);
});

test("a Content-Length above 10 MiB is refused before reading", async () => {
  let canceled = false;
  const body = streamOf(1, 10, () => {
    canceled = true;
  });
  const h = harness([
    new Response(body, {
      status: 200,
      headers: { "Content-Length": String(10 * 1024 * 1024 + 1) },
    }),
  ]);
  const err = await h
    .send("getEmail", { path: { id: "e1" } })
    .catch((e: unknown) => e);
  assert.ok(err instanceof TransportError);
  assert.equal(err.reason, "body_too_large");
  assert.ok(canceled);
});

test("exactly 10 MiB is read", async () => {
  const size = 10 * 1024 * 1024;
  const bytes = new Uint8Array(size).fill(0x20);
  bytes[0] = 0x22;
  bytes[size - 1] = 0x22;
  const h = harness([new Response(bytes, { status: 200 })]);
  const result = await h.send("getEmail", { path: { id: "e1" } });
  assert.equal((result.data as string).length, size - 2);
});

test("an oversized error body is treated as no error envelope", async () => {
  const body = streamOf(11, 1024 * 1024, () => undefined);
  const h = harness([new Response(body, { status: 500 })], { maxRetries: 0 });
  const err = await h
    .send("createBroadcast", { body: {} })
    .catch((e: unknown) => e);
  assert.ok(err instanceof ApiError);
  assert.equal(err.code, "http_error");
});

// --- retries (AC8–AC12) ---

const allOperations: OperationId[] = [
  "createEmail",
  "listEmails",
  "getEmail",
  "listEmailEvents",
  "cancelEmail",
  "createContact",
  "listContacts",
  "getContact",
  "updateContact",
  "deleteContact",
  "createAudience",
  "listAudiences",
  "getAudience",
  "addAudienceContacts",
  "listAudienceContacts",
  "addAudienceContact",
  "removeAudienceContact",
  "reportUnsubscribe",
  "createBroadcast",
  "listBroadcasts",
  "getBroadcast",
  "scheduleBroadcast",
  "cancelBroadcast",
  "createAutomation",
  "listAutomations",
  "getAutomation",
  "updateAutomation",
  "listAutomationRuns",
  "getAutomationRun",
  "cancelAutomationRun",
  "createContactEvent",
  "getUsage",
  "createTemplate",
  "getTemplate",
  "publishTemplateVersion",
  "getTemplateVersion",
  "createWebhookEndpoint",
];

function params(id: OperationId) {
  const path = { id: "x1", contact_id: "c1", version: 1 };
  return OPERATIONS[id].method === "GET" ? { path } : { path, body: {} };
}

test("429 is retried on every operation after Retry-After plus jitter", async () => {
  for (const id of allOperations) {
    const h = harness([
      apiError(429, "rate_limited", { "Retry-After": "3" }),
      json(200, {}),
    ]);
    const result = await h.send(id, params(id));
    assert.equal(result.status, 200, id);
    assert.deepEqual(h.waits, [3000], id);
    assert.equal(h.sent.length, 2, id);
  }
});

test("Retry-After is clamped to 1–60 s and jittered; unusable values fall back to backoff", async () => {
  const cases: [string | undefined, number][] = [
    ["0", 1000],
    ["120", 60_000],
    ["abc", 250],
    [undefined, 250],
  ];
  for (const [value, wait] of cases) {
    const headers: Record<string, string> =
      value === undefined ? {} : { "Retry-After": value };
    const h = harness([apiError(429, "rate_limited", headers), json(200, {})], {
      timeout: 600_000,
    });
    await h.send("createBroadcast", { body: {} });
    assert.deepEqual(h.waits, [wait], String(value));
  }
});

const unknownOutcome: [string, () => Reply][] = [
  [
    "503 service_unavailable",
    () => apiError(503, "service_unavailable", { "Retry-After": "2" }),
  ],
  ["503 without a code", () => new Response("busy", { status: 503 })],
  ["500", () => apiError(500, "internal_error")],
  ["502", () => new Response("bad gateway", { status: 502 })],
  ["504", () => new Response("gateway timeout", { status: 504 })],
  ["network error", () => networkError()],
  ["undici connect timeout", () => networkError("UND_ERR_CONNECT_TIMEOUT")],
];

const retryable = new Set<OperationId>([
  "createEmail",
  "createContactEvent",
  "listEmails",
  "getEmail",
  "listEmailEvents",
  "listContacts",
  "getContact",
  "listAudiences",
  "getAudience",
  "listAudienceContacts",
  "listBroadcasts",
  "getBroadcast",
  "listAutomations",
  "getAutomation",
  "listAutomationRuns",
  "getAutomationRun",
  "getUsage",
  "getTemplate",
  "getTemplateVersion",
]);

test("unknown outcomes are retried only for reads, createEmail and createContactEvent", async () => {
  for (const [label, reply] of unknownOutcome) {
    for (const id of allOperations) {
      const h = harness([reply(), json(200, {})]);
      const outcome = await h.send(id, params(id)).then(
        (r) => r.status,
        (e: unknown) => e,
      );
      if (retryable.has(id)) {
        assert.equal(outcome, 200, `${label} ${id}`);
        assert.equal(h.sent.length, 2, `${label} ${id}`);
      } else {
        assert.ok(outcome instanceof Error, `${label} ${id}`);
        assert.equal(h.sent.length, 1, `${label} ${id}`);
      }
    }
  }
});

test("the 503 wait follows its Retry-After plus jitter", async () => {
  const h = harness([
    apiError(503, "service_unavailable", { "Retry-After": "2" }),
    json(202, {}),
  ]);
  await h.send("createEmail", { body: email });
  assert.deepEqual(h.waits, [2000]);
});

test("permanent answers are never retried", async () => {
  const cases: Response[] = [
    apiError(503, "webhook_registration_disabled"),
    apiError(409, "idempotency_key_reused"),
    apiError(400, "invalid_json"),
    apiError(401, "unauthorized"),
    apiError(403, "forbidden"),
    apiError(404, "not_found"),
    apiError(422, "validation_failed"),
  ];
  for (const reply of cases) {
    const h = harness([reply, json(200, {})]);
    const err = await h
      .send("createEmail", { body: email })
      .catch((e: unknown) => e);
    assert.ok(err instanceof ApiError);
    assert.equal(h.sent.length, 1, err.code);
  }
});

test("a timeout is never retried", async () => {
  const h = harness([networkError(), json(200, {})], { deadlineFires: true });
  const err = await h
    .send("getEmail", { path: { id: "e1" } })
    .catch((e: unknown) => e);
  assert.ok(err instanceof TransportError);
  assert.equal(err.reason, "timeout");
  assert.equal(h.sent.length, 1);
});

test("backoff without Retry-After grows per attempt", async () => {
  const h = harness(
    [networkError(), networkError(), networkError(), json(200, {})],
    { maxRetries: 3 },
  );
  await h.send("getEmail", { path: { id: "e1" } });
  assert.deepEqual(h.waits, [250, 500, 1000]);
});

test("at most maxRetries retries; 0 sends once; values above 5 are clamped per call", async () => {
  const fail = () => apiError(429, "rate_limited", { "Retry-After": "1" });
  const h = harness([fail(), fail(), fail()]);
  const err = await h
    .send("getEmail", { path: { id: "e1" } })
    .catch((e: unknown) => e);
  assert.ok(err instanceof ApiError);
  assert.equal(h.sent.length, 3);

  const once = harness([fail()], { maxRetries: 0 });
  await assert.rejects(once.send("getEmail", { path: { id: "e1" } }), ApiError);
  assert.equal(once.sent.length, 1);

  const many = harness(Array.from({ length: 10 }, fail), { timeout: 600_000 });
  await assert.rejects(
    many.send("getEmail", { path: { id: "e1" } }, { maxRetries: 50 }),
    ApiError,
  );
  assert.equal(many.sent.length, 6);
});

test("a wait that would reach the deadline is not scheduled; the error is thrown", async () => {
  const h = harness(
    [apiError(429, "rate_limited", { "Retry-After": "30" }), json(200, {})],
    { timeout: 10_000 },
  );
  const err = await h
    .send("createEmail", { body: email })
    .catch((e: unknown) => e);
  assert.ok(err instanceof ApiError);
  assert.equal(err.retryAfter, 30);
  assert.deepEqual(h.waits, []);
  assert.equal(h.sent.length, 1);
});

test("the remaining time bounds every attempt", async () => {
  const h = harness(
    [apiError(429, "rate_limited", { "Retry-After": "4" }), json(200, {})],
    { timeout: 10_000 },
  );
  await h.send("getEmail", { path: { id: "e1" } });
  assert.deepEqual(h.deadlines, [10_000, 6000]);
});

test("an abort during a wait reaches the wait and stops before the next request", async () => {
  const controller = new AbortController();
  const reason = new Error("stop");
  const h = harness(
    [apiError(429, "rate_limited", { "Retry-After": "1" }), json(200, {})],
    { pendingSleep: true },
  );
  const call = h
    .send("getEmail", { path: { id: "e1" } }, { signal: controller.signal })
    .catch((e: unknown) => e);
  setImmediate(() => {
    controller.abort(reason);
  });
  const err = await call;
  assert.ok(err instanceof TransportError);
  assert.equal(err.reason, "aborted");
  assert.equal(err.cause, reason);
  assert.deepEqual(h.waits, [1000]);
  assert.equal(h.sent.length, 1);
});

test("a wait that would end exactly at the deadline is not scheduled", async () => {
  const h = harness(
    [apiError(429, "rate_limited", { "Retry-After": "9" }), json(200, {})],
    { timeout: 9000 },
  );
  await assert.rejects(h.send("getEmail", { path: { id: "e1" } }), ApiError);
  assert.deepEqual(h.waits, []);
  const fits = harness(
    [apiError(429, "rate_limited", { "Retry-After": "9" }), json(200, {})],
    { timeout: 9001 },
  );
  assert.equal(
    (await fits.send("getEmail", { path: { id: "e1" } })).status,
    200,
  );
});

// --- idempotency (AC13, AC14) ---

test("the caller's idempotency key is sent on every retry with identical bytes", async () => {
  let serialised = 0;
  const body = {
    ...email,
    toJSON() {
      serialised++;
      return email;
    },
  };
  const h = harness([
    apiError(503, "service_unavailable", { "Retry-After": "2" }),
    networkError(),
    json(202, { id: "e1" }),
  ]);
  await h.send("createEmail", { body }, { idempotencyKey: "order-42:receipt" });
  assert.equal(h.sent.length, 3);
  assert.equal(serialised, 1);
  for (const req of h.sent) {
    assert.equal(header(req, "Idempotency-Key"), "order-42:receipt");
    assert.equal(req.init.body, JSON.stringify(email));
  }
});

test("without a key, one is generated per logical call and reused on its retries", async () => {
  const h = harness([
    networkError(),
    json(202, { id: "e1" }),
    json(202, { id: "e2" }),
  ]);
  await h.send("createEmail", { body: email });
  await h.send("createContactEvent", { body: {} });
  assert.deepEqual(
    h.sent.map((r) => header(r, "Idempotency-Key")),
    ["generated-key-1", "generated-key-1", "generated-key-2"],
  );
});

test("invalid idempotency keys throw before any request without echoing the key", async () => {
  const h = harness([]);
  for (const key of ["", "has space", "ümlaut", "x".repeat(256), "tab\tkey"]) {
    const err = await h
      .send("createEmail", { body: email }, { idempotencyKey: key })
      .catch((e: unknown) => e);
    assert.ok(err instanceof TypeError, JSON.stringify(key));
    if (key !== "") {
      assert.ok(!err.message.includes(key));
    }
  }
  assert.equal(h.sent.length, 0);
  const ok = harness([json(202, { id: "e1" })]);
  await ok.send(
    "createEmail",
    { body: email },
    { idempotencyKey: "!~".repeat(127) + "x" },
  );
  assert.equal(ok.sent.length, 1);
});

test("a key that is not a string throws instead of being stringified", async () => {
  const h = harness([]);
  for (const key of [null, 42, {}]) {
    await assert.rejects(
      h.send(
        "createEmail",
        { body: email },
        {
          idempotencyKey: key as unknown as string,
        },
      ),
      TypeError,
    );
  }
  assert.equal(h.sent.length, 0);
});

test("other operations refuse an idempotency key", async () => {
  const h = harness([]);
  for (const id of [
    "createBroadcast",
    "createWebhookEndpoint",
    "getEmail",
  ] as const) {
    await assert.rejects(h.send(id, params(id), { idempotencyKey: "k1" }), {
      name: "TypeError",
      message:
        "idempotencyKey is accepted only by createEmail and createContactEvent",
    });
  }
  assert.equal(h.sent.length, 0);
});

// --- per-call options (AC1) ---

test("per-call timeout and maxRetries are validated like the client options", async () => {
  const h = harness([]);
  for (const timeout of [0, -1, Number.NaN, Infinity, 2 ** 31]) {
    await assert.rejects(h.send("getUsage", {}, { timeout }), RangeError);
  }
  for (const maxRetries of [-1, 1.5, Number.NaN]) {
    await assert.rejects(h.send("getUsage", {}, { maxRetries }), RangeError);
  }
  assert.equal(h.sent.length, 0);
});

// --- redaction of errors (AC15) ---

test("errors never contain the key, the query, the request body or response secrets", async () => {
  const replies: Reply[] = [
    apiError(422, "validation_failed"),
    new Response("<html>whsec_proxy_secret</html>", { status: 502 }),
    new Response("whsec_response_secret", { status: 200 }),
    networkError(),
  ];
  for (const reply of replies) {
    const h = harness([reply], { maxRetries: 0 });
    const err = await h
      .send("createEmail", {
        body: email,
        query: { recipient: "user@example.com" },
      })
      .catch((e: unknown) => e);
    assert.ok(err instanceof Error);
    const texts = [
      err.message,
      err.stack ?? "",
      inspect(err, { showHidden: true, depth: 10 }),
      JSON.stringify(err),
    ];
    for (const text of texts) {
      for (const secret of [apiKey, "user@example.com", "whsec_", "Bearer"]) {
        assert.ok(!text.includes(secret), `${secret} in ${text}`);
      }
    }
  }
});
