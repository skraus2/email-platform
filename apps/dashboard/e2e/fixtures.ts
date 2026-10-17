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

/** The script nonce of a Content-Security-Policy header. */
export function cspNonce(csp: string | undefined): string {
  const m = /'nonce-([^']+)'/.exec(csp ?? "");
  if (!m) {
    throw new Error(`no nonce in CSP: ${csp}`);
  }
  return m[1];
}

/**
 * Asserts the security headers of a page response and that every script
 * element in the served HTML carries the nonce of its CSP header (scripts
 * that Next.js adds later are trusted through 'strict-dynamic').
 */
export async function expectSecureDocument(res: Response | null) {
  expect(res, "navigation response").not.toBeNull();
  const headers = res!.headers();
  const csp = headers["content-security-policy"];
  const nonce = cspNonce(csp);
  expect(csp).toContain("frame-ancestors 'none'");
  expect(csp).toContain("img-src 'self' data:");
  expect(csp).toContain("base-uri 'none'");
  expect(headers["cache-control"]).toContain("no-store");
  const html = await res!.text();
  const tags = html.match(/<script\b[^>]*>/g) ?? [];
  expect(tags.length).toBeGreaterThan(0);
  for (const tag of tags) {
    expect(tag, "script tag carries the page nonce").toContain(
      `nonce="${nonce}"`,
    );
  }
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
