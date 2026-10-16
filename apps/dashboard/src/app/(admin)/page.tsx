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
      </ul>
    </>
  );
}
