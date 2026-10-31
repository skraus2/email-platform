// Parsing of the configured base URL and building of request URLs. Error
// messages never repeat the configured value: it may carry credentials.

export type BaseUrl = { readonly origin: string; readonly prefix: string };

export type PathParams = Readonly<Record<string, string | number>>;

export type QueryValue = string | number | boolean | readonly string[];

export type Query = Readonly<Record<string, QueryValue | undefined>>;

// Hostnames as the URL parser reports them; plain http is allowed only here.
const loopbackHosts = new Set(["localhost", "127.0.0.1", "[::1]"]);

/** Validates `baseUrl` and splits it into origin and path prefix. */
export function parseBaseUrl(raw: unknown): BaseUrl {
  if (typeof raw !== "string" || raw === "") {
    throw new TypeError("baseUrl is required");
  }
  if (!URL.canParse(raw)) {
    throw new TypeError("baseUrl is not a valid URL");
  }
  const url = new URL(raw);
  if (url.protocol === "http:") {
    if (!loopbackHosts.has(url.hostname)) {
      throw new TypeError(
        "baseUrl must use https (plain http only for localhost, 127.0.0.1 and [::1])",
      );
    }
  } else if (url.protocol !== "https:") {
    throw new TypeError("baseUrl must use https");
  }
  if (url.username !== "" || url.password !== "" || raw.includes("@")) {
    throw new TypeError("baseUrl must not contain credentials");
  }
  // An empty "?" or "#" leaves search and hash empty, so check the text.
  if (raw.includes("?") || raw.includes("#")) {
    throw new TypeError("baseUrl must not contain a query or fragment");
  }
  const prefix = url.pathname.replace(/\/+/g, "/").replace(/\/$/, "");
  return { origin: url.origin, prefix };
}

/** Joins base, operation path (with parameters filled in) and query. */
export function buildUrl(
  base: BaseUrl,
  template: string,
  pathParams: PathParams,
  query: Query,
): string {
  const path = template.replace(/\{([a-z_]+)\}/g, (_, name: string) => {
    const value = pathParams[name];
    if (value === undefined || value === "") {
      throw new TypeError(`missing path parameter ${name}`);
    }
    const text = String(value);
    // The URL parser resolves these as dot segments (also as %2e, which
    // cannot arise here because "%" is encoded), so a value like ".." would
    // send the request to another operation's path.
    if (text === "." || text === "..") {
      throw new TypeError(`path parameter ${name} must not be . or ..`);
    }
    return encodeURIComponent(text);
  });
  const search = new URLSearchParams();
  for (const [name, value] of Object.entries(query)) {
    if (value === undefined) {
      continue;
    }
    if (typeof value === "string") {
      search.append(name, value);
    } else if (typeof value === "number" || typeof value === "boolean") {
      search.append(name, String(value));
    } else {
      for (const item of value) {
        search.append(name, item);
      }
    }
  }
  const qs = search.toString();
  return `${base.origin}${base.prefix}${path}${qs === "" ? "" : `?${qs}`}`;
}
