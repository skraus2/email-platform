"use server";

import { redirect } from "next/navigation";
import { AdminAPIError, adminFetch } from "@/lib/admin-api";
import { clearSession, sessionToken, storeSession } from "@/lib/session";

export type LoginState = { error?: string; username?: string } | undefined;

/** Logs in at the admin API and keeps the session token in the cookie. */
export async function login(
  _prev: LoginState,
  form: FormData,
): Promise<LoginState> {
  const username = String(form.get("username") ?? "").trim();
  const password = String(form.get("password") ?? "");
  if (!username || !password || username.length > 64 || password.length > 512) {
    return {
      error: "Enter your username and password.",
      username: username.slice(0, 64),
    };
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
        return { error: "Username or password is wrong.", username };
      }
      if (err.status === 429) {
        const minutes = Math.max(1, Math.ceil((err.retryAfter ?? 60) / 60));
        return {
          error: `Too many failed attempts. Try again in ${minutes} minute${minutes === 1 ? "" : "s"}.`,
          username,
        };
      }
      if (err.status === 503) {
        return {
          error: "The server is busy. Try again in a moment.",
          username,
        };
      }
    }
    return {
      error: "Login is unavailable right now. Try again later.",
      username,
    };
  }
  await storeSession(session.token, new Date(session.expires_at));
  redirect("/");
}

/** Ends the session at the admin API and removes the cookie. */
export async function logout() {
  const token = await sessionToken();
  if (token) {
    // The session may already be gone; the cookie is removed regardless.
    await fetch(
      (process.env.ADMIN_API_URL ?? "http://127.0.0.1:8081") +
        "/admin/v1/sessions/current",
      {
        method: "DELETE",
        headers: { Authorization: `Session ${token}` },
        cache: "no-store",
        redirect: "error",
      },
    ).catch(() => undefined);
  }
  await clearSession();
  redirect("/login");
}
