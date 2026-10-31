// Method and path of every /v1 operation. The type checks below fail the
// build when an operation is missing, misspelled or points at a path and
// method that belong to another operation in the generated contract types.
import type { operations, paths } from "./generated/openapi.ts";

export type OperationId = Exclude<keyof operations, "emailStatusChanged">;

type Method = "GET" | "POST" | "PUT" | "PATCH" | "DELETE";

type Operation = {
  readonly method: Method;
  readonly path: keyof paths;
  /** The server honours an `Idempotency-Key` header on this operation. */
  readonly idempotencyKey?: true;
};

export const OPERATIONS = {
  createEmail: { method: "POST", path: "/v1/emails", idempotencyKey: true },
  listEmails: { method: "GET", path: "/v1/emails" },
  getEmail: { method: "GET", path: "/v1/emails/{id}" },
  listEmailEvents: { method: "GET", path: "/v1/emails/{id}/events" },
  cancelEmail: { method: "POST", path: "/v1/emails/{id}/cancel" },
  createContact: { method: "POST", path: "/v1/contacts" },
  listContacts: { method: "GET", path: "/v1/contacts" },
  getContact: { method: "GET", path: "/v1/contacts/{id}" },
  updateContact: { method: "PATCH", path: "/v1/contacts/{id}" },
  deleteContact: { method: "DELETE", path: "/v1/contacts/{id}" },
  createAudience: { method: "POST", path: "/v1/audiences" },
  listAudiences: { method: "GET", path: "/v1/audiences" },
  getAudience: { method: "GET", path: "/v1/audiences/{id}" },
  addAudienceContacts: { method: "POST", path: "/v1/audiences/{id}/contacts" },
  listAudienceContacts: { method: "GET", path: "/v1/audiences/{id}/contacts" },
  addAudienceContact: {
    method: "PUT",
    path: "/v1/audiences/{id}/contacts/{contact_id}",
  },
  removeAudienceContact: {
    method: "DELETE",
    path: "/v1/audiences/{id}/contacts/{contact_id}",
  },
  reportUnsubscribe: { method: "POST", path: "/v1/unsubscribes" },
  createBroadcast: { method: "POST", path: "/v1/broadcasts" },
  listBroadcasts: { method: "GET", path: "/v1/broadcasts" },
  getBroadcast: { method: "GET", path: "/v1/broadcasts/{id}" },
  scheduleBroadcast: { method: "POST", path: "/v1/broadcasts/{id}/schedule" },
  cancelBroadcast: { method: "POST", path: "/v1/broadcasts/{id}/cancel" },
  createAutomation: { method: "POST", path: "/v1/automations" },
  listAutomations: { method: "GET", path: "/v1/automations" },
  getAutomation: { method: "GET", path: "/v1/automations/{id}" },
  updateAutomation: { method: "PATCH", path: "/v1/automations/{id}" },
  listAutomationRuns: { method: "GET", path: "/v1/automations/{id}/runs" },
  getAutomationRun: { method: "GET", path: "/v1/automation-runs/{id}" },
  cancelAutomationRun: {
    method: "POST",
    path: "/v1/automation-runs/{id}/cancel",
  },
  createContactEvent: {
    method: "POST",
    path: "/v1/contact-events",
    idempotencyKey: true,
  },
  getUsage: { method: "GET", path: "/v1/usage" },
  createTemplate: { method: "POST", path: "/v1/templates" },
  getTemplate: { method: "GET", path: "/v1/templates/{id}" },
  publishTemplateVersion: {
    method: "POST",
    path: "/v1/templates/{id}/versions",
  },
  getTemplateVersion: {
    method: "GET",
    path: "/v1/templates/{id}/versions/{version}",
  },
  createWebhookEndpoint: { method: "POST", path: "/v1/webhook-endpoints" },
} as const satisfies Record<OperationId, Operation>;

// The operation the generated `paths` type has at a path and method; `never`
// when there is none.
type At<P extends keyof paths, M extends Method> =
  Lowercase<M> extends keyof paths[P]
    ? NonNullable<paths[P][Lowercase<M>]>
    : never;

// Exact structural equality; tuples keep `never` from passing.
type Same<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false;

type Mismatches = {
  [K in OperationId]: Same<
    At<(typeof OPERATIONS)[K]["path"], (typeof OPERATIONS)[K]["method"]>,
    operations[K]
  > extends true
    ? never
    : K;
}[OperationId];

type Expect<T extends true> = T;

/**
 * Fails to compile while an entry's method or path names another operation;
 * the error shows the operation's name.
 */
export type TableMatchesContract = Expect<
  [Mismatches] extends [never] ? true : Mismatches
>;

/**
 * Whether a failed attempt whose outcome is unknown may be sent again:
 * reads, and the two writes whose `Idempotency-Key` the server honours.
 */
export function isRetryable(id: OperationId): boolean {
  const op: Operation = OPERATIONS[id];
  return op.method === "GET" || op.idempotencyKey === true;
}
