// The client method of every /v1 operation. The type checks below fail the
// build when an operation of the contract has no entry, or an entry names a
// resource or method the client does not have.
import type { EmailPlatform } from "./client.ts";
import type { OperationId } from "./operations.ts";

type Resource = {
  [R in keyof EmailPlatform]: EmailPlatform[R] extends (
    ...args: never[]
  ) => unknown
    ? never
    : R;
}[keyof EmailPlatform];

export const METHODS = {
  createEmail: ["emails", "send"],
  listEmails: ["emails", "list"],
  getEmail: ["emails", "get"],
  listEmailEvents: ["emails", "listEvents"],
  cancelEmail: ["emails", "cancel"],
  createContact: ["contacts", "create"],
  listContacts: ["contacts", "list"],
  getContact: ["contacts", "get"],
  updateContact: ["contacts", "update"],
  deleteContact: ["contacts", "delete"],
  createAudience: ["audiences", "create"],
  listAudiences: ["audiences", "list"],
  getAudience: ["audiences", "get"],
  addAudienceContacts: ["audiences", "addContacts"],
  listAudienceContacts: ["audiences", "listContacts"],
  addAudienceContact: ["audiences", "addContact"],
  removeAudienceContact: ["audiences", "removeContact"],
  reportUnsubscribe: ["unsubscribes", "report"],
  createBroadcast: ["broadcasts", "create"],
  listBroadcasts: ["broadcasts", "list"],
  getBroadcast: ["broadcasts", "get"],
  scheduleBroadcast: ["broadcasts", "schedule"],
  cancelBroadcast: ["broadcasts", "cancel"],
  createAutomation: ["automations", "create"],
  listAutomations: ["automations", "list"],
  getAutomation: ["automations", "get"],
  updateAutomation: ["automations", "update"],
  listAutomationRuns: ["automations", "listRuns"],
  getAutomationRun: ["automationRuns", "get"],
  cancelAutomationRun: ["automationRuns", "cancel"],
  createContactEvent: ["contactEvents", "create"],
  getUsage: ["usage", "get"],
  createTemplate: ["templates", "create"],
  getTemplate: ["templates", "get"],
  publishTemplateVersion: ["templates", "publishVersion"],
  getTemplateVersion: ["templates", "getVersion"],
  createWebhookEndpoint: ["webhookEndpoints", "create"],
} as const satisfies Record<OperationId, readonly [Resource, string]>;

// The operations whose entry is not a method of the client.
type Dangling = {
  [K in OperationId]: (typeof METHODS)[K] extends readonly [
    infer R extends Resource,
    infer M extends string,
  ]
    ? M extends keyof EmailPlatform[R]
      ? EmailPlatform[R][M] extends (...args: never[]) => unknown
        ? never
        : K
      : K
    : K;
}[OperationId];

type Expect<T extends true> = T;

/**
 * Fails to compile while an entry names a method the client does not have;
 * the error shows the operation's name.
 */
export type MethodsExist = Expect<[Dangling] extends [never] ? true : Dangling>;
