import "server-only";
import { cookies } from "next/headers";

/** Name of the dashboard session cookie (holds the admin API token). */
export const SESSION_COOKIE = "ep_session";

/** The session token of this request, if any. */
export async function sessionToken(): Promise<string | undefined> {
  return (await cookies()).get(SESSION_COOKIE)?.value;
}

/**
 * Stores the token: HttpOnly (never readable by page scripts), SameSite=Lax
 * (shared filter links keep working; every mutation is a POST Server Action
 * whose Origin Next.js checks), Secure in production, and no longer than the
 * session itself.
 */
export async function storeSession(token: string, expiresAt: Date) {
  (await cookies()).set(SESSION_COOKIE, token, {
    httpOnly: true,
    sameSite: "lax",
    secure: process.env.NODE_ENV === "production",
    path: "/",
    expires: expiresAt,
  });
}

export async function clearSession() {
  (await cookies()).delete(SESSION_COOKIE);
}
