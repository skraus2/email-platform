// The request loop behind every operation: one logical call, its deadline,
// its retries, and the translation of answers into results or errors.

import {
  ApiError,
  TransportError,
  type TransportErrorReason,
} from "./errors.ts";
import { OPERATIONS, isRetryable, type OperationId } from "./operations.ts";
import { retryAfterSeconds, waitMs } from "./retry.ts";
import { buildUrl, type BaseUrl, type PathParams, type Query } from "./url.ts";

/** Everything the loop needs from the outside world; replaced in tests. */
export type Runtime = {
  fetch: (input: string, init: RequestInit) => Promise<Response>;
  /** Monotonic milliseconds. */
  now: () => number;
  /** Resolves after `ms`; rejects with the signal's reason on abort. */
  sleep: (ms: number, signal: AbortSignal | undefined) => Promise<void>;
  /** A signal that aborts after `ms`. */
  deadlineSignal: (ms: number) => AbortSignal;
  /** A number in [0, 1). */
  random: () => number;
  randomUUID: () => string;
};

export type Settings = {
  readonly apiKey: string;
  readonly base: BaseUrl;
  readonly timeout: number;
  readonly maxRetries: number;
};

export type CallInput = {
  readonly path?: PathParams;
  readonly query?: Query;
  readonly body?: unknown;
};

export type CallOptions = {
  /** Aborts the call, including waits between retries. */
  readonly signal?: AbortSignal | undefined;
  /** Milliseconds for the whole call, retries and waits included. */
  readonly timeout?: number | undefined;
  /** Retries after the first attempt; at most 5. */
  readonly maxRetries?: number | undefined;
  /** Only for `createEmail` and `createContactEvent`. */
  readonly idempotencyKey?: string | undefined;
};

export type CallResult = {
  readonly status: number;
  /** Parsed JSON body; `undefined` for `204`. */
  readonly data: unknown;
  readonly requestId: string | undefined;
};

export type Send = (
  operationId: OperationId,
  input?: CallInput,
  options?: CallOptions,
) => Promise<CallResult>;

const maxBodyBytes = 10 * 1024 * 1024;
const maxRetriesLimit = 5;
// Longer timers fire at once in Node.
const maxTimeoutMs = 2 ** 31 - 1;
const userAgent = "email-platform-sdk-typescript";
const requestIdPattern = /^[A-Za-z0-9_-]{1,64}$/;
const idempotencyKeyPattern = /^[\x21-\x7e]{1,255}$/;
const systemCodePattern = /^[A-Z][A-Z0-9_]{1,40}$/;
const errorCodePattern = /^[a-z][a-z0-9_]{0,63}$/;
const maxMessageLength = 1024;

/** Validates a timeout in milliseconds; the message names only the option. */
export function checkTimeout(value: unknown, name: string): number {
  if (
    typeof value !== "number" ||
    !Number.isFinite(value) ||
    value <= 0 ||
    value > maxTimeoutMs
  ) {
    throw new RangeError(
      `${name} must be a number of milliseconds between 1 and ${maxTimeoutMs}`,
    );
  }
  return value;
}

/** Validates a retry count and clamps it to the limit. */
export function checkMaxRetries(value: unknown, name: string): number {
  if (typeof value !== "number" || !Number.isInteger(value) || value < 0) {
    throw new RangeError(`${name} must be a non-negative integer`);
  }
  return Math.min(value, maxRetriesLimit);
}

type Attempt =
  | { ok: true; result: CallResult }
  | { ok: false; error: ApiError | TransportError };

type Target = {
  readonly operationId: OperationId;
  readonly method: string;
  readonly path: string;
};

/** Builds the function that runs one logical call per invocation. */
export function createSender(settings: Settings, runtime: Runtime): Send {
  return async (operationId, input = {}, options = {}) => {
    const op = OPERATIONS[operationId];
    const target: Target = { operationId, method: op.method, path: op.path };
    const timeout =
      options.timeout === undefined
        ? settings.timeout
        : checkTimeout(options.timeout, "timeout");
    const maxRetries =
      options.maxRetries === undefined
        ? settings.maxRetries
        : checkMaxRetries(options.maxRetries, "maxRetries");

    const headers: Record<string, string> = {
      Authorization: `Bearer ${settings.apiKey}`,
      Accept: "application/json",
      "User-Agent": userAgent,
    };
    if (options.idempotencyKey !== undefined) {
      if (!("idempotencyKey" in op)) {
        throw new TypeError(
          "idempotencyKey is accepted only by createEmail and createContactEvent",
        );
      }
      if (
        typeof options.idempotencyKey !== "string" ||
        !idempotencyKeyPattern.test(options.idempotencyKey)
      ) {
        throw new TypeError(
          "idempotencyKey must be 1–255 printable ASCII characters without spaces",
        );
      }
      headers["Idempotency-Key"] = options.idempotencyKey;
    } else if ("idempotencyKey" in op) {
      // Protects this call's own retries; the server replays the first
      // answer instead of queueing a second email.
      headers["Idempotency-Key"] = runtime.randomUUID();
    }

    let body: string | undefined;
    if (input.body !== undefined) {
      if (op.method === "GET") {
        throw new TypeError(`${operationId} takes no request body`);
      }
      // Serialised once: every retry sends the same bytes.
      body = JSON.stringify(input.body);
      headers["Content-Type"] = "application/json";
    }
    const url = buildUrl(
      settings.base,
      op.path,
      input.path ?? {},
      input.query ?? {},
    );

    const signal = options.signal;
    const deadline = runtime.now() + timeout;
    for (let retry = 0; ; retry++) {
      if (signal?.aborted === true) {
        throw transportError(target, "aborted", undefined, signal.reason);
      }
      const attempt = await sendOnce(runtime, target, url, {
        method: op.method,
        headers,
        body,
        signal,
        remainingMs: deadline - runtime.now(),
      });
      if (attempt.ok) {
        return attempt.result;
      }
      const { error } = attempt;
      if (retry >= maxRetries || !shouldRetry(operationId, error)) {
        throw error;
      }
      const retryAfter =
        error instanceof ApiError ? error.retryAfter : undefined;
      const wait = waitMs(retry, retryAfter, runtime.random);
      if (runtime.now() + wait >= deadline) {
        throw error;
      }
      try {
        await runtime.sleep(wait, signal);
      } catch {
        throw transportError(target, "aborted", undefined, signal?.reason);
      }
    }
  };
}

/**
 * `429` is refused before anything is stored, so any operation may repeat
 * it. Other failures may have left the request applied; only operations
 * that cannot apply twice are repeated.
 */
function shouldRetry(
  operationId: OperationId,
  error: ApiError | TransportError,
): boolean {
  if (error instanceof ApiError) {
    if (error.status === 429) {
      return true;
    }
    const unknownOutcome =
      error.status === 500 ||
      error.status === 502 ||
      error.status === 504 ||
      (error.status === 503 && error.code === "service_unavailable");
    return unknownOutcome && isRetryable(operationId);
  }
  return error.reason === "network" && isRetryable(operationId);
}

async function sendOnce(
  runtime: Runtime,
  target: Target,
  url: string,
  request: {
    method: string;
    headers: Record<string, string>;
    body: string | undefined;
    signal: AbortSignal | undefined;
    remainingMs: number;
  },
): Promise<Attempt> {
  const deadlineSignal = runtime.deadlineSignal(
    Math.max(1, Math.ceil(request.remainingMs)),
  );
  const signal =
    request.signal === undefined
      ? deadlineSignal
      : AbortSignal.any([request.signal, deadlineSignal]);
  const failed = (error: unknown): Attempt => ({
    ok: false,
    error: classify(target, error, request.signal, deadlineSignal),
  });

  let response: Response;
  try {
    response = await runtime.fetch(url, {
      method: request.method,
      headers: request.headers,
      ...(request.body === undefined ? {} : { body: request.body }),
      redirect: "manual",
      signal,
    });
  } catch (error) {
    return failed(error);
  }

  const requestId = validRequestId(response.headers.get("X-Request-Id"));
  const { status } = response;
  if (status >= 300 && status < 400) {
    await discard(response);
    return {
      ok: false,
      error: new ApiError({
        status,
        code: "redirect",
        message: `HTTP ${status}: redirects are not followed`,
        retryAfter: undefined,
        requestId,
        operationId: target.operationId,
      }),
    };
  }
  if (status === 204) {
    await discard(response);
    return { ok: true, result: { status, data: undefined, requestId } };
  }

  let bytes: Uint8Array | undefined;
  try {
    bytes = await readBody(response);
  } catch (error) {
    return failed(error);
  }

  if (status >= 200 && status < 300) {
    if (bytes === undefined) {
      return {
        ok: false,
        error: transportError(target, "body_too_large"),
      };
    }
    const data = parseJson(bytes);
    if (data === invalid) {
      return { ok: false, error: transportError(target, "invalid_body") };
    }
    return { ok: true, result: { status, data, requestId } };
  }

  const envelope = bytes === undefined ? undefined : errorEnvelope(bytes);
  return {
    ok: false,
    error: new ApiError({
      status,
      code:
        envelope?.code ??
        (status === 503 ? "service_unavailable" : "http_error"),
      message: envelope?.message ?? `HTTP ${status}`,
      retryAfter: retryAfterSeconds(response.headers.get("Retry-After")),
      requestId,
      operationId: target.operationId,
    }),
  };
}

function classify(
  target: Target,
  error: unknown,
  userSignal: AbortSignal | undefined,
  deadlineSignal: AbortSignal,
): TransportError {
  if (userSignal?.aborted === true) {
    return transportError(target, "aborted", undefined, userSignal.reason);
  }
  if (deadlineSignal.aborted) {
    return transportError(target, "timeout");
  }
  return transportError(target, "network", systemCode(error));
}

function transportError(
  target: Target,
  reason: TransportErrorReason,
  code?: string,
  cause?: unknown,
): TransportError {
  return new TransportError(
    { ...target, reason, systemCode: code },
    reason === "aborted" ? { cause } : undefined,
  );
}

/** The first plain system code (`ECONNRESET`) in the error's cause chain. */
function systemCode(error: unknown): string | undefined {
  let current: unknown = error;
  for (
    let depth = 0;
    depth < 5 && typeof current === "object" && current !== null;
    depth++
  ) {
    const code: unknown = (current as { code?: unknown }).code;
    if (typeof code === "string" && systemCodePattern.test(code)) {
      return code;
    }
    current = (current as { cause?: unknown }).cause;
  }
  return undefined;
}

function validRequestId(raw: string | null): string | undefined {
  return raw !== null && requestIdPattern.test(raw) ? raw : undefined;
}

async function discard(response: Response): Promise<void> {
  await response.body?.cancel().catch(() => undefined);
}

/**
 * Reads the body up to the size limit; `undefined` when it is larger (the
 * rest is cancelled). Throws when the stream breaks.
 */
async function readBody(response: Response): Promise<Uint8Array | undefined> {
  const length = Number(response.headers.get("Content-Length") ?? "0");
  if (length > maxBodyBytes) {
    await discard(response);
    return undefined;
  }
  const chunks: Uint8Array[] = [];
  let total = 0;
  if (response.body !== null) {
    const reader = (response.body as ReadableStream<Uint8Array>).getReader();
    for (;;) {
      const { done, value } = await reader.read();
      if (done) {
        break;
      }
      total += value.byteLength;
      if (total > maxBodyBytes) {
        await reader.cancel().catch(() => undefined);
        return undefined;
      }
      chunks.push(value);
    }
  }
  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return bytes;
}

const invalid = Symbol("invalid");

// UTF-8 JSON, or `invalid`. The decoder's and the parser's own errors are
// dropped: their messages may quote the input, which may hold a secret (a
// webhook endpoint's signing secret, for one).
function parseJson(bytes: Uint8Array): unknown {
  if (bytes.byteLength === 0) {
    return invalid;
  }
  try {
    return JSON.parse(
      new TextDecoder("utf-8", { fatal: true }).decode(bytes),
    ) as unknown;
  } catch {
    return invalid;
  }
}

// The API's error body. A code that is not the API's snake_case form means
// the body came from somewhere else; long messages are cut.
function errorEnvelope(
  bytes: Uint8Array,
): { code: string; message: string } | undefined {
  const data = parseJson(bytes);
  if (typeof data !== "object" || data === null) {
    return undefined;
  }
  const error: unknown = (data as { error?: unknown }).error;
  if (typeof error !== "object" || error === null) {
    return undefined;
  }
  const { code, message } = error as { code?: unknown; message?: unknown };
  if (
    typeof code !== "string" ||
    !errorCodePattern.test(code) ||
    typeof message !== "string"
  ) {
    return undefined;
  }
  return { code, message: message.slice(0, maxMessageLength) };
}
