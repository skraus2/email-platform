import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// The calendars take "today" from the browser clock in UTC: fixed, so the
// days and disabled future days are the same on every run. The emails
// themselves are seeded at the real time; these tests check the fields and
// the URL, not which emails match.
const NOW = new Date("2026-10-30T12:00:00Z");

const field = (page: Page, end: "From" | "To") =>
  page.getByRole("textbox", {
    name: end === "From" ? "From date (UTC)" : "To date (UTC, inclusive)",
  });
const opener = (page: Page, end: "from" | "to") =>
  page.getByRole("button", { name: `Choose ${end} date`, exact: true });
const calendar = (page: Page, end: "from" | "to") =>
  page.getByRole("dialog", { name: `Choose ${end} date` });
const day = (page: Page, end: "from" | "to", name: string) =>
  calendar(page, end).getByRole("button", { name, exact: true });

test.describe("the calendar", () => {
  test.beforeEach(async ({ page }) => {
    await page.clock.setFixedTime(NOW);
  });

  test("the keyboard moves through the calendar and Enter picks the day", async ({
    page,
  }) => {
    await page.goto("/emails");
    await opener(page, "from").click();
    await expect(calendar(page, "from")).toBeVisible();
    await expect(calendar(page, "from")).toContainText("October 2026");
    // It opens on today (UTC).
    await expect(day(page, "from", "Friday, 30 October 2026")).toBeFocused();

    for (const [key, name] of [
      ["ArrowLeft", "Thursday, 29 October 2026"],
      ["ArrowUp", "Thursday, 22 October 2026"],
      ["Home", "Monday, 19 October 2026"],
      ["End", "Sunday, 25 October 2026"],
      ["ArrowDown", "Sunday, 1 November 2026"],
      ["PageUp", "Thursday, 1 October 2026"],
      ["Shift+PageUp", "Wednesday, 1 October 2025"],
      ["PageDown", "Saturday, 1 November 2025"],
    ] as const) {
      await page.keyboard.press(key);
      await expect(day(page, "from", name), key).toBeFocused();
    }
    await expect(calendar(page, "from")).toContainText("November 2025");

    await page.keyboard.press("Enter");
    await expect(calendar(page, "from")).toBeHidden();
    await expect(field(page, "From")).toHaveValue("2025-11-01");
    await expect(opener(page, "from")).toBeFocused();

    await opener(page, "to").click();
    await expect(day(page, "to", "Friday, 30 October 2026")).toBeFocused();
    await page.keyboard.press("Space");
    await expect(field(page, "To")).toHaveValue("2026-10-30");

    // The same URL parameters as typing the dates.
    await page.getByRole("button", { name: "Apply filters" }).click();
    await expect(page).toHaveURL(/[?&]from=2025-11-01&to=2026-10-30(&|$)/);
    await expect(field(page, "From")).toHaveValue("2025-11-01");
    await expect(field(page, "To")).toHaveValue("2026-10-30");
  });

  test("focus stays in the calendar; Escape and a click elsewhere close it unchanged", async ({
    page,
  }) => {
    await page.goto("/emails?from=2026-10-12");
    await opener(page, "from").click();
    // It opens on the field's day, which is the range's from end.
    await expect(
      day(page, "from", "Monday, 12 October 2026, from date"),
    ).toBeFocused();

    // Tab cycles through the month buttons, the day, Today and Clear.
    const nextMonth = calendar(page, "from").getByRole("button", {
      name: "Next month",
    });
    const order = [
      calendar(page, "from").getByRole("button", { name: "Today" }),
      calendar(page, "from").getByRole("button", { name: "Clear" }),
      calendar(page, "from").getByRole("button", { name: "Previous month" }),
      nextMonth,
      day(page, "from", "Monday, 12 October 2026, from date"),
    ];
    for (const next of order) {
      await page.keyboard.press("Tab");
      await expect(next).toBeFocused();
    }
    await page.keyboard.press("Shift+Tab");
    await expect(nextMonth).toBeFocused();

    await page.keyboard.press("Escape");
    await expect(calendar(page, "from")).toBeHidden();
    await expect(opener(page, "from")).toBeFocused();
    await expect(field(page, "From")).toHaveValue("2026-10-12");

    await opener(page, "from").click();
    await page.keyboard.press("ArrowRight");
    // A click elsewhere leaves the focus where the click put it.
    const search = page.getByRole("searchbox", { name: "Search" });
    await search.click();
    await expect(calendar(page, "from")).toBeHidden();
    await expect(search).toBeFocused();
    await expect(field(page, "From")).toHaveValue("2026-10-12");

    // Going back to the text field closes it too.
    await opener(page, "from").click();
    await expect(calendar(page, "from")).toBeVisible();
    await field(page, "From").click();
    await expect(calendar(page, "from")).toBeHidden();
    await expect(field(page, "From")).toBeFocused();
  });

  test("Today and Clear set and empty the field; future days cannot be picked", async ({
    page,
  }) => {
    await page.goto("/emails?from=2026-10-12");
    await opener(page, "from").click();
    await calendar(page, "from").getByRole("button", { name: "Today" }).click();
    await expect(calendar(page, "from")).toBeHidden();
    await expect(field(page, "From")).toHaveValue("2026-10-30");

    await opener(page, "from").click();
    const tomorrow = day(page, "from", "Saturday, 31 October 2026");
    await expect(tomorrow).toHaveAttribute("aria-disabled", "true");
    await expect(
      calendar(page, "from").getByRole("button", { name: "Next month" }),
    ).toHaveAttribute("aria-disabled", "true");
    // Neither a press nor the keyboard picks it; both may focus it.
    await tomorrow.click({ force: true });
    await expect(calendar(page, "from")).toBeVisible();
    await expect(tomorrow).toBeFocused();
    await page.keyboard.press("ArrowLeft");
    await expect(
      day(page, "from", "Friday, 30 October 2026, from date"),
    ).toBeFocused();
    await page.keyboard.press("ArrowRight");
    await expect(tomorrow).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(calendar(page, "from")).toBeVisible();
    await expect(field(page, "From")).toHaveValue("2026-10-30");

    await calendar(page, "from").getByRole("button", { name: "Clear" }).click();
    await expect(calendar(page, "from")).toBeHidden();
    await expect(field(page, "From")).toHaveValue("");
    await page.getByRole("button", { name: "Apply filters" }).click();
    await expect(page).toHaveURL(/[?&]from=(&|$)/);
  });

  test("both calendars show the range between the from and to dates", async ({
    page,
  }) => {
    await page.goto("/emails?from=2026-10-07&to=2026-10-21");
    await opener(page, "from").click();
    const inRange = calendar(page, "from").locator("td[data-in-range]");
    await expect(inRange).toHaveCount(15);
    await expect(
      day(page, "from", "Wednesday, 7 October 2026, from date"),
    ).toBeVisible();
    await expect(
      day(page, "from", "Wednesday, 21 October 2026, to date"),
    ).toBeVisible();

    // Opening the other calendar closes this one.
    await opener(page, "to").click();
    await expect(calendar(page, "from")).toBeHidden();
    await expect(calendar(page, "to")).toBeVisible();
    await expect(calendar(page, "to").locator("td[data-in-range]")).toHaveCount(
      15,
    );
    await page.keyboard.press("Escape");

    // Typing follows at once.
    await field(page, "To").fill("2026-10-14");
    await opener(page, "from").click();
    await expect(inRange).toHaveCount(8);
    await page.keyboard.press("Escape");

    // A reversed range shows both ends but no band; the server still rejects it.
    await field(page, "To").fill("2026-10-01");
    await opener(page, "from").click();
    await expect(inRange).toHaveCount(0);
    await expect(
      day(page, "from", "Thursday, 1 October 2026, to date"),
    ).toBeVisible();
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Apply filters" }).click();
    await expect(page.locator("#e-to")).toHaveText(
      "To date must not be before the from date.",
    );
  });
});

// The messages page streams its content, so it needs JavaScript as a whole;
// what the server sends is still a plain text field that a form can submit,
// and the calendar button only appears once the page is interactive.
test("the server renders the date fields as text fields without the calendar button", async ({
  page,
}) => {
  const res = await page.request.get("/emails?from=2026-10-01");
  expect(res.status()).toBe(200);
  const html = await res.text();
  const input = /<input[^>]*\bid="f-from"[^>]*>/.exec(html)?.[0] ?? "";
  expect(input).toContain('type="text"');
  expect(input).toContain('name="from"');
  expect(input).toContain('value="2026-10-01"');
  expect(input).not.toContain('type="date"');
  expect(html).not.toContain("date-field-button");
});
