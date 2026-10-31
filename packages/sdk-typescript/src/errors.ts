// Errors the client throws. They carry only what an app needs to react and
// to correlate with server logs: never the API key, a request body, the full
// URL or its query string (list filters carry recipient addresses).

import type { OperationId } from "./operations.ts";

/** The API answered with a status outside 2xx. */
export class ApiError extends Error {
  override readonly name = "ApiError";
  /** HTTP status. */
  readonly status: number;
  /**
   * Stable snake_case error code from the API, or one of the client's own:
   * `redirect` (a 3xx, never followed), `http_error` (no error body, e.g.
   * from a proxy). A `503` without an error body is `service_unavailable`.
   */
  readonly code: string;
  /** Seconds from the `Retry-After` header, as sent. */
  readonly retryAfter: number | undefined;
  /** `X-Request-Id` of the response, for the server's logs. */
  readonly requestId: string | undefined;
  readonly operationId: OperationId;

  constructor(init: {
    status: number;
    code: string;
    message: string;
    retryAfter: number | undefined;
    requestId: string | undefined;
    operationId: OperationId;
  }) {
    super(init.message);
    this.status = init.status;
    this.code = init.code;
    this.retryAfter = init.retryAfter;
    this.requestId = init.requestId;
    this.operationId = init.operationId;
  }
}

export type TransportErrorReason =
  /** The connection failed or broke; the request may have reached the API. */
  | "network"
  /** The call's `timeout` ran out; the request may have reached the API. */
  | "timeout"
  /** The caller's `signal` aborted the call. */
  | "aborted"
  /** A 2xx response whose body is not JSON. */
  | "invalid_body"
  /** The response body exceeded the client's size limit. */
  | "body_too_large";

/** No usable response arrived. */
export class TransportError extends Error {
  override readonly name = "TransportError";
  readonly reason: TransportErrorReason;
  readonly operationId: OperationId;
  readonly method: string;
  /** The operation's path template, e.g. `/v1/emails/{id}`. */
  readonly path: string;
  /** System error code of a network failure, e.g. `ECONNREFUSED`. */
  readonly systemCode: string | undefined;

  constructor(
    init: {
      reason: TransportErrorReason;
      operationId: OperationId;
      method: string;
      path: string;
      systemCode?: string | undefined;
    },
    options?: { cause: unknown },
  ) {
    const detail = init.systemCode === undefined ? "" : ` (${init.systemCode})`;
    super(
      `${init.method} ${init.path} (${init.operationId}): ${init.reason}${detail}`,
      options,
    );
    this.reason = init.reason;
    this.operationId = init.operationId;
    this.method = init.method;
    this.path = init.path;
    this.systemCode = init.systemCode;
  }
}
