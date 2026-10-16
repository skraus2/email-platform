import "server-only";
import { redirect } from "next/navigation";
import { buildAdminURL } from "./admin-url";
import { sessionToken } from "./session";

/** Requests to the admin API give up after this long. */
const TIMEOUT_MS = 10_000;

/** Builds an admin API URL on the configured base (ADMIN_API_URL). */
export function adminURL(path: string, query?: URLSearchParams): URL {
  return buildAdminURL(
    process.env.ADMIN_API_URL ?? "http://127.0.0.1:8081",
    path,
    query,
  );
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
  query?: URLSearchParams;
  /** Send the request without the session (login). */
  anonymous?: boolean;
};

/**
 * Calls the admin API with the session of this request. Never cached.
 *
 * This is the session check of every dashboard page: a missing or rejected
 * session ends at the login page (via redirect(), which throws — callers
 * that catch errors must rethrow it with unstable_rethrow). Layouts do not
 * re-run on client navigation, so each page must load its data through
 * this function, never rely on the layout.
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
  const res = await fetch(adminURL(path, init.query), {
    method: init.method ?? "GET",
    headers,
    body: init.body === undefined ? undefined : JSON.stringify(init.body),
    cache: "no-store",
    redirect: "error",
    signal: AbortSignal.timeout(TIMEOUT_MS),
  });
  if (res.status === 401 && !init.anonymous) {
    redirect("/login?expired=1");
  }
  if (res.status === 204) {
    return undefined as T;
  }
  if (res.ok) {
    return (await res.json()) as T;
  }
  const data = await res.json().catch(() => ({}));
  const err = (data as { error?: { code?: string; message?: string } }).error;
  const retry = Number(res.headers.get("Retry-After"));
  throw new AdminAPIError(
    res.status,
    err?.code ?? "unexpected",
    err?.message ?? `admin API answered ${res.status}`,
    Number.isFinite(retry) && retry > 0 ? retry : undefined,
  );
}

export type Me = { id: string; username: string; session_expires_at: string };

export type Page<T> = { data: T[]; next_cursor: string | null };
