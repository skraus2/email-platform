import type { Page, Request } from "@playwright/test";
import { REMOTE_HOST, expect, login, seed, test } from "./fixtures";

const isPreviewAction = (r: Request) =>
  r.method() === "POST" && r.headers()["next-action"] !== undefined;

/** Submits the preview form and waits for the action's answer. */
async function render(page: Page, submit?: () => Promise<void>) {
  const [request] = await Promise.all([
    page.waitForRequest(isPreviewAction),
    page.waitForResponse((r) => isPreviewAction(r.request())),
    submit
      ? submit()
      : page.getByRole("button", { name: "Render preview" }).click(),
  ]);
  return request;
}

test("the template lists its versions and an old version shows its source unchanged", async ({
  page,
}) => {
  const s = seed();
  await page.goto(`/templates/${s.order.id}`);
  const versions = page.getByRole("region", { name: "Versions" });
  await expect(versions.locator("tbody tr")).toHaveCount(2);
  await expect(versions.locator("tbody tr").first()).toContainText(
    "Version 2 (latest)",
  );
  await versions.getByRole("link", { name: "Version 1", exact: true }).click();

  await expect(page).toHaveURL(
    new RegExp(`/templates/${s.order.id}/versions/1$`),
  );
  await expect(page.getByText("This is an older version.")).toBeVisible();
  await expect(
    page.getByRole("link", { name: "Latest version: 2" }),
  ).toBeVisible();
  const source = (name: string) =>
    page
      .getByRole("region", { name, exact: true })
      .locator("pre")
      .evaluate((el) => el.textContent);
  // After the client navigation and in the server-rendered page (the HTML
  // parser drops a newline right after <pre>).
  // A server render that lost the newline would be repaired by hydration
  // with a console error, so errors fail the check.
  const errors: string[] = [];
  page.on("console", (m) => {
    if (m.type() === "error") {
      errors.push(m.text());
    }
  });
  for (const load of ["navigation", "reload"]) {
    if (load === "reload") {
      await page.reload();
      await page.waitForLoadState("networkidle");
    }
    expect(await source("Subject"), load).toBe(s.order.v1.subject);
    expect(await source("Text part"), load).toBe(s.order.v1.text);
    expect(await source("HTML part"), load).toBe(s.order.v1.html);
  }
  expect(errors).toEqual([]);
});

test("the preview renders escaped HTML in a sandboxed frame without remote content", async ({
  page,
}) => {
  const s = seed();
  // Chromium reports a resource the CSP blocks as a request that failed
  // with "csp" before it reached the network; anything else is a leak.
  const remote: string[] = [];
  page.on("request", (r) => {
    if (r.url().includes(REMOTE_HOST)) {
      remote.push(r.url());
    }
  });
  const blocked: string[] = [];
  page.on("requestfailed", (r) => {
    if (r.url().includes(REMOTE_HOST) && r.failure()?.errorText === "csp") {
      blocked.push(r.url());
    }
  });
  const answered: string[] = [];
  page.on("response", (r) => {
    if (r.url().includes(REMOTE_HOST)) {
      answered.push(r.url());
    }
  });
  const sandboxed: string[] = [];
  const cspReports: string[] = [];
  page.on("console", (m) => {
    if (/sandboxed/i.test(m.text())) {
      sandboxed.push(m.text());
    }
    if (
      /Content Security Policy/i.test(m.text()) &&
      m.text().includes(REMOTE_HOST)
    ) {
      cspReports.push(m.text());
    }
  });
  await page.goto(`/templates/${s.order.id}/versions/2`);
  const value = `<b>${s.previewValue}</b>`;
  await page.getByLabel("name").fill(value);
  await page.getByLabel("headline").fill("Preview headline");
  await page.getByLabel("amount").fill("12.5");
  // Once interactive, so the click opens the list popup.
  await expect(page.getByLabel("vip")).toHaveAttribute(
    "aria-haspopup",
    "listbox",
  );
  await page.getByLabel("vip").click();
  await page.getByRole("option", { name: "true" }).click();
  const request = await render(page);
  expect(request.url()).not.toContain(s.previewValue);
  expect(page.url()).not.toContain(s.previewValue);

  await expect(page.getByRole("status")).toHaveText("Preview updated.");
  const result = page.locator(".preview-result");
  await expect(result).toContainText("Preview headline");
  await expect(result.locator("pre")).toHaveText(
    `Hello ${value}, total 12.5 (VIP)`,
  );

  const frameEl = page.locator("iframe.preview-frame");
  await expect(frameEl).toHaveAttribute("sandbox", "");
  await expect(frameEl).toHaveAttribute("title", "Rendered HTML part");
  const frame = page.frameLocator("iframe.preview-frame");
  const greet = frame.locator("#greet");
  // Escaped: the markup is text, no element was created from it.
  await expect(greet).toHaveText(`Hello ${value}`);
  await expect(greet.locator("b")).toHaveCount(0);
  // The inline style applies; the script did not run.
  await expect(greet).toHaveCSS("color", "rgb(0, 128, 0)");
  await expect(frame.locator("body")).not.toHaveAttribute("data-ran", "yes");
  // The remote image was blocked (see the request listeners above).
  expect(
    await frame
      .locator("#remote")
      .evaluate((img) => (img as HTMLImageElement).naturalWidth),
  ).toBe(0);
  await expect.poll(() => sandboxed.length).toBeGreaterThan(0);
  // The same console stream reports CSP violations (the suite-wide check
  // relies on it).
  await expect.poll(() => cspReports.length).toBeGreaterThan(0);
  await expect.poll(() => blocked.length).toBeGreaterThan(0);
  expect(blocked).toEqual(remote);
  expect(answered).toEqual([]);
});

test("entered values survive a field error, an API error and a success", async ({
  page,
}) => {
  const s = seed();
  await page.goto(`/templates/${s.order.id}/versions/2`);
  const name = page.getByLabel("name");
  const headline = page.getByLabel("headline");
  const amount = page.getByLabel("amount");
  await name.fill("Grace");
  await amount.fill("abc");

  // Checked in the action, the API is not called.
  await render(page);
  await expect(page.getByRole("main").getByRole("alert")).toContainText(
    "Check these variables:",
  );
  await expect(page.locator("#var-amount-error")).toHaveText(
    "Enter a number, for example 12 or -3.5.",
  );
  await expect(amount).toHaveAttribute("aria-invalid", "true");
  await expect(name).toHaveValue("Grace");
  await expect(amount).toHaveValue("abc");

  // An empty headline renders an empty subject: the API answers 422.
  await amount.fill("7");
  await render(page);
  await expect(page.getByRole("main").getByRole("alert")).toContainText(
    "The template could not be rendered:",
  );
  await expect(name).toHaveValue("Grace");
  await expect(amount).toHaveValue("7");
  await expect(headline).toHaveValue("");

  await headline.fill("Thanks");
  await render(page);
  await expect(page.getByRole("main").getByRole("alert")).toHaveCount(0);
  await expect(page.locator(".preview-result")).toContainText("Thanks");
  await expect(name).toHaveValue("Grace");
  await expect(headline).toHaveValue("Thanks");
  await expect(amount).toHaveValue("7");
});

test("Enter in a field keeps the focus there after the result", async ({
  page,
}) => {
  const s = seed();
  await page.goto(`/templates/${s.order.id}/versions/2`);
  await page.getByLabel("amount").fill("3");
  await page.getByLabel("headline").fill("Keyboard");
  const name = page.getByLabel("name");
  await name.fill("Linus");
  await render(page, () => name.press("Enter"));
  await expect(page.locator(".preview-result")).toContainText("Keyboard");
  await expect(name).toBeFocused();
});

test("two large previews in a row succeed and the second sends only the form", async ({
  page,
}) => {
  const s = seed();
  await page.goto(`/templates/${s.large.id}/versions/1`);
  const frame = page.frameLocator("iframe.preview-frame");

  await page.getByLabel("name").fill("first");
  await render(page);
  await expect(frame.locator("#large")).toHaveText("Large first");

  await page.getByLabel("name").fill("second");
  const second = await render(page);
  await expect(frame.locator("#large")).toHaveText("Large second");
  await expect(page.locator(".preview-result")).toContainText("Large second");
  const body = second.postDataBuffer() ?? Buffer.alloc(0);
  // ~450 KiB of output came back; only the small form goes out again.
  expect(body.length).toBeLessThan(10_000);
  expect(body.toString("utf8")).not.toContain("large preview filler");
});

test.describe("with its own session", () => {
  test.use({ storageState: { cookies: [], origins: [] } });

  test("a session revoked while previewing ends at the login page", async ({
    page,
    browser,
    baseURL,
  }) => {
    const s = seed();
    await login(page);
    await page.goto(`/templates/${s.order.id}/versions/2`);
    await expect(
      page.getByRole("button", { name: "Render preview" }),
    ).toBeVisible();
    // Sign out the same session in another browser.
    const other = await browser.newContext({
      ...(baseURL === undefined ? {} : { baseURL }),
      storageState: { cookies: await page.context().cookies(), origins: [] },
    });
    try {
      const second = await other.newPage();
      await second.goto("/");
      await second.getByRole("button", { name: "Sign out" }).click();
      await expect(second).toHaveURL(/\/login$/);
    } finally {
      await other.close();
    }
    await page.getByLabel("name").fill("Late");
    await page.getByLabel("amount").fill("1");
    await page.getByRole("button", { name: "Render preview" }).click();
    await expect(page).toHaveURL(/\/login\?expired=1$/);
    await expect(
      page.getByText("Your session ended. Please sign in again."),
    ).toBeVisible();
  });
});
