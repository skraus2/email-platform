import type { Page } from "@playwright/test";
import { expect, seed, test } from "./fixtures";

/** Rows of the results table (header row excluded). */
const rows = (page: Page) => page.locator("table").first().locator("tbody tr");

/**
 * Waits until the results table with this caption is the only one: while
 * the next results stream in, the previous table can still be in the page.
 */
async function expectCaption(page: Page, text: string) {
  await expect(page.getByText(text, { exact: true })).toBeVisible();
  await expect(page.locator("#emails-caption")).toHaveCount(1);
}

test("the list pages through the seeded emails and keeps focus on the results", async ({
  page,
}) => {
  const s = seed();
  await page.goto("/emails");
  await expect(page.getByRole("heading", { name: "Messages" })).toBeVisible();
  await expectCaption(page, "50 emails, newest first; more on the next page");
  await expect(rows(page)).toHaveCount(50);

  await page.getByRole("link", { name: "Next page" }).click();
  await expect(page).toHaveURL(/cursor=/);
  const rest = s.emails.total - 50;
  await expectCaption(page, `${rest} emails on this later page, newest first`);
  await expect(rows(page)).toHaveCount(rest);
  await expect(page.locator("#results-h")).toBeFocused();
  // The oldest seeded email is on the last page.
  await expect(
    page.getByRole("link", { name: s.emails.searchSubject }),
  ).toBeVisible();

  await page.getByRole("link", { name: "First page" }).click();
  await expect(page).not.toHaveURL(/cursor=/);
  await expect(rows(page)).toHaveCount(50);
});

test("filtering by status and recipient narrows the list and keeps the filters in the URL", async ({
  page,
}) => {
  const s = seed();
  await page.goto("/emails");
  await page.getByRole("checkbox", { name: "failed" }).check();
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect(page).toHaveURL(/[?&]status=failed(&|$)/);
  await expectCaption(page, "1 email, newest first");
  await expect(page.locator("#results-h")).toBeFocused();
  const failed = rows(page).first();
  await expect(failed).toContainText("E2E blocked");
  await expect(failed).toContainText("failed");
  await expect(failed).toContainText("suppressed");

  // The Failures view is the same list preset to status=failed.
  await page.goto("/");
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Failures" })
    .click();
  await expect(page).toHaveURL(/\/emails\?status=failed$/);
  await expect(page.getByRole("heading", { name: "Failures" })).toBeVisible();
  await expect(rows(page)).toHaveCount(1);

  const carol = Object.keys(s.emails.recipients).find((r) =>
    r.startsWith("carol@"),
  )!;
  await page.goto("/emails");
  await page.getByLabel("Recipient").fill(carol);
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect(page).toHaveURL(
    new RegExp(`[?&]recipient=${encodeURIComponent(carol)}(&|$)`),
  );
  const count = s.emails.recipients[carol];
  await expectCaption(page, `${count} emails, newest first`);
  await expect(rows(page)).toHaveCount(count);
  for (const cell of await rows(page).locator("td:nth-child(3)").all()) {
    await expect(cell).toHaveText(carol);
  }
  // The fields follow the URL after a reload.
  await page.reload();
  await expect(page.getByLabel("Recipient")).toHaveValue(carol);
});

test("searching by email id finds it and opens the detail without a reload", async ({
  page,
}) => {
  const s = seed();
  await page.goto("/emails");
  await page.getByLabel("Search").fill(s.emails.searchId);
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect(rows(page)).toHaveCount(1);
  await expectCaption(page, "1 email, newest first");
  // A marker on the window survives client navigation, not a document load.
  await page.evaluate(() => {
    (window as unknown as { e2eMarker: number }).e2eMarker = 1;
  });
  await page.getByRole("link", { name: s.emails.searchSubject }).click();
  await expect(page).toHaveURL(new RegExp(`/emails/${s.emails.searchId}$`));
  await expect(
    page.getByRole("heading", { level: 1, name: s.emails.searchSubject }),
  ).toBeVisible();
  expect(
    await page.evaluate(
      () => (window as unknown as { e2eMarker?: number }).e2eMarker,
    ),
  ).toBe(1);
});

test("invalid filters show a summary and no results", async ({ page }) => {
  await page.goto("/emails?status=bogus");
  await expect(page.locator("#filter-errors")).toContainText(
    "The filters could not be applied:",
  );
  await expect(page.getByRole("heading", { name: "Results" })).toHaveCount(0);
  await expect(page).toHaveTitle(/^Error: Messages/);

  await page.goto(`/emails?cursor=${"x".repeat(201)}`);
  await expect(page.locator("#filter-errors")).toBeVisible();
  await expect(
    page.locator("#filter-errors").getByRole("link", {
      name: "Go to the first page",
    }),
  ).toBeVisible();

  // Short enough for the dashboard, but not a cursor the API issued.
  await page.goto("/emails?cursor=garbage");
  await expect(
    page.getByText("This page link is no longer valid."),
  ).toBeVisible();
  await page.getByRole("link", { name: "Go to the first page" }).click();
  await expect(page).not.toHaveURL(/cursor=/);
  await expect(rows(page)).toHaveCount(50);
});

test("the detail shows events with separate event and receive times", async ({
  page,
}) => {
  const s = seed();
  await page.goto(`/emails/${s.emails.templateEmailId}`);
  await expect(page.getByRole("heading", { level: 1 })).toHaveText(
    "Your order",
  );
  const details = page.locator("dl.meta");
  await expect(details).toContainText("captured");
  await expect(details).toContainText(s.projectName);
  await expect(
    details.getByRole("link", { name: "Version 2" }),
  ).toHaveAttribute("href", `/templates/${s.order.id}/versions/2`);

  const events = page.getByRole("region", { name: "Events" });
  const headers = events.locator("thead th");
  await expect(headers).toHaveText([
    "Type",
    "Event time",
    "Received",
    "Provider",
    "Origin",
    "Details",
  ]);
  const captured = events
    .locator("tbody tr")
    .filter({ has: page.getByRole("cell", { name: "captured", exact: true }) });
  await expect(captured).toHaveCount(1);
  await expect(captured.locator("td").nth(1).locator("time")).toHaveAttribute(
    "datetime",
    /^\d{4}-\d\d-\d\dT/,
  );
  await expect(captured.locator("td").nth(2).locator("time")).toHaveAttribute(
    "datetime",
    /^\d{4}-\d\d-\d\dT/,
  );
  await expect(captured.locator("td").nth(4)).toHaveText("platform");
});

test("malformed and unknown email ids show not found inside the console", async ({
  page,
}) => {
  for (const id of ["not-a-uuid", "00000000-0000-4000-8000-000000000000"]) {
    await page.goto(`/emails/${id}`);
    await expect(
      page.getByRole("heading", { name: "Email not found" }),
    ).toBeVisible();
    await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  }
});
