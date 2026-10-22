import type { Page } from "@playwright/test";
import { expect, seed, test } from "./fixtures";

const rows = (page: Page) => page.locator("table").first().locator("tbody tr");

test("the broadcast list and detail show progress and lead to the recipient emails", async ({
  page,
}) => {
  const s = seed();
  await page.goto("/");
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Broadcasts" })
    .click();
  await expect(page.getByRole("heading", { name: "Broadcasts" })).toBeVisible();
  const row = rows(page).filter({ hasText: s.broadcast.name });
  await expect(row).toHaveCount(1);
  await expect(row).toContainText(s.projectName);
  await expect(row).toContainText("completed");

  await row.getByRole("link", { name: s.broadcast.name }).click();
  await expect(page).toHaveURL(new RegExp(`/broadcasts/${s.broadcast.id}$`));
  await expect(
    page.getByRole("heading", { name: s.broadcast.name }),
  ).toBeVisible();
  await expect(page).toHaveTitle(new RegExp(`Broadcast: ${s.broadcast.name}`));
  const progress = page
    .getByRole("region", { name: "Progress" })
    .or(page.locator("section", { has: page.locator("#progress-h") }));
  await expect(
    progress.locator("dt:text-is('Recipient emails created') + dd"),
  ).toHaveText("2");
  await expect(progress).toContainText(
    "2 (1 unsubscribed, 1 suppressed, 0 not renderable)",
  );
  const table = page.getByRole("table", { name: "Recipient emails by status" });
  await expect(
    table.getByRole("row", { name: /captured/ }).getByRole("cell"),
  ).toHaveText("2");
  await expect(
    table.getByRole("row", { name: /canceled/ }).getByRole("cell"),
  ).toHaveText("0");

  await page.getByRole("link", { name: "Show the recipient emails" }).click();
  await expect(page).toHaveURL(
    new RegExp(`/emails\\?broadcast_id=${s.broadcast.id}$`),
  );
  await expect(
    page.getByText("Only recipient emails of broadcast"),
  ).toBeVisible();
  await expect(rows(page)).toHaveCount(2);
  for (const r of s.broadcast.recipients) {
    await expect(rows(page).filter({ hasText: r })).toHaveCount(1);
  }

  // A recipient email links back to its broadcast.
  await rows(page).first().getByRole("link").first().click();
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  const back = page.getByRole("link", { name: `Broadcast ${s.broadcast.id}` });
  await expect(back).toBeVisible();
  await back.click();
  await expect(page).toHaveURL(new RegExp(`/broadcasts/${s.broadcast.id}$`));
});

test("a cancelled planned email is filterable and shows its planned time", async ({
  page,
}) => {
  const s = seed();
  await page.goto("/emails");
  await page.getByRole("checkbox", { name: "canceled" }).check();
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect(page).toHaveURL(/[?&]status=canceled(&|$)/);
  await expect(rows(page)).toHaveCount(1);
  await expect(rows(page).first()).toContainText("E2E planned");
  await expect(rows(page).first()).toContainText("canceled");

  await page.goto(`/emails/${s.emails.plannedId}`);
  const details = page.locator("dl.meta").first();
  const plannedFor = details.locator("dt:text-is('Planned for') + dd");
  await expect(plannedFor.locator("time")).toBeVisible();
  await expect(plannedFor).not.toContainText("not planned");
  await expect(details).toContainText("none (single send)");
});

test("an unknown broadcast is not found inside the console", async ({
  page,
}) => {
  // The loading state streams first, so the status is 200 (as for emails).
  await page.goto("/broadcasts/0199f0a2-7c1e-7d3a-9b1e-2f5c8a9d0e99");
  await expect(
    page.getByRole("heading", { name: "Broadcast not found" }),
  ).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Main" })).toBeVisible();
  await expect(page).toHaveTitle(/Broadcast not found/);
  await page.getByRole("link", { name: "All broadcasts" }).click();
  await expect(page).toHaveURL(/\/broadcasts$/);
});
