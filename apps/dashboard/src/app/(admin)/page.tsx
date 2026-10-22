import type { Metadata } from "next";
import Link from "next/link";

export const metadata: Metadata = { title: "Overview" };

export default function Overview() {
  return (
    <>
      <h1>Overview</h1>
      <p>Operator console for the internal email platform.</p>
      <ul>
        <li>
          <Link href="/emails">Messages</Link>: search and filter the emails of
          all projects.
        </li>
        <li>
          <Link href="/emails?status=failed">Failures</Link>: emails that could
          not be sent.
        </li>
        <li>
          <Link href="/broadcasts">Broadcasts</Link>: newsletters to an audience
          and how far their recipient emails got.
        </li>
        <li>
          <Link href="/automations">Automations</Link>: flows started by app
          events, their steps and the state of each run.
        </li>
        <li>
          <Link href="/templates">Templates</Link>: published versions, their
          source and a preview with sample values.
        </li>
        <li>
          <Link href="/projects">Projects</Link>: sender identities and webhook
          endpoints per project.
        </li>
      </ul>
    </>
  );
}
