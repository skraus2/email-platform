"use server";

import { redirect } from "next/navigation";
import { AdminAPIError, adminFetch, adminURL } from "@/lib/admin-api";
import { clearSession, sessionToken, storeSession } from "@/lib/session";

export type LoginState =
  | {
      error: string;
      /** credentials: the username/password pair was refused (fields invalid). */
      kind: "credentials" | "other";
      username: string;
      /** Changes on every failure, so a repeated message is announced again. */
      attempt: number;
    }
  | undefined;

/** Logs in at the admin API and keeps the session token in the cookie. */
export async function login(
  prev: LoginState,
  form: FormData,
): Promise<LoginState> {
  const attempt = (prev?.attempt ?? 0) + 1;
  const username = String(form.get("username") ?? "").trim();
  const password = String(form.get("password") ?? "");
  const fail = (
    error: string,
    kind: "credentials" | "other" = "other",
  ): LoginState => ({
    error,
    kind,
    username: username.slice(0, 64),
    attempt,
  });
  if (
    !username ||
    !password ||
    username.length > 64 ||
    [...password].length > 128
  ) {
    return fail(
      "Enter your username and a password of at most 128 characters.",
      "credentials",
    );
  }
  let session: { token: string; expires_at: string };
  try {
    session = await adminFetch("/admin/v1/sessions", {
      method: "POST",
      body: { username, password },
      anonymous: true,
    });
  } catch (err) {
    if (err instanceof AdminAPIError) {
      if (err.status === 401) {
        return fail("Username or password is wrong.", "credentials");
      }
      if (err.status === 429) {
        const minutes = Math.max(1, Math.ceil((err.retryAfter ?? 60) / 60));
        return fail(
          `Too many failed attempts. Try again in ${minutes} minute${minutes === 1 ? "" : "s"}.`,
        );
      }
      if (err.status === 503) {
        return fail("The server is busy. Try again in a moment.");
      }
    }
    return fail("Login is unavailable right now. Try again later.");
  }
  await storeSession(session.token, new Date(session.expires_at));
  redirect("/");
}

/** Ends the session at the admin API and removes the cookie. */
export async function logout() {
  const token = await sessionToken();
  if (token) {
    try {
      const res = await fetch(adminURL("/admin/v1/sessions/current"), {
        method: "DELETE",
        headers: { Authorization: `Session ${token}` },
        cache: "no-store",
        redirect: "error",
        signal: AbortSignal.timeout(10_000),
      });
      if (!res.ok && res.status !== 401) {
        console.warn(
          `dashboard sign-out: admin API answered ${res.status}; the session ends at its idle timeout`,
        );
      }
    } catch {
      console.warn(
        "dashboard sign-out: admin API unreachable; the session ends at its idle timeout",
      );
    }
  }
  await clearSession();
  redirect("/login");
}
