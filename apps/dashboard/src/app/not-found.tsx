import type { Metadata } from "next";
import Link from "next/link";
import { Brand } from "@/components/brand";

export const metadata: Metadata = { title: "Not found" };

export default function NotFound() {
  return (
    <main className="login">
      <Brand />
      <h1>Not found</h1>
      <p>There is nothing at this address.</p>
      <Link href="/">Back to the overview</Link>
    </main>
  );
}
