// Server-side client for the Email Platform /v1 API.
export { EmailPlatform, type EmailPlatformOptions } from "./client.ts";
export {
  ApiError,
  TransportError,
  type TransportErrorReason,
} from "./errors.ts";
export { paginate, type Page } from "./pagination.ts";
export {
  verifyWebhook,
  WebhookVerificationError,
  type VerifiedWebhook,
  type VerifyWebhookOptions,
  type WebhookEvent,
  type WebhookHeaders,
  type WebhookVerificationReason,
} from "./webhook.ts";
export type {
  Audiences,
  AutomationRuns,
  Automations,
  Broadcasts,
  ContactEvents,
  Contacts,
  Emails,
  IdempotentRequestOptions,
  RequestOptions,
  Templates,
  Unsubscribes,
  Usage,
  WebhookEndpoints,
} from "./resources.ts";

// Request and response types of the /v1 API, generated from the OpenAPI
// contract. Wire names are kept as they are on the wire.
export type {
  components,
  operations,
  paths,
  webhooks,
} from "./generated/openapi.ts";
