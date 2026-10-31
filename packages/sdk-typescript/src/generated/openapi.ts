// Code generated from api/openapi.yaml by scripts/generate.mts. DO NOT EDIT.
// Regenerate with `pnpm --filter @email-platform/sdk generate`.

export interface paths {
  "/v1/emails": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * List emails of the authenticated project, newest first
     * @description Requires scope `emails:read`. Filters combine with AND. Unknown or
     *     repeated single-value parameters and malformed values give `400`
     *     (`invalid_filter`, `invalid_limit`, `invalid_cursor`); the message
     *     names the parameter, never its value.
     */
    get: operations["listEmails"];
    put?: never;
    /**
     * Queue one email for one recipient
     * @description Returns `202` only after the email and its dispatch job are committed.
     *
     *     Requires scope `emails:send`.
     *
     *     Send an `Idempotency-Key` whenever a request may be retried. Within 24
     *     hours, a repeated request with the same key and the same payload
     *     returns the original response (same `id`, header
     *     `Idempotent-Replayed: true`) and creates nothing; the same key with a
     *     different payload returns `409 idempotency_key_reused`. Keys are scoped
     *     to the project. Without the header, every request creates a new email.
     *
     *     Checks run in this order: validation, idempotency replay, sender
     *     verification (`422 sender_not_verified`, only where verified senders
     *     are required — always when sending through a real provider),
     *     schedule horizon and cap on planned sends (`422 validation_failed`,
     *     `422 schedule_limit_reached`), suppression (`422 recipient_suppressed`), project rate limit
     *     (`429 rate_limited`).
     *     Rejected requests store nothing; neither they nor idempotent replays
     *     count towards the rate limit.
     *
     *     Two forms: raw (`subject` plus `text` and/or `html`) or template
     *     (`template_id`, `template_version`, `variables`). Mixing them gives
     *     `422 validation_failed`. In the template form the version is resolved
     *     (`"published"` = highest version at this moment) and rendered after
     *     the checks above (validation, idempotency replay, sender,
     *     suppression, rate limit); unknown templates, templates of other projects,
     *     unknown versions, and `"published"` on a template without versions
     *     all give the same `422 template_not_found`;
     *     variables that do not match the version's schema give
     *     `422 variables_invalid`; output that breaks the limits (subject
     *     1–255 characters without line breaks, bodies at most 512 KiB) gives
     *     `422 render_failed`. An idempotent replay is answered before any
     *     rendering, so a retry stays a replay even after a newer version was
     *     published. The rendered subject is kept with the email (bodies are
     *     deleted after sending), so do not put secrets or one-time codes into
     *     subject variables. `503` can also mean the server is busy rendering
     *     other templates; retry with the same `Idempotency-Key`.
     *
     *     A `503` (or a lost connection) means the outcome is unknown: the
     *     email may have been accepted. Retry with the same `Idempotency-Key`
     *     to avoid a duplicate.
     */
    post: operations["createEmail"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/emails/{id}": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * Get the status of an email of the authenticated project
     * @description Requires scope `emails:read`. Unknown ids, malformed ids and ids of
     *     other projects all return the same `404`.
     */
    get: operations["getEmail"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/emails/{id}/events": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * List the events of an email of the authenticated project
     * @description Requires scope `emails:read`. Events are immutable and listed in the
     *     order they were received; provider events may arrive late, twice or
     *     out of order (duplicates are stored once). `occurred_at` is the
     *     event time reported by its origin, `received_at` when the platform
     *     stored it. Unknown ids, malformed ids and ids of other projects all
     *     return the same `404`.
     */
    get: operations["listEmailEvents"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/emails/{id}/cancel": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /**
     * Cancel a queued (typically scheduled) email
     * @description Requires scope `emails:send`. Only an email in status `queued` can be
     *     cancelled; it becomes `canceled` (terminal, content deleted, an
     *     `email.canceled` event is recorded and sent to subscribed webhook
     *     endpoints). Any other status gives `409 email_not_cancelable` — for
     *     example when a worker claimed the email a moment earlier. Unknown,
     *     malformed and foreign ids give the same `404`. No request body.
     */
    post: operations["cancelEmail"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/contacts": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * List contacts of the authenticated project, newest first
     * @description Requires scope `contacts:read`. Unknown or repeated parameters and
     *     malformed values give `400` (`invalid_filter`, `invalid_limit`,
     *     `invalid_cursor`).
     */
    get: operations["listContacts"];
    put?: never;
    /**
     * Create a contact of the authenticated project
     * @description Requires scope `contacts:write`. The address is unique per project,
     *     case-insensitively (`409 contact_exists`). `status` is required:
     *     the app states whether the person consented to broadcasts; a
     *     `subscribed` contact needs a `consent_source` (where the consent
     *     came from, e.g. `signup-form`). A project holds at most 250000
     *     contacts by default (`422 contact_limit_reached`).
     */
    post: operations["createContact"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/contacts/{id}": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * Get a contact
     * @description Requires scope `contacts:read`. Unknown, malformed and foreign ids give the same `404`.
     */
    get: operations["getContact"];
    put?: never;
    post?: never;
    /**
     * Delete a contact (erasure)
     * @description Requires scope `contacts:write`. Removes the contact and its audience
     *     memberships. Emails already created keep their recipient address
     *     (email retention applies); queued broadcast emails to the contact
     *     are not sent (`failed` with `contact_deleted`).
     */
    delete: operations["deleteContact"];
    options?: never;
    head?: never;
    /**
     * Change a contact's consent status, consent source or attributes
     * @description Requires scope `contacts:write`. Fields not sent stay unchanged;
     *     `attributes` replaces all attributes. A request setting `status`
     *     to `subscribed` needs `consent_source` too; `null` values give
     *     `422 validation_failed`. `status_changed_at`
     *     moves only when the status changes.
     */
    patch: operations["updateContact"];
    trace?: never;
  };
  "/v1/audiences": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * List audiences of the authenticated project, newest first
     * @description Requires scope `contacts:read`. Without member counts (see `GET /v1/audiences/{id}`).
     */
    get: operations["listAudiences"];
    put?: never;
    /**
     * Create an audience (a named list of contacts)
     * @description Requires scope `contacts:write`. Names are unique per project,
     *     case-insensitively (`409 audience_exists`). A project holds at most
     *     100 audiences by default (`422 audience_limit_reached`).
     */
    post: operations["createAudience"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/audiences/{id}": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * Get an audience with its member count
     * @description Requires scope `contacts:read`.
     */
    get: operations["getAudience"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/audiences/{id}/contacts": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * List the contacts of an audience
     * @description Requires scope `contacts:read`. Ordered by contact id.
     */
    get: operations["listAudienceContacts"];
    put?: never;
    /**
     * Add up to 1000 contacts to an audience
     * @description Requires scope `contacts:write`. Ids that are not contacts of the
     *     project count as `not_found`. An audience holds at most 100000
     *     contacts by default; an add that would exceed it adds nothing
     *     (`422 audience_full`).
     */
    post: operations["addAudienceContacts"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/audiences/{id}/contacts/{contact_id}": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    /**
     * Add one contact to an audience (idempotent)
     * @description Requires scope `contacts:write`. `422 audience_full` when the audience is at its limit.
     */
    put: operations["addAudienceContact"];
    post?: never;
    /**
     * Remove one contact from an audience (idempotent)
     * @description Requires scope `contacts:write`.
     */
    delete: operations["removeAudienceContact"];
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/unsubscribes": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /**
     * Report an unsubscribe received on the app's unsubscribe page
     * @description Requires scope `contacts:write`. Broadcast emails link to the app's
     *     own unsubscribe page (the broadcast's `unsubscribe_url` with
     *     `{token}` replaced by the contact's token) and carry
     *     `List-Unsubscribe` + `List-Unsubscribe-Post: List-Unsubscribe=One-Click`
     *     (RFC 8058) with the same URL; the page must also accept that
     *     one-click `POST` (body `List-Unsubscribe=One-Click`). The app
     *     forwards the token here. The contact becomes `unsubscribed`: no
     *     broadcast reaches it any more, including emails already queued.
     *     Transactional sends to the address are not affected. Reports are
     *     idempotent and never rate-limited. Unknown, malformed, missing and
     *     foreign tokens give the same `404`.
     */
    post: operations["reportUnsubscribe"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/broadcasts": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * List broadcasts of the authenticated project, newest first
     * @description Requires scope `broadcasts:read`. Without counts (see `GET /v1/broadcasts/{id}`).
     */
    get: operations["listBroadcasts"];
    put?: never;
    /**
     * Create a broadcast draft
     * @description Requires scope `broadcasts:write`. A broadcast sends one template
     *     version to every `subscribed` member of an audience whose address is
     *     not suppressed — one email per recipient. Creating it only stores a
     *     draft (a retried create leaves another draft, nothing is sent);
     *     `POST /v1/broadcasts/{id}/schedule` starts it.
     *
     *     The version is resolved now (`"published"` = highest version). Its
     *     schema must declare `unsubscribe_url` (string), its text part must
     *     print `{{.unsubscribe_url}}` outside any condition, and its html
     *     part, if any, must link it as the whole `href` of an `<a>` (or
     *     `<area>`) outside any condition, e.g.
     *     `<a href="{{.unsubscribe_url}}">Unsubscribe</a>`
     *     (`422 template_missing_unsubscribe`). Hiding the link by styling is
     *     not detected; it stays the app's responsibility. Per recipient,
     *     the variables are the shared `variables`, overlaid by the contact's
     *     attributes whose names the schema declares, plus `unsubscribe_url`.
     *     `unsubscribe_url` is an absolute `https` URL of the app's page with
     *     `{token}` exactly once (at most 900 characters).
     *
     *     A project has at most 20 broadcasts in `draft`, `scheduled` or
     *     `sending` by default (`422 broadcast_limit_reached`) and creates at
     *     most 100 per hour by default (`429 rate_limited`, `Retry-After`).
     */
    post: operations["createBroadcast"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/broadcasts/{id}": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * Get a broadcast with its progress
     * @description Requires scope `broadcasts:read`.
     */
    get: operations["getBroadcast"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/broadcasts/{id}/schedule": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /**
     * Start a draft now or at a planned time
     * @description Requires scope `broadcasts:write`. Moves `draft` → `scheduled`; at
     *     `scheduled_at` (default: now; at most 30 days ahead) the platform
     *     creates the recipient emails in batches (`sending`) and finally
     *     `completed` — every recipient email exists, which says nothing about
     *     delivery. Any other status gives `409 broadcast_not_draft`: after a
     *     lost response, a retry answers `409` and `GET` shows the state. The
     *     sender is checked again (`422 sender_not_verified`).
     */
    post: operations["scheduleBroadcast"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/broadcasts/{id}/cancel": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /**
     * Cancel a broadcast
     * @description Requires scope `broadcasts:write`. Moves `draft`, `scheduled`,
     *     `sending` or `completed` → `canceled`: no further recipient emails
     *     are created, and queued ones become `canceled` instead of being sent
     *     (as their jobs run; an email already handed to the provider is not
     *     recalled). An already cancelled broadcast gives
     *     `409 broadcast_not_cancelable`. No request body.
     */
    post: operations["cancelBroadcast"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/automations": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * List automations of the authenticated project, newest first
     * @description Requires scope `automations:read`. Without run counts (see `GET /v1/automations/{id}`).
     */
    get: operations["listAutomations"];
    put?: never;
    /**
     * Create an automation
     * @description Requires scope `automations:write`. An automation starts a run for a
     *     contact each time the app reports its trigger event for that contact
     *     (`POST /v1/contact-events`) and executes its steps in order:
     *
     *     - `wait`: pause `seconds` (60 to 2592000, i.e. 30 days);
     *     - `condition`: continue only if the contact's attribute matches
     *       (`eq`, `neq`, `exists`, `not_exists`) or an event of that name was
     *       (`occurred`) or was not (`not_occurred`) reported for the contact
     *       after the run started; otherwise the run ends `exited`;
     *     - `send`: one email from a template version to the contact — only to
     *       `subscribed` contacts whose address is not suppressed, with an
     *       unsubscribe link and List-Unsubscribe headers, validated exactly
     *       like a broadcast (`template_missing_unsubscribe`,
     *       `template_not_found`, `sender_not_verified`). Variables: the
     *       step's `variables`, overlaid by the contact's attributes, then by
     *       the trigger event's `data`, plus `unsubscribe_url`.
     *
     *     Steps cannot be changed later (create a new automation and archive
     *     the old one). A contact has at most one active run per automation;
     *     with `allow_reentry: false` (default) at most one run ever. A project
     *     has at most 50 automations that are not archived by default
     *     (`422 automation_limit_reached`) and creates at most 100 per hour,
     *     archived ones included (`429 rate_limited`, `Retry-After`). A retried
     *     create makes a second automation.
     */
    post: operations["createAutomation"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/automations/{id}": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * Get an automation with its run counts
     * @description Requires scope `automations:read`.
     */
    get: operations["getAutomation"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    /**
     * Rename an automation or change its status
     * @description Requires scope `automations:write`. Only `name` and `status` can
     *     change. `active` ↔ `paused`: a paused automation starts no new runs;
     *     runs in flight continue. `archived` is final: every active run is
     *     cancelled (reason `automation_archived`) — in the background, within
     *     seconds; no further step of them runs meanwhile — and queued emails of
     *     those runs become `canceled` instead of being sent. Any change to an
     *     archived automation gives `409 automation_archived`.
     */
    patch: operations["updateAutomation"];
    trace?: never;
  };
  "/v1/automations/{id}/runs": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * List runs of an automation, newest first
     * @description Requires scope `automations:read`. Without emails (see `GET /v1/automation-runs/{id}`).
     */
    get: operations["listAutomationRuns"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/automation-runs/{id}": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * Get a run with the emails it created
     * @description Requires scope `automations:read`. The trigger event's `data` is never returned.
     */
    get: operations["getAutomationRun"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/automation-runs/{id}/cancel": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /**
     * Cancel an active run
     * @description Requires scope `automations:write`. An `active` run becomes
     *     `canceled` (reason `canceled`): no further step runs, and its queued
     *     emails become `canceled` instead of being sent (an email already
     *     handed to the provider is not recalled). Any other status gives
     *     `409 run_not_cancelable`. No request body.
     */
    post: operations["cancelAutomationRun"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/contact-events": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /**
     * Report an event of a contact (starts automation runs)
     * @description Requires scope `events:write`. Reports that something happened for an
     *     existing contact of the project (by `contact_id` or `email`; unknown
     *     contacts give `422 contact_not_found`, contacts are never created).
     *     Every `active` automation whose trigger is `name` starts a run for
     *     the contact (subject to its re-entry rule); the response lists the
     *     started runs. The event is stored (name, contact, time) for
     *     `occurred`/`not_occurred` conditions; `data` is kept only on the
     *     started runs, for rendering their emails, and never returned.
     *
     *     Send an `Idempotency-Key` so a retry after a lost response or a
     *     timeout starts nothing new: the same key with the same payload
     *     returns the original response, with a different payload
     *     `409 idempotency_key_reused`. Keys are remembered as long as the
     *     event is retained (90 days by default). At most 1200 events per
     *     project and minute by default (`429 rate_limited`, `Retry-After`);
     *     reports refused as `contact_not_found` count too. Request bodies are
     *     limited to 64 KiB.
     */
    post: operations["createContactEvent"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/usage": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * Daily usage and limits of the authenticated project
     * @description Requires scope `emails:read`. One entry per UTC day from `from` to
     *     `to` (both inclusive, ascending), counting what happened on that
     *     day: `accepted` emails (one per recipient email, including
     *     broadcast and automation emails — not API requests), emails handed
     *     to the provider (`sent`: `captured` or `submitted`; an email SES
     *     later rejects stays counted here), `failed`, `canceled`, and with a
     *     real provider `delivered`, `bounced` (permanent bounces only) and
     *     `complained`. Each email counts once per kind and day; `totals`
     *     sums the days, so an email that bounced on two days counts twice.
     *
     *     Figures are refreshed every 5 minutes and may lag by that much.
     *     Days without activity are zeros — so are days before usage was
     *     first computed, days missed by a worker outage of more than 14
     *     days, and future days.
     *
     *     `limits` are the limits that apply to this project's requests:
     *     accepted sends and reported contact events per minute.
     *
     *     Unknown or repeated parameters, malformed dates, `from` after `to`
     *     and ranges over 93 days give `400 invalid_filter`; the message
     *     names the parameter, never its value.
     */
    get: operations["getUsage"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/templates": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /**
     * Create a template of the authenticated project
     * @description Requires scope `templates:write`. Template creates and publishes of
     *     a project are limited to 60 per minute by default, counting every
     *     attempt (`429 rate_limited` with `Retry-After`); when the server is
     *     busy with other template writes it answers `503`. The slug is unique per project
     *     (`409 slug_taken`). A project holds at most 200 templates by default
     *     (`422 template_limit_reached`). A new template has no version yet.
     */
    post: operations["createTemplate"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/templates/{id}": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * Get a template of the authenticated project
     * @description Requires scope `templates:read`. Unknown ids, malformed ids and ids
     *     of other projects all return the same `404`.
     */
    get: operations["getTemplate"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/templates/{id}/versions": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /**
     * Publish a new immutable version of a template
     * @description Requires scope `templates:write`. The version gets the next number
     *     (1, 2, …) and can never be changed or deleted. The sources are
     *     checked before storing (`422 template_invalid`, the message names
     *     the part and line): only `{{.name}}` for declared variables and
     *     `{{if}}`/`{{else if}}`/`{{else}}`/`{{end}}` on a declared string or
     *     boolean variable, or `eq`/`ne` of such a variable with a constant
     *     of the same type, are allowed; comments are allowed; anything else
     *     (loops, `with`, nested templates, functions, `$` variables) is
     *     refused. `html` is rendered with contextual escaping (variables
     *     cannot inject markup or `javascript:` URLs); `subject` and `text`
     *     are plain text. `text` is required so every HTML mail has a text
     *     alternative. A template holds at most 500 versions by default
     *     (`422 version_limit_reached`), and a project stores at most 50 MiB of
     *     template source over all versions by default
     *     (`422 template_storage_limit_reached`). Actions right after `<` (dynamic
     *     tag names) and inside `srcdoc`, `content` or `http-equiv` attributes
     *     are refused as well. Unknown templates and templates of
     *     other projects return `404`.
     */
    post: operations["publishTemplateVersion"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/templates/{id}/versions/{version}": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    /**
     * Get a published template version exactly as stored
     * @description Requires scope `templates:read`. Returns the sources exactly as
     *     published, also when newer versions exist. Unknown templates,
     *     templates of other projects and unknown versions return `404`.
     */
    get: operations["getTemplateVersion"];
    put?: never;
    post?: never;
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
  "/v1/webhook-endpoints": {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /**
     * Register a webhook endpoint of the authenticated project
     * @description Requires scope `webhooks:write`. The response is the only time the
     *     signing `secret` is shown; store it. Without `event_types` the
     *     endpoint receives all types. The URL must be `https` (or `http`
     *     where the operator allows it) and must not point to a forbidden
     *     address (`422 invalid_url`; the message never contains resolved
     *     addresses). A project holds at most 10 endpoints by default
     *     (`422 endpoint_limit_reached`). A retried request creates another
     *     endpoint; ask an operator to disable one you lost the secret of.
     *
     *     Registration through the API is optional per deployment: where it
     *     is not enabled the route answers
     *     `503 webhook_registration_disabled`, and endpoints are registered by
     *     an administrator instead.
     */
    post: operations["createWebhookEndpoint"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
}
export interface webhooks {
  emailStatusChanged: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    get?: never;
    put?: never;
    /**
     * An email changed its status
     * @description Sent to every active webhook endpoint of the project subscribed to
     *     the event type, once per status change (duplicate or late provider
     *     events that do not change the status are not sent). Endpoints are
     *     registered by an administrator or through
     *     `POST /v1/webhook-endpoints`; the endpoint secret is shown once.
     *
     *     Verify every request: compute HMAC-SHA256 keyed with the endpoint
     *     secret exactly as shown (the full `whsec_…` string as UTF-8 bytes)
     *     over `<Webhook-Timestamp>.<raw request body>` and compare it in
     *     constant time with each hex value after `v1=` in
     *     `Webhook-Signature`. Accept the request if any `v1=` value matches,
     *     ignore values of other versions, reject it if none matches. The
     *     header currently always carries one value (rotating a secret
     *     replaces it immediately); accept several for forward compatibility.
     *     Reject timestamps more than 5 minutes away from your clock, and use
     *     `Webhook-Id` (equal to the body `id`) to drop replays and
     *     duplicates: keep seen ids at least for the timestamp tolerance,
     *     better for the maximum retry duration (about 5 hours). A retry repeats the identical body with a new timestamp
     *     and signature. Events carry no ordering guarantee; fetch
     *     `GET /v1/emails/{id}` for the current status.
     *
     *     Respond with any `2xx` to acknowledge receipt (not processing).
     *     Timeouts (10 s by default), connection errors, `408`, `429` and
     *     `5xx` are retried with exponential backoff and jitter (12 attempts
     *     within about 5 hours by default); any other status, including redirects,
     *     ends the delivery as failed.
     */
    post: operations["emailStatusChanged"];
    delete?: never;
    options?: never;
    head?: never;
    patch?: never;
    trace?: never;
  };
}
export interface components {
  schemas: {
    /**
     * @description Raw form: `subject` plus `text` and/or `html`. Template form:
     *     `template_id` and `template_version`, plus `variables` (may be
     *     omitted when the version has no required variables). Mixing the
     *     forms gives `422 validation_failed`.
     */
    CreateEmailRequest: {
      /**
       * Format: email
       * @description Bare address, no display name, ASCII only (international domains
       *     in punycode, e.g. `xn--bcher-kva.test`).
       */
      from: string;
      /**
       * Format: email
       * @description Exactly one bare recipient address, ASCII only (international
       *     domains in punycode). Other addresses get `422 validation_failed`.
       */
      to: string;
      /** @description Raw form only. Must not contain CR, LF or NUL. */
      subject?: string;
      /** @description Plain-text body, at most 512 KiB. At least one of text/html is required. */
      text?: string;
      /** @description HTML body, at most 512 KiB. At least one of text/html is required. */
      html?: string;
      /**
       * Format: uuid
       * @description Template form. A template of the authenticated project.
       */
      template_id?: string;
      /** @description Template form. A version number, or `"published"` for the highest version at this moment. */
      template_version?: number | "published";
      /**
       * @description Template form. Values for the version's declared variables:
       *     strings, numbers or booleans as declared (`null` is not
       *     accepted). Unknown names, missing required variables, wrong
       *     types and strings over their `max_length` give
       *     `422 variables_invalid`. Variables are not stored.
       */
      variables?: {
        [key: string]: string | number | boolean;
      };
      /**
       * Format: date-time
       * @description Send not before this time (RFC 3339), at most 30 days ahead by
       *     default (`422 validation_failed` otherwise). A time at or before
       *     now sends immediately (stored as `null`). A project may have at
       *     most 10000 planned sends waiting by default
       *     (`422 schedule_limit_reached`). Part of the idempotency
       *     fingerprint when present.
       */
      scheduled_at?: string;
    };
    CreateEmailResponse: {
      /** Format: uuid */
      id: string;
      /** @constant */
      status: "queued";
    };
    /**
     * @description `queued` → `sending` → `captured` (local test sink), `submitted`
     *     (real provider) or `failed`; a `queued` email can be cancelled
     *     (`canceled`, terminal, never handed to a provider). A scheduled
     *     email stays `queued` until it is due. After `submitted`, provider events move
     *     the status to `delivered`, `bounced` or `complained`; a later event
     *     never moves it back (`complained` > `bounced` > `delivered` >
     *     `submitted`).
     * @enum {string}
     */
    EmailStatus:
      | "queued"
      | "sending"
      | "captured"
      | "submitted"
      | "delivered"
      | "bounced"
      | "complained"
      | "failed"
      | "canceled";
    /**
     * @description Why the last dispatch attempt failed. Retryable codes (the email is
     *     retried until its attempts are used up): `provider_unreachable`,
     *     `provider_timeout`, `provider_protocol_error`, `provider_deferred`,
     *     `provider_throttled`, `internal_error`. Permanent codes (no further
     *     attempts): `provider_rejected`, `provider_auth_failed`,
     *     `sender_not_verified`, `message_invalid`, `unsafe_target`,
     *     `suppressed`, `project_disabled`, `content_missing`, `expired`,
     *     and for broadcast emails `unsubscribed` (the contact unsubscribed
     *     before the send), `contact_deleted`, `broadcast_canceled` (the
     *     broadcast was cancelled after an attempt with unknown outcome),
     *     `render_failed` (the contact's attributes no longer render); for
     *     automation emails `unsubscribed`, `contact_deleted` and
     *     `automation_canceled` (the run was cancelled after an attempt with
     *     unknown outcome).
     *
     *     `provider_timeout` and `provider_protocol_error` mean the outcome of
     *     that attempt is unknown: a `failed` email with one of these codes may
     *     still have been delivered. With a real provider, the same holds for
     *     `provider_unreachable` when the provider answered with a server error
     *     (5xx) rather than refusing the connection. The same holds for `suppressed` and
     *     `project_disabled` on an email with `attempts` > 0 whose earlier
     *     attempt ended with an unknown outcome: no further attempt is made.
     * @enum {string}
     */
    ErrorCode:
      | "provider_unreachable"
      | "provider_timeout"
      | "provider_protocol_error"
      | "provider_deferred"
      | "provider_throttled"
      | "provider_rejected"
      | "provider_auth_failed"
      | "sender_not_verified"
      | "message_invalid"
      | "unsafe_target"
      | "suppressed"
      | "project_disabled"
      | "content_missing"
      | "expired"
      | "unsubscribed"
      | "contact_deleted"
      | "broadcast_canceled"
      | "render_failed"
      | "automation_canceled"
      | "internal_error";
    Email: {
      /** Format: uuid */
      id: string;
      status: components["schemas"]["EmailStatus"];
      from: string;
      to: string;
      subject: string;
      /** @description Dispatch attempts started so far. */
      attempts: number;
      /**
       * @description `null` when no attempt has failed or the email was dispatched.
       *     While the status is `queued` or `sending`, the reason of the last
       *     failed attempt (a retry may be pending; otherwise the email is
       *     closed by expiry); with status `failed`, the
       *     final reason.
       */
      error_code: components["schemas"]["ErrorCode"] | null;
      /** Format: date-time */
      created_at: string;
      /** Format: date-time */
      updated_at: string;
      /** Format: date-time */
      finished_at: string | null;
      /**
       * Format: uuid
       * @description Template of a template send; `null` for raw sends.
       */
      template_id: string | null;
      /** @description The resolved version number (also when `"published"` was requested). */
      template_version: number | null;
      /**
       * Format: date-time
       * @description Planned send time; `null` for immediate sends.
       */
      scheduled_at: string | null;
      /**
       * Format: uuid
       * @description The broadcast this email belongs to; `null` otherwise.
       */
      broadcast_id: string | null;
      /**
       * Format: uuid
       * @description The automation whose run sent this email; `null` otherwise.
       */
      automation_id: string | null;
      /**
       * Format: uuid
       * @description The run that sent this email; `null` otherwise (also once the run was deleted with its contact or by retention).
       */
      automation_run_id: string | null;
    };
    EmailList: {
      data: components["schemas"]["Email"][];
      /** @description Pass as `cursor` for the next page; `null` on the last page. */
      next_cursor: string | null;
    };
    VariableSpec: {
      /** @enum {string} */
      type: "string" | "number" | "boolean";
      /** @default false */
      required: boolean;
      /** @description Strings only; default 1000 characters. */
      max_length?: number;
    };
    /** @description Declared variables by name (`^[a-z][a-z0-9_]{0,39}$`). */
    VariableSchema: {
      [key: string]: components["schemas"]["VariableSpec"];
    };
    CreateTemplateRequest: {
      slug: string;
      name?: string;
    };
    Template: {
      /** Format: uuid */
      id: string;
      slug: string;
      name: string;
      /** @description Highest published version; `null` while none exists. */
      latest_version: number | null;
      /** Format: date-time */
      created_at: string;
    };
    PublishTemplateVersionRequest: {
      /** @description Plain-text template, at most 1000 bytes. */
      subject: string;
      /** @description Plain-text template, at most 512 KiB. */
      text: string;
      /** @description HTML template, at most 512 KiB, rendered with contextual escaping. */
      html?: string;
      variables?: components["schemas"]["VariableSchema"];
    };
    TemplateVersionCreated: {
      /** Format: uuid */
      template_id: string;
      version: number;
      /** Format: date-time */
      created_at: string;
    };
    TemplateVersion: {
      /** Format: uuid */
      template_id: string;
      version: number;
      subject: string;
      text: string;
      html: string | null;
      variables: components["schemas"]["VariableSchema"];
      /** Format: date-time */
      created_at: string;
    };
    /** @enum {string} */
    WebhookEventType:
      | "email.captured"
      | "email.submitted"
      | "email.failed"
      | "email.delivered"
      | "email.bounced"
      | "email.complained"
      | "email.canceled";
    CreateWebhookEndpointRequest: {
      /** Format: uri */
      url: string;
      event_types?: components["schemas"]["WebhookEventType"][];
    };
    WebhookEndpointCreated: {
      /** Format: uuid */
      id: string;
      url: string;
      event_types: components["schemas"]["WebhookEventType"][];
      /** @enum {string} */
      status: "active";
      /** Format: date-time */
      created_at: string;
      /** @description Signing secret (`whsec_…`). Shown only in this response. */
      secret: string;
    };
    EmailEvent: {
      /** Format: uuid */
      id: string;
      /**
       * @description Platform events: `captured`, `submitted`, `failed`, `canceled`. Provider
       *     events: `send`, `delivered`, `bounced`, `complained`, `rejected`,
       *     `delivery_delayed`. Engagement (open/click) is not tracked.
       * @enum {string}
       */
      type:
        | "captured"
        | "submitted"
        | "failed"
        | "canceled"
        | "send"
        | "delivered"
        | "bounced"
        | "complained"
        | "rejected"
        | "delivery_delayed";
      /** @enum {string} */
      origin: "platform" | "ses";
      /** @description Provider involved (`mailpit`, `ses`); `null` for `failed` events. */
      provider: string | null;
      /** Format: date-time */
      occurred_at: string;
      /** Format: date-time */
      received_at: string;
      /**
       * @description Non-personal details only: `error_code` (failed),
       *     `bounce_type`/`bounce_subtype` (bounced),
       *     `complaint_feedback_type` (complained), `delay_type`
       *     (delivery_delayed). Never addresses or diagnostic text.
       */
      details: {
        [key: string]: string;
      };
    };
    EmailEventList: {
      data: components["schemas"]["EmailEvent"][];
      /** @description Pass as `cursor` for the next page; `null` on the last page. */
      next_cursor: string | null;
    };
    WebhookEvent: {
      /**
       * Format: uuid
       * @description Event id, stable across retries; equals `Webhook-Id`.
       */
      id: string;
      /** @enum {string} */
      type:
        | "email.captured"
        | "email.submitted"
        | "email.failed"
        | "email.delivered"
        | "email.bounced"
        | "email.complained"
        | "email.canceled";
      /**
       * Format: date-time
       * @description When the platform received and stored the event.
       */
      created_at: string;
      data: {
        /** Format: uuid */
        email_id: string;
        /** Format: uuid */
        project_id: string;
      };
    };
    /**
     * @description `subscribed`: the person consented to broadcasts. `unsubscribed`: no broadcast is sent to the contact.
     * @enum {string}
     */
    ContactStatus: "subscribed" | "unsubscribed";
    /**
     * @description Values for personalising broadcasts: at most 20 entries, names
     *     `^[a-z][a-z0-9_]{0,39}$` (not `unsubscribe_url`), values strings (at
     *     most 1000 characters), finite numbers or booleans; at most 8 KiB as
     *     compact JSON. Attributes are matched by name to a template's
     *     declared variables; undeclared ones are ignored. Never logged.
     */
    ContactAttributes: {
      [key: string]: string | number | boolean;
    };
    CreateContactRequest: {
      /**
       * Format: email
       * @description Bare address, ASCII only (as for `to`).
       */
      email: string;
      status: components["schemas"]["ContactStatus"];
      /** @description Where the consent came from (printable characters). Required when `status` is `subscribed`. */
      consent_source?: string;
      attributes?: components["schemas"]["ContactAttributes"];
    };
    UpdateContactRequest: {
      status?: components["schemas"]["ContactStatus"];
      /** @description Required whenever `status` is `subscribed` in the same request. */
      consent_source?: string;
      attributes?: components["schemas"]["ContactAttributes"];
    };
    Contact: {
      /** Format: uuid */
      id: string;
      email: string;
      status: components["schemas"]["ContactStatus"];
      consent_source: string | null;
      /** Format: date-time */
      status_changed_at: string;
      attributes: components["schemas"]["ContactAttributes"];
      /** Format: date-time */
      created_at: string;
      /** Format: date-time */
      updated_at: string;
    };
    ContactList: {
      data: components["schemas"]["Contact"][];
      /** @description Pass as `cursor` for the next page; `null` on the last page. */
      next_cursor: string | null;
    };
    CreateAudienceRequest: {
      name: string;
    };
    Audience: {
      /** Format: uuid */
      id: string;
      name: string;
      /** Format: date-time */
      created_at: string;
    };
    AudienceWithCount: components["schemas"]["Audience"] & {
      contact_count: number;
    };
    AudienceList: {
      data: components["schemas"]["Audience"][];
      next_cursor: string | null;
    };
    AddAudienceContactsRequest: {
      contact_ids: string[];
    };
    AddAudienceContactsResult: {
      added: number;
      already_members: number;
      not_found: number;
    };
    UnsubscribeRequest: {
      /** @description The token from the unsubscribe URL (43 characters, base64url). */
      token: string;
    };
    UnsubscribeResult: {
      /** Format: uuid */
      contact_id: string;
      /** @constant */
      status: "unsubscribed";
    };
    /**
     * @description `draft` → `scheduled` → `sending` (recipient emails are being
     *     created) → `completed` (all recipient emails exist; delivery is
     *     tracked per email). `canceled` from any other status.
     * @enum {string}
     */
    BroadcastStatus:
      "draft" | "scheduled" | "sending" | "completed" | "canceled";
    CreateBroadcastRequest: {
      name?: string;
      /** Format: uuid */
      audience_id: string;
      /** Format: email */
      from: string;
      /** Format: uuid */
      template_id: string;
      template_version: number | "published";
      /** @description Shared values for declared variables (at most 16 KiB); contact attributes override them. Not echoed. */
      variables?: {
        [key: string]: string | number | boolean;
      };
      /** @description Absolute `https` URL of the app's unsubscribe page containing `{token}` exactly once, e.g. `https://app.example.test/unsubscribe?t={token}`. */
      unsubscribe_url: string;
    };
    ScheduleBroadcastRequest: {
      /**
       * Format: date-time
       * @description Start time (RFC 3339), at most 30 days ahead by default; omitted or past = now.
       */
      scheduled_at?: string;
    };
    Broadcast: {
      /** Format: uuid */
      id: string;
      name: string;
      status: components["schemas"]["BroadcastStatus"];
      /** Format: uuid */
      audience_id: string;
      from: string;
      /** Format: uuid */
      template_id: string;
      template_version: number;
      unsubscribe_url: string;
      /** Format: date-time */
      scheduled_at: string | null;
      /** Format: date-time */
      started_at: string | null;
      /**
       * Format: date-time
       * @description When the last recipient email was created, or when the broadcast was cancelled.
       */
      finished_at: string | null;
      /** Format: date-time */
      created_at: string;
    };
    BroadcastWithCounts: components["schemas"]["Broadcast"] & {
      counts: {
        /** @description Recipient emails created. */
        recipients: number;
        skipped_unsubscribed: number;
        skipped_suppressed: number;
        /** @description Members whose attributes did not render with the version. */
        skipped_invalid: number;
        /** @description Current status of the recipient emails (emails deleted by retention are no longer counted). */
        by_status: {
          queued: number;
          sending: number;
          captured: number;
          submitted: number;
          delivered: number;
          bounced: number;
          complained: number;
          failed: number;
          canceled: number;
        };
      };
    };
    BroadcastList: {
      data: components["schemas"]["Broadcast"][];
      next_cursor: string | null;
    };
    /**
     * @description `active`: starts runs. `paused`: starts no new runs; runs in flight continue. `archived`: final; all runs cancelled.
     * @enum {string}
     */
    AutomationStatus: "active" | "paused" | "archived";
    /** @description Event name, e.g. `user.signed_up`. */
    EventName: string;
    /** @description One step; `type` selects the shape. */
    AutomationStep:
      | components["schemas"]["WaitStep"]
      | components["schemas"]["AttributeConditionStep"]
      | components["schemas"]["EventConditionStep"]
      | components["schemas"]["SendStep"];
    WaitStep: {
      /** @constant */
      type: "wait";
      seconds: number;
    };
    /**
     * @description Compares the contact's current attribute. `eq`/`neq` need `value` of
     *     the same JSON type (`"1"` is not `1`; numbers compare numerically); a
     *     missing attribute fails `eq` and passes `neq`. `exists`/`not_exists`
     *     take no `value`.
     */
    AttributeConditionStep: {
      /** @constant */
      type: "condition";
      attribute: string;
      /** @enum {string} */
      op: "eq" | "neq" | "exists" | "not_exists";
      value?: string | number | boolean;
    };
    /** @description Checks whether an event of that name was reported for the contact after the run started (the trigger event itself does not count). */
    EventConditionStep: {
      /** @constant */
      type: "condition";
      event: components["schemas"]["EventName"];
      /** @enum {string} */
      op: "occurred" | "not_occurred";
    };
    SendStep: {
      /** @constant */
      type: "send";
      /** Format: email */
      from: string;
      /** Format: uuid */
      template_id: string;
      /** @description In requests a positive integer or `"published"` (resolved at create); in responses always the number. */
      template_version: number | "published";
      /** @description Shared values for declared variables (at most 16 KiB); contact attributes and event data override them. */
      variables?: {
        [key: string]: string | number | boolean;
      };
      /** @description Absolute `https` URL of the app's unsubscribe page containing `{token}` exactly once. */
      unsubscribe_url: string;
    };
    CreateAutomationRequest: {
      name: string;
      trigger: components["schemas"]["AutomationTrigger"];
      /** @default false */
      allow_reentry: boolean;
      /**
       * @default active
       * @enum {string}
       */
      status: "active" | "paused";
      /** @description At least one `send` step; at most 64 KiB as stored. */
      steps: components["schemas"]["AutomationStep"][];
    };
    AutomationTrigger: {
      event: components["schemas"]["EventName"];
    };
    UpdateAutomationRequest: {
      name?: string;
      status?: components["schemas"]["AutomationStatus"];
    };
    Automation: {
      /** Format: uuid */
      id: string;
      name: string;
      status: components["schemas"]["AutomationStatus"];
      trigger: components["schemas"]["AutomationTrigger"];
      allow_reentry: boolean;
      steps: components["schemas"]["AutomationStep"][];
      /** Format: date-time */
      created_at: string;
      /** Format: date-time */
      updated_at: string;
    };
    AutomationWithCounts: components["schemas"]["Automation"] & {
      run_counts: {
        active: number;
        completed: number;
        exited: number;
        canceled: number;
        failed: number;
      };
    };
    AutomationList: {
      data: components["schemas"]["Automation"][];
      next_cursor: string | null;
    };
    /**
     * @description `active` (waiting for its next step) → `completed` (all steps done),
     *     `exited` (a condition did not hold, the contact unsubscribed, the
     *     address is suppressed or the project was disabled), `canceled` (by
     *     `POST /v1/automation-runs/{id}/cancel` or by archiving the
     *     automation) or `failed` (the email could not be rendered, or an
     *     internal error persisted). All but `active` are final.
     * @enum {string}
     */
    AutomationRunStatus:
      "active" | "completed" | "exited" | "canceled" | "failed";
    /**
     * @description Why a run ended; `null` for `active` and `completed`.
     * @enum {string|null}
     */
    AutomationRunReason:
      | "condition_not_met"
      | "unsubscribed"
      | "suppressed"
      | "project_disabled"
      | "canceled"
      | "automation_archived"
      | "render_failed"
      | "internal_error"
      | null;
    AutomationRun: {
      /** Format: uuid */
      id: string;
      /** Format: uuid */
      automation_id: string;
      /** Format: uuid */
      contact_id: string;
      status: components["schemas"]["AutomationRunStatus"];
      reason: components["schemas"]["AutomationRunReason"];
      /** @description Index of the next step to run (equals the number of steps once completed). */
      step: number;
      /**
       * Format: date-time
       * @description When the next step is due; `null` once the run ended.
       */
      next_step_at: string | null;
      /** Format: date-time */
      created_at: string;
      /** Format: date-time */
      updated_at: string;
      /** Format: date-time */
      finished_at: string | null;
    };
    AutomationRunWithEmails: components["schemas"]["AutomationRun"] & {
      emails: {
        /** Format: uuid */
        id: string;
        step: number;
        status: components["schemas"]["EmailStatus"];
      }[];
    };
    AutomationRunList: {
      data: components["schemas"]["AutomationRun"][];
      next_cursor: string | null;
    };
    /** @description Exactly one of `contact_id` and `email`. */
    CreateContactEventRequest: {
      name: components["schemas"]["EventName"];
      /** Format: uuid */
      contact_id?: string;
      /** Format: email */
      email?: string;
      data?: components["schemas"]["ContactAttributes"];
    };
    ContactEvent: {
      /** Format: uuid */
      id: string;
      name: components["schemas"]["EventName"];
      /** Format: uuid */
      contact_id: string;
      /** Format: date-time */
      created_at: string;
      /** @description Runs this event started (empty when no active automation has this trigger or re-entry rules prevented it). */
      runs: {
        /** Format: uuid */
        id: string;
        /** Format: uuid */
        automation_id: string;
      }[];
    };
    UsageCounts: {
      /** Format: int64 */
      accepted: number;
      /** Format: int64 */
      sent: number;
      /** Format: int64 */
      failed: number;
      /** Format: int64 */
      canceled: number;
      /** Format: int64 */
      delivered: number;
      /** Format: int64 */
      bounced: number;
      /** Format: int64 */
      complained: number;
    };
    UsageDay: {
      /** Format: date */
      date: string;
    } & components["schemas"]["UsageCounts"];
    Usage: {
      /** Format: date */
      from: string;
      /** Format: date */
      to: string;
      data: components["schemas"]["UsageDay"][];
      totals: components["schemas"]["UsageCounts"];
      limits: {
        sends_per_minute: number;
        contact_events_per_minute: number;
      };
    };
    Error: {
      error: {
        /** @description Stable snake_case error code. */
        code: string;
        /** @description Human-readable explanation; never contains internals. */
        message: string;
      };
    };
  };
  responses: {
    /** @description Malformed request: `invalid_json` (also for unknown, case-variant or duplicate field names — names are exact and case-sensitive), `invalid_idempotency_key`, `invalid_cursor`, `invalid_limit` or `invalid_filter`. */
    BadRequest: {
      headers: {
        "X-Request-Id": components["headers"]["RequestId"];
        [name: string]: unknown;
      };
      content: {
        "application/json": components["schemas"]["Error"];
      };
    };
    /** @description `unauthorized`: missing, malformed, unknown or revoked key, or disabled project. */
    Unauthorized: {
      headers: {
        "X-Request-Id": components["headers"]["RequestId"];
        [name: string]: unknown;
      };
      content: {
        "application/json": components["schemas"]["Error"];
      };
    };
    /** @description `forbidden`: the key lacks the required scope. */
    Forbidden: {
      headers: {
        "X-Request-Id": components["headers"]["RequestId"];
        [name: string]: unknown;
      };
      content: {
        "application/json": components["schemas"]["Error"];
      };
    };
    /** @description `idempotency_key_reused` (the key was already used with a different payload), `slug_taken` (the project already has a template with this slug), `contact_exists`, `audience_exists`, `email_not_cancelable`, `broadcast_not_draft`, `broadcast_not_cancelable`, `automation_archived` or `run_not_cancelable`. */
    Conflict: {
      headers: {
        "X-Request-Id": components["headers"]["RequestId"];
        [name: string]: unknown;
      };
      content: {
        "application/json": components["schemas"]["Error"];
      };
    };
    /** @description `rate_limited`: the project's send rate limit (or, for template writes, its template write limit; for broadcast creates, its hourly broadcast limit) is exhausted; retry after `Retry-After` seconds. */
    TooManyRequests: {
      headers: {
        "X-Request-Id": components["headers"]["RequestId"];
        /** @description Seconds until the current rate-limit window ends (1–60). */
        "Retry-After"?: number;
        [name: string]: unknown;
      };
      content: {
        "application/json": components["schemas"]["Error"];
      };
    };
    /** @description `not_found`. */
    NotFound: {
      headers: {
        "X-Request-Id": components["headers"]["RequestId"];
        [name: string]: unknown;
      };
      content: {
        "application/json": components["schemas"]["Error"];
      };
    };
    /** @description `payload_too_large`: request body larger than 1 MiB (3 MiB for publishing a template version). */
    PayloadTooLarge: {
      headers: {
        "X-Request-Id": components["headers"]["RequestId"];
        [name: string]: unknown;
      };
      content: {
        "application/json": components["schemas"]["Error"];
      };
    };
    /** @description `unsupported_media_type`: Content-Type is not application/json. */
    UnsupportedMediaType: {
      headers: {
        "X-Request-Id": components["headers"]["RequestId"];
        [name: string]: unknown;
      };
      content: {
        "application/json": components["schemas"]["Error"];
      };
    };
    /** @description `validation_failed` (message names the field), `recipient_suppressed` (the recipient is on the project's suppression list), `sender_not_verified` (`from` is not a verified sender address or domain of the project), `template_not_found`, `variables_invalid`, `render_failed`, `template_invalid`, `template_limit_reached`, `version_limit_reached`, `template_storage_limit_reached`, `invalid_url`, `endpoint_limit_reached`, `contact_limit_reached`, `audience_limit_reached`, `audience_full`, `audience_not_found`, `broadcast_limit_reached`, `template_missing_unsubscribe`, `schedule_limit_reached`, `automation_limit_reached` or `contact_not_found`. Messages name fields or variables, never their values. */
    UnprocessableEntity: {
      headers: {
        "X-Request-Id": components["headers"]["RequestId"];
        [name: string]: unknown;
      };
      content: {
        "application/json": components["schemas"]["Error"];
      };
    };
    /** @description `service_unavailable`: a dependency (e.g. the database) is temporarily unavailable; retry later. `webhook_registration_disabled`: webhook registration through the API is not enabled in this deployment (no `Retry-After`). */
    ServiceUnavailable: {
      headers: {
        "X-Request-Id": components["headers"]["RequestId"];
        /** @description Seconds to wait before retrying. */
        "Retry-After"?: number;
        [name: string]: unknown;
      };
      content: {
        "application/json": components["schemas"]["Error"];
      };
    };
    /** @description `internal_error`: unexpected server error; details are logged, never returned. */
    InternalError: {
      headers: {
        "X-Request-Id": components["headers"]["RequestId"];
        [name: string]: unknown;
      };
      content: {
        "application/json": components["schemas"]["Error"];
      };
    };
  };
  parameters: {
    ResourceID: string;
    ContactID: string;
    /** @description Page size; values outside 1–100 give `400 invalid_limit`. */
    Limit: number;
    /** @description Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`. */
    Cursor: string;
    TemplateID: string;
  };
  requestBodies: never;
  headers: {
    /**
     * @description Identifies the request in the server logs; quote it when reporting
     *     a problem. Sent on every response the API server produces, success
     *     or error (declared here on the shared error responses). A
     *     caller-supplied `X-Request-Id` request header of 1–64 characters
     *     from `[A-Za-z0-9_-]` is echoed; otherwise the server generates a
     *     unique one. Only generated ids are guaranteed unique.
     */
    RequestId: string;
  };
  pathItems: never;
}
export type $defs = Record<string, never>;
export interface operations {
  listEmails: {
    parameters: {
      query?: {
        /** @description Repeat for several statuses (at most 9). */
        status?: components["schemas"]["EmailStatus"][];
        /** @description Exact recipient address, case-insensitive. */
        recipient?: string;
        /** @description Only emails sent from this template (available together with template sends). */
        template_id?: string;
        /** @description Only recipient emails of this broadcast. */
        broadcast_id?: string;
        /** @description Inclusive lower bound (RFC 3339). */
        created_after?: string;
        /** @description Exclusive upper bound (RFC 3339); must be later than `created_after`. */
        created_before?: string;
        /** @description Page size; values outside 1–100 give `400 invalid_limit`. */
        limit?: number;
        /** @description Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`. */
        cursor?: string;
      };
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description One page of emails. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["EmailList"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  createEmail: {
    parameters: {
      query?: never;
      header?: {
        /**
         * @description Client-chosen key, 1–255 printable ASCII characters without spaces
         *     (0x21–0x7E), sent at most once. Empty, repeated or invalid keys
         *     are rejected with `400 invalid_idempotency_key`. Expires 24 hours
         *     after first use: a retry later than that creates a new email.
         */
        "Idempotency-Key"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["CreateEmailRequest"];
      };
    };
    responses: {
      /**
       * @description Persisted and queued for dispatch, or the replay of an earlier
       *     request with the same idempotency key.
       */
      202: {
        headers: {
          /** @description `true` when this is the stored response of an earlier request with the same key. */
          "Idempotent-Replayed"?: "true";
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["CreateEmailResponse"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      409: components["responses"]["Conflict"];
      413: components["responses"]["PayloadTooLarge"];
      415: components["responses"]["UnsupportedMediaType"];
      422: components["responses"]["UnprocessableEntity"];
      429: components["responses"]["TooManyRequests"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  getEmail: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: string;
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The email. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Email"];
        };
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  listEmailEvents: {
    parameters: {
      query?: {
        /** @description Page size; values outside 1–100 give `400 invalid_limit`. */
        limit?: number;
        /** @description Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`. */
        cursor?: string;
      };
      header?: never;
      path: {
        id: string;
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description One page of events. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["EmailEventList"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  cancelEmail: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The cancelled email. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Email"];
        };
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      409: components["responses"]["Conflict"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  listContacts: {
    parameters: {
      query?: {
        /** @description Exact address, case-insensitive. */
        email?: string;
        status?: components["schemas"]["ContactStatus"];
        /** @description Page size; values outside 1–100 give `400 invalid_limit`. */
        limit?: components["parameters"]["Limit"];
        /** @description Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`. */
        cursor?: components["parameters"]["Cursor"];
      };
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description One page of contacts. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["ContactList"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  createContact: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["CreateContactRequest"];
      };
    };
    responses: {
      /** @description Created. */
      201: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Contact"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      409: components["responses"]["Conflict"];
      413: components["responses"]["PayloadTooLarge"];
      415: components["responses"]["UnsupportedMediaType"];
      422: components["responses"]["UnprocessableEntity"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  getContact: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The contact. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Contact"];
        };
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  deleteContact: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description Deleted. */
      204: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  updateContact: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["UpdateContactRequest"];
      };
    };
    responses: {
      /** @description The updated contact. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Contact"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      413: components["responses"]["PayloadTooLarge"];
      415: components["responses"]["UnsupportedMediaType"];
      422: components["responses"]["UnprocessableEntity"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  listAudiences: {
    parameters: {
      query?: {
        /** @description Page size; values outside 1–100 give `400 invalid_limit`. */
        limit?: components["parameters"]["Limit"];
        /** @description Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`. */
        cursor?: components["parameters"]["Cursor"];
      };
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description One page of audiences. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["AudienceList"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  createAudience: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["CreateAudienceRequest"];
      };
    };
    responses: {
      /** @description Created. */
      201: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Audience"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      409: components["responses"]["Conflict"];
      413: components["responses"]["PayloadTooLarge"];
      415: components["responses"]["UnsupportedMediaType"];
      422: components["responses"]["UnprocessableEntity"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  getAudience: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The audience. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["AudienceWithCount"];
        };
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  listAudienceContacts: {
    parameters: {
      query?: {
        /** @description Page size; values outside 1–100 give `400 invalid_limit`. */
        limit?: components["parameters"]["Limit"];
        /** @description Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`. */
        cursor?: components["parameters"]["Cursor"];
      };
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description One page of member contacts. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["ContactList"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  addAudienceContacts: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["AddAudienceContactsRequest"];
      };
    };
    responses: {
      /** @description Counts of the outcome. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["AddAudienceContactsResult"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      413: components["responses"]["PayloadTooLarge"];
      415: components["responses"]["UnsupportedMediaType"];
      422: components["responses"]["UnprocessableEntity"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  addAudienceContact: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
        contact_id: components["parameters"]["ContactID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The contact is a member. */
      204: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      422: components["responses"]["UnprocessableEntity"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  removeAudienceContact: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
        contact_id: components["parameters"]["ContactID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The contact is not a member (any more). */
      204: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  reportUnsubscribe: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["UnsubscribeRequest"];
      };
    };
    responses: {
      /** @description The contact is unsubscribed. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["UnsubscribeResult"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      413: components["responses"]["PayloadTooLarge"];
      415: components["responses"]["UnsupportedMediaType"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  listBroadcasts: {
    parameters: {
      query?: {
        status?: components["schemas"]["BroadcastStatus"];
        /** @description Page size; values outside 1–100 give `400 invalid_limit`. */
        limit?: components["parameters"]["Limit"];
        /** @description Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`. */
        cursor?: components["parameters"]["Cursor"];
      };
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description One page of broadcasts. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["BroadcastList"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  createBroadcast: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["CreateBroadcastRequest"];
      };
    };
    responses: {
      /** @description The draft. */
      201: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Broadcast"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      413: components["responses"]["PayloadTooLarge"];
      415: components["responses"]["UnsupportedMediaType"];
      422: components["responses"]["UnprocessableEntity"];
      429: components["responses"]["TooManyRequests"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  getBroadcast: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The broadcast with counts. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["BroadcastWithCounts"];
        };
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  scheduleBroadcast: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody?: {
      content: {
        "application/json": components["schemas"]["ScheduleBroadcastRequest"];
      };
    };
    responses: {
      /** @description The scheduled broadcast. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Broadcast"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      409: components["responses"]["Conflict"];
      415: components["responses"]["UnsupportedMediaType"];
      422: components["responses"]["UnprocessableEntity"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  cancelBroadcast: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The cancelled broadcast. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Broadcast"];
        };
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      409: components["responses"]["Conflict"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  listAutomations: {
    parameters: {
      query?: {
        status?: components["schemas"]["AutomationStatus"];
        /** @description Page size; values outside 1–100 give `400 invalid_limit`. */
        limit?: components["parameters"]["Limit"];
        /** @description Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`. */
        cursor?: components["parameters"]["Cursor"];
      };
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description One page of automations. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["AutomationList"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  createAutomation: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["CreateAutomationRequest"];
      };
    };
    responses: {
      /** @description The automation. */
      201: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Automation"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      413: components["responses"]["PayloadTooLarge"];
      415: components["responses"]["UnsupportedMediaType"];
      422: components["responses"]["UnprocessableEntity"];
      429: components["responses"]["TooManyRequests"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  getAutomation: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The automation with run counts. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["AutomationWithCounts"];
        };
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  updateAutomation: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["UpdateAutomationRequest"];
      };
    };
    responses: {
      /** @description The automation. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Automation"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      409: components["responses"]["Conflict"];
      415: components["responses"]["UnsupportedMediaType"];
      422: components["responses"]["UnprocessableEntity"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  listAutomationRuns: {
    parameters: {
      query?: {
        status?: components["schemas"]["AutomationRunStatus"];
        contact_id?: string;
        /** @description Page size; values outside 1–100 give `400 invalid_limit`. */
        limit?: components["parameters"]["Limit"];
        /** @description Opaque `next_cursor` of the previous page; invalid values give `400 invalid_cursor`. */
        cursor?: components["parameters"]["Cursor"];
      };
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description One page of runs. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["AutomationRunList"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  getAutomationRun: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The run. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["AutomationRunWithEmails"];
        };
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  cancelAutomationRun: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["ResourceID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The cancelled run. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["AutomationRun"];
        };
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      409: components["responses"]["Conflict"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  createContactEvent: {
    parameters: {
      query?: never;
      header?: {
        "Idempotency-Key"?: string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["CreateContactEventRequest"];
      };
    };
    responses: {
      /** @description The event and the runs it started. */
      202: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["ContactEvent"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      409: components["responses"]["Conflict"];
      413: components["responses"]["PayloadTooLarge"];
      415: components["responses"]["UnsupportedMediaType"];
      422: components["responses"]["UnprocessableEntity"];
      429: components["responses"]["TooManyRequests"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  getUsage: {
    parameters: {
      query?: {
        /** @description First UTC day (`YYYY-MM-DD`); default `to` minus 29 days. */
        from?: string;
        /** @description Last UTC day (`YYYY-MM-DD`); default today (UTC). */
        to?: string;
      };
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description Daily usage of the project. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Usage"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  createTemplate: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["CreateTemplateRequest"];
      };
    };
    responses: {
      /** @description Created. */
      201: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Template"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      409: components["responses"]["Conflict"];
      413: components["responses"]["PayloadTooLarge"];
      415: components["responses"]["UnsupportedMediaType"];
      422: components["responses"]["UnprocessableEntity"];
      429: components["responses"]["TooManyRequests"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  getTemplate: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["TemplateID"];
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The template. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["Template"];
        };
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  publishTemplateVersion: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["TemplateID"];
      };
      cookie?: never;
    };
    requestBody: {
      content: {
        /**
         * @example {
         *       "subject": "Welcome, {{.name}}",
         *       "text": "Hello {{.name}}, {{if eq .plan \"pro\"}}thanks for going pro.{{else}}your account is ready.{{end}}",
         *       "html": "<p>Hello {{.name}},</p>{{if eq .plan \"pro\"}}<p>Thanks for going pro.</p>{{end}}",
         *       "variables": {
         *         "name": {
         *           "type": "string",
         *           "required": true,
         *           "max_length": 100
         *         },
         *         "plan": {
         *           "type": "string",
         *           "required": false
         *         }
         *       }
         *     }
         */
        "application/json": components["schemas"]["PublishTemplateVersionRequest"];
      };
    };
    responses: {
      /** @description Published. */
      201: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["TemplateVersionCreated"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      413: components["responses"]["PayloadTooLarge"];
      415: components["responses"]["UnsupportedMediaType"];
      422: components["responses"]["UnprocessableEntity"];
      429: components["responses"]["TooManyRequests"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  getTemplateVersion: {
    parameters: {
      query?: never;
      header?: never;
      path: {
        id: components["parameters"]["TemplateID"];
        version: number;
      };
      cookie?: never;
    };
    requestBody?: never;
    responses: {
      /** @description The version. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["TemplateVersion"];
        };
      };
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      404: components["responses"]["NotFound"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  createWebhookEndpoint: {
    parameters: {
      query?: never;
      header?: never;
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["CreateWebhookEndpointRequest"];
      };
    };
    responses: {
      /** @description Created. */
      201: {
        headers: {
          [name: string]: unknown;
        };
        content: {
          "application/json": components["schemas"]["WebhookEndpointCreated"];
        };
      };
      400: components["responses"]["BadRequest"];
      401: components["responses"]["Unauthorized"];
      403: components["responses"]["Forbidden"];
      413: components["responses"]["PayloadTooLarge"];
      415: components["responses"]["UnsupportedMediaType"];
      422: components["responses"]["UnprocessableEntity"];
      500: components["responses"]["InternalError"];
      503: components["responses"]["ServiceUnavailable"];
    };
  };
  emailStatusChanged: {
    parameters: {
      query?: never;
      header: {
        "Webhook-Id": string;
        /** @description Unix time in seconds when this attempt was signed. */
        "Webhook-Timestamp": string;
        "Webhook-Signature": string;
      };
      path?: never;
      cookie?: never;
    };
    requestBody: {
      content: {
        "application/json": components["schemas"]["WebhookEvent"];
      };
    };
    responses: {
      /** @description Any 2xx acknowledges receipt. */
      200: {
        headers: {
          [name: string]: unknown;
        };
        content?: never;
      };
    };
  };
}
