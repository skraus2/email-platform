import type { NextConfig } from "next";

// Every page renders per request: pages read the admin session, show live
// operator data and carry a per-request CSP nonce, which Partial
// Prerendering (Cache Components) cannot provide.
const nextConfig: NextConfig = {
  poweredByHeader: false,
  // The container image runs the self-contained server; local runs and the
  // E2E tests keep `next start`.
  output: process.env.NEXT_OUTPUT === "standalone" ? "standalone" : undefined,
};

export default nextConfig;
