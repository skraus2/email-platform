import { expect, expectSecureDocument, login, seed, test } from "./fixtures";

// Every test here starts without a session.
test.use({ storageState: { cookies: [], origins: [] } });

/** The production cookie name (`next start` runs with NODE_ENV=production). */
const COOKIE = "__Host-ep_session";
const LOGIN = /\/login$/;
const EXPIRED = /\/login\?expired=1$/;

test("a data page without a session redirects to the login page", async ({
  page,
}) => {
  await page.goto("/emails?status=failed");
  await expect(page).toHaveURL(LOGIN);
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expect(page.getByText("Your session ended")).toHaveCount(0);
});

test("a tampered cookie ends at the login page through the session check", async ({
  page,
  context,
  baseURL,
}) => {
  await context.addCookies([
    {
      name: COOKIE,
      value: `tampered${"x".repeat(40)}`,
      domain: new URL(baseURL!).hostname,
      path: "/",
      secure: true,
      httpOnly: true,
      sameSite: "Lax",
    },
  ]);
  // The proxy lets the request through (a cookie exists); the admin API
  // refuses the token, so the page sends the browser to the login page.
  await page.goto("/");
  await expect(page).toHaveURL(EXPIRED);
  await expect(
    page.getByText("Your session ended. Please sign in again."),
  ).toBeVisible();
  // The login page stays (no redirect loop with the stale cookie).
  await page.waitForLoadState("networkidle");
  await expect(page).toHaveURL(EXPIRED);
});

test("a session revoked elsewhere ends at the login page", async ({
  page,
  browser,
  baseURL,
}) => {
  await login(page);
  const other = await browser.newContext({
    baseURL,
    storageState: { cookies: await page.context().cookies(), origins: [] },
  });
  try {
    const second = await other.newPage();
    await second.goto("/emails");
    await expect(second.getByText("Signed in as")).toBeVisible();
    // Signing out in the first browser deletes the session at the API.
    await page.getByRole("button", { name: "Sign out" }).click();
    await expect(page).toHaveURL(LOGIN);
    await second.goto("/emails");
    await expect(second).toHaveURL(EXPIRED);
  } finally {
    await other.close();
  }
});

test.describe("sign-in throttling", () => {
  // The lock persists in the database, so a retry could never pass.
  test.describe.configure({ retries: 0 });

  test("wrong passwords show one generic message and then the lockout", async ({
    page,
  }) => {
    const { locked } = seed();
    await page.goto("/login");
    const attempt = async (username: string, password: string) => {
      await page.getByLabel("Username").fill(username);
      await page.getByLabel("Password").fill(password);
      // Wait for the action's answer: the alert of the previous attempt has
      // the same text.
      await Promise.all([
        page.waitForResponse(
          (r) => r.request().method() === "POST" && r.url().endsWith("/login"),
        ),
        page.getByRole("button", { name: "Sign in" }).click(),
      ]);
    };
    const alert = page.getByRole("main").getByRole("alert");
    // An unknown name gets the same answer as a wrong password.
    await attempt("e2e-nobody", "wrong-password-0");
    await expect(alert).toHaveText("Username or password is wrong.");
    for (let i = 1; i <= 5; i++) {
      await attempt(locked.username, `wrong-password-${i}`);
      await expect(alert).toHaveText("Username or password is wrong.");
      await expect(page.getByLabel("Username")).toHaveValue(locked.username);
      await expect(page.getByLabel("Password")).toHaveValue("");
    }
    // The sixth attempt is refused even with the right password.
    await attempt(locked.username, locked.password);
    await expect(alert).toHaveText(
      /^Too many failed attempts\. Try again in \d+ minutes?\.$/,
    );
    await expect(page).toHaveURL(LOGIN);
  });
});

test("sign-in sets a strict session cookie and every page carries a nonce CSP", async ({
  page,
  context,
}) => {
  const s = seed();
  const before = Date.now();
  await login(page);
  await expect(
    page.getByText(`Signed in as ${s.admin.username}`),
  ).toBeVisible();

  const cookie = (await context.cookies()).find((c) => c.name === COOKIE);
  expect(cookie, "session cookie").toBeDefined();
  expect(cookie).toMatchObject({
    httpOnly: true,
    sameSite: "Lax",
    path: "/",
    secure: true,
    domain: "127.0.0.1",
  });
  // Never longer than the session (ADMIN_SESSION_MAX=2h for the run).
  const expires = cookie!.expires * 1000;
  expect(expires).toBeGreaterThan(before);
  expect(expires).toBeLessThanOrEqual(Date.now() + 2 * 3600_000 + 60_000);
  expect(await page.evaluate(() => document.cookie)).not.toContain(
    "ep_session",
  );

  const nonces = new Set<string>();
  const paths = [
    "/",
    "/emails",
    `/emails/${s.emails.templateEmailId}`,
    "/templates",
    `/templates/${s.order.id}`,
    `/templates/${s.order.id}/versions/1`,
    "/projects",
    `/projects/${s.projectId}`,
    "/login",
  ];
  for (const path of paths) {
    await test.step(path, async () => {
      nonces.add(await expectSecureDocument(await page.goto(path)));
    });
  }
  // A fresh nonce per response.
  expect(nonces.size).toBe(paths.length);
});

test("sign-out ends the session and removes the cookie", async ({
  page,
  context,
}) => {
  await login(page);
  await page.goto("/emails");
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(LOGIN);
  expect((await context.cookies()).map((c) => c.name)).not.toContain(COOKIE);
  // Back to the protected page: no cookie, so the proxy sends the browser
  // to the login page (not "session ended").
  await page.goBack();
  await expect(page).toHaveURL(LOGIN);
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await page.goto("/");
  await expect(page).toHaveURL(LOGIN);
});
