package emailplatform

import (
	"net/http"
	"time"
)

type callOptions struct {
	timeout        time.Duration
	maxRetries     int
	idempotencyKey string
	// keySet tells an empty key given by the caller from none at all.
	keySet bool
	err    error
}

// RequestOption changes one call.
type RequestOption func(*callOptions)

// IdempotentOption changes a call of Emails.Send or ContactEvents.Create,
// the two operations whose retries the API deduplicates. Every
// RequestOption is one; WithIdempotencyKey is only that.
type IdempotentOption interface {
	applyIdempotent(o *callOptions)
}

func (f RequestOption) applyIdempotent(o *callOptions) { f(o) }

// WithTimeout sets the time for the whole call, retries and the waits
// between them included (default 30 s).
func WithTimeout(d time.Duration) RequestOption {
	return func(o *callOptions) {
		if d <= 0 {
			o.err = invalid("timeout must be positive")
			return
		}
		o.timeout = d
	}
}

// WithMaxRetries sets the number of retries after the first attempt
// (default 2, at most 5).
func WithMaxRetries(n int) RequestOption {
	return func(o *callOptions) {
		if n < 0 {
			o.err = invalid("max retries must not be negative")
			return
		}
		o.maxRetries = min(n, maxRetriesLimit)
	}
}

type idempotencyKey string

func (k idempotencyKey) applyIdempotent(o *callOptions) {
	o.idempotencyKey, o.keySet = string(k), true
}

// WithIdempotencyKey sends key as Idempotency-Key (1–255 printable ASCII
// characters without spaces). Without it a random key covers this call and
// its own retries only; pass a stable key of your own when your app may
// repeat the call later. Scope it to the purpose and keep personal data out
// of it: an order or user id, never an email address.
func WithIdempotencyKey(key string) IdempotentOption { return idempotencyKey(key) }

// ClientOption changes the defaults of a Client.
type ClientOption func(*clientOptions)

type clientOptions struct {
	http       *http.Client
	timeout    time.Duration
	maxRetries int
	err        error
}

// WithHTTPClient sends requests through c, e.g. for its Transport. The
// client is copied and redirects are refused on the copy: a redirect would
// carry the API key to wherever it points.
func WithHTTPClient(c *http.Client) ClientOption {
	return func(o *clientOptions) {
		if c == nil {
			o.err = invalid("http client must not be nil")
			return
		}
		o.http = c
	}
}

// WithDefaultTimeout sets the timeout of every call (default 30 s).
func WithDefaultTimeout(d time.Duration) ClientOption {
	return func(o *clientOptions) {
		if d <= 0 {
			o.err = invalid("default timeout must be positive")
			return
		}
		o.timeout = d
	}
}

// WithDefaultMaxRetries sets the retries of every call (default 2, at most
// 5).
func WithDefaultMaxRetries(n int) ClientOption {
	return func(o *clientOptions) {
		if n < 0 {
			o.err = invalid("default max retries must not be negative")
			return
		}
		o.maxRetries = min(n, maxRetriesLimit)
	}
}
