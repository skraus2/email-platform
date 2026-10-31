// Verification of the webhook requests the platform sends to an app. A
// request is accepted only when its timestamp is within the tolerance of the
// app's clock and one `v1=` value of `Webhook-Signature` is the HMAC-SHA256,
// keyed with the endpoint secret, of `<Webhook-Timestamp>.<raw body>`. The
// body is parsed only after the signature matched, and the event id to
// deduplicate on is the signed body `id`, never the unsigned header alone.

import { createHmac, timingSafeEqual } from "node:crypto";
import { types } from "node:util";

import type { components } from "./generated/openapi.ts";

/**
 * Request headers as WHATWG `Headers`, Node's `IncomingHttpHeaders` or a
 * plain record (names in any case).
 */
export type WebhookHeaders =
  Headers | Readonly<Record<string, string | readonly string[] | undefined>>;

export type VerifyWebhookOptions = {
  /** The endpoint's signing secret (`whsec_…`), exactly as shown. */
  secret: string;
  headers: WebhookHeaders;
  /** The raw request body, as received; never re-serialised JSON. */
  body: string | Uint8Array;
  /** Accepted clock difference in milliseconds (default 300 000, at most 3 600 000). */
  tolerance?: number | undefined;
  /** The current time in milliseconds since the epoch (default `Date.now()`). */
  now?: number | undefined;
};

type SchemaEvent = components["schemas"]["WebhookEvent"];

/**
 * A verified event. Event types added by later platform versions are
 * passed through, so `type` may be a value this SDK version does not know.
 */
export type WebhookEvent = Omit<SchemaEvent, "type"> & {
  type: SchemaEvent["type"] | (string & {});
};

export type VerifiedWebhook = {
  /** The signed event id: deduplicate on it. */
  id: string;
  event: WebhookEvent;
};

export type WebhookVerificationReason =
  /** No usable `whsec_…` secret was passed: a configuration fault. */
  | "missing_secret"
  /** The body is not a string or bytes, or the signed body is not an event. */
  | "bad_body"
  /** `Webhook-Id`, `Webhook-Timestamp` or `Webhook-Signature` is absent or empty. */
  | "missing_header"
  /** A webhook header is repeated, joined or not a string. */
  | "bad_header"
  /** The timestamp is malformed or outside the tolerance (see `skew`). */
  | "bad_timestamp"
  /** No `v1=` value matches. */
  | "signature_mismatch"
  /** `Webhook-Id` differs from the signed body `id`. */
  | "id_mismatch";

/** The request must be rejected. Carries no input values. */
export class WebhookVerificationError extends Error {
  override readonly name = "WebhookVerificationError";
  readonly reason: WebhookVerificationReason;
  /**
   * For a timestamp outside the tolerance: `past` when it is older than the
   * app's clock allows, `future` when it is ahead of it.
   */
  readonly skew: "past" | "future" | undefined;

  constructor(reason: WebhookVerificationReason, skew?: "past" | "future") {
    super(
      `webhook verification failed: ${reason}` +
        (skew === undefined ? "" : ` (timestamp in the ${skew})`),
    );
    this.reason = reason;
    this.skew = skew;
  }
}

const defaultToleranceMs = 5 * 60 * 1000;
const maxToleranceMs = 60 * 60 * 1000;
const maxSignatureValues = 8;
// Eight `v1=` values with separators fit with room to spare; anything
// longer is rejected before it is split.
const maxSignatureLength = 1024;
const timestampPattern = /^[0-9]{1,12}$/;
const macPattern = /^[0-9a-f]{64}$/;
const headerId = "webhook-id";
const headerTimestamp = "webhook-timestamp";
const headerSignature = "webhook-signature";

/**
 * Verifies a webhook request and returns the event with its signed id.
 * Throws `WebhookVerificationError` for a request to reject, and
 * `TypeError`/`RangeError` for invalid options.
 */
export function verifyWebhook(options: VerifyWebhookOptions): VerifiedWebhook {
  if (typeof options !== "object" || (options as unknown) === null) {
    throw new TypeError("verifyWebhook needs an options object");
  }
  const secret: unknown = options.secret;
  if (
    typeof secret !== "string" ||
    !secret.startsWith("whsec_") ||
    secret.length === "whsec_".length
  ) {
    throw new WebhookVerificationError("missing_secret");
  }
  const tolerance = checkTolerance(options.tolerance);
  const now = checkNow(options.now);

  const body = bodyBytes(options.body);
  const read = headerReader(options.headers);
  const id = read(headerId);
  const timestamp = read(headerTimestamp);
  const signature = read(headerSignature);
  if (id.includes(",") || timestamp.includes(",")) {
    throw new WebhookVerificationError("bad_header");
  }

  if (!timestampPattern.test(timestamp)) {
    throw new WebhookVerificationError("bad_timestamp");
  }
  const age = now - Number(timestamp) * 1000;
  if (age > tolerance) {
    throw new WebhookVerificationError("bad_timestamp", "past");
  }
  if (age < -tolerance) {
    throw new WebhookVerificationError("bad_timestamp", "future");
  }

  if (signature.length > maxSignatureLength) {
    throw new WebhookVerificationError("signature_mismatch");
  }
  const values = signature.split(/[ \t]+/).filter((value) => value !== "");
  if (values.length > maxSignatureValues) {
    throw new WebhookVerificationError("signature_mismatch");
  }
  const expected = createHmac("sha256", Buffer.from(secret, "utf8"))
    .update(`${timestamp}.`, "utf8")
    .update(body)
    .digest();
  let matched = false;
  for (const value of values) {
    const hex = value.startsWith("v1=") ? value.slice("v1=".length) : "";
    if (
      macPattern.test(hex) &&
      timingSafeEqual(Buffer.from(hex, "hex"), expected)
    ) {
      matched = true;
    }
  }
  if (!matched) {
    throw new WebhookVerificationError("signature_mismatch");
  }

  const event = parseEvent(body);
  if (event.id !== id) {
    throw new WebhookVerificationError("id_mismatch");
  }
  return { id: event.id, event };
}

function checkTolerance(value: unknown): number {
  if (value === undefined) {
    return defaultToleranceMs;
  }
  if (
    typeof value !== "number" ||
    !Number.isFinite(value) ||
    value <= 0 ||
    value > maxToleranceMs
  ) {
    throw new RangeError(
      `tolerance must be a number of milliseconds between 1 and ${maxToleranceMs}`,
    );
  }
  return value;
}

function checkNow(value: unknown): number {
  if (value === undefined) {
    return Date.now();
  }
  if (typeof value !== "number" || !Number.isFinite(value)) {
    throw new RangeError("now must be a finite number of milliseconds");
  }
  return value;
}

function bodyBytes(value: unknown): Uint8Array {
  if (typeof value === "string") {
    return Buffer.from(value, "utf8");
  }
  // Also byte arrays from another realm (e.g. a vm context).
  if (types.isUint8Array(value)) {
    return value;
  }
  throw new WebhookVerificationError("bad_body");
}

/**
 * Returns a lookup of one header's single value. Headers that are absent
 * or empty, repeated (two names differing in case, an array) or not a
 * string are rejected.
 */
function headerReader(headers: unknown): (name: string) => string {
  if (typeof headers !== "object" || headers === null) {
    throw new WebhookVerificationError("missing_header");
  }
  // A `get` method marks `Headers`; one inherited from a polluted
  // `Object.prototype` does not turn a plain record into one.
  const get: unknown = (headers as { get?: unknown }).get;
  if (
    typeof get === "function" &&
    get !== (Object.prototype as { get?: unknown }).get
  ) {
    return (name) => single(get.call(headers, name) as unknown);
  }
  const record = headers as Record<string, unknown>;
  return (name) => {
    const keys = Object.keys(record).filter(
      (key) => key.toLowerCase() === name,
    );
    if (keys.length > 1) {
      throw new WebhookVerificationError("bad_header");
    }
    const [key] = keys;
    return single(key === undefined ? undefined : record[key]);
  };
}

function single(value: unknown): string {
  if (value === undefined || value === null || value === "") {
    throw new WebhookVerificationError("missing_header");
  }
  if (typeof value !== "string") {
    throw new WebhookVerificationError("bad_header");
  }
  return value;
}

const utf8 = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true });

function parseEvent(body: Uint8Array): WebhookEvent {
  let event: unknown;
  try {
    event = JSON.parse(utf8.decode(body));
  } catch {
    throw new WebhookVerificationError("bad_body");
  }
  if (
    typeof event !== "object" ||
    event === null ||
    Array.isArray(event) ||
    typeof (event as { id?: unknown }).id !== "string" ||
    (event as { id: string }).id === "" ||
    typeof (event as { type?: unknown }).type !== "string"
  ) {
    throw new WebhookVerificationError("bad_body");
  }
  return event as WebhookEvent;
}
