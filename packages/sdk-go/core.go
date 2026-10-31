package emailplatform

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	mathrand "math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// runtime is everything the request loop needs from the outside world;
// tests replace it.
type runtime struct {
	now func() time.Time
	// sleep waits d or until ctx is done (then it returns ctx's error).
	sleep  func(ctx context.Context, d time.Duration) error
	random func() float64
	newKey func() string
}

var realRuntime = runtime{
	now: time.Now,
	sleep: func(ctx context.Context, d time.Duration) error {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	},
	random: mathrand.Float64,
	newKey: func() string {
		b := make([]byte, 16)
		_, _ = rand.Read(b) // never fails (crypto/rand)
		return hex.EncodeToString(b)
	},
}

// core runs one logical call per invocation. It is immutable after New, so
// a Client is safe for concurrent use. The API key is held only by the
// authorize closure: fmt prints a func as an address, so no way of printing
// a Client, a service or a core (even through reflection) shows the key.
type core struct {
	authorize  func(http.Header)
	base       string
	timeout    time.Duration
	maxRetries int
	http       *http.Client
	rt         runtime
}

var (
	requestIDPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	idempotencyKeyPattern = regexp.MustCompile(`^[\x21-\x7e]{1,255}$`)
	errorCodePattern      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	errCallTimeout        = errors.New("emailplatform: call timeout")
)

func findOperation(id string) *operation {
	for i := range operations {
		if operations[i].id == id {
			return &operations[i]
		}
	}
	panic("emailplatform: unknown operation " + id) // a bug in this package
}

// request is one logical call; body is nil for none, result nil when the
// operation has no result.
type request struct {
	pathParams []string
	params     any
	body       any
	result     any
}

func (c *core) call(ctx context.Context, opID string, req request, opts []func(*callOptions)) error {
	op := findOperation(opID)
	if ctx == nil {
		return invalid("context is required")
	}
	o := callOptions{timeout: c.timeout, maxRetries: c.maxRetries}
	for _, apply := range opts {
		apply(&o)
	}
	if o.err != nil {
		return o.err
	}

	header := http.Header{}
	c.authorize(header)
	header.Set("Accept", "application/json")
	header.Set("User-Agent", userAgent)
	switch {
	case o.keySet && !keyed(op):
		return invalid("idempotency key is accepted only by createEmail and createContactEvent")
	case o.keySet:
		// An empty key is refused, not replaced: a random key would let
		// the app's own retries send the email again.
		if !idempotencyKeyPattern.MatchString(o.idempotencyKey) {
			return invalid("idempotency key must be 1–255 printable ASCII characters without spaces")
		}
		header.Set("Idempotency-Key", o.idempotencyKey)
	case keyed(op):
		// Protects this call's own retries: the API replays the first answer
		// instead of queueing a second email.
		header.Set("Idempotency-Key", c.rt.newKey())
	}

	var body []byte
	if req.body != nil {
		var err error
		// Marshalled once: every retry sends the same bytes. The JSON error
		// is dropped, it may quote values.
		if body, err = json.Marshal(req.body); err != nil {
			return invalid("body cannot be encoded as JSON")
		}
		header.Set("Content-Type", "application/json")
	}
	path, err := buildPath(op.path, req.pathParams)
	if err != nil {
		return err
	}
	query, err := encodeQuery(req.params)
	if err != nil {
		return err
	}
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	deadline := c.rt.now().Add(o.timeout)
	callCtx, cancel := context.WithTimeoutCause(ctx, o.timeout, errCallTimeout)
	defer cancel()
	for retry := 0; ; retry++ {
		if ctx.Err() != nil {
			return abortedError(op, ctx)
		}
		err := c.attempt(ctx, callCtx, op, target, header, body, req.result)
		if err == nil {
			return nil
		}
		if retry >= o.maxRetries || !shouldRetry(op, err) {
			return err
		}
		var after time.Duration
		var sent bool
		if apiErr, ok := errors.AsType[*APIError](err); ok {
			after, sent = apiErr.RetryAfter, apiErr.retryAfterSent
		}
		d := wait(retry, after, sent, c.rt.random)
		if !c.rt.now().Add(d).Before(deadline) {
			return err
		}
		if c.rt.sleep(callCtx, d) != nil {
			return classifyDone(op, ctx, callCtx, nil)
		}
	}
}

// attempt sends the request once and decodes the answer into result.
func (c *core) attempt(ctx, callCtx context.Context, op *operation, target string, header http.Header, body []byte, result any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(callCtx, op.method, target, reader)
	if err != nil {
		// The URL was built from a validated base and escaped parts.
		return transportError(op, ReasonNetwork)
	}
	httpReq.Header = header.Clone()
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return classifyDone(op, ctx, callCtx, err)
	}
	defer func() {
		// A little of an unread body is read so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()

	requestID := resp.Header.Get("X-Request-Id")
	if !requestIDPattern.MatchString(requestID) {
		requestID = ""
	}
	status := resp.StatusCode
	if status >= 300 && status < 400 {
		return &APIError{Status: status, Code: "redirect", Message: "HTTP " + itoa(status) + ": redirects are not followed", RequestID: requestID, OperationID: op.id}
	}
	if status >= 200 && status < 300 && result == nil {
		return nil
	}

	data, err := readBody(resp)
	if err != nil {
		return classifyDone(op, ctx, callCtx, err)
	}
	if status >= 200 && status < 300 {
		if data == nil {
			return transportError(op, ReasonBodyTooLarge)
		}
		if len(data) == 0 || !utf8.Valid(data) || json.Unmarshal(data, result) != nil {
			return transportError(op, ReasonInvalidBody)
		}
		return nil
	}

	apiErr := &APIError{Status: status, RequestID: requestID, OperationID: op.id}
	apiErr.RetryAfter, apiErr.retryAfterSent = retryAfter(resp.Header)
	if code, message, ok := errorEnvelope(data); ok {
		apiErr.Code, apiErr.Message = code, message
	} else {
		apiErr.Code = "http_error"
		if status == http.StatusServiceUnavailable {
			apiErr.Code = "service_unavailable"
		}
		apiErr.Message = "HTTP " + itoa(status)
	}
	return apiErr
}

// classifyDone names a failed request, read or wait: the caller's context
// ended → aborted; the call's own deadline → timeout; else the network.
func classifyDone(op *operation, ctx, callCtx context.Context, err error) error {
	if ctx.Err() != nil {
		return abortedError(op, ctx)
	}
	if errors.Is(context.Cause(callCtx), errCallTimeout) {
		return transportError(op, ReasonTimeout, context.DeadlineExceeded)
	}
	// The *url.Error holds the full URL with its query; keep only its cause.
	if urlErr, ok := errors.AsType[*url.Error](err); ok {
		err = urlErr.Err
	}
	if err == nil {
		return transportError(op, ReasonNetwork)
	}
	return transportError(op, ReasonNetwork, err)
}

// readBody reads up to the size limit; nil (not empty) when it is larger.
func readBody(resp *http.Response) ([]byte, error) {
	if resp.ContentLength > maxBodyBytes {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, err //nolint:wrapcheck // classified by the caller, never shown
	}
	if len(data) > maxBodyBytes {
		return nil, nil
	}
	if data == nil {
		data = []byte{}
	}
	return data, nil
}

// errorEnvelope reads the API's error body. A code that is not the API's
// snake_case form means the body came from somewhere else.
func errorEnvelope(data []byte) (code, message string, ok bool) {
	if len(data) == 0 || !utf8.Valid(data) {
		return "", "", false
	}
	var envelope struct {
		Error *struct {
			Code    *string `json:"code"`
			Message *string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Error == nil ||
		envelope.Error.Code == nil || envelope.Error.Message == nil ||
		!errorCodePattern.MatchString(*envelope.Error.Code) {
		return "", "", false
	}
	return *envelope.Error.Code, truncate(withoutControls(*envelope.Error.Message), maxMessageBytes), true
}

// withoutControls turns control characters into spaces, so a message
// cannot start a new line in a log that prints Error() unquoted.
func withoutControls(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func itoa(n int) string { return strconv.Itoa(n) }
