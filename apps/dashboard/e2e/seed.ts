/**
 * Seeds the throwaway database of an end-to-end run through the admin CLI
 * and the /v1 API, then (second call) waits until every seeded email has
 * reached a final state. Run by scripts/e2e.sh with Node's type stripping:
 *
 *   node e2e/seed.ts seed   (API running, worker not yet started)
 *   node e2e/seed.ts wait   (after the worker started)
 *
 * Secrets go to the seed file (mode 0600) and to the needles file the
 * script checks the logs against; passwords reach the CLI on stdin only.
 */
import { execFileSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
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
const BLOCKED = `blocked@${DOMAIN}`;

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

async function seedAll() {
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
    "emails:send,emails:read,templates:read,templates:write",
  ]);
  const accounts: Account[] = ["e2e-admin", "e2e-locked"].map((username) => ({
    username,
    password: password(),
  }));
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

  const previewValue = `e2e-preview-${randomBytes(6).toString("hex")}`;
  const seed: Seed = {
    projectId,
    projectName,
    apiKey,
    admin: accounts[0],
    locked: accounts[1],
    sender: { id: senderId, address: FROM },
    order: { id: order.id, v1: ORDER_V1 },
    large: { id: large.id },
    emails: {
      total: n + 2,
      searchId,
      searchSubject,
      templateEmailId: templateEmail.id,
      failedId: failed.id,
      recipients: RECIPIENTS,
    },
    previewValue,
  };
  writeFileSync(SEED, JSON.stringify(seed, null, 2), { mode: 0o600 });
  const needles = [
    apiKey,
    ...accounts.map((a) => a.password),
    ...Object.keys(RECIPIENTS),
    TEMPLATE_RECIPIENT,
    BLOCKED,
    previewValue,
  ];
  writeFileSync(env("E2E_NEEDLES"), `${needles.join("\n")}\n`, {
    mode: 0o600,
  });
}

async function waitFinal() {
  const { apiKey } = JSON.parse(readFileSync(SEED, "utf8")) as Seed;
  const deadline = Date.now() + 60_000;
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

const step = process.argv[2];
if (step === "seed") {
  await seedAll();
} else if (step === "wait") {
  await waitFinal();
} else {
  throw new Error("usage: node e2e/seed.ts seed|wait");
}
