import "server-only";
import { redirect } from "next/navigation";
import { buildAdminURL } from "./admin-url";
import { collectPages, type Page } from "./paging";
import type { VariableSchema } from "./preview-vars";
import { sessionToken } from "./session";

/** Requests to the admin API give up after this long. */
const TIMEOUT_MS = 10_000;

/** Builds an admin API URL on the configured base (ADMIN_API_URL). */
export function adminURL(path: string, query?: URLSearchParams): URL {
  return buildAdminURL(
    process.env["ADMIN_API_URL"] ?? "http://127.0.0.1:8081",
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
    headers["Authorization"] = `Session ${token}`;
  }
  if (init.body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  const res = await fetch(adminURL(path, init.query), {
    method: init.method ?? "GET",
    headers,
    body: init.body === undefined ? null : JSON.stringify(init.body),
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

export type { Page };

/** Loads every page of a list, up to maxRows rows (see collectPages). */
export function adminFetchAll<T>(
  path: string,
  maxRows = 500,
  filter: Record<string, string> = {},
): Promise<{ rows: T[]; more: boolean }> {
  return collectPages<T>((cursor) => {
    const query = new URLSearchParams({ ...filter, limit: "100" });
    if (cursor) {
      query.set("cursor", cursor);
    }
    return adminFetch<Page<T>>(path, { query });
  }, maxRows);
}

export type Project = {
  id: string;
  name: string;
  status: "active" | "disabled";
  created_at: string;
};

export type AdminEmail = {
  id: string;
  project_id: string;
  project_name: string;
  status: string;
  from: string;
  to: string;
  subject: string;
  provider: string | null;
  provider_id: string | null;
  attempts: number;
  error_code: string | null;
  template_id: string | null;
  template_version: number | null;
  created_at: string;
  updated_at: string;
  finished_at: string | null;
  scheduled_at: string | null;
  broadcast_id: string | null;
  automation_id: string | null;
  automation_run_id: string | null;
};

export type EmailEvent = {
  id: string;
  type: string;
  origin: string;
  provider: string | null;
  occurred_at: string;
  received_at: string;
  details: Record<string, string>;
};

export type WebhookDelivery = {
  id: string;
  endpoint_id: string;
  endpoint_url: string;
  event_id: string;
  event_type: string;
  status: "pending" | "delivered" | "failed";
  attempts: number;
  last_status_code: number | null;
  last_error_code: string | null;
  next_attempt_at: string | null;
  delivered_at: string | null;
  created_at: string;
  updated_at: string;
};

export type Template = {
  id: string;
  project_id: string;
  slug: string;
  name: string;
  latest_version: number | null;
  created_at: string;
};

export type TemplateDetail = Template & {
  /** All versions, newest first. */
  versions: { version: number; created_at: string }[];
};

export type TemplateVersion = {
  template_id: string;
  project_id: string;
  version: number;
  subject: string;
  text: string;
  html: string | null;
  variables: VariableSchema;
  created_at: string;
};

export type Rendered = { subject: string; text: string; html: string | null };

export type Sender = {
  id: string;
  project_id: string;
  kind: "address" | "domain";
  value: string;
  status: string;
  verified_at: string | null;
  created_at: string;
};

export type WebhookEndpoint = {
  id: string;
  project_id: string;
  url: string;
  event_types: string[];
  status: "active" | "disabled";
  created_at: string;
  updated_at: string;
};

export type BroadcastStatus =
  "draft" | "scheduled" | "sending" | "completed" | "canceled";

export type Broadcast = {
  id: string;
  project_id: string;
  project_name: string;
  name: string;
  status: BroadcastStatus;
  audience_id: string;
  from: string;
  template_id: string;
  template_version: number;
  unsubscribe_url: string;
  scheduled_at: string | null;
  started_at: string | null;
  finished_at: string | null;
  created_at: string;
};

export type BroadcastDetail = Broadcast & {
  counts: {
    recipients: number;
    skipped_unsubscribed: number;
    skipped_suppressed: number;
    skipped_invalid: number;
    by_status: Record<string, number>;
  };
};

export type AutomationStatus = "active" | "paused" | "archived";

export type Automation = {
  id: string;
  project_id: string;
  project_name: string;
  name: string;
  status: AutomationStatus;
  trigger_event: string;
  allow_reentry: boolean;
  step_count: number;
  created_at: string;
  updated_at: string;
};

/** A step as operators see it (send steps without shared variables). */
export type AutomationStep =
  | { type: "wait"; seconds: number }
  | {
      type: "condition";
      attribute?: string;
      event?: string;
      op: string;
      value?: string | number | boolean;
    }
  | {
      type: "send";
      from: string;
      template_id: string;
      template_version: number;
      unsubscribe_url: string;
    };

export type RunStatus =
  "active" | "completed" | "exited" | "canceled" | "failed";

export type AutomationDetail = Automation & {
  steps: AutomationStep[];
  run_counts: Record<RunStatus, number>;
};

export type AutomationRun = {
  id: string;
  contact_id: string;
  status: RunStatus;
  reason: string | null;
  step: number;
  next_step_at: string | null;
  created_at: string;
  updated_at: string;
  finished_at: string | null;
};

export type AutomationRunDetail = AutomationRun & {
  project_id: string;
  project_name: string;
  automation_id: string;
  automation_name: string;
  emails: { id: string; step: number; status: string }[];
};
