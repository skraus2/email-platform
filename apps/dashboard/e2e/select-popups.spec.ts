import type { Page } from "@playwright/test";
import { expect, seed, test } from "./fixtures";

// The filter form's own list popups: the project select and the error code
// suggestions. These tests check the fields and the URL, not which emails
// match.

const project = (page: Page) =>
  page.getByRole("combobox", { name: "Project", exact: true });
const projectList = (page: Page) =>
  page.getByRole("listbox", { name: "Project", exact: true });
const errorCode = (page: Page) =>
  page.getByRole("combobox", { name: "Error code", exact: true });
const codeList = (page: Page) =>
  page.getByRole("listbox", { name: "Error code", exact: true });
/**
 * Opens the messages page once the custom controls have replaced the native
 * ones: those have the same roles and names, so acting earlier would hit a
 * control that is about to be swapped.
 */
async function open(page: Page, url = "/emails") {
  await page.goto(url);
  await expect(project(page)).toHaveAttribute("aria-haspopup", "listbox");
  await expect(errorCode(page)).toHaveAttribute("aria-autocomplete", "list");
}
const apply = (page: Page) =>
  page.getByRole("button", { name: "Apply filters" }).click();

test.describe("the project select", () => {
  test("the keyboard opens the list, moves and picks; the URL is as before", async ({
    page,
  }) => {
    const s = seed();
    await open(page);
    await expect(project(page)).toHaveText("All projects");

    await project(page).focus();
    await page.keyboard.press("ArrowDown");
    await expect(projectList(page)).toBeVisible();
    await expect(project(page)).toHaveAttribute("aria-expanded", "true");
    const all = projectList(page).getByRole("option", { name: "All projects" });
    await expect(all).toHaveAttribute("aria-selected", "true");
    // The active option is the current one.
    await expect(project(page)).toHaveAttribute(
      "aria-activedescendant",
      (await all.getAttribute("id")) ?? "",
    );

    await page.keyboard.press("End");
    const shop = projectList(page).getByRole("option", { name: s.projectName });
    await expect(project(page)).toHaveAttribute(
      "aria-activedescendant",
      (await shop.getAttribute("id")) ?? "",
    );
    await page.keyboard.press("Enter");
    await expect(projectList(page)).toBeHidden();
    await expect(project(page)).toHaveText(s.projectName);
    await expect(project(page)).toBeFocused();

    await apply(page);
    await expect(page).toHaveURL(
      new RegExp(`[?&]project_id=${s.projectId}(&|$)`),
    );
    await expect(project(page)).toHaveText(s.projectName);
  });

  test("Escape and a click elsewhere close the list unchanged; Tab picks", async ({
    page,
  }) => {
    const s = seed();
    await open(page);

    await project(page).click();
    await expect(projectList(page)).toBeVisible();
    await page.keyboard.press("End");
    await page.keyboard.press("Escape");
    await expect(projectList(page)).toBeHidden();
    await expect(project(page)).toHaveText("All projects");
    await expect(project(page)).toBeFocused();

    await project(page).click();
    await page.getByRole("heading", { name: "Messages", level: 1 }).click();
    await expect(projectList(page)).toBeHidden();
    await expect(project(page)).toHaveText("All projects");

    // Tab picks the active option and moves on.
    await project(page).click();
    await page.keyboard.press("End");
    await page.keyboard.press("Tab");
    await expect(projectList(page)).toBeHidden();
    await expect(project(page)).toHaveText(s.projectName);
    await expect(project(page)).not.toBeFocused();
  });

  test("a click picks an option; typing jumps while closed", async ({
    page,
  }) => {
    const s = seed();
    await open(page);

    await project(page).click();
    await projectList(page)
      .getByRole("option", { name: s.projectName })
      .click();
    await expect(projectList(page)).toBeHidden();
    await expect(project(page)).toHaveText(s.projectName);

    // Type-ahead, as a native select: "a" goes to "All projects".
    await project(page).focus();
    await page.keyboard.press("a");
    await expect(projectList(page)).toBeHidden();
    await expect(project(page)).toHaveText("All projects");
  });
});

test.describe("the error code suggestions", () => {
  test("typing filters the suggestions and Enter picks one", async ({
    page,
  }) => {
    await open(page);
    await errorCode(page).fill("time");
    await expect(codeList(page)).toBeVisible();
    await expect(codeList(page).getByRole("option")).toHaveText([
      "provider_timeout",
    ]);

    await page.keyboard.press("ArrowDown");
    await page.keyboard.press("Enter");
    await expect(codeList(page)).toBeHidden();
    await expect(errorCode(page)).toHaveValue("provider_timeout");
    await expect(errorCode(page)).toBeFocused();
    // Picking a suggestion does not submit the form.
    await expect(page).not.toHaveURL(/error_code=/);

    // Without an active suggestion, Enter submits as before.
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/[?&]error_code=provider_timeout(&|$)/);
    await expect(errorCode(page)).toHaveValue("provider_timeout");
  });

  test("any text is allowed; the arrow button shows all suggestions", async ({
    page,
  }) => {
    await open(page);
    await errorCode(page).fill("custom_code");
    // Nothing matches, so the list stays closed.
    await expect(codeList(page)).toBeHidden();
    await expect(errorCode(page)).toHaveAttribute("aria-expanded", "false");

    await page.getByRole("button", { name: "Show error codes" }).click();
    await expect(codeList(page)).toBeVisible();
    await expect(
      codeList(page).getByRole("option", { name: "suppressed" }),
    ).toBeVisible();
    expect(await codeList(page).getByRole("option").count()).toBeGreaterThan(
      10,
    );
    await expect(errorCode(page)).toBeFocused();

    await page.keyboard.press("Escape");
    await expect(codeList(page)).toBeHidden();
    await expect(errorCode(page)).toHaveValue("custom_code");

    await apply(page);
    await expect(page).toHaveURL(/[?&]error_code=custom_code(&|$)/);
  });
});

// The page streams its content, which needs JavaScript; until the bundle
// has loaded, the streamed form shows the native controls.
test("the server renders native fields with the same names and values", async ({
  page,
}) => {
  const s = seed();
  const res = await page.request.get(
    `/emails?project_id=${s.projectId}&error_code=provider_timeout`,
  );
  expect(res.status()).toBe(200);
  const html = await res.text();

  const select = /<select[^>]*\bid="f-project_id"[^>]*>/.exec(html)?.[0] ?? "";
  expect(select).toContain('name="project_id"');
  const chosen =
    new RegExp(`<option[^>]*value="${s.projectId}"[^>]*>`).exec(html)?.[0] ??
    "";
  expect(chosen).toContain("selected");

  const input = /<input[^>]*\bid="f-error_code"[^>]*>/.exec(html)?.[0] ?? "";
  expect(input).toContain('name="error_code"');
  expect(input).toContain('value="provider_timeout"');
  expect(input).toContain("list=");
  expect(input).not.toContain('role="combobox"');
  expect(html).not.toContain("select-button");
});
