// The client object apps construct. The API key stays in a private field
// and in the closure of the request function; nothing reachable from the
// object as data holds it, so printing or serialising the client is safe.

import { randomUUID } from "node:crypto";
import { setTimeout as sleep } from "node:timers/promises";

import {
  checkMaxRetries,
  checkTimeout,
  createSender,
  type Runtime,
  type Send,
} from "./core.ts";
import { parseBaseUrl } from "./url.ts";

export type EmailPlatformOptions = {
  /** Project API key (`Authorization: Bearer …`). */
  apiKey: string;
  /** API origin, e.g. `https://api.example.com`; plain http only on loopback. */
  baseUrl: string;
  /** Milliseconds for a whole call, retries and waits included (default 30 000). */
  timeout?: number | undefined;
  /** Retries after the first attempt (default 2, at most 5). */
  maxRetries?: number | undefined;
  /** Replaces the global `fetch`, e.g. in tests. */
  fetch?: typeof globalThis.fetch | undefined;
};

const defaultTimeoutMs = 30_000;
const defaultMaxRetries = 2;
const apiKeyPattern = /^[\x21-\x7e]+$/;
const inspectSymbol = Symbol.for("nodejs.util.inspect.custom");

// Request functions per client, reachable only through `senderOf`.
const senders = new WeakMap<EmailPlatform, Send>();

/** The request function of a client, for the resource methods. */
export function senderOf(client: EmailPlatform): Send {
  const send = senders.get(client);
  if (send === undefined) {
    throw new TypeError("not an EmailPlatform client");
  }
  return send;
}

/** Client for the Email Platform `/v1` API. Server-side only. */
export class EmailPlatform {
  readonly #baseUrl: string;
  readonly #timeout: number;
  readonly #maxRetries: number;

  constructor(options: EmailPlatformOptions) {
    if (typeof options !== "object" || (options as unknown) === null) {
      throw new TypeError("options are required");
    }
    const { apiKey, baseUrl, fetch } = options;
    if (typeof apiKey !== "string" || !apiKeyPattern.test(apiKey)) {
      throw new TypeError(
        "apiKey is required and must be printable ASCII without spaces",
      );
    }
    const base = parseBaseUrl(baseUrl);
    const timeout =
      options.timeout === undefined
        ? defaultTimeoutMs
        : checkTimeout(options.timeout, "timeout");
    const maxRetries =
      options.maxRetries === undefined
        ? defaultMaxRetries
        : checkMaxRetries(options.maxRetries, "maxRetries");
    if (fetch !== undefined && typeof fetch !== "function") {
      throw new TypeError("fetch must be a function");
    }

    this.#baseUrl = `${base.origin}${base.prefix}`;
    this.#timeout = timeout;
    this.#maxRetries = maxRetries;
    senders.set(
      this,
      createSender(
        { apiKey, base, timeout, maxRetries },
        realRuntime(fetch ?? ((input, init) => globalThis.fetch(input, init))),
      ),
    );
  }

  toJSON(): Record<string, unknown> {
    return {
      baseUrl: this.#baseUrl,
      timeout: this.#timeout,
      maxRetries: this.#maxRetries,
      apiKey: "[redacted]",
    };
  }

  toString(): string {
    return `EmailPlatform(${this.#baseUrl})`;
  }

  [inspectSymbol](): string {
    return `EmailPlatform ${JSON.stringify(this.toJSON())}`;
  }
}

function realRuntime(
  fetch: (input: string, init: RequestInit) => Promise<Response>,
): Runtime {
  return {
    fetch,
    now: () => performance.now(),
    sleep: (ms, signal) =>
      sleep(ms, undefined, signal === undefined ? {} : { signal }),
    deadlineSignal: (ms) => AbortSignal.timeout(ms),
    random: Math.random,
    randomUUID,
  };
}
