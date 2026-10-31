// Runs the client against real API and worker processes. Started by
// scripts/sdk-integration.sh, which passes the API origin, a project key and
// the webhook endpoint's secret in the environment. This process also plays
// the app's webhook endpoint, so the verifier sees a real delivery the way an
// app handler does: Node's request headers and the raw body bytes.
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { once } from "node:events";
import { createServer, type IncomingHttpHeaders } from "node:http";
import { test } from "node:test";
import { setTimeout as sleep } from "node:timers/promises";

import {
  ApiError,
  EmailPlatform,
  verifyWebhook,
  WebhookVerificationError,
} from "../src/index.ts";

function env(name: string): string {
  const value = process.env[name];
  if (value === undefined || value === "") {
    throw new Error(`${name} must be set (scripts/sdk-integration.sh sets it)`);
  }
  return value;
}

const baseUrl = env("EP_BASE_URL");
const apiKey = env("EP_API_KEY");
const secret = env("EP_WEBHOOK_SECRET");
const receiverAddr = env("EP_WEBHOOK_ADDR");
const run = env("EP_RUN");

const maxWebhookBody = 64 * 1024;

type Delivery = {
  headers: IncomingHttpHeaders;
  body: Buffer;
  /** The status this endpoint answered. */
  status: number;
};

/** Polls until `probe` returns a value, or fails after `timeoutMs`. */
async function eventually<T>(
  what: string,
  probe: () => Promise<T | undefined> | T | undefined,
  timeoutMs = 20_000,
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const value = await probe();
    if (value !== undefined) {
      return value;
    }
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for ${what}`);
    }
    await sleep(100);
  }
}

/** The app's webhook endpoint: raw body with a size cap, then verification. */
async function startReceiver(deliveries: Delivery[]) {
  const server = createServer((req, res) => {
    const chunks: Buffer[] = [];
    let size = 0;
    const onData = (chunk: Buffer) => {
      size += chunk.length;
      if (size > maxWebhookBody) {
        req.off("data", onData);
        res.writeHead(413).end();
        req.destroy();
        return;
      }
      chunks.push(chunk);
    };
    req.on("data", onData);
    req.on("end", () => {
      const body = Buffer.concat(chunks);
      let status = 204;
      try {
        verifyWebhook({ secret, headers: req.headers, body });
      } catch (error) {
        status = error instanceof WebhookVerificationError ? 400 : 500;
      }
      deliveries.push({ headers: req.headers, body, status });
      res.writeHead(status).end();
    });
  });
  const colon = receiverAddr.lastIndexOf(":");
  server.listen(
    Number(receiverAddr.slice(colon + 1)),
    receiverAddr.slice(0, colon),
  );
  // Bound before the first send; a taken port fails here, not later.
  await once(server, "listening");
  return server;
}

test("the client against the real API and worker", async (t) => {
  const deliveries: Delivery[] = [];
  const server = await startReceiver(deliveries);
  t.after(() => server.close());

  const ep = new EmailPlatform({ apiKey, baseUrl });
  const recipient = `sdk-it-${run}@example.test`;
  const message = {
    from: "sdk@example.test",
    to: recipient,
    subject: `sdk ${run}`,
    text: "sdk integration body",
  };
  let id = "";

  await t.test("send with an app key, replay, get until captured", async () => {
    const idempotencyKey = `sdk-it-${run}:welcome`;
    const sent = await ep.emails.send(message, { idempotencyKey });
    assert.equal(sent.status, "queued");
    id = sent.id;

    const replayed = await ep.emails.send(message, { idempotencyKey });
    assert.equal(replayed.id, id, "the same key returns the same email");

    const email = await eventually("captured", async () => {
      const current = await ep.emails.get(id);
      assert.notEqual(current.status, "failed");
      return current.status === "captured" ? current : undefined;
    });
    assert.equal(email.to, recipient);
  });

  await t.test("events through the iterator", async () => {
    const events = [];
    for await (const event of ep.emails.listAllEvents(id)) {
      events.push(event);
    }
    assert.ok(
      events.some((e) => e.type === "captured" && e.origin === "platform"),
      `no captured platform event in ${JSON.stringify(events.map((e) => e.type))}`,
    );
  });

  await t.test("list follows next_cursor across pages", async () => {
    const more = await Promise.all([
      ep.emails.send({ ...message, subject: `sdk ${run} 2` }),
      ep.emails.send({ ...message, subject: `sdk ${run} 3` }),
    ]);
    const want = [id, ...more.map((e) => e.id)].sort();
    assert.equal(new Set(want).size, 3);

    const firstPage = await ep.emails.list({ recipient, limit: 1 });
    assert.equal(firstPage.data.length, 1);
    assert.equal(typeof firstPage.next_cursor, "string");

    const got = [];
    for await (const email of ep.emails.listAll({ recipient, limit: 1 })) {
      got.push(email.id);
    }
    assert.deepEqual(got.sort(), want);
  });

  await t.test("an API error carries the code and the request id", async () => {
    await assert.rejects(ep.emails.get(randomUUID()), (error: unknown) => {
      assert.ok(error instanceof ApiError);
      assert.equal(error.status, 404);
      assert.equal(error.code, "not_found");
      assert.match(error.requestId ?? "", /\S/);
      return true;
    });
  });

  await t.test(
    "a real webhook delivery verifies; a changed one does not",
    async () => {
      const delivery = await eventually("the email.captured webhook", () =>
        deliveries.find((d) => {
          // Only to find the delivery; whether it verified is asserted below.
          let event: { type?: unknown; data?: { email_id?: unknown } };
          try {
            event = JSON.parse(d.body.toString("utf8")) as typeof event;
          } catch {
            return false;
          }
          return event.type === "email.captured" && event.data?.email_id === id;
        }),
      );
      assert.equal(delivery.status, 204, "the endpoint accepted the delivery");
      assert.deepEqual(
        deliveries.filter((d) => d.status !== 204),
        [],
        "every delivery verified",
      );

      const verified = verifyWebhook({
        secret,
        headers: delivery.headers,
        body: delivery.body,
      });
      assert.equal(verified.id, delivery.headers["webhook-id"]);
      assert.equal(verified.event.id, verified.id);
      assert.equal(verified.event.type, "email.captured");
      assert.equal(verified.event.data.email_id, id);

      const changed = Buffer.from(delivery.body);
      const at = changed.indexOf(id);
      changed[at] = changed[at] === 0x30 ? 0x31 : 0x30;
      assert.throws(
        () =>
          verifyWebhook({ secret, headers: delivery.headers, body: changed }),
        (error: unknown) =>
          error instanceof WebhookVerificationError &&
          error.reason === "signature_mismatch",
      );

      // The same changed delivery through the endpoint is refused.
      const forward: Record<string, string> = {
        "content-type": "application/json",
      };
      for (const name of [
        "webhook-id",
        "webhook-timestamp",
        "webhook-signature",
      ]) {
        const value = delivery.headers[name];
        assert.equal(typeof value, "string", name);
        forward[name] = value as string;
      }
      const refused = await fetch(`http://${receiverAddr}/hooks`, {
        method: "POST",
        headers: forward,
        body: changed,
      });
      assert.equal(refused.status, 400);
      assert.equal(deliveries.at(-1)?.status, 400);
    },
  );
});
