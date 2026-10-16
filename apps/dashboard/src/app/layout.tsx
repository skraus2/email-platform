import type { Metadata } from "next";
import { connection } from "next/server";
import "./globals.css";

// No next/font/google: builds must not fetch from external services.
export const metadata: Metadata = {
  title: { template: "%s · Email Platform", default: "Email Platform" },
  description: "Internal email platform dashboard",
  robots: { index: false, follow: false },
};

// Every page renders per request so it carries the request's CSP nonce
// (no prerendered pages: their scripts would lack the nonce).
export default async function RootLayout({ children }: LayoutProps<"/">) {
  await connection();
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
