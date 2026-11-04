# @email-platform/sdk

Server-side TypeScript client for the Email Platform `/v1` API. Node 26,
ESM only, no runtime dependencies. Never use it in a browser: the API key
and the webhook secret are server secrets (a browser bundle that imports
the package fails on purpose).

## Install

Releases are tarballs attached to the GitHub releases of this repository
(tags `packages/sdk-typescript/vX.Y.Z`); there is no registry package.
Install the tarball by its URL, as listed on the release page:

```bash
pnpm add <tarball URL from the release>
```

Your lockfile records the tarball's integrity hash (`sha512-…`); compare
it with the one published on the release, and commit the lockfile. To
update, install the next release's URL.

## Client

```ts
import { EmailPlatform } from "@email-platform/sdk";

const ep = new EmailPlatform({
  apiKey: process.env.EP_API_KEY!, // project key, ep_…
  baseUrl: "https://api.example.com", // plain http only for localhost
  // timeout: 30_000,  // ms for the whole call, retries included
  // maxRetries: 2,    // at most 5
});
```

Give each app a key with only the scopes it needs (`make admin ARGS="key
create -project <id> -scopes …"`; the default is `emails:send,emails:read`).

Every `/v1` operation has a method, grouped by resource (`ep.emails`,
`ep.templates`, `ep.contacts`, `ep.audiences`, `ep.broadcasts`,
`ep.automations`, `ep.automationRuns`, `ep.contactEvents`,
`ep.unsubscribes`, `ep.webhookEndpoints`, `ep.usage`). Request and response
fields keep their wire names (`next_cursor`, `created_at`). Methods resolve
to the response body only; invalid arguments reject the returned promise
(`TypeError` / `RangeError`) before anything is sent. The client never logs,
and its errors never contain the key, a request body or a query string.

## Sending

```ts
const email = await ep.emails.send(
  {
    from: "shop@example.com",
    to: customer.email,
    subject: "Your receipt",
    text: "…",
  },
  { idempotencyKey: `order-${order.id}:receipt` },
);
// email.status === "queued"; follow it with ep.emails.get(email.id)
```

Pass your own idempotency key whenever the send may be repeated by your
app: a job re-run, a retry after a crash, a second click. Make it stable
and specific to the purpose (`order-42:receipt`, not `order-42`), and keep
personal data out of it (no addresses or names). The API replays the
original answer for the same key and body for 24 hours and answers `409`
for the same key with a different body. Without a key the client generates
one per call, which only protects the client's own retries: if such a send
fails with an unknown outcome, look for the email (`ep.emails.list({
recipient })`) before sending it again.

## Retries

The client retries by itself, within `timeout`:

- `429`: after `Retry-After`, for every operation (nothing was stored).
- `500`, `502`, `504`, `503 service_unavailable` and network errors: only
  for reads and for `emails.send` / `contactEvents.create`,
  which always carry an idempotency key.
- Everything else, including writes without a key (creating a broadcast,
  a contact, a webhook endpoint, …): never. After a failure their outcome is
  unknown; check with a `get` or a list before you try again, or you may
  create a second one.

## Errors

```ts
import { ApiError, TransportError } from "@email-platform/sdk";

try {
  await ep.emails.send(message, { idempotencyKey });
} catch (error) {
  if (error instanceof ApiError) {
    // error.status, error.code ("rate_limited", "validation_failed", …),
    // error.retryAfter (seconds), error.requestId (quote it in reports)
  } else if (error instanceof TransportError) {
    // error.reason, see below
  }
}
```

`TransportError.reason`:

- `invalid_body`, `body_too_large`: the API answered `2xx`, so the request
  was applied (an email was accepted), but its answer could not be read.
- `network`, `timeout` (the call's whole `timeout` ran out, retries
  included): the request may or may not have reached the API — the outcome
  is unknown. Repeat a send with your own idempotency key, or check first
  as described above.
- `aborted`: your `signal` cancelled the call; the outcome is unknown too.

## Lists

`list…` returns one page (`data`, `next_cursor`); `listAll…` iterates over
every item, following the cursor:

```ts
for await (const email of ep.emails.listAll({ status: ["failed"] })) {
  console.log(email.id, email.error_code);
}
```

## Webhooks

The platform signs every webhook request (`Webhook-Id`,
`Webhook-Timestamp`, `Webhook-Signature`). Verify it against the **raw
request body**, exactly as received — a body parsed and serialised again by
your framework no longer matches the signature.

```ts
import express from "express";
import { verifyWebhook, WebhookVerificationError } from "@email-platform/sdk";

app.post(
  "/hooks/email",
  // Raw bytes, capped; mount this before any JSON body parser.
  express.raw({ type: "application/json", limit: "64kb" }),
  async (req, res) => {
    let verified;
    try {
      verified = verifyWebhook({
        secret: process.env.EP_WEBHOOK_SECRET!,
        headers: req.headers,
        body: req.body,
      });
    } catch (error) {
      if (
        error instanceof WebhookVerificationError &&
        error.reason !== "missing_secret"
      ) {
        return res.sendStatus(400); // not from the platform, or changed
      }
      throw error; // configuration fault → 500 (Express 5), the platform retries
    }
    const { id, event } = verified;
    await db.transaction(async (tx) => {
      // Insert-if-absent on the signed event id (the table's primary key),
      // in the same transaction as the side effect: a repeated delivery
      // changes nothing.
      const fresh = await tx.query(
        "INSERT INTO processed_webhooks (id) VALUES ($1) ON CONFLICT DO NOTHING",
        [id],
      );
      if (fresh.rowCount === 1) {
        await handle(tx, event); // e.g. event.type === "email.bounced"
      }
    });
    res.sendStatus(204); // also for duplicates
  },
);
```

```sql
CREATE TABLE processed_webhooks (
  id text PRIMARY KEY,
  processed_at timestamptz NOT NULL DEFAULT now()
);
```

Next.js route handler (`app/api/hooks/email/route.ts`, Node.js runtime —
not `edge`). Route handlers have no body limit of their own, so read the
body with a running cap instead of `request.arrayBuffer()`:

```ts
import { verifyWebhook, WebhookVerificationError } from "@email-platform/sdk";

const maxBody = 64 * 1024;

async function readCapped(request: Request): Promise<Uint8Array | null> {
  if (Number(request.headers.get("content-length") ?? 0) > maxBody) {
    return null;
  }
  const chunks: Uint8Array[] = [];
  let size = 0;
  const reader = request.body?.getReader();
  for (let r = await reader?.read(); r && !r.done; r = await reader?.read()) {
    size += r.value.byteLength;
    if (size > maxBody) {
      await reader?.cancel();
      return null;
    }
    chunks.push(r.value);
  }
  return Buffer.concat(chunks);
}

export async function POST(request: Request) {
  const body = await readCapped(request);
  if (body === null) {
    return new Response(null, { status: 413 });
  }
  try {
    const { id, event } = verifyWebhook({
      secret: process.env.EP_WEBHOOK_SECRET!,
      headers: request.headers,
      body,
    });
    // …insert-if-absent on id and handle event in one transaction…
    return new Response(null, { status: 204 });
  } catch (error) {
    if (
      error instanceof WebhookVerificationError &&
      error.reason !== "missing_secret"
    ) {
      return new Response(null, { status: 400 });
    }
    throw error; // 500, the platform retries
  }
}
```

Plain `node:http`:

```ts
import { createServer } from "node:http";
import { verifyWebhook, WebhookVerificationError } from "@email-platform/sdk";

createServer((req, res) => {
  const chunks: Buffer[] = [];
  let size = 0;
  const onData = (chunk: Buffer) => {
    size += chunk.length;
    if (size > 64 * 1024) {
      req.off("data", onData);
      res.writeHead(413).end();
      req.destroy();
      return;
    }
    chunks.push(chunk);
  };
  req.on("data", onData);
  req.on("end", () => {
    try {
      const { id, event } = verifyWebhook({
        secret: process.env.EP_WEBHOOK_SECRET!,
        headers: req.headers,
        body: Buffer.concat(chunks),
      });
      // …insert-if-absent on id and handle event in one transaction…
      res.writeHead(204).end();
    } catch (error) {
      const refused =
        error instanceof WebhookVerificationError &&
        error.reason !== "missing_secret";
      res.writeHead(refused ? 400 : 500).end();
    }
  });
}).listen(3000);
```

- Deduplicate on the returned `id` (the signed event id), never on the
  unsigned `Webhook-Id` header alone. Keep processed ids longer than the
  platform's retry window: a little over 5 hours with the default settings,
  so 7 days covers it with margin. Operators can lengthen the window (up to
  `WEBHOOK_MAX_ATTEMPTS` × `WEBHOOK_RETRY_MAX_DELAY`); ask yours if they
  changed it. Every retry is signed anew, so only your stored ids stop a
  late duplicate.
- Answer `2xx` once the event is handled or was a duplicate, within
  10 seconds. Timeouts, connection errors, `5xx`, `408` and `429` make the
  platform retry (12 attempts over about 5 hours); every other status,
  redirects included, ends the delivery as failed.
- `WebhookVerificationError.reason` tells why: `missing_header`,
  `bad_header`, `bad_body`, `signature_mismatch`, `id_mismatch`,
  `bad_timestamp` (with `skew`: `past` or `future` when the timestamp is
  outside the tolerance) → answer `400`;
  `missing_secret` (no `whsec_…` secret configured) → answer `500`, as for
  an invalid `tolerance` or `now` option (`RangeError`).
- A timestamp more than 5 minutes off your server's clock is rejected
  (`tolerance` changes this, up to 1 hour).
- A `400` ends the delivery for good, and so does a wrong clock
  (`bad_timestamp` on every delivery), a stale secret (`signature_mismatch`)
  or a JSON parser that ran before the handler (`bad_body`). Alert on a
  sustained rate of rejected deliveries rather than on single ones (anyone
  can send a request that is rejected), and recover missed status changes
  with `ep.emails.listAllEvents(id)` or `ep.emails.list(...)`.
- Unknown event types from newer platform versions are returned, not
  rejected; ignore what you do not handle and answer `2xx`.
