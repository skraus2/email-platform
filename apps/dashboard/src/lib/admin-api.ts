import "server-only";
import { redirect } from "next/navigation";
import { sessionToken } from "./session";

/**
 * The admin API base URL. Server-side only: it is never sent to browsers,
 * which talk to the dashboard alone.
 */
function baseURL(): string {
  return process.env.ADMIN_API_URL ?? "http://127.0.0.1:8081";
}

/** An error answer of the admin API ({"error":{"code","message"}}). */
export class AdminAPIError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly retryAfter?: number,
  ) {
    super(message);
  }
}

type Init = {
  method?: "GET" | "POST" | "DELETE";
  body?: unknown;
  /** Send the request without the session (login). */
  anonymous?: boolean;
};

/**
 * Calls the admin API with the session of this request. Never cached. A
 * missing or rejected session ends at the login page.
 */
export async function adminFetch<T>(path: string, init: Init = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (!init.anonymous) {
    const token = await sessionToken();
    if (!token) {
      redirect("/login");
    }
    headers.Authorization = `Session ${token}`;
  }
  if (init.body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  const res = await fetch(baseURL() + path, {
    method: init.method ?? "GET",
    headers,
    body: init.body === undefined ? undefined : JSON.stringify(init.body),
    cache: "no-store",
    redirect: "error",
  });
  if (res.status === 401 && !init.anonymous) {
    redirect("/login?expired=1");
  }
  if (res.status === 204) {
    return undefined as T;
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string } }).error;
    const retry = Number(res.headers.get("Retry-After"));
    throw new AdminAPIError(
      res.status,
      err?.code ?? "unexpected",
      err?.message ?? `admin API answered ${res.status}`,
      Number.isFinite(retry) && retry > 0 ? retry : undefined,
    );
  }
  return data as T;
}

export type Me = { id: string; username: string; session_expires_at: string };

export type Page<T> = { data: T[]; next_cursor: string | null };
