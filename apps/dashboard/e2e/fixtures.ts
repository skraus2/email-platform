import { readFileSync } from "node:fs";
import {
  test as base,
  expect,
  type Page,
  type Response,
} from "@playwright/test";

/** What the seed step created (written by e2e/seed.ts, read by the specs). */
export type Seed = {
  projectId: string;
  projectName: string;
  apiKey: string;
  admin: Account;
  locked: Account;
  sender: { id: string; address: string };
  order: {
    id: string;
    v1: { subject: string; text: string; html: string };
  };
  large: { id: string };
  emails: {
    total: number;
    searchId: string;
    searchSubject: string;
    templateEmailId: string;
    failedId: string;
    recipients: Record<string, number>;
  };
  /** A preview value the logs must never contain. */
  previewValue: string;
};

export type Account = { username: string; password: string };

let cached: Seed | undefined;

export function seed(): Seed {
  const path = process.env.E2E_SEED;
  if (!path) {
    throw new Error("E2E_SEED is not set; run the suite with `make test-e2e`");
  }
  cached ??= JSON.parse(readFileSync(path, "utf8")) as Seed;
  return cached;
}

/** The host the seeded template HTML points remote resources at. */
export const REMOTE_HOST = "e2e-remote.test";

/** Signs in through the login form and waits for the console. */
export async function login(page: Page, account: Account = seed().admin) {
  await page.goto("/login");
  await page.getByLabel("Username").fill(account.username);
  await page.getByLabel("Password").fill(account.password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByText("Signed in as")).toBeVisible();
}

/** A Content-Security-Policy header as directive → sources. */
function directives(csp: string): Map<string, string[]> {
  const map = new Map<string, string[]>();
  for (const part of csp.split(";")) {
    const [name, ...sources] = part.trim().split(/\s+/);
    if (name) {
      map.set(name, sources);
    }
  }
  return map;
}

/**
 * Asserts the security headers of a page response: the exact production
 * policy, and every script element in the served HTML carrying the nonce
 * of its `script-src` (scripts Next.js adds later are trusted through
 * 'strict-dynamic'). Returns the nonce, so callers can check it is fresh
 * per response.
 */
export async function expectSecureDocument(
  res: Response | null,
): Promise<string> {
  expect(res, "navigation response").not.toBeNull();
  const headers = res!.headers();
  const csp = directives(headers["content-security-policy"] ?? "");
  const script = csp.get("script-src") ?? [];
  const nonce = /^'nonce-([A-Za-z0-9+/=]+)'$/.exec(script[1] ?? "")?.[1];
  expect(nonce, "script-src nonce").toBeTruthy();
  expect(script).toEqual(["'self'", `'nonce-${nonce}'`, "'strict-dynamic'"]);
  expect(csp.get("default-src")).toEqual(["'self'"]);
  expect(csp.get("style-src")).toEqual(["'self'", "'unsafe-inline'"]);
  expect(csp.get("img-src")).toEqual(["'self'", "data:"]);
  expect(csp.get("object-src")).toEqual(["'none'"]);
  expect(csp.get("base-uri")).toEqual(["'none'"]);
  expect(csp.get("form-action")).toEqual(["'self'"]);
  expect(csp.get("frame-ancestors")).toEqual(["'none'"]);
  expect(headers["cache-control"]).toContain("no-store");
  const html = await res!.text();
  const tags = html.match(/<script\b[^>]*>/g) ?? [];
  expect(tags.length).toBeGreaterThan(0);
  for (const tag of tags) {
    expect(tag, "script tag carries the page nonce").toContain(
      `nonce="${nonce}"`,
    );
  }
  return nonce!;
}

/**
 * The page fixture fails a test when the browser reports a Content Security
 * Policy violation, except for the remote resources of the seeded template
 * HTML, which the policy is expected to block.
 */
export const test = base.extend({
  page: async ({ page }, provide) => {
    const violations: string[] = [];
    page.on("console", (msg) => {
      const text = msg.text();
      if (
        /Content Security Policy/i.test(text) &&
        !text.includes(REMOTE_HOST)
      ) {
        violations.push(text);
      }
    });
    await provide(page);
    expect(violations, "CSP violations").toEqual([]);
  },
});

export { expect };
