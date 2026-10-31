import assert from "node:assert/strict";
import { createHmac } from "node:crypto";
import { test } from "node:test";
import { inspect } from "node:util";
import { runInNewContext } from "node:vm";

import {
  verifyWebhook,
  WebhookVerificationError,
  type VerifyWebhookOptions,
  type WebhookHeaders,
  type WebhookVerificationReason,
} from "./webhook.ts";

// Published test vector of the platform's signer (computed independently
// with Python's hmac module); the same constants as the server's tests.
const secret = "whsec_7Xq2t9Lm4Rk8Vp1Wn6Ys3Zb5Hc0Jd2Fg4Ae6Ui8Oo";
const timestamp = "1760428800";
const eventId = "0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0c";
const body = `{"id":"${eventId}","type":"email.delivered","created_at":"2026-10-14T08:00:00Z","data":{"email_id":"0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b","project_id":"0199e39f-0000-7000-8000-000000000001"}}`;
const signature =
  "v1=a3828bc3f00cff5d3a0d2e5d9e16472c13b89cdfabf73dbbca8de569f559a7a4";
const mac = signature.slice("v1=".length);

const second = 1000;
const minute = 60 * second;
const signedAt = Number(timestamp) * second;
const now = signedAt + 2 * minute;

function sign(payload: string, ts = timestamp, key = secret): string {
  return (
    "v1=" +
    createHmac("sha256", key).update(`${ts}.${payload}`, "utf8").digest("hex")
  );
}

function headers(
  overrides: Record<string, string | string[] | undefined> = {},
): Record<string, string | string[] | undefined> {
  return {
    "webhook-id": eventId,
    "webhook-timestamp": timestamp,
    "webhook-signature": signature,
    ...overrides,
  };
}

function options(
  overrides: Partial<Record<keyof VerifyWebhookOptions, unknown>> = {},
): VerifyWebhookOptions {
  return {
    secret,
    headers: headers(),
    body,
    now,
    ...overrides,
  } as VerifyWebhookOptions;
}

test("the published vector verifies", () => {
  const result = verifyWebhook(options());
  assert.equal(result.id, eventId);
  assert.deepEqual(result.event, JSON.parse(body));
});

test("the vector is the signer's: the test helper reproduces it", () => {
  assert.equal(sign(body), signature);
});

test("the body may be a string, a Uint8Array or a Buffer, also a view", () => {
  const junk = Buffer.from("junk before the body");
  const view = Buffer.concat([junk, Buffer.from(body)]).subarray(junk.length);
  assert.notEqual(view.byteOffset, 0);
  for (const raw of [
    body,
    new TextEncoder().encode(body),
    Buffer.from(body, "utf8"),
    view,
  ]) {
    assert.equal(verifyWebhook(options({ body: raw })).id, eventId);
  }
});

test("exactly five minutes of skew either way is accepted", () => {
  for (const at of [signedAt - 5 * minute, signedAt + 5 * minute]) {
    assert.equal(verifyWebhook(options({ now: at })).id, eventId);
  }
});

test("a byte body from another realm is accepted", () => {
  const foreign = runInNewContext("new Uint8Array(length)", {
    length: Buffer.byteLength(body),
  }) as Uint8Array;
  assert.notEqual(Object.getPrototypeOf(foreign), Uint8Array.prototype);
  foreign.set(Buffer.from(body));
  assert.equal(verifyWebhook(options({ body: foreign })).id, eventId);
});

test("explicitly undefined options mean the defaults", () => {
  assert.equal(verifyWebhook(options({ tolerance: undefined })).id, eventId);
  assert.throws(
    () => verifyWebhook(options({ tolerance: undefined, now: undefined })),
    { reason: "bad_timestamp", skew: "past" },
  );
});

test("without a clock the current time is used", () => {
  const fresh = String(Math.floor(Date.now() / second));
  const result = verifyWebhook({
    secret,
    headers: headers({
      "webhook-timestamp": fresh,
      "webhook-signature": sign(body, fresh),
    }),
    body,
  });
  assert.equal(result.id, eventId);
});

test("several signature values are accepted when one v1 value matches", () => {
  const other = "v1=" + "0".repeat(64);
  for (const value of [
    `${other} ${signature}`,
    `${signature} ${other}`,
    `v2=abc ${signature}`,
    `${other}   ${signature}`,
    ` ${signature} `,
    Array.from({ length: 7 }, () => other).join(" ") + ` ${signature}`,
  ]) {
    assert.equal(
      verifyWebhook(
        options({ headers: headers({ "webhook-signature": value }) }),
      ).id,
      eventId,
      value,
    );
  }
});

test("unknown event types and fields are passed through", () => {
  const future = JSON.stringify({
    id: eventId,
    type: "email.opened",
    created_at: "2026-10-14T08:00:00Z",
    data: { email_id: "e1", project_id: "p1", link: "x" },
    extra: { nested: true },
  });
  const result = verifyWebhook(
    options({
      body: future,
      headers: headers({ "webhook-signature": sign(future) }),
    }),
  );
  assert.deepEqual(result.event, JSON.parse(future));
  assert.equal(result.event.type, "email.opened");
});

test("headers may be Headers, IncomingHttpHeaders or a record in any case", () => {
  const shapes: WebhookHeaders[] = [
    new Headers({
      "Webhook-Id": eventId,
      "Webhook-Timestamp": timestamp,
      "Webhook-Signature": signature,
    }),
    // Node lower-cases incoming header names.
    headers({ host: "app.example.com", "set-cookie": ["a=b"] }),
    {
      "Webhook-Id": eventId,
      "WEBHOOK-TIMESTAMP": timestamp,
      "webhook-Signature": signature,
    },
  ];
  for (const shape of shapes) {
    assert.equal(verifyWebhook(options({ headers: shape })).id, eventId);
  }
});

test("a one-hour tolerance widens the window", () => {
  assert.throws(() => verifyWebhook(options({ now: signedAt + 30 * minute })), {
    reason: "bad_timestamp",
    skew: "past",
  });
  assert.equal(
    verifyWebhook(
      options({ now: signedAt + 60 * minute, tolerance: 60 * minute }),
    ).id,
    eventId,
  );
});

type Rejection = [
  name: string,
  options: () => VerifyWebhookOptions,
  reason: WebhookVerificationReason,
  skew?: "past" | "future",
];

const throwingHeaders = new Proxy(
  {},
  {
    get() {
      throw new Error("headers were read");
    },
    ownKeys() {
      throw new Error("headers were read");
    },
  },
);

const untouchableBody = {
  toJSON() {
    throw new Error("body was serialised");
  },
};

function withSignature(value: string | string[]) {
  return () => options({ headers: headers({ "webhook-signature": value }) });
}

function signedBody(payload: string) {
  return () =>
    options({
      body: payload,
      headers: headers({ "webhook-signature": sign(payload) }),
    });
}

const invalidUtf8 = new Uint8Array([
  ...new TextEncoder().encode(`{"id":"${eventId}","type":"x","n":"`),
  0xff,
  ...new TextEncoder().encode(`"}`),
]);

const rejections: Rejection[] = [
  // Secret.
  ["no secret", () => options({ secret: undefined }), "missing_secret"],
  ["empty secret", () => options({ secret: "" }), "missing_secret"],
  ["numeric secret", () => options({ secret: 42 }), "missing_secret"],
  ["object secret", () => options({ secret: { secret } }), "missing_secret"],
  [
    "secret without the whsec_ prefix",
    () => options({ secret: secret.slice("whsec_".length) }),
    "missing_secret",
  ],
  [
    "secret checked before the headers are read",
    () => options({ secret: "", headers: throwingHeaders }),
    "missing_secret",
  ],
  // Body type.
  [
    "object body, never serialised",
    () => options({ body: untouchableBody }),
    "bad_body",
  ],
  ["parsed JSON body", () => options({ body: JSON.parse(body) }), "bad_body"],
  ["null body", () => options({ body: null }), "bad_body"],
  ["numeric body", () => options({ body: 1 }), "bad_body"],
  [
    "ArrayBuffer body",
    () => options({ body: new TextEncoder().encode(body).buffer }),
    "bad_body",
  ],
  // Body content after a matching signature.
  ["signed non-JSON body", signedBody("not json"), "bad_body"],
  ["signed JSON array", signedBody(`[${body}]`), "bad_body"],
  ["signed JSON string", signedBody(`"${eventId}"`), "bad_body"],
  ["signed JSON null", signedBody("null"), "bad_body"],
  ["signed body without id", signedBody(`{"type":"email.sent"}`), "bad_body"],
  [
    "signed body with an empty id",
    signedBody(`{"id":"","type":"email.sent"}`),
    "bad_body",
  ],
  [
    "signed body with a numeric id",
    signedBody(`{"id":1,"type":"email.sent"}`),
    "bad_body",
  ],
  ["signed body without type", signedBody(`{"id":"${eventId}"}`), "bad_body"],
  [
    "signed body that is not UTF-8",
    () =>
      options({
        body: invalidUtf8,
        headers: headers({
          "webhook-signature":
            "v1=" +
            createHmac("sha256", secret)
              .update(`${timestamp}.`)
              .update(invalidUtf8)
              .digest("hex"),
        }),
      }),
    "bad_body",
  ],
  ["signed string body with a BOM", signedBody(`\uFEFF${body}`), "bad_body"],
  [
    "signed byte body with a BOM",
    () => {
      const bytes = Buffer.from(`\uFEFF${body}`, "utf8");
      return options({
        body: bytes,
        headers: headers({
          "webhook-signature":
            "v1=" +
            createHmac("sha256", secret)
              .update(`${timestamp}.`)
              .update(bytes)
              .digest("hex"),
        }),
      });
    },
    "bad_body",
  ],
  [
    "unsigned non-JSON body is never parsed",
    () => options({ body: "not json" }),
    "signature_mismatch",
  ],
  // Missing headers.
  ["no headers", () => options({ headers: undefined }), "missing_header"],
  ["null headers", () => options({ headers: null }), "missing_header"],
  [
    "string headers",
    () => options({ headers: "webhook-id" }),
    "missing_header",
  ],
  ["numeric headers", () => options({ headers: 1 }), "missing_header"],
  [
    "numeric header value",
    () =>
      options({
        headers: headers({ "webhook-timestamp": 1760428800 as never }),
      }),
    "bad_header",
  ],
  [
    "object header value",
    () => options({ headers: headers({ "webhook-id": { eventId } as never }) }),
    "bad_header",
  ],
  [
    "get() returning a non-string",
    () =>
      options({
        headers: {
          get: (name: string) => (name === "webhook-id" ? 1 : timestamp),
        },
      }),
    "bad_header",
  ],
  [
    "secret checked before the tolerance",
    () => options({ secret: "", tolerance: 0 }),
    "missing_secret",
  ],
  ...(
    ["webhook-id", "webhook-timestamp", "webhook-signature"] as const
  ).flatMap((name): Rejection[] => [
    [
      `${name} absent`,
      () => options({ headers: headers({ [name]: undefined }) }),
      "missing_header",
    ],
    [
      `${name} empty`,
      () => options({ headers: headers({ [name]: "" }) }),
      "missing_header",
    ],
    [
      `${name} as an array`,
      () =>
        options({
          headers: headers({ [name]: [headers()[name] as string] }),
        }),
      "bad_header",
    ],
    [
      `${name} twice in different case`,
      () =>
        options({
          headers: {
            ...headers(),
            [name.toUpperCase()]: headers()[name],
          },
        }),
      "bad_header",
    ],
  ]),
  [
    "joined webhook-id",
    () =>
      options({ headers: headers({ "webhook-id": `${eventId}, ${eventId}` }) }),
    "bad_header",
  ],
  [
    "joined webhook-timestamp",
    () =>
      options({
        headers: headers({ "webhook-timestamp": `${timestamp}, ${timestamp}` }),
      }),
    "bad_header",
  ],
  [
    "repeated header through Headers",
    () => {
      const repeated = new Headers({
        "Webhook-Timestamp": timestamp,
        "Webhook-Signature": signature,
      });
      repeated.append("Webhook-Id", eventId);
      repeated.append("Webhook-Id", eventId);
      return options({ headers: repeated });
    },
    "bad_header",
  ],
  // Timestamp format.
  ...[
    "+1760428800",
    " 1760428800",
    "1760428800 ",
    "1760428800.0",
    "-1",
    "soon",
    "0x68ed",
    "1760428800000",
  ].map((value): Rejection => [
    `timestamp ${JSON.stringify(value)}`,
    () =>
      options({
        headers: headers({
          "webhook-timestamp": value,
          "webhook-signature": sign(body, value),
        }),
      }),
    "bad_timestamp",
  ]),
  // Timestamp window.
  [
    "five minutes and a second old",
    () => options({ now: signedAt + 5 * minute + second }),
    "bad_timestamp",
    "past",
  ],
  [
    "five minutes and a second ahead",
    () => options({ now: signedAt - 5 * minute - second }),
    "bad_timestamp",
    "future",
  ],
  // Signature.
  [
    "tampered body",
    () => options({ body: body.replace("delivered", "bounced") }),
    "signature_mismatch",
  ],
  [
    "wrong secret",
    () => options({ secret: `${secret}x` }),
    "signature_mismatch",
  ],
  [
    "signature for another timestamp",
    () =>
      options({
        headers: headers({ "webhook-timestamp": "1760428801" }),
      }),
    "signature_mismatch",
  ],
  ["only an unknown version", withSignature(`v2=${mac}`), "signature_mismatch"],
  ["63 hex digits", withSignature(`v1=${mac.slice(1)}`), "signature_mismatch"],
  ["65 hex digits", withSignature(`v1=${mac}0`), "signature_mismatch"],
  ["not hex", withSignature(`v1=${"z".repeat(64)}`), "signature_mismatch"],
  [
    "upper-case hex",
    withSignature(`v1=${mac.toUpperCase()}`),
    "signature_mismatch",
  ],
  ["no v1= prefix", withSignature(mac), "signature_mismatch"],
  [
    "more than eight values",
    withSignature(
      Array.from({ length: 8 }, () => "v1=" + "0".repeat(64)).join(" ") +
        ` ${signature}`,
    ),
    "signature_mismatch",
  ],
  // Id.
  [
    "header id differs from the signed body id",
    () =>
      options({
        headers: headers({
          "webhook-id": "0199e3a0-1c2d-7e3f-8a4b-000000000000",
        }),
      }),
    "id_mismatch",
  ],
];

const reasons = new Set<WebhookVerificationReason>([
  "missing_secret",
  "bad_body",
  "missing_header",
  "bad_header",
  "bad_timestamp",
  "signature_mismatch",
  "id_mismatch",
]);

test("rejections carry the documented reason", async (t) => {
  for (const [name, build, reason, skew] of rejections) {
    await t.test(name, () => {
      assert.throws(
        () => verifyWebhook(build()),
        (error: unknown) => {
          assert.ok(error instanceof WebhookVerificationError);
          assert.ok(error instanceof Error);
          assert.equal(error.name, "WebhookVerificationError");
          assert.equal(error.reason, reason);
          assert.equal(error.skew, skew);
          assert.ok(reasons.has(error.reason));
          return true;
        },
      );
    });
  }
});

test("errors leak no secret, signature or body", () => {
  const forbidden = [secret, mac, signature, body, eventId, timestamp];
  for (const [name, build] of rejections) {
    let caught: unknown;
    try {
      verifyWebhook(build());
    } catch (error) {
      caught = error;
    }
    assert.ok(caught instanceof WebhookVerificationError, name);
    const texts = [
      caught.message,
      caught.stack ?? "",
      inspect(caught, { showHidden: true, depth: Infinity }),
      JSON.stringify(caught),
      String(caught),
    ];
    for (const text of texts) {
      for (const value of forbidden) {
        assert.ok(!text.includes(value), `${name}: error shows input`);
      }
    }
    assert.equal(caught.cause, undefined, name);
  }
});

test("invalid options throw before any input is read", () => {
  const cases: [
    Partial<Record<keyof VerifyWebhookOptions, unknown>>,
    ErrorConstructor,
    RegExp,
  ][] = [
    [{ tolerance: 0 }, RangeError, /tolerance/],
    [{ tolerance: -1 }, RangeError, /tolerance/],
    [{ tolerance: Number.NaN }, RangeError, /tolerance/],
    [{ tolerance: Infinity }, RangeError, /tolerance/],
    [{ tolerance: 60 * minute + 1 }, RangeError, /tolerance/],
    [{ tolerance: "300000" }, RangeError, /tolerance/],
    [{ now: Number.NaN }, RangeError, /now/],
    [{ now: Infinity }, RangeError, /now/],
    [{ now: new Date(now) }, RangeError, /now/],
  ];
  for (const [overrides, type, message] of cases) {
    assert.throws(
      () =>
        verifyWebhook(
          options({
            ...overrides,
            headers: throwingHeaders,
            body: untouchableBody,
          }),
        ),
      (error: unknown) => {
        assert.ok(error instanceof type, inspect(overrides));
        assert.ok(!(error instanceof WebhookVerificationError));
        assert.match(error.message, message);
        return true;
      },
    );
  }
  for (const bad of [undefined, null, "secret"]) {
    assert.throws(() => verifyWebhook(bad as never), TypeError);
  }
});

test("an oversized whitespace run in the signature is rejected quickly", () => {
  for (const run of [" ", "\t"]) {
    const value = `${signature}${run.repeat(64 * 1024)}y`;
    const started = performance.now();
    assert.throws(
      () =>
        verifyWebhook(
          options({ headers: headers({ "webhook-signature": value }) }),
        ),
      { reason: "signature_mismatch" },
    );
    assert.ok(performance.now() - started < 50, "took too long");
  }
});

test("whitespace runs below the length cap are split, not rejected", () => {
  for (const run of [" ", "\t"]) {
    const value = `${signature}${run.repeat(950)}v2=y`;
    assert.ok(value.length <= 1024);
    assert.equal(
      verifyWebhook(
        options({ headers: headers({ "webhook-signature": value }) }),
      ).id,
      eventId,
    );
  }
});

test("a signature header of exactly 1024 characters is accepted", () => {
  const padded = `${" ".repeat(1024 - signature.length)}${signature}`;
  assert.equal(padded.length, 1024);
  assert.equal(
    verifyWebhook(
      options({ headers: headers({ "webhook-signature": padded }) }),
    ).id,
    eventId,
  );
});

test("an oversized signature header is rejected even with a valid value", () => {
  const padded = `${" ".repeat(1024)}${signature}`;
  assert.throws(
    () =>
      verifyWebhook(
        options({ headers: headers({ "webhook-signature": padded }) }),
      ),
    { reason: "signature_mismatch" },
  );
});

test("a polluted Object.prototype.get does not take over record headers", () => {
  const proto = Object.prototype as { get?: unknown };
  proto.get = () => {
    throw new Error("polluted get was called");
  };
  try {
    assert.equal(verifyWebhook(options()).id, eventId);
  } finally {
    delete proto.get;
  }
});
