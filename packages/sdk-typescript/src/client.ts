// The client object apps construct. The API key lives only in the closure
// of the request function, which the resources keep out of reach; nothing
// reachable from the object as data holds it, so printing or serialising
// the client is safe.

import { randomUUID } from "node:crypto";
import { setTimeout as sleep } from "node:timers/promises";

import {
  checkMaxRetries,
  checkTimeout,
  createSender,
  type Runtime,
} from "./core.ts";
import {
  Audiences,
  AutomationRuns,
  Automations,
  Broadcasts,
  ContactEvents,
  Contacts,
  Emails,
  Templates,
  Unsubscribes,
  Usage,
  WebhookEndpoints,
} from "./resources.ts";
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

/** Client for the Email Platform `/v1` API. Server-side only. */
export class EmailPlatform {
  readonly #baseUrl: string;
  readonly #timeout: number;
  readonly #maxRetries: number;

  readonly emails: Emails;
  readonly contacts: Contacts;
  readonly audiences: Audiences;
  readonly unsubscribes: Unsubscribes;
  readonly broadcasts: Broadcasts;
  readonly automations: Automations;
  readonly automationRuns: AutomationRuns;
  readonly contactEvents: ContactEvents;
  readonly usage: Usage;
  readonly templates: Templates;
  readonly webhookEndpoints: WebhookEndpoints;

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
    const send = createSender(
      { apiKey, base, timeout, maxRetries },
      realRuntime(fetch ?? ((input, init) => globalThis.fetch(input, init))),
    );
    this.emails = new Emails(send);
    this.contacts = new Contacts(send);
    this.audiences = new Audiences(send);
    this.unsubscribes = new Unsubscribes(send);
    this.broadcasts = new Broadcasts(send);
    this.automations = new Automations(send);
    this.automationRuns = new AutomationRuns(send);
    this.contactEvents = new ContactEvents(send);
    this.usage = new Usage(send);
    this.templates = new Templates(send);
    this.webhookEndpoints = new WebhookEndpoints(send);
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
