import type { Page } from "@playwright/test";
import { createElement, type ComponentType } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import AutomationRunLoading from "../src/app/(admin)/automation-runs/[id]/loading";
import AutomationLoading from "../src/app/(admin)/automations/[id]/loading";
import AutomationsLoading from "../src/app/(admin)/automations/loading";
import BroadcastLoading from "../src/app/(admin)/broadcasts/[id]/loading";
import BroadcastsLoading from "../src/app/(admin)/broadcasts/loading";
import EmailLoading from "../src/app/(admin)/emails/[id]/loading";
import EmailsLoading from "../src/app/(admin)/emails/loading";
import ProjectLoading from "../src/app/(admin)/projects/[id]/loading";
import ProjectsLoading from "../src/app/(admin)/projects/loading";
import TemplateLoading from "../src/app/(admin)/templates/[id]/loading";
import VersionLoading from "../src/app/(admin)/templates/[id]/versions/[version]/loading";
import TemplatesLoading from "../src/app/(admin)/templates/loading";
import { expect, seed, test } from "./fixtures";

// Each page's loading skeleton must keep the shape of the page: the same
// sequence of back link, title, section headings, cards, tables (with their
// column count), filter panel and text blocks. A page that gains a section
// or a column without its skeleton fails here. Row counts are not compared.
//
// The skeleton is rendered from the route's loading component (it loads no
// data, so this is deterministic; the server only streams it when the page
// is slow). The page's shape comes from the loaded page.

/** The skeleton's shape, from the rendered loading component. */
async function skeletonShape(
  page: Page,
  loading: ComponentType,
): Promise<string[]> {
  const html = renderToStaticMarkup(createElement(loading));
  return page.evaluate((markup) => {
    const doc = new DOMParser().parseFromString(markup, "text/html");
    const root = doc.querySelector(".skeleton");
    if (!root) {
      return ["no skeleton"];
    }
    return Array.from(root.children).map((el) => {
      const c = el.classList;
      if (c.contains("sk-back")) {
        return "back";
      }
      if (c.contains("sk-title")) {
        return "title";
      }
      if (c.contains("sk-h2")) {
        return "heading";
      }
      if (c.contains("sk-filters")) {
        return "filters";
      }
      if (c.contains("sk-block")) {
        return "block";
      }
      if (c.contains("sk-meta")) {
        return "card";
      }
      if (c.contains("sk-table")) {
        const cols = /sk-cols-(\d+)/.exec(el.className)?.[1] ?? "?";
        return `table:${cols}`;
      }
      return `unknown:${el.className}`;
    });
  }, html);
}

/**
 * The loaded page's shape. An empty state stands for whatever the section
 * shows when it has data, so it matches any skeleton part.
 */
async function pageShape(page: Page, url: string): Promise<string[]> {
  await page.goto(url);
  await expect(page.locator("main h1")).toBeVisible();
  await expect(page.locator("main .skeleton")).toHaveCount(0);
  return page.evaluate(() => {
    const main = document.querySelector("main");
    if (!main) {
      return ["no main"];
    }
    const parts = main.querySelectorAll(
      ".back, h1, h2, .table-wrap, dl.meta, ol.steps, form.filters, .source, .preview-form, .empty",
    );
    return Array.from(parts)
      .filter((el) => !el.closest(".preview-result"))
      .map((el) => {
        if (el.classList.contains("back")) {
          return "back";
        }
        if (el.tagName === "H1") {
          return "title";
        }
        if (el.tagName === "H2") {
          return "heading";
        }
        if (el.classList.contains("table-wrap")) {
          const cols = String(el.querySelectorAll("thead th").length);
          // Template tables come one per project; marked so the groups
          // can be counted once below.
          return el.classList.contains("templates")
            ? `table:${cols}*`
            : `table:${cols}`;
        }
        if (el.matches("dl.meta, ol.steps")) {
          return "card";
        }
        if (el.matches("form.filters")) {
          return "filters";
        }
        if (el.matches(".source, .preview-form")) {
          return "block";
        }
        return "any";
      });
  });
}

function pages(): [string, string, ComponentType][] {
  const s = seed();
  return [
    ["messages", "/emails", EmailsLoading],
    ["an email", `/emails/${s.emails.templateEmailId}`, EmailLoading],
    ["broadcasts", "/broadcasts", BroadcastsLoading],
    ["a broadcast", `/broadcasts/${s.broadcast.id}`, BroadcastLoading],
    ["automations", "/automations", AutomationsLoading],
    ["an automation", `/automations/${s.automation.id}`, AutomationLoading],
    [
      "an automation run",
      `/automation-runs/${s.automation.runId}`,
      AutomationRunLoading,
    ],
    ["templates", "/templates", TemplatesLoading],
    ["a template", `/templates/${s.order.id}`, TemplateLoading],
    [
      "a template version",
      `/templates/${s.order.id}/versions/2`,
      VersionLoading,
    ],
    ["projects", "/projects", ProjectsLoading],
    ["a project", `/projects/${s.projectId}`, ProjectLoading],
  ];
}

/**
 * Counts a run of per-project template groups (a heading and a template
 * table each) once: how many projects have templates depends on the data.
 */
function oneTemplateGroup(shape: string[]): string[] {
  const out: string[] = [];
  for (const part of shape) {
    const repeat =
      part.endsWith("*") &&
      out.at(-1) === "heading" &&
      out.at(-2) === part &&
      out.at(-3) === "heading";
    if (repeat) {
      out.pop();
    } else {
      out.push(part);
    }
  }
  return out.map((part) => part.replace(/\*$/, ""));
}

test("every loading skeleton has the shape of its page", async ({ page }) => {
  for (const [name, url, loading] of pages()) {
    const skeleton = await skeletonShape(page, loading);
    const loaded = oneTemplateGroup(await pageShape(page, url));
    expect(
      loaded.length === skeleton.length &&
        loaded.every((part, i) => part === "any" || part === skeleton[i]),
      `${name} (${url})\nskeleton: ${skeleton.join(", ")}\npage:     ${loaded.join(", ")}`,
    ).toBe(true);
  }
});
