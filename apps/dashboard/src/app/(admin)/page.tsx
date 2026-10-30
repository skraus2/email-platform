import type { Metadata } from "next";
import Link from "next/link";
import { Icon, type IconName } from "@/components/icons";

export const metadata: Metadata = { title: "Overview" };

/** The console's areas, in the order of the navigation. */
const AREAS: { href: string; icon: IconName; title: string; text: string }[] = [
  {
    href: "/emails",
    icon: "messages",
    title: "Messages",
    text: "Search and filter the emails of all projects.",
  },
  {
    href: "/emails?status=failed",
    icon: "failures",
    title: "Failures",
    text: "Emails that could not be sent.",
  },
  {
    href: "/broadcasts",
    icon: "broadcasts",
    title: "Broadcasts",
    text: "Newsletters to an audience and how far their recipient emails got.",
  },
  {
    href: "/automations",
    icon: "automations",
    title: "Automations",
    text: "Flows started by app events, their steps and the state of each run.",
  },
  {
    href: "/templates",
    icon: "templates",
    title: "Templates",
    text: "Published versions, their source and a preview with sample values.",
  },
  {
    href: "/projects",
    icon: "projects",
    title: "Projects",
    text: "Sender identities and webhook endpoints per project.",
  },
];

export default function Overview() {
  return (
    <>
      <h1>Overview</h1>
      <p className="muted">Operator console for the internal email platform.</p>
      {/* role: Safari drops the list role of a list without markers. */}
      <ul className="areas" role="list">
        {AREAS.map((a) => (
          <li key={a.href}>
            <span className="area-icon">
              <Icon name={a.icon} size={18} />
            </span>
            {/* The link covers the whole card (see .areas a::after), but its
                name stays the title. */}
            <Link href={a.href}>{a.title}</Link>
            <p className="muted">{a.text}</p>
          </li>
        ))}
      </ul>
    </>
  );
}
