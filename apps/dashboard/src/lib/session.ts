import "server-only";
import { cookies, headers } from "next/headers";
import { SESSION_COOKIE } from "./cookie";

export { SESSION_COOKIE };

/** The session token of this request, if any. */
export async function sessionToken(): Promise<string | undefined> {
  return (await cookies()).get(SESSION_COOKIE)?.value;
}

/**
 * Attributes of the session cookie: HttpOnly (never readable by page
 * scripts), SameSite=Lax (shared filter links keep working; every mutation
 * is a POST Server Action whose Origin Next.js checks), Secure in production
 * or over HTTPS, Path=/. Setting and clearing must use the same attributes:
 * a __Host- cookie without Secure is ignored by browsers, so a clearing
 * header without it would leave the session in the browser.
 */
async function cookieAttributes() {
  const https = (await headers()).get("x-forwarded-proto") === "https";
  return {
    httpOnly: true,
    sameSite: "lax" as const,
    secure: process.env.NODE_ENV === "production" || https,
    path: "/",
  };
}

/** Stores the token, no longer than the session itself. */
export async function storeSession(token: string, expiresAt: Date) {
  (await cookies()).set(SESSION_COOKIE, token, {
    ...(await cookieAttributes()),
    expires: expiresAt,
  });
}

/** Removes the session cookie from the browser. */
export async function clearSession() {
  (await cookies()).set(SESSION_COOKIE, "", {
    ...(await cookieAttributes()),
    expires: new Date(0),
    maxAge: 0,
  });
}
