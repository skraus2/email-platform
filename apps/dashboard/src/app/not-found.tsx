import type { Metadata } from "next";
import Link from "next/link";

export const metadata: Metadata = { title: "Not found" };

export default function NotFound() {
  return (
    <main className="login">
      <h1>Not found</h1>
      <p>There is nothing at this address.</p>
      <Link href="/">Back to the overview</Link>
    </main>
  );
}
