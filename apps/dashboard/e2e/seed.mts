/**
 * Seeds the throwaway database of an end-to-end run through the admin CLI
 * and the /v1 API, then (second call) waits until every seeded email has
 * reached a final state. Run by scripts/e2e.sh with Node's type stripping:
 *
 *   node e2e/seed.mts seed   (API running, worker not yet started)
 *   node e2e/seed.mts wait   (after the worker started)
 *   node e2e/seed.mts check  (after wait: template sends in Mailpit)
 *
 * Secrets go to the seed file (mode 0600) and, as soon as each exists, to
 * the needles file the script checks the logs against; passwords reach the
 * CLI on stdin only.
 */
import { execFileSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { appendFileSync, readFileSync, writeFileSync } from "node:fs";
import type { Account, Seed } from "./fixtures";

const env = (name: string): string => {
  const v = process.env[name];
  if (!v) {
    throw new Error(`${name} is not set`);
  }
  return v;
};

const SEED = env("E2E_SEED");
const API = env("E2E_API_URL");
const MAILPIT = process.env.TEST_MAILPIT_URL ?? "http://127.0.0.1:8025";
// Only a local Mailpit can vouch for what the worker sent.
if (
  !["127.0.0.1", "localhost", "[::1]", "mailpit"].includes(
    new URL(MAILPIT).hostname,
  )
) {
  throw new Error("TEST_MAILPIT_URL must point at a local Mailpit");
}

function admin(args: string[], input?: string): string {
  return execFileSync(env("E2E_ADMIN_BIN"), args, {
    input,
    encoding: "utf8",
    stdio: ["pipe", "pipe", "pipe"],
  }).trim();
}

async function v1<T>(key: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${API}${path}`, {
    method: body === undefined ? "GET" : "POST",
    headers: {
      Authorization: `Bearer ${key}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`${path}: ${res.status} ${await res.text()}`);
  }
  return (await res.json()) as T;
}

const DOMAIN = "e2e-shop.test";
const FROM = `sender@${DOMAIN}`;

/** Recipients of the raw emails and how many each gets (54 in total). */
const RECIPIENTS: Record<string, number> = {
  [`alice@${DOMAIN}`]: 20,
  [`bob@${DOMAIN}`]: 20,
  [`carol@${DOMAIN}`]: 14,
};
const TEMPLATE_RECIPIENT = `dora@${DOMAIN}`;
/** Sent with version 1: its HTML part must show it escaped. */
const V1_NAME = "<Ada & Co>";
const BLOCKED = `blocked@${DOMAIN}`;
/** Audience members: two subscribed, one unsubscribed (gets nothing). */
const READERS = [`reader1@${DOMAIN}`, `reader2@${DOMAIN}`];
const FORMER_READER = `former@${DOMAIN}`;

const NEWSLETTER = {
  subject: "E2E newsletter for {{.name}}",
  text: "Hello {{.name}}\nUnsubscribe: {{.unsubscribe_url}}",
  html: '<p>Hello {{.name}}</p><a href="{{.unsubscribe_url}}">Unsubscribe</a>',
  variables: {
    name: { type: "string", required: true },
    unsubscribe_url: { type: "string", required: true },
    campaign: { type: "string" },
  },
};

const ORDER_V1 = {
  subject: "Order {{.order}}",
  // Leading newline, indentation and markup must show exactly as stored.
  text: "\n  Version one for {{.name}}\n\t<b>raw</b> &amp; kept",
  html: "<p>Version one {{.name}}</p>",
  variables: {
    name: { type: "string" },
    order: { type: "string" },
  },
};

const ORDER_V2 = {
  subject: "{{.headline}}",
  text: "Hello {{.name}}, total {{.amount}}{{if .vip}} (VIP){{end}}",
  // An inline style that must apply, and a script, a remote image and a
  // resource hint that the sandbox and the CSP must stop.
  html:
    '<link rel="dns-prefetch" href="//prefetch.e2e-remote.test">' +
    '<p id="greet" style="color: rgb(0, 128, 0)">Hello {{.name}}</p>' +
    '<img id="remote" src="http://images.e2e-remote.test/pixel.png" alt="">' +
    '<script>document.body.setAttribute("data-ran", "yes")</script>',
  variables: {
    name: { type: "string", required: true },
    headline: { type: "string" },
    amount: { type: "number", required: true },
    vip: { type: "boolean" },
  },
};

/** About 450 KiB of static HTML around one variable. */
const LARGE = {
  subject: "Large {{.name}}",
  text: "Large preview for {{.name}}",
  html: `<p id="large">Large {{.name}}</p><div>${"<p>large preview filler line</p>".repeat(14_400)}</div>`,
  variables: { name: { type: "string" } },
};

function password(): string {
  return randomBytes(24).toString("base64url");
}

/** Adds values the logs must never contain (the script redacts them too). */
function needles(...values: string[]) {
  appendFileSync(env("E2E_NEEDLES"), `${values.join("\n")}\n`, {
    mode: 0o600,
  });
}

async function seedAll() {
  const previewValue = `e2e-preview-${randomBytes(6).toString("hex")}`;
  needles(
    ...Object.keys(RECIPIENTS),
    ...READERS,
    FORMER_READER,
    TEMPLATE_RECIPIENT,
    BLOCKED,
    previewValue,
    V1_NAME,
  );
  const projectName = "E2E shop";
  const projectId = admin(["project", "create", "-name", projectName]).split(
    "\t",
  )[0];
  const apiKey = admin([
    "key",
    "create",
    "-project",
    projectId,
    "-scopes",
    "emails:send,emails:read,templates:read,templates:write,contacts:write,broadcasts:read,broadcasts:write",
  ]);
  needles(apiKey);
  const accounts: Account[] = ["e2e-admin", "e2e-locked"].map((username) => ({
    username,
    password: password(),
  }));
  needles(...accounts.map((a) => a.password));
  for (const a of accounts) {
    admin(["user", "create", "-username", a.username], `${a.password}\n`);
  }
  const senderId = admin([
    "sender",
    "add",
    "-project",
    projectId,
    "-address",
    FROM,
  ]).split("\t")[0];
  admin(["sender", "verify", "-project", projectId, "-id", senderId]);

  const order = await v1<{ id: string }>(apiKey, "/v1/templates", {
    slug: "e2e-order",
    name: "E2E order confirmation",
  });
  await v1(apiKey, `/v1/templates/${order.id}/versions`, ORDER_V1);
  await v1(apiKey, `/v1/templates/${order.id}/versions`, ORDER_V2);
  const large = await v1<{ id: string }>(apiKey, "/v1/templates", {
    slug: "e2e-large",
    name: "E2E large",
  });
  await v1(apiKey, `/v1/templates/${large.id}/versions`, LARGE);

  type Accepted = { id: string };
  const send = (body: Record<string, unknown>) =>
    v1<Accepted>(apiKey, "/v1/emails", { from: FROM, ...body });
  let n = 0;
  let searchId = "";
  let searchSubject = "";
  for (const [to, count] of Object.entries(RECIPIENTS)) {
    for (let i = 0; i < count; i++) {
      n++;
      const subject = `E2E message ${String(n).padStart(2, "0")}`;
      const { id } = await send({ to, subject, text: `Body ${n}` });
      if (n === 1) {
        searchId = id;
        searchSubject = subject;
      }
    }
  }
  const templateEmail = await send({
    to: TEMPLATE_RECIPIENT,
    template_id: order.id,
    template_version: "published",
    variables: { name: "Ada", headline: "Your order", amount: 12.5, vip: true },
  });
  const v1Subject = `Order e2e-${randomBytes(6).toString("hex")}`;
  const templateV1Email = await send({
    to: TEMPLATE_RECIPIENT,
    template_id: order.id,
    template_version: 1,
    variables: { name: V1_NAME, order: v1Subject.slice("Order ".length) },
  });
  // Accepted before the suppression exists; the worker's re-check fails it.
  const failed = await send({
    to: BLOCKED,
    subject: "E2E blocked",
    text: "Never sent",
  });
  admin([
    "suppression",
    "add",
    "-project",
    projectId,
    "-email",
    BLOCKED,
    "-reason",
    "manual",
  ]);

  // A newsletter to an audience, started now: the worker fans it out.
  const audience = await v1<{ id: string }>(apiKey, "/v1/audiences", {
    name: "E2E readers",
  });
  for (const [i, email] of [...READERS, FORMER_READER].entries()) {
    const subscribed = email !== FORMER_READER;
    const c = await v1<{ id: string }>(apiKey, "/v1/contacts", {
      email,
      status: subscribed ? "subscribed" : "unsubscribed",
      ...(subscribed ? { consent_source: "e2e-signup" } : {}),
      attributes: { name: `Reader ${i + 1}` },
    });
    await v1(apiKey, `/v1/audiences/${audience.id}/contacts`, {
      contact_ids: [c.id],
    });
  }
  const newsletter = await v1<{ id: string }>(apiKey, "/v1/templates", {
    slug: "e2e-newsletter",
    name: "E2E newsletter",
  });
  await v1(apiKey, `/v1/templates/${newsletter.id}/versions`, NEWSLETTER);
  // A shared value no page or log may show.
  const shared = `e2e-shared-${randomBytes(6).toString("hex")}`;
  needles(shared);
  const broadcast = await v1<{ id: string }>(apiKey, "/v1/broadcasts", {
    name: "E2E October newsletter",
    variables: { campaign: shared },
    audience_id: audience.id,
    from: FROM,
    template_id: newsletter.id,
    template_version: "published",
    unsubscribe_url: "https://app.e2e-shop.test/unsubscribe?t={token}",
  });
  await v1(apiKey, `/v1/broadcasts/${broadcast.id}/schedule`, {});

  // A planned email the worker sends once it is due (during "wait").
  const plannedSent = await send({
    to: TEMPLATE_RECIPIENT,
    subject: "E2E planned and sent",
    text: "Sent when due",
    scheduled_at: new Date(Date.now() + 12_000).toISOString(),
  });

  // A planned email, cancelled before it was due.
  const planned = await send({
    to: TEMPLATE_RECIPIENT,
    subject: "E2E planned",
    text: "Never sent",
    scheduled_at: new Date(Date.now() + 3_600_000).toISOString(),
  });
  await v1(apiKey, `/v1/emails/${planned.id}/cancel`, {});

  const seed: Seed = {
    projectId,
    projectName,
    apiKey,
    admin: accounts[0],
    locked: accounts[1],
    sender: { id: senderId, address: FROM },
    order: { id: order.id, v1: ORDER_V1 },
    large: { id: large.id },
    broadcast: {
      id: broadcast.id,
      name: "E2E October newsletter",
      recipients: READERS,
    },
    emails: {
      // + 2 newsletter recipients, + 2 planned emails (one cancelled),
      // + 1 follow-up recipient (created by "check").
      total: n + 8,
      plannedId: planned.id,
      plannedSentId: plannedSent.id,
      searchId,
      searchSubject,
      templateEmailId: templateEmail.id,
      templateV1: { id: templateV1Email.id, subject: v1Subject },
      failedId: failed.id,
      recipients: RECIPIENTS,
    },
    previewValue,
  };
  writeFileSync(SEED, JSON.stringify(seed, null, 2), { mode: 0o600 });
}

async function waitFinal() {
  const { apiKey, broadcast } = JSON.parse(readFileSync(SEED, "utf8")) as Seed;
  const deadline = Date.now() + 60_000;
  // The newsletter's recipient emails exist once its fan-out completed.
  while (Date.now() < deadline) {
    const b = await v1<{ status: string }>(
      apiKey,
      `/v1/broadcasts/${broadcast.id}`,
    );
    if (b.status === "completed") {
      break;
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  while (Date.now() < deadline) {
    const open = await v1<{ data: unknown[] }>(
      apiKey,
      "/v1/emails?status=queued&status=sending&limit=1",
    );
    if (open.data.length === 0) {
      return;
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  throw new Error("emails still queued or sending after 60 s");
}

type MailpitMessage = { Subject: string; Text: string; HTML: string };

/** The Mailpit messages carrying this email's Message-ID (waits up to 5 s). */
async function mailpitMessages(emailId: string): Promise<MailpitMessage[]> {
  const get = async <T,>(path: string): Promise<T> => {
    const res = await fetch(`${MAILPIT}${path}`, {
      signal: AbortSignal.timeout(5_000),
    });
    if (!res.ok) {
      throw new Error(`mailpit ${path}: ${res.status}`);
    }
    return (await res.json()) as T;
  };
  const query = encodeURIComponent(
    `message-id:"${emailId}@email-platform.local"`,
  );
  const deadline = Date.now() + 5_000;
  for (;;) {
    const { messages } = await get<{ messages: { ID: string }[] }>(
      `/api/v1/search?query=${query}`,
    );
    if (messages.length > 0 || Date.now() > deadline) {
      return Promise.all(
        messages.map((m) =>
          get<MailpitMessage>(`/api/v1/message/${encodeURIComponent(m.ID)}`),
        ),
      );
    }
    await new Promise((r) => setTimeout(r, 100));
  }
}

/**
 * Both template sends went through the real worker: each is captured with
 * its version, Mailpit holds it once with that version's rendered content,
 * and the list filtered by template returns exactly these two.
 */
async function checkTemplateSends() {
  const s = JSON.parse(readFileSync(SEED, "utf8")) as Seed;
  const published = s.emails.templateEmailId;
  const old = s.emails.templateV1.id;
  const fail = (msg: string): never => {
    throw new Error(`template send check: ${msg}`);
  };

  for (const [id, version] of [
    [published, 2],
    [old, 1],
  ] as const) {
    const e = await v1<{
      status: string;
      template_id: string;
      template_version: number;
    }>(s.apiKey, `/v1/emails/${id}`);
    if (
      e.status !== "captured" ||
      e.template_id !== s.order.id ||
      e.template_version !== version
    ) {
      fail(
        `email ${id}: status ${e.status}, version ${e.template_version}, want captured ${version}`,
      );
    }
  }

  const latest = await mailpitMessages(published);
  if (latest.length !== 1) {
    fail(`published send: ${latest.length} Mailpit messages, want 1`);
  }
  const [l] = latest;
  if (l.Subject !== "Your order") {
    fail(`published send subject ${JSON.stringify(l.Subject)}`);
  }
  if (l.Text.trim() !== "Hello Ada, total 12.5 (VIP)") {
    fail(`published send text part ${JSON.stringify(l.Text.trim())}`);
  }
  if (!l.HTML.includes(">Hello Ada</p>")) {
    fail("published send HTML part does not show the rendered greeting");
  }

  const older = await mailpitMessages(old);
  if (older.length !== 1) {
    fail(`version 1 send: ${older.length} Mailpit messages, want 1`);
  }
  const [o] = older;
  if (o.Subject !== s.emails.templateV1.subject) {
    fail(`version 1 subject ${JSON.stringify(o.Subject)}`);
  }
  if (
    !o.HTML.includes("<p>Version one &lt;Ada &amp; Co&gt;</p>") ||
    o.HTML.includes(V1_NAME)
  ) {
    fail("version 1 HTML part does not show the variable escaped");
  }
  if (!o.Text.includes(`Version one for ${V1_NAME}`)) {
    fail("version 1 text part is missing or does not show the variable");
  }

  const listed = await v1<{ data: { id: string }[] }>(
    s.apiKey,
    `/v1/emails?template_id=${s.order.id}`,
  );
  const ids = listed.data.map((e) => e.id);
  if (ids.length !== 2 || ids[0] !== old || ids[1] !== published) {
    fail(`listed by template: ${ids.join(",")}, want ${old},${published}`);
  }
}

/** Mailpit messages (with ids) sent to an address. */
async function mailpitTo(address: string): Promise<{ ID: string }[]> {
  const res = await fetch(
    `${MAILPIT}/api/v1/search?query=${encodeURIComponent(`to:"${address}"`)}`,
    { signal: AbortSignal.timeout(5_000) },
  );
  if (!res.ok) {
    throw new Error(`mailpit search: ${res.status}`);
  }
  return ((await res.json()) as { messages: { ID: string }[] }).messages;
}

async function mailpitHeaders(id: string): Promise<Record<string, string[]>> {
  const res = await fetch(
    `${MAILPIT}/api/v1/message/${encodeURIComponent(id)}/headers`,
    { signal: AbortSignal.timeout(5_000) },
  );
  if (!res.ok) {
    throw new Error(`mailpit headers: ${res.status}`);
  }
  return (await res.json()) as Record<string, string[]>;
}

/**
 * Broadcasts and planned sends through the real binaries: one newsletter
 * message per subscribed member with one-click unsubscribe headers and a
 * personal link; an unsubscribe reported with that link's token keeps the
 * reader out of the next broadcast; the planned email arrived after it was
 * due, the cancelled one never.
 */
async function checkBroadcasts() {
  const s = JSON.parse(readFileSync(SEED, "utf8")) as Seed;
  const fail = (msg: string): never => {
    throw new Error(`broadcast check: ${msg}`);
  };
  const listed = await v1<{
    data: { id: string; to: string; status: string }[];
  }>(s.apiKey, `/v1/emails?broadcast_id=${s.broadcast.id}`);
  if (listed.data.length !== s.broadcast.recipients.length) {
    fail(`${listed.data.length} recipient emails`);
  }
  const tokens = new Map<string, string>();
  for (const e of listed.data) {
    const msgs = await mailpitMessages(e.id);
    if (e.status !== "captured" || msgs.length !== 1) {
      fail(`${e.to}: status ${e.status}, ${msgs.length} messages`);
    }
    const found = await mailpitTo(e.to);
    const headers = await mailpitHeaders(found[0].ID);
    const link =
      /^<(https:\/\/app\.e2e-shop\.test\/unsubscribe\?t=([A-Za-z0-9_-]{43}))>$/.exec(
        headers["List-Unsubscribe"]?.[0] ?? "",
      );
    if (
      !link ||
      headers["List-Unsubscribe-Post"]?.[0] !== "List-Unsubscribe=One-Click"
    ) {
      fail(`${e.to}: unsubscribe headers missing or malformed`);
    }
    if (!msgs[0].Text.includes(link![1]) || !msgs[0].HTML.includes(link![1])) {
      fail(`${e.to}: the body does not carry the personal unsubscribe link`);
    }
    tokens.set(e.to, link![2]);
  }
  if (new Set(tokens.values()).size !== tokens.size) {
    fail("unsubscribe tokens are not personal");
  }
  if ((await mailpitTo(FORMER_READER)).length !== 0) {
    fail("the unsubscribed reader got the newsletter");
  }

  // The app's unsubscribe page reports the first reader's token.
  const [first, second] = s.broadcast.recipients;
  needles(...tokens.values());
  const un = await v1<{ status: string }>(s.apiKey, "/v1/unsubscribes", {
    token: tokens.get(first),
  });
  if (un.status !== "unsubscribed") {
    fail("unsubscribe report not taken");
  }
  const original = await v1<{ audience_id: string; template_id: string }>(
    s.apiKey,
    `/v1/broadcasts/${s.broadcast.id}`,
  );
  const followUp = await v1<{ id: string }>(s.apiKey, "/v1/broadcasts", {
    name: "E2E follow-up",
    audience_id: original.audience_id,
    from: FROM,
    template_id: original.template_id,
    template_version: "published",
    unsubscribe_url: "https://app.e2e-shop.test/unsubscribe?t={token}",
  });
  await v1(s.apiKey, `/v1/broadcasts/${followUp.id}/schedule`, {});
  const deadline = Date.now() + 30_000;
  let done: { data: { to: string; status: string }[] } = { data: [] };
  while (Date.now() < deadline) {
    const b = await v1<{ status: string }>(
      s.apiKey,
      `/v1/broadcasts/${followUp.id}`,
    );
    done = await v1(s.apiKey, `/v1/emails?broadcast_id=${followUp.id}`);
    if (
      b.status === "completed" &&
      done.data.every((e) => e.status === "captured")
    ) {
      break;
    }
    await new Promise((r) => setTimeout(r, 200));
  }
  if (
    done.data.length !== 1 ||
    done.data[0].to !== second ||
    done.data[0].status !== "captured"
  ) {
    fail(
      `follow-up went to ${done.data.map((e) => `${e.to}:${e.status}`).join(",")}, want only ${second}`,
    );
  }

  const planned = await v1<{
    status: string;
    scheduled_at: string;
    finished_at: string;
  }>(s.apiKey, `/v1/emails/${s.emails.plannedSentId}`);
  if (
    planned.status !== "captured" ||
    Date.parse(planned.finished_at) < Date.parse(planned.scheduled_at) ||
    (await mailpitMessages(s.emails.plannedSentId)).length !== 1
  ) {
    fail(
      `planned email: ${planned.status}, finished ${planned.finished_at} before due ${planned.scheduled_at}?`,
    );
  }
  if ((await mailpitMessages(s.emails.plannedId)).length !== 0) {
    fail("the cancelled planned email reached Mailpit");
  }
}

const step = process.argv[2];
if (step === "seed") {
  await seedAll();
} else if (step === "wait") {
  await waitFinal();
} else if (step === "check") {
  await checkTemplateSends();
  await checkBroadcasts();
} else {
  throw new Error("usage: node e2e/seed.mts seed|wait|check");
}
