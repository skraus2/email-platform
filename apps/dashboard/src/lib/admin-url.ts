/**
 * Builds an admin API URL from the configured base (ADMIN_API_URL) and a
 * fixed path (dynamic segments already encodeURIComponent-ed). The base must
 * be a plain http(s) URL without credentials, query or fragment; a path
 * prefix keeps working with or without a trailing slash. The result always
 * stays on the base's origin.
 */
export function buildAdminURL(
  base: string,
  path: string,
  query?: URLSearchParams,
): URL {
  const url = new URL(base);
  if (
    (url.protocol !== "http:" && url.protocol !== "https:") ||
    url.username ||
    url.password ||
    url.search ||
    url.hash
  ) {
    throw new Error("ADMIN_API_URL must be a plain http(s) URL");
  }
  if (
    !path.startsWith("/admin/v1/") ||
    path.includes("..") ||
    path.includes("//") ||
    /[?#\\]/.test(path)
  ) {
    throw new Error("invalid admin API path");
  }
  const prefix = url.pathname.replace(/\/+$/, "");
  const out = new URL(url.origin + prefix + path);
  if (out.origin !== url.origin) {
    throw new Error("invalid admin API path");
  }
  if (query) {
    out.search = query.toString();
  }
  return out;
}
