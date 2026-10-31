// Server-side client for the Email Platform /v1 API.
export { EmailPlatform, type EmailPlatformOptions } from "./client.ts";
export type { CallOptions, CallResult } from "./core.ts";
export {
  ApiError,
  TransportError,
  type TransportErrorReason,
} from "./errors.ts";
export { paginate, type Page } from "./pagination.ts";

// Request and response types of the /v1 API, generated from the OpenAPI
// contract. Wire names are kept as they are on the wire.
export type {
  components,
  operations,
  paths,
  webhooks,
} from "./generated/openapi.ts";
