import type { Metadata } from "next";
import "./globals.css";

// No next/font/google: builds must not fetch from external services.
export const metadata: Metadata = {
  title: "Email Platform",
  description: "Internal email platform dashboard",
  robots: { index: false, follow: false },
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
