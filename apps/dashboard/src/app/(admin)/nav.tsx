"use client";

import Link from "next/link";
import { usePathname, useSearchParams } from "next/navigation";
import { Icon } from "@/components/icons";
import {
  isFailuresView,
  parseEmailFilters,
  toSearchParams,
} from "@/lib/email-filters";

/**
 * The main navigation. A client component because the layout around it does
 * not re-render on navigation, so the current page is read from the URL here.
 */
export function MainNav() {
  const pathname = usePathname();
  const search = useSearchParams();
  const failures =
    pathname === "/emails" &&
    isFailuresView(parseEmailFilters(toSearchParams(search)).filters);

  // "page" marks the exact page; "true" marks the section of a detail page.
  const section = (base: string) =>
    pathname === base
      ? "page"
      : pathname.startsWith(`${base}/`)
        ? "true"
        : undefined;
  const current = (
    item:
      | "overview"
      | "messages"
      | "failures"
      | "broadcasts"
      | "automations"
      | "templates"
      | "projects",
  ) => {
    if (item === "overview") {
      return pathname === "/" ? "page" : undefined;
    }
    if (item === "templates" || item === "projects" || item === "broadcasts") {
      return section(`/${item}`);
    }
    if (item === "automations") {
      return (
        section("/automations") ??
        (pathname.startsWith("/automation-runs/") ? "true" : undefined)
      );
    }
    if (item === "failures") {
      return failures ? "page" : undefined;
    }
    if (pathname === "/emails") {
      return failures ? undefined : "page";
    }
    return pathname.startsWith("/emails/") ? "true" : undefined;
  };

  return (
    <nav aria-label="Main">
      <ul>
        <li>
          <Link href="/" aria-current={current("overview")}>
            <Icon name="overview" />
            Overview
          </Link>
        </li>
        <li>
          <Link href="/emails" aria-current={current("messages")}>
            <Icon name="messages" />
            Messages
          </Link>
        </li>
        <li>
          <Link href="/emails?status=failed" aria-current={current("failures")}>
            <Icon name="failures" />
            Failures
          </Link>
        </li>
        <li>
          <Link href="/broadcasts" aria-current={current("broadcasts")}>
            <Icon name="broadcasts" />
            Broadcasts
          </Link>
        </li>
        <li>
          <Link href="/automations" aria-current={current("automations")}>
            <Icon name="automations" />
            Automations
          </Link>
        </li>
        <li>
          <Link href="/templates" aria-current={current("templates")}>
            <Icon name="templates" />
            Templates
          </Link>
        </li>
        <li>
          <Link href="/projects" aria-current={current("projects")}>
            <Icon name="projects" />
            Projects
          </Link>
        </li>
      </ul>
    </nav>
  );
}
