import { expect, seed, test } from "./fixtures";

test("the project page lists the seeded sender identity", async ({ page }) => {
  const s = seed();
  await page.goto("/projects");
  await page.getByRole("link", { name: s.projectName }).click();
  await expect(page).toHaveURL(new RegExp(`/projects/${s.projectId}$`));
  await expect(
    page.getByRole("heading", { level: 1, name: s.projectName }),
  ).toBeVisible();

  const senders = page.getByRole("region", { name: "Sender identities" });
  const row = senders.locator("tbody tr").filter({ hasText: s.sender.address });
  await expect(row).toHaveCount(1);
  await expect(row.locator("td").nth(1)).toHaveText("address");
  await expect(row.locator("td").nth(2)).toHaveText("verified");
  await expect(row.locator("td").nth(3).locator("time")).toHaveAttribute(
    "datetime",
    /^\d{4}-\d\d-\d\dT/,
  );
});
