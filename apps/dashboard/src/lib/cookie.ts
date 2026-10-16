/**
 * Name of the dashboard session cookie. In production the __Host- prefix
 * makes the browser refuse any version of it that is not Secure, Path=/ and
 * without Domain, so a sibling subdomain cannot plant its own session.
 * Shared by the proxy and the server code (no server-only imports here).
 */
export const SESSION_COOKIE =
  process.env.NODE_ENV === "production" ? "__Host-ep_session" : "ep_session";
