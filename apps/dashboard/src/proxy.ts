import { NextResponse, type NextRequest } from "next/server";
import { SESSION_COOKIE } from "./lib/cookie";

/**
 * Sets a per-request Content Security Policy with a script nonce and sends
 * requests without a session cookie to the login page. The redirect is only
 * an optimistic shortcut: every page checks the session again when it calls
 * the admin API.
 */
export function proxy(request: NextRequest) {
  const nonce = Buffer.from(crypto.randomUUID()).toString("base64");
  const dev = process.env.NODE_ENV === "development";
  // Styles allow inline (email previews carry inline styles; scripts stay
  // nonce-locked). Images only from the dashboard itself or data: URLs, so a
  // previewed email cannot load remote content.
  const csp = [
    "default-src 'self'",
    `script-src 'self' 'nonce-${nonce}' 'strict-dynamic'${dev ? " 'unsafe-eval'" : ""}`,
    "style-src 'self' 'unsafe-inline'",
    "img-src 'self' data:",
    "font-src 'self'",
    "connect-src 'self'",
    "object-src 'none'",
    "base-uri 'none'",
    "form-action 'self'",
    "frame-ancestors 'none'",
    "frame-src 'self'",
  ].join("; ");

  const { pathname } = request.nextUrl;
  if (pathname !== "/login" && !request.cookies.has(SESSION_COOKIE)) {
    const url = request.nextUrl.clone();
    url.pathname = "/login";
    url.search = "";
    return withHeaders(NextResponse.redirect(url), csp);
  }

  const requestHeaders = new Headers(request.headers);
  requestHeaders.set("x-nonce", nonce);
  requestHeaders.set("Content-Security-Policy", csp);
  return withHeaders(
    NextResponse.next({ request: { headers: requestHeaders } }),
    csp,
  );
}

function withHeaders(res: NextResponse, csp: string) {
  res.headers.set("Content-Security-Policy", csp);
  res.headers.set("Cache-Control", "no-store");
  res.headers.set("Referrer-Policy", "no-referrer");
  res.headers.set("X-Content-Type-Options", "nosniff");
  res.headers.set(
    "Permissions-Policy",
    "camera=(), microphone=(), geolocation=(), payment=(), usb=()",
  );
  if (process.env.NODE_ENV === "production") {
    // Production runs behind TLS (the session cookie is Secure); browsers
    // ignore this header over plain HTTP.
    res.headers.set("Strict-Transport-Security", "max-age=31536000");
  }
  return res;
}

export const config = {
  matcher: [
    {
      source: "/((?!_next/static|_next/image|favicon.ico).*)",
      missing: [
        { type: "header", key: "next-router-prefetch" },
        { type: "header", key: "purpose", value: "prefetch" },
      ],
    },
  ],
};
