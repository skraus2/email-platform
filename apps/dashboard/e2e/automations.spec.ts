import type { Page } from "@playwright/test";
import { expect, seed, test } from "./fixtures";

const rows = (page: Page) => page.locator("table").first().locator("tbody tr");

test("the automation list, detail and run pages show steps, runs and their emails", async ({
  page,
}) => {
  const s = seed();
  await page.goto("/");
  await page
    .getByRole("navigation", { name: "Main" })
    .getByRole("link", { name: "Automations" })
    .click();
  await expect(
    page.getByRole("heading", { name: "Automations" }),
  ).toBeVisible();
  const row = rows(page).filter({ hasText: s.automation.name });
  await expect(row).toHaveCount(1);
  await expect(row).toContainText(s.projectName);
  await expect(row).toContainText("active");
  await expect(row).toContainText("user.signed_up");

  await row.getByRole("link", { name: s.automation.name }).click();
  await expect(page).toHaveURL(new RegExp(`/automations/${s.automation.id}$`));
  await expect(page).toHaveTitle(
    new RegExp(`Automation: ${s.automation.name}`),
  );
  const steps = page
    .locator("section", { has: page.locator("#steps-h") })
    .locator("ol li");
  await expect(steps).toHaveCount(3);
  await expect(steps.nth(1)).toHaveText("Wait 1 hour");
  await expect(steps.nth(0)).toContainText("Send version 1 of template");
  await expect(steps.nth(0)).toContainText(`from ${s.sender.address}`);
  await expect(
    steps.nth(0).getByRole("link", { name: "version 1" }),
  ).toBeVisible();
  const counts = page.getByRole("table", { name: "Runs by status" });
  await expect(
    counts.getByRole("row", { name: /active/ }).getByRole("cell"),
  ).toHaveText("1");
  await expect(
    counts.getByRole("row", { name: /completed/ }).getByRole("cell"),
  ).toHaveText("0");

  const runs = page.getByRole("table", { name: "Latest runs, newest first" });
  await expect(runs.locator("tbody tr")).toHaveCount(1);
  await expect(runs.locator("tbody tr")).toContainText("Step 3, due");
  await runs.getByRole("link", { name: `Run ${s.automation.runId}` }).click();

  await expect(page).toHaveURL(
    new RegExp(`/automation-runs/${s.automation.runId}$`),
  );
  await expect(
    page.getByRole("heading", { name: "Automation run" }),
  ).toBeVisible();
  const emails = page.getByRole("table", {
    name: "Emails of this run by step",
  });
  await expect(emails.locator("tbody tr")).toHaveCount(1);
  await expect(emails.locator("tbody tr")).toContainText("captured");
  await emails.getByRole("link").first().click();

  // The email links back to its run.
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
  await expect(page.getByText(s.automation.contact)).toBeVisible();
  await page
    .getByRole("link", { name: `Automation run ${s.automation.runId}` })
    .click();
  await expect(page).toHaveURL(
    new RegExp(`/automation-runs/${s.automation.runId}$`),
  );
  await page
    .getByRole("link", { name: `Automation ${s.automation.name}` })
    .click();
  await expect(page).toHaveURL(new RegExp(`/automations/${s.automation.id}$`));
});

test("unknown automations and runs show not-found pages", async ({ page }) => {
  for (const path of [
    "/automations/00000000-0000-4000-8000-000000000000",
    "/automations/not-an-id",
    "/automation-runs/00000000-0000-4000-8000-000000000000",
  ]) {
    await page.goto(path);
    await expect(
      page.getByRole("heading", { name: /not found/ }),
    ).toBeVisible();
    await expect(page).toHaveTitle(/not found/);
  }
});
