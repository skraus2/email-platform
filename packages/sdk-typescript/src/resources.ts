// One method per /v1 operation, grouped by resource. Request and response
// types come straight from the generated contract types, so field names stay
// as they are on the wire. The client's request function is kept in a
// module-private map, not on the resources: no property leads to the API key.

import type { CallOptions, Send } from "./core.ts";
import type { operations } from "./generated/openapi.ts";
import type { OperationId } from "./operations.ts";
import { paginate, type Page } from "./pagination.ts";

/** Options every method accepts; they apply to this call only. */
export type RequestOptions = {
  /** Aborts the call, including waits between retries. */
  readonly signal?: AbortSignal | undefined;
  /** Milliseconds for the whole call, retries and waits included. */
  readonly timeout?: number | undefined;
  /** Retries after the first attempt; at most 5. */
  readonly maxRetries?: number | undefined;
};

/** Options of the two operations whose retries the server deduplicates. */
export type IdempotentRequestOptions = RequestOptions & {
  /**
   * Sent as `Idempotency-Key` (1–255 printable ASCII characters without
   * spaces). Without one, a random key is used for this call and its
   * retries only; pass a stable key of your own when your app may repeat
   * the call later. Scope it to the purpose and keep personal data out of
   * it: an order or user id, never an email address.
   */
  readonly idempotencyKey?: string | undefined;
};

type Json<T> = T extends { content: { "application/json": infer B } }
  ? B
  : undefined;

/** Request body of an operation. */
export type Body<K extends OperationId> = Json<
  NonNullable<operations[K]["requestBody"]>
>;

/** Query parameters of an operation. */
export type Query<K extends OperationId> = NonNullable<
  operations[K]["parameters"]["query"]
>;

type Responses<K extends OperationId> = operations[K]["responses"];

/** Body of an operation's success response; `undefined` for `204`. */
export type Result<K extends OperationId> = Json<
  Responses<K>[Extract<keyof Responses<K>, 200 | 201 | 202 | 204>]
>;

type ListId =
  | "listEmails"
  | "listEmailEvents"
  | "listContacts"
  | "listAudiences"
  | "listAudienceContacts"
  | "listBroadcasts"
  | "listAutomations"
  | "listAutomationRuns";

/** An item of a list operation's page. */
export type Item<K extends ListId> =
  Result<K> extends Page<infer T> ? T : never;

/** Filters of a list operation; the iterator manages `cursor` itself. */
export type Filters<K extends ListId> = Omit<Query<K>, "cursor">;

// The filters with the page cursor; a cursor in the filters is replaced so
// that every iteration starts at the first page.
function withCursor<F extends object>(
  filters: F | undefined,
  cursor: string | undefined,
): F & { cursor?: string } {
  const query: Record<string, unknown> = { ...filters };
  delete query["cursor"];
  if (cursor !== undefined) {
    query["cursor"] = cursor;
  }
  return query as F & { cursor?: string };
}

// SDK argument names of the path parameters, for error messages.
const argumentNames: Readonly<Record<string, string>> = {
  id: "id",
  contact_id: "contactId",
  version: "version",
};

/**
 * Checks path parameters: ids are non-empty strings, `version` a positive
 * integer. Messages name the argument but never repeat its value.
 */
function checkPath(path: Readonly<Record<string, unknown>>) {
  const checked: Record<string, string | number> = {};
  for (const [name, value] of Object.entries(path)) {
    const argument = Object.hasOwn(argumentNames, name)
      ? (argumentNames[name] ?? name)
      : name;
    if (name === "version") {
      if (
        typeof value !== "number" ||
        !Number.isSafeInteger(value) ||
        value < 1
      ) {
        throw new TypeError(`${argument} must be a positive integer`);
      }
    } else if (typeof value !== "string" || value === "") {
      throw new TypeError(`${argument} must be a non-empty string`);
    } else if (value === "." || value === "..") {
      // The URL parser would resolve these as dot segments and send the
      // request to another operation's path.
      throw new TypeError(`${argument} must not be . or ..`);
    }
    checked[name] = value;
  }
  return checked;
}

// Request functions per resource object. Module-private, so neither the
// function nor a generic "call any operation" helper is reachable from a
// resource at runtime.
const senders = new WeakMap<Resource, Send>();

/** Base of the resource classes; holds nothing but their identity. */
// eslint-disable-next-line @typescript-eslint/no-extraneous-class -- the shared constructor registers each resource's request function out of reach
abstract class Resource {
  constructor(send: Send) {
    senders.set(this, send);
  }
}

/** One call; its JSON result typed by the operation. */
async function call<K extends OperationId>(
  resource: Resource,
  operationId: K,
  input: {
    path?: Readonly<Record<string, unknown>>;
    query?: Query<K> | undefined;
    body?: unknown;
  },
  options: CallOptions | undefined,
): Promise<Result<K>> {
  const send = senders.get(resource);
  if (send === undefined) {
    throw new TypeError("not a resource of an EmailPlatform client");
  }
  const { data } = await send(
    operationId,
    {
      ...(input.path === undefined ? {} : { path: checkPath(input.path) }),
      ...(input.query === undefined ? {} : { query: input.query }),
      ...(input.body === undefined ? {} : { body: input.body }),
    },
    options,
  );
  return data as Result<K>;
}

/** Every item of a list, page by page, with the same filters each time. */
function all<K extends ListId>(
  fetchPage: (cursor: string | undefined) => Promise<Result<K>>,
): AsyncGenerator<Item<K>, void, undefined> {
  return paginate((cursor) => fetchPage(cursor) as Promise<Page<Item<K>>>);
}

/** `/v1/emails`. */
export class Emails extends Resource {
  /** Accepts an email for sending (raw content or a template). */
  send(
    body: Body<"createEmail">,
    options?: IdempotentRequestOptions,
  ): Promise<Result<"createEmail">> {
    return call(this, "createEmail", { body }, options);
  }

  /** One page of emails, newest first. */
  list(
    query?: Query<"listEmails">,
    options?: RequestOptions,
  ): Promise<Result<"listEmails">> {
    return call(this, "listEmails", { query }, options);
  }

  /** Every email matching the filters, across pages. */
  listAll(
    filters?: Filters<"listEmails">,
    options?: RequestOptions,
  ): AsyncGenerator<Item<"listEmails">, void, undefined> {
    return all<"listEmails">((cursor) =>
      this.list(withCursor(filters, cursor), options),
    );
  }

  get(id: string, options?: RequestOptions): Promise<Result<"getEmail">> {
    return call(this, "getEmail", { path: { id } }, options);
  }

  /** One page of an email's events. */
  listEvents(
    id: string,
    query?: Query<"listEmailEvents">,
    options?: RequestOptions,
  ): Promise<Result<"listEmailEvents">> {
    return call(this, "listEmailEvents", { path: { id }, query }, options);
  }

  /** Every event of an email, across pages. */
  listAllEvents(
    id: string,
    filters?: Filters<"listEmailEvents">,
    options?: RequestOptions,
  ): AsyncGenerator<Item<"listEmailEvents">, void, undefined> {
    return all<"listEmailEvents">((cursor) =>
      this.listEvents(id, withCursor(filters, cursor), options),
    );
  }

  /** Cancels a scheduled email. */
  cancel(id: string, options?: RequestOptions): Promise<Result<"cancelEmail">> {
    return call(this, "cancelEmail", { path: { id } }, options);
  }
}

/** `/v1/contacts`. */
export class Contacts extends Resource {
  create(
    body: Body<"createContact">,
    options?: RequestOptions,
  ): Promise<Result<"createContact">> {
    return call(this, "createContact", { body }, options);
  }

  list(
    query?: Query<"listContacts">,
    options?: RequestOptions,
  ): Promise<Result<"listContacts">> {
    return call(this, "listContacts", { query }, options);
  }

  listAll(
    filters?: Filters<"listContacts">,
    options?: RequestOptions,
  ): AsyncGenerator<Item<"listContacts">, void, undefined> {
    return all<"listContacts">((cursor) =>
      this.list(withCursor(filters, cursor), options),
    );
  }

  get(id: string, options?: RequestOptions): Promise<Result<"getContact">> {
    return call(this, "getContact", { path: { id } }, options);
  }

  update(
    id: string,
    body: Body<"updateContact">,
    options?: RequestOptions,
  ): Promise<Result<"updateContact">> {
    return call(this, "updateContact", { path: { id }, body }, options);
  }

  async delete(id: string, options?: RequestOptions): Promise<void> {
    await call(this, "deleteContact", { path: { id } }, options);
  }
}

/** `/v1/audiences`. */
export class Audiences extends Resource {
  create(
    body: Body<"createAudience">,
    options?: RequestOptions,
  ): Promise<Result<"createAudience">> {
    return call(this, "createAudience", { body }, options);
  }

  list(
    query?: Query<"listAudiences">,
    options?: RequestOptions,
  ): Promise<Result<"listAudiences">> {
    return call(this, "listAudiences", { query }, options);
  }

  listAll(
    filters?: Filters<"listAudiences">,
    options?: RequestOptions,
  ): AsyncGenerator<Item<"listAudiences">, void, undefined> {
    return all<"listAudiences">((cursor) =>
      this.list(withCursor(filters, cursor), options),
    );
  }

  get(id: string, options?: RequestOptions): Promise<Result<"getAudience">> {
    return call(this, "getAudience", { path: { id } }, options);
  }

  /** Adds several contacts at once. */
  addContacts(
    id: string,
    body: Body<"addAudienceContacts">,
    options?: RequestOptions,
  ): Promise<Result<"addAudienceContacts">> {
    return call(this, "addAudienceContacts", { path: { id }, body }, options);
  }

  /** One page of an audience's contacts. */
  listContacts(
    id: string,
    query?: Query<"listAudienceContacts">,
    options?: RequestOptions,
  ): Promise<Result<"listAudienceContacts">> {
    return call(this, "listAudienceContacts", { path: { id }, query }, options);
  }

  /** Every contact of an audience, across pages. */
  listAllContacts(
    id: string,
    filters?: Filters<"listAudienceContacts">,
    options?: RequestOptions,
  ): AsyncGenerator<Item<"listAudienceContacts">, void, undefined> {
    return all<"listAudienceContacts">((cursor) =>
      this.listContacts(id, withCursor(filters, cursor), options),
    );
  }

  /** Adds one contact. */
  async addContact(
    id: string,
    contactId: string,
    options?: RequestOptions,
  ): Promise<void> {
    await call(
      this,
      "addAudienceContact",
      {
        path: {
          id,
          contact_id: contactId,
        },
      },
      options,
    );
  }

  async removeContact(
    id: string,
    contactId: string,
    options?: RequestOptions,
  ): Promise<void> {
    await call(
      this,
      "removeAudienceContact",
      {
        path: {
          id,
          contact_id: contactId,
        },
      },
      options,
    );
  }
}

/** `/v1/unsubscribes`. */
export class Unsubscribes extends Resource {
  /** Reports an unsubscribe your app received on its own page. */
  report(
    body: Body<"reportUnsubscribe">,
    options?: RequestOptions,
  ): Promise<Result<"reportUnsubscribe">> {
    return call(this, "reportUnsubscribe", { body }, options);
  }
}

/** `/v1/broadcasts`. */
export class Broadcasts extends Resource {
  /** Creates a draft. */
  create(
    body: Body<"createBroadcast">,
    options?: RequestOptions,
  ): Promise<Result<"createBroadcast">> {
    return call(this, "createBroadcast", { body }, options);
  }

  list(
    query?: Query<"listBroadcasts">,
    options?: RequestOptions,
  ): Promise<Result<"listBroadcasts">> {
    return call(this, "listBroadcasts", { query }, options);
  }

  listAll(
    filters?: Filters<"listBroadcasts">,
    options?: RequestOptions,
  ): AsyncGenerator<Item<"listBroadcasts">, void, undefined> {
    return all<"listBroadcasts">((cursor) =>
      this.list(withCursor(filters, cursor), options),
    );
  }

  get(id: string, options?: RequestOptions): Promise<Result<"getBroadcast">> {
    return call(this, "getBroadcast", { path: { id } }, options);
  }

  /** Schedules a draft; without a body it is sent now. */
  schedule(
    id: string,
    body?: Body<"scheduleBroadcast">,
    options?: RequestOptions,
  ): Promise<Result<"scheduleBroadcast">> {
    return call(this, "scheduleBroadcast", { path: { id }, body }, options);
  }

  cancel(
    id: string,
    options?: RequestOptions,
  ): Promise<Result<"cancelBroadcast">> {
    return call(this, "cancelBroadcast", { path: { id } }, options);
  }
}

/** `/v1/automations`. */
export class Automations extends Resource {
  create(
    body: Body<"createAutomation">,
    options?: RequestOptions,
  ): Promise<Result<"createAutomation">> {
    return call(this, "createAutomation", { body }, options);
  }

  list(
    query?: Query<"listAutomations">,
    options?: RequestOptions,
  ): Promise<Result<"listAutomations">> {
    return call(this, "listAutomations", { query }, options);
  }

  listAll(
    filters?: Filters<"listAutomations">,
    options?: RequestOptions,
  ): AsyncGenerator<Item<"listAutomations">, void, undefined> {
    return all<"listAutomations">((cursor) =>
      this.list(withCursor(filters, cursor), options),
    );
  }

  get(id: string, options?: RequestOptions): Promise<Result<"getAutomation">> {
    return call(this, "getAutomation", { path: { id } }, options);
  }

  update(
    id: string,
    body: Body<"updateAutomation">,
    options?: RequestOptions,
  ): Promise<Result<"updateAutomation">> {
    return call(this, "updateAutomation", { path: { id }, body }, options);
  }

  /** One page of an automation's runs. */
  listRuns(
    id: string,
    query?: Query<"listAutomationRuns">,
    options?: RequestOptions,
  ): Promise<Result<"listAutomationRuns">> {
    return call(this, "listAutomationRuns", { path: { id }, query }, options);
  }

  /** Every run of an automation matching the filters, across pages. */
  listAllRuns(
    id: string,
    filters?: Filters<"listAutomationRuns">,
    options?: RequestOptions,
  ): AsyncGenerator<Item<"listAutomationRuns">, void, undefined> {
    return all<"listAutomationRuns">((cursor) =>
      this.listRuns(id, withCursor(filters, cursor), options),
    );
  }
}

/** `/v1/automation-runs`. */
export class AutomationRuns extends Resource {
  get(
    id: string,
    options?: RequestOptions,
  ): Promise<Result<"getAutomationRun">> {
    return call(this, "getAutomationRun", { path: { id } }, options);
  }

  cancel(
    id: string,
    options?: RequestOptions,
  ): Promise<Result<"cancelAutomationRun">> {
    return call(this, "cancelAutomationRun", { path: { id } }, options);
  }
}

/** `/v1/contact-events`. */
export class ContactEvents extends Resource {
  /** Records an event that may start automations. */
  create(
    body: Body<"createContactEvent">,
    options?: IdempotentRequestOptions,
  ): Promise<Result<"createContactEvent">> {
    return call(this, "createContactEvent", { body }, options);
  }
}

/** `/v1/usage`. */
export class Usage extends Resource {
  /** Daily counts; defaults to the last 30 UTC days. */
  get(
    query?: Query<"getUsage">,
    options?: RequestOptions,
  ): Promise<Result<"getUsage">> {
    return call(this, "getUsage", { query }, options);
  }
}

/** `/v1/templates`. */
export class Templates extends Resource {
  create(
    body: Body<"createTemplate">,
    options?: RequestOptions,
  ): Promise<Result<"createTemplate">> {
    return call(this, "createTemplate", { body }, options);
  }

  get(id: string, options?: RequestOptions): Promise<Result<"getTemplate">> {
    return call(this, "getTemplate", { path: { id } }, options);
  }

  /** Publishes a new immutable version. */
  publishVersion(
    id: string,
    body: Body<"publishTemplateVersion">,
    options?: RequestOptions,
  ): Promise<Result<"publishTemplateVersion">> {
    return call(
      this,
      "publishTemplateVersion",
      { path: { id }, body },
      options,
    );
  }

  getVersion(
    id: string,
    version: number,
    options?: RequestOptions,
  ): Promise<Result<"getTemplateVersion">> {
    return call(
      this,
      "getTemplateVersion",
      {
        path: {
          id,
          version,
        },
      },
      options,
    );
  }
}

/** `/v1/webhook-endpoints`. */
export class WebhookEndpoints extends Resource {
  /**
   * Registers an endpoint. Not retried after an unknown outcome: a second
   * request would register a second endpoint.
   */
  create(
    body: Body<"createWebhookEndpoint">,
    options?: RequestOptions,
  ): Promise<Result<"createWebhookEndpoint">> {
    return call(this, "createWebhookEndpoint", { body }, options);
  }
}
