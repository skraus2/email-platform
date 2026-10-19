import { readFileSync } from "node:fs";
import {
  test as base,
  expect,
  type Page,
  type Response,
} from "@playwright/test";

/** What the seed step created (written by e2e/seed.mts, read by the specs). */
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
    /** The same template sent with version 1. */
    templateV1: { id: string; subject: string };
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
function directives(csp: string): Record<string, string[]> {
  const map: Record<string, string[]> = {};
  for (const part of csp.split(";")) {
    const [name, ...sources] = part.trim().split(/\s+/);
    if (name) {
      // Browsers enforce the first of repeated directives; none may repeat.
      expect(map[name], `directive ${name} repeated`).toBeUndefined();
      map[name] = sources;
    }
  }
  return map;
}

/**
 * Asserts the security headers of a page response: exactly the production
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
  const nonce = /^'nonce-([A-Za-z0-9+/=]+)'$/.exec(
    csp["script-src"]?.[1] ?? "",
  )?.[1];
  expect(nonce, "script-src nonce").toBeTruthy();
  expect(csp).toEqual({
    "default-src": ["'self'"],
    "script-src": ["'self'", `'nonce-${nonce}'`, "'strict-dynamic'"],
    "style-src": ["'self'", "'unsafe-inline'"],
    "img-src": ["'self'", "data:"],
    "font-src": ["'self'"],
    "connect-src": ["'self'"],
    "object-src": ["'none'"],
    "base-uri": ["'none'"],
    "form-action": ["'self'"],
    "frame-ancestors": ["'none'"],
    "frame-src": ["'self'"],
  });
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
