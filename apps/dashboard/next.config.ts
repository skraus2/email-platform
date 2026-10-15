import type { NextConfig } from "next";

// Every page renders per request: pages read the admin session, show live
// operator data and carry a per-request CSP nonce, which Partial
// Prerendering (Cache Components) cannot provide.
const nextConfig: NextConfig = {
  poweredByHeader: false,
};

export default nextConfig;
