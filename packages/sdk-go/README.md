# Go client (`emailplatform`)

Server-side Go client for the Email Platform `/v1` API. Go 1.26 or later,
standard library only. The API key and the webhook secret are server
secrets.

## Install

Releases are tags `packages/sdk-go/vX.Y.Z` of this repository:

```bash
go get github.com/skraus2/email-platform/packages/sdk-go@v0.1.0
```

`go.sum` records the module's hash and the Go checksum database confirms
it; commit `go.sum`, and do not put the module in `GONOSUMDB` or `GOPRIVATE`, nor
set `GOSUMDB=off` (each turns the check off).

## Client

```go
import emailplatform "github.com/skraus2/email-platform/packages/sdk-go"

ep, err := emailplatform.New(
	os.Getenv("EP_API_KEY"),   // project key, ep_…
	"https://api.example.com", // plain http only for localhost
	// emailplatform.WithDefaultTimeout(30*time.Second), // whole call, retries included
	// emailplatform.WithDefaultMaxRetries(2),           // at most 5
	// emailplatform.WithHTTPClient(c),                  // e.g. for its Transport
)
```

Give each app a key with only the scopes it needs (`make admin ARGS="key
create -project <id> -scopes …"`; the default is `emails:send,emails:read`).

Every `/v1` operation has a method, grouped by resource (`ep.Emails`,
`ep.Templates`, `ep.Contacts`, `ep.Audiences`, `ep.Broadcasts`,
`ep.Automations`, `ep.AutomationRuns`, `ep.ContactEvents`,
`ep.Unsubscribes`, `ep.WebhookEndpoints`, `ep.Usage`). Request and
response types are generated from the API contract and keep its wire names
in their JSON tags (`next_cursor`, `created_at`). Every method takes a
`context.Context` first; per-call options are `WithTimeout` and
`WithMaxRetries`. Invalid arguments return an error wrapping
`emailplatform.ErrInvalidArgument` before anything is sent. Redirects are
never followed, also not with your own `*http.Client` (the client copies
it). The client never logs, and neither it nor its errors print the key, a
request body or a query string.

`template_version` and automation steps have helpers, since the contract
allows several shapes there: `TemplateVersionNumber(n)`,
`TemplateVersionPublished()`, `ParseTemplateVersion(raw)`,
`NewAutomationStep(step)` and `DecodeAutomationStep(raw)`.

## Sending

```go
email, err := ep.Emails.Send(ctx, &emailplatform.CreateEmailRequest{
	From:    "shop@example.com",
	To:      customer.Email,
	Subject: new("Your receipt"),
	Text:    new("…"),
}, emailplatform.WithIdempotencyKey("order-"+order.ID+":receipt"))
// email.Status == "queued"; follow it with ep.Emails.Get(ctx, email.Id)
```

Pass your own idempotency key whenever the send may be repeated by your
app: a job re-run, a retry after a crash, a second click. Make it stable
and specific to the purpose (`order-42:receipt`, not `order-42`), and keep
personal data out of it (no addresses or names). The API replays the
original answer for the same key and body for 24 hours and answers `409`
for the same key with a different body. An empty key is refused. Without a
key the client generates one per call, which only protects the client's
own retries: if such a send fails with an unknown outcome, look for the
email (`ep.Emails.List(ctx, &emailplatform.ListEmailsParams{Recipient:
new(addr)})`) before sending it again.

`WithIdempotencyKey` is accepted only by `Emails.Send` and
`ContactEvents.Create`. To pass shared options to them, collect them as
`[]emailplatform.IdempotentOption` (every `RequestOption` is one).

## Retries

The client retries by itself, within the timeout:

- `429`: after `Retry-After`, for every operation (nothing was stored).
- `500`, `502`, `504`, `503 service_unavailable` and network errors: only
  for reads and for `Emails.Send` / `ContactEvents.Create`, which always
  carry an idempotency key.
- Everything else, including writes without a key (creating a broadcast,
  a contact, a webhook endpoint, …): never. After a failure their outcome
  is unknown; check with a `Get` or a list before you try again, or you may
  create a second one.

## Errors

```go
_, err := ep.Emails.Send(ctx, req, key)
if e, ok := errors.AsType[*emailplatform.APIError](err); ok {
	// e.Status, e.Code ("rate_limited", "validation_failed", …),
	// e.RetryAfter, e.RequestID (quote it in reports)
} else if e, ok := errors.AsType[*emailplatform.TransportError](err); ok {
	// e.Reason, see below
}
```

`TransportError.Reason`:

- `invalid_body`, `body_too_large`: the API answered `2xx`, so the request
  was applied (an email was accepted), but its answer could not be read.
- `network`, `timeout` (the client's timeout ran out, retries included):
  the request may or may not have reached the API — the outcome is
  unknown. Repeat a send with your own idempotency key, or check first as
  described above.
- `aborted`: your context ended the call (cancelled, or its own deadline
  passed); the outcome is unknown too. `errors.Is(err, context.Canceled)`
  / `context.DeadlineExceeded` tell which.

## Lists

`List…` returns one page (`Data`, `NextCursor`); `ListAll…` returns an
iterator over every item, following the cursor, starting at the first page
each time you range over it:

```go
for email, err := range ep.Emails.ListAll(ctx, &emailplatform.ListEmailsParams{
	Status: &[]emailplatform.EmailStatus{emailplatform.EmailStatusFailed},
}) {
	if err != nil {
		return err
	}
	fmt.Println(email.Id, email.Subject)
}
```

Each page is its own call with its own timeout. Give the context a
deadline when you need the whole iteration bounded.

## Webhooks

The platform signs every webhook request (`Webhook-Id`,
`Webhook-Timestamp`, `Webhook-Signature`). Verify it against the **raw
request body**, exactly as received — a body decoded and encoded again no
longer matches the signature.

```go
import "github.com/skraus2/email-platform/packages/sdk-go/webhook"

http.HandleFunc("POST /hooks/email", func(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	} else if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	verified, err := webhook.Verify(os.Getenv("EP_WEBHOOK_SECRET"), r.Header, body, time.Now())
	if err != nil {
		if e, ok := errors.AsType[*webhook.Error](err); ok && e.Reason != webhook.ReasonMissingSecret {
			w.WriteHeader(http.StatusBadRequest) // not from the platform, or changed
			return
		}
		w.WriteHeader(http.StatusInternalServerError) // configuration fault: the platform retries
		return
	}
	// Insert-if-absent on the signed event id (the table's primary key), in
	// the same transaction as the side effect: a repeated delivery changes
	// nothing.
	err = inTx(r.Context(), db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(r.Context(),
			"INSERT INTO processed_webhooks (id) VALUES ($1) ON CONFLICT DO NOTHING", verified.ID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 1 {
			return handle(r.Context(), tx, verified.Event) // e.g. Event.Type == emailplatform.EmailBounced
		}
		return nil
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent) // also for duplicates
})
```

```sql
CREATE TABLE processed_webhooks (
  id text PRIMARY KEY,
  processed_at timestamptz NOT NULL DEFAULT now()
);
```

Serve the endpoint from an `http.Server` with time limits
(`ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second`);
the zero `http.Server` behind `http.ListenAndServe` has none, so slow
senders could hold connections open.

- Deduplicate on `verified.ID` (the signed event id), never on the
  unsigned `Webhook-Id` header alone. Keep processed ids longer than the
  platform's retry window: a little over 5 hours with the default
  settings, so 7 days covers it with margin. Operators can lengthen the
  window (up to `WEBHOOK_MAX_ATTEMPTS` × `WEBHOOK_RETRY_MAX_DELAY`); ask
  yours if they changed it. Every retry is signed anew, so only your stored
  ids stop a late duplicate.
- Answer `2xx` once the event is handled or was a duplicate, within
  10 seconds. Timeouts, connection errors, `5xx`, `408` and `429` make the
  platform retry (12 attempts over about 5 hours); every other status,
  redirects included, ends the delivery as failed.
- `webhook.Error.Reason` tells why: `missing_header`, `bad_header`,
  `bad_body`, `signature_mismatch`, `id_mismatch`, `bad_timestamp` (with
  `Skew`: `past` or `future` when the timestamp is outside the tolerance)
  → answer `400`; `missing_secret` (no `whsec_…` secret configured) →
  answer `500`, as for an invalid option (an error wrapping
  `emailplatform.ErrInvalidArgument`: a tolerance outside (0, 1 h] or a
  zero `now`).
- A timestamp more than 5 minutes off your server's clock is rejected
  (`webhook.WithTolerance` changes this, up to 1 hour).
- A `400` ends the delivery for good, and so does a wrong clock
  (`bad_timestamp` on every delivery), a stale secret (`signature_mismatch`)
  or a body changed before the handler (`bad_body`). Alert on a sustained
  rate of rejected deliveries rather than on single ones (anyone can send a
  request that is rejected), and recover missed status changes with
  `ep.Emails.ListAllEvents` or `ep.Emails.List`.
- Unknown event types from newer platform versions are returned, not
  rejected (`Event.Type.Valid()` is false for them); ignore what you do not
  handle and answer `2xx`. Fields this version does not know are left out
  of `Event`; decode the raw body yourself if you need them.
