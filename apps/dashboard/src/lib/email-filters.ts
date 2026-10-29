/**
 * Filters of the messages view. They live in the page URL (shareable, the
 * back button works) and are validated here before any admin API call, so
 * a bad value is shown next to the form instead of as a failed page.
 * Messages name the field, never its value. No imports: unit-tested with
 * node --test.
 */

export const EMAIL_STATUSES = [
  "queued",
  "sending",
  "captured",
  "submitted",
  "delivered",
  "bounced",
  "complained",
  "failed",
  "canceled",
] as const;

export type EmailStatus = (typeof EMAIL_STATUSES)[number];

/** Failure codes the platform records; suggestions for the error code field. */
export const FAILURE_CODES = [
  "provider_unreachable",
  "provider_timeout",
  "provider_protocol_error",
  "provider_deferred",
  "provider_throttled",
  "provider_rejected",
  "provider_auth_failed",
  "sender_not_verified",
  "message_invalid",
  "unsafe_target",
  "suppressed",
  "project_disabled",
  "content_missing",
  "expired",
  "internal_error",
  "unsubscribed",
  "contact_deleted",
  "broadcast_canceled",
  "render_failed",
  "automation_canceled",
] as const;

export const PAGE_SIZE = 50;

/** An unset filter is absent or undefined; both mean "no filter". */
export type EmailFilters = {
  project_id?: string | undefined;
  status: EmailStatus[];
  recipient?: string | undefined;
  /** UTC calendar date (YYYY-MM-DD), inclusive. */
  from?: string | undefined;
  /** UTC calendar date (YYYY-MM-DD), inclusive. */
  to?: string | undefined;
  error_code?: string | undefined;
  q?: string | undefined;
  template_id?: string | undefined;
  broadcast_id?: string | undefined;
  cursor?: string | undefined;
};

export type FilterField = Exclude<keyof EmailFilters, "status"> | "status";

export type FilterError = { field: FilterField; message: string };

export type SearchParams = Record<string, string | string[] | undefined>;

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isUUID(v: string): boolean {
  return UUID.test(v);
}

const LABELS: Record<FilterField, string> = {
  project_id: "Project",
  status: "Status",
  recipient: "Recipient",
  from: "From date",
  to: "To date",
  error_code: "Error code",
  q: "Search",
  template_id: "Template",
  broadcast_id: "Broadcast",
  cursor: "Page link",
};

export function parseEmailFilters(sp: SearchParams): {
  filters: EmailFilters;
  errors: FilterError[];
} {
  const filters: EmailFilters = { status: [] };
  const errors: FilterError[] = [];
  const fail = (field: FilterField, message: string) =>
    errors.push({ field, message: `${LABELS[field]} ${message}` });

  const single = (field: Exclude<FilterField, "status">) => {
    const raw = sp[field];
    if (Array.isArray(raw)) {
      fail(field, "may be given only once.");
      return undefined;
    }
    const v = raw?.trim();
    return v ? v : undefined;
  };

  const project = single("project_id");
  if (project !== undefined) {
    if (isUUID(project)) {
      filters.project_id = project.toLowerCase();
    } else {
      fail("project_id", "must be a project id.");
    }
  }

  const statuses = [sp["status"] ?? []].flat().filter((s) => s !== "");
  for (const s of statuses) {
    if (!(EMAIL_STATUSES as readonly string[]).includes(s)) {
      fail("status", "must be one of the listed statuses.");
      break;
    }
    if (!filters.status.includes(s as EmailStatus)) {
      filters.status.push(s as EmailStatus);
    }
  }
  if (errors.some((e) => e.field === "status")) {
    filters.status = [];
  }

  const recipient = single("recipient");
  if (recipient !== undefined) {
    if (
      recipient.length > 254 ||
      !/^[^\s@,;<>"]+@[^\s@,;<>"]+$/.test(recipient)
    ) {
      fail("recipient", "must be a single email address.");
    } else {
      filters.recipient = recipient;
    }
  }

  for (const field of ["from", "to"] as const) {
    const v = single(field);
    if (v === undefined) {
      continue;
    }
    if (dayStart(v) === undefined) {
      fail(field, "must be a date (YYYY-MM-DD).");
    } else {
      filters[field] = v;
    }
  }
  if (filters.from && filters.to && filters.from > filters.to) {
    fail("to", "must not be before the from date.");
    delete filters.to;
  }

  const code = single("error_code");
  if (code !== undefined) {
    if (/^[a-z_]{1,40}$/.test(code)) {
      filters.error_code = code;
    } else {
      fail("error_code", "must be lowercase letters and underscores.");
    }
  }

  const q = single("q");
  if (q !== undefined) {
    if ([...q].length > 512) {
      fail("q", "must be at most 512 characters.");
    } else {
      filters.q = q;
    }
  }

  const template = single("template_id");
  if (template !== undefined) {
    if (isUUID(template)) {
      filters.template_id = template.toLowerCase();
    } else {
      fail("template_id", "must be a template id.");
    }
  }

  const broadcast = single("broadcast_id");
  if (broadcast !== undefined) {
    if (isUUID(broadcast)) {
      filters.broadcast_id = broadcast.toLowerCase();
    } else {
      fail("broadcast_id", "must be a broadcast id.");
    }
  }

  // The cursor is opaque; the admin API checks it.
  const cursor = single("cursor");
  if (cursor !== undefined) {
    if (cursor.length > 200) {
      fail("cursor", "is not valid.");
    } else {
      filters.cursor = cursor;
    }
  }

  return { filters, errors };
}

/** Milliseconds of 00:00 UTC of a YYYY-MM-DD date (years 1000–9998), undefined if invalid. */
function dayStart(date: string): number | undefined {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(date);
  if (!m) {
    return undefined;
  }
  const [y, mo, d] = [Number(m[1]), Number(m[2]), Number(m[3])];
  // Four-digit years only, with room for the day after (to date + 1).
  if (y < 1000 || y > 9998) {
    return undefined;
  }
  const t = Date.UTC(y, mo - 1, d);
  const back = new Date(t);
  if (
    back.getUTCFullYear() !== y ||
    back.getUTCMonth() !== mo - 1 ||
    back.getUTCDate() !== d
  ) {
    return undefined;
  }
  return t;
}

const DAY_MS = 24 * 60 * 60 * 1000;

function rfc3339(ms: number): string {
  return new Date(ms).toISOString().replace(".000Z", "Z");
}

/** The admin API query (GET /admin/v1/emails) for valid filters. */
export function apiQuery(f: EmailFilters): URLSearchParams {
  const q = new URLSearchParams();
  if (f.project_id) q.set("project_id", f.project_id);
  for (const s of f.status) q.append("status", s);
  if (f.recipient) q.set("recipient", f.recipient);
  if (f.from) q.set("created_after", rfc3339(dayStart(f.from)!));
  if (f.to) q.set("created_before", rfc3339(dayStart(f.to)! + DAY_MS));
  if (f.error_code) q.set("error_code", f.error_code);
  if (f.q) q.set("q", f.q);
  if (f.template_id) q.set("template_id", f.template_id);
  if (f.broadcast_id) q.set("broadcast_id", f.broadcast_id);
  q.set("limit", String(PAGE_SIZE));
  if (f.cursor) q.set("cursor", f.cursor);
  return q;
}

/** A link to the messages view with these filters (and a page cursor). */
export function emailsHref(f: EmailFilters, cursor?: string): string {
  const q = new URLSearchParams();
  if (f.project_id) q.set("project_id", f.project_id);
  for (const s of f.status) q.append("status", s);
  if (f.recipient) q.set("recipient", f.recipient);
  if (f.from) q.set("from", f.from);
  if (f.to) q.set("to", f.to);
  if (f.error_code) q.set("error_code", f.error_code);
  if (f.q) q.set("q", f.q);
  if (f.template_id) q.set("template_id", f.template_id);
  if (f.broadcast_id) q.set("broadcast_id", f.broadcast_id);
  if (cursor) q.set("cursor", cursor);
  const s = q.toString();
  return s ? `/emails?${s}` : "/emails";
}

/** Whether any filter (not the page cursor) is set. */
export function hasFilters(f: EmailFilters): boolean {
  return emailsHref(f) !== "/emails";
}

/** The "Failures" preset: exactly status=failed and nothing else. */
export function isFailuresView(f: EmailFilters): boolean {
  return emailsHref(f) === "/emails?status=failed";
}

/** Search params of a URLSearchParams in the shape pages receive. */
export function toSearchParams(q: URLSearchParams): SearchParams {
  const out: SearchParams = {};
  for (const key of new Set(q.keys())) {
    const all = q.getAll(key);
    out[key] = all.length === 1 ? all[0] : all;
  }
  return out;
}

/**
 * The page's query string as received (invalid and empty values included),
 * without the `omit` parameters. Identifies what the URL asked for, also
 * when the parsed filters are the same.
 */
export function queryString(sp: SearchParams, omit: string[] = []): string {
  const q = new URLSearchParams();
  for (const [key, value] of Object.entries(sp)) {
    if (omit.includes(key) || value === undefined) {
      continue;
    }
    for (const v of [value].flat()) {
      q.append(key, v);
    }
  }
  return q.toString();
}
