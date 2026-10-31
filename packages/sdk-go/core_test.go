package emailplatform

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestNewDefaultsAndArguments(t *testing.T) {
	cl, err := New(testKey, testBase)
	if err != nil {
		t.Fatal(err)
	}
	if cl.c.timeout != 30*time.Second || cl.c.maxRetries != 2 {
		t.Fatalf("defaults %v, %d", cl.c.timeout, cl.c.maxRetries)
	}
	cl, err = New(testKey, testBase, WithDefaultMaxRetries(9), WithDefaultTimeout(time.Minute))
	if err != nil || cl.c.maxRetries != 5 || cl.c.timeout != time.Minute {
		t.Fatalf("options: %v, %d, %v", err, cl.c.maxRetries, cl.c.timeout)
	}
	refused := map[string]struct {
		key  string
		opts []ClientOption
		name string
	}{
		"empty key":        {"", nil, "API key"},
		"key with a space": {"ep test", nil, "API key"},
		"key with unicode": {"ep_tést", nil, "API key"},
		"zero timeout":     {testKey, []ClientOption{WithDefaultTimeout(0)}, "timeout"},
		"negative retries": {testKey, []ClientOption{WithDefaultMaxRetries(-1)}, "retries"},
		"nil http client":  {testKey, []ClientOption{WithHTTPClient(nil)}, "http client"},
	}
	for name, tc := range refused {
		t.Run(name, func(t *testing.T) {
			_, err := New(tc.key, testBase, tc.opts...)
			if !errors.Is(err, ErrInvalidArgument) || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("New = %v", err)
			}
			if tc.key != "" && strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error echoes the key: %v", err)
			}
		})
	}
	if _, err := New(testKey, testBase, nil); err != nil {
		t.Fatalf("nil option: %v", err)
	}
}

func TestBaseURL(t *testing.T) {
	accepted := map[string]string{
		"https://api.example.com":         "https://api.example.com",
		"https://api.example.com/":        "https://api.example.com",
		"https://api.example.com/prefix/": "https://api.example.com/prefix",
		"https://api.example.com//a//b/":  "https://api.example.com/a/b",
		"http://localhost:8080":           "http://localhost:8080",
		"http://127.0.0.1:8080":           "http://127.0.0.1:8080",
		"http://[::1]:8080":               "http://[::1]:8080",
	}
	for raw, want := range accepted {
		if got, err := parseBaseURL(raw); err != nil || got != want {
			t.Errorf("%s → %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"", "http://api.example.com", "http://localhost.evil.com", "http://localhost@evil.com",
		"http://127.0.0.1.nip.io", "https://user:pw@api.example.com", "https://api.example.com/?a=1",
		"https://api.example.com/?", "https://api.example.com/#x", "https://api.example.com/#",
		"ftp://api.example.com", "https:///nohost", "api.example.com", "/v1", "https://exa mple.com",
		"https://%zz", "mailto:a@example.com",
	} {
		_, err := parseBaseURL(raw)
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%q accepted: %v", raw, err)
			continue
		}
		if raw != "" && strings.Contains(err.Error(), raw) {
			t.Errorf("error echoes %q: %v", raw, err)
		}
	}
	// A path prefix is kept in front of the operation path.
	s := &stub{respond: always(200, `{"id":"e1"}`)}
	cl, err := New(testKey, "https://api.example.com/prefix/", WithHTTPClient(&http.Client{Transport: s}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Emails.Get(context.Background(), "e1"); err != nil {
		t.Fatal(err)
	}
	if got := s.last().path; got != "/prefix/v1/emails/e1" {
		t.Fatalf("path %s", got)
	}
}

func TestRequestHeaders(t *testing.T) {
	s := &stub{respond: always(200, `{"id":"e1"}`)}
	cl, _ := newTestClient(t, s)
	if _, err := cl.Contacts.Update(context.Background(), "c1", &UpdateContactRequest{}); err != nil {
		t.Fatal(err)
	}
	h := s.last().header
	for name, want := range map[string]string{
		"Authorization": "Bearer " + testKey, "Accept": "application/json",
		"User-Agent": "email-platform-sdk-go", "Content-Type": "application/json",
	} {
		if h.Get(name) != want {
			t.Errorf("%s = %q, want %q", name, h.Get(name), want)
		}
	}
	if h.Get("Idempotency-Key") != "" {
		t.Error("Idempotency-Key on a keyless operation")
	}
}

func TestPathParameters(t *testing.T) {
	s := &stub{respond: always(200, `{"id":"x"}`)}
	cl, _ := newTestClient(t, s)
	ctx := context.Background()
	if _, err := cl.Emails.Get(ctx, "a/b?c#d%e f"); err != nil {
		t.Fatal(err)
	}
	if got := s.last().path; got != "/v1/emails/a%2Fb%3Fc%23d%25e%20f" {
		t.Fatalf("escaped path %s", got)
	}
	if _, err := cl.Templates.GetVersion(ctx, "t1", 3); err != nil || s.last().path != "/v1/templates/t1/versions/3" {
		t.Fatalf("version path %s, %v", s.last().path, err)
	}
	before := s.count()
	refused := map[string]func() error{
		"empty id":    func() error { _, err := cl.Emails.Get(ctx, ""); return err },
		"dot":         func() error { _, err := cl.Emails.Get(ctx, "."); return err },
		"dot dot":     func() error { return cl.Audiences.RemoveContact(ctx, "..", "c1") },
		"second dot":  func() error { return cl.Audiences.RemoveContact(ctx, "a1", "..") },
		"version 0":   func() error { _, err := cl.Templates.GetVersion(ctx, "t1", 0); return err },
		"version -1":  func() error { _, err := cl.Templates.GetVersion(ctx, "t1", -1); return err },
		"nil context": func() error { var nilCtx context.Context; _, err := cl.Emails.Get(nilCtx, "e1"); return err },
		"bad option":  func() error { _, err := cl.Emails.Get(ctx, "e1", WithMaxRetries(-1)); return err },
		"wrong op key": func() error {
			return cl.c.call(ctx, "createBroadcast", request{}, []func(*callOptions){WithIdempotencyKey("k").applyIdempotent})
		},
		"unmarshalable": func() error {
			_, err := cl.Contacts.Create(ctx, &CreateContactRequest{Attributes: new(ContactAttributes{"n": func() {}})})
			return err
		},
	}
	for name, f := range refused {
		if err := f(); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if s.count() != before {
		t.Fatalf("%d requests sent for invalid arguments", s.count()-before)
	}
	// Nil options are skipped.
	if _, err := cl.Emails.Get(ctx, "e1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Emails.Send(ctx, &CreateEmailRequest{}, nil, RequestOption(nil)); err != nil {
		t.Fatal(err)
	}
}

func TestQueryParameters(t *testing.T) {
	s := &stub{respond: always(200, `{"data":[],"next_cursor":null}`)}
	cl, _ := newTestClient(t, s)
	after := time.Date(2026, 10, 31, 12, 0, 0, 500, time.FixedZone("", 2*3600))
	_, err := cl.Emails.List(context.Background(), &ListEmailsParams{
		Status:       &[]EmailStatus{"queued", "sent"},
		Recipient:    new("a+b@example.com"),
		CreatedAfter: &after,
		Limit:        new(10),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "created_after=2026-10-31T12%3A00%3A00.0000005%2B02%3A00&limit=10&recipient=a%2Bb%40example.com&status=queued&status=sent"
	if got := s.last().query; got != want {
		t.Fatalf("query %s\nwant  %s", got, want)
	}
}

func TestQueryEncodingFailsClosed(t *testing.T) {
	type float struct {
		F *float64 `form:"f"`
	}
	type untagged struct {
		A *string
	}
	type nested struct {
		N *struct{ X string } `form:"n"`
	}
	for name, params := range map[string]any{"float": &float{F: new(1.5)}, "untagged": untagged{}, "nested": nested{N: &struct{ X string }{"x"}}, "not a struct": new(3)} {
		if _, err := encodeQuery(params); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if v, err := encodeQuery((*ListEmailsParams)(nil)); err != nil || len(v) != 0 {
		t.Fatalf("nil params: %v, %v", v, err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	// The caller's policy follows (up to 3 hops, so a regression fails fast).
	follow := func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return http.ErrUseLastResponse
		}
		return nil
	}
	for _, status := range []int{301, 307} {
		s := &stub{respond: always(status, "", "Location", "https://evil.example/")}
		hc := &http.Client{Transport: s, CheckRedirect: follow}
		cl, err := New(testKey, testBase, WithHTTPClient(hc), WithDefaultMaxRetries(0))
		if err != nil {
			t.Fatal(err)
		}
		cl.c.rt = (&clock{random: 0.5}).runtime()
		_, err = cl.Emails.Get(context.Background(), "e1")
		apiErr, ok := errors.AsType[*APIError](err)
		if !ok || apiErr.Code != "redirect" || apiErr.Status != status || s.count() != 1 {
			t.Fatalf("%d: %v after %d requests", status, err, s.count())
		}
		if hc.CheckRedirect == nil || hc.CheckRedirect(nil, nil) != nil {
			t.Fatal("the caller's client was modified")
		}
	}
}

func TestAPIErrors(t *testing.T) {
	long := strings.Repeat("é", 600) // 1200 bytes
	cases := map[string]struct {
		status        int
		body          string
		header        []string
		code, message string
		retryAfter    time.Duration
		requestID     string
	}{
		"envelope":           {422, `{"error":{"code":"validation_failed","message":"to is invalid"}}`, []string{"X-Request-Id", "req_1", "Retry-After", "7"}, "validation_failed", "to is invalid", 7 * time.Second, "req_1"},
		"long message":       {400, `{"error":{"code":"bad","message":"` + long + `"}}`, nil, "bad", strings.Repeat("é", 512), 0, ""},
		"html 502":           {502, "<html>whsec_abc</html>", nil, "http_error", "HTTP 502", 0, ""},
		"html 503":           {503, "<html>", nil, "service_unavailable", "HTTP 503", 0, ""},
		"control characters": {400, `{"error":{"code":"bad","message":"a\r\nFAKE log line\u0000"}}`, nil, "bad", "a  FAKE log line ", 0, ""},
		"bad code":           {400, `{"error":{"code":"Bad-Code","message":"x"}}`, nil, "http_error", "HTTP 400", 0, ""},
		"no message":         {400, `{"error":{"code":"bad"}}`, nil, "http_error", "HTTP 400", 0, ""},
		"bad id":             {404, `{"error":{"code":"not_found","message":"no"}}`, []string{"X-Request-Id", "has space"}, "not_found", "no", 0, ""},
		"date retry":         {429, `{"error":{"code":"rate_limited","message":"x"}}`, []string{"Retry-After", "Fri, 31 Oct 2026 12:00:00 GMT"}, "rate_limited", "x", 0, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := &stub{respond: always(tc.status, tc.body, tc.header...)}
			cl, _ := newTestClient(t, s, WithDefaultMaxRetries(0))
			_, err := cl.Emails.Get(context.Background(), "e1")
			e, ok := errors.AsType[*APIError](err)
			if !ok {
				t.Fatalf("err %v", err)
			}
			if e.Status != tc.status || e.Code != tc.code || e.Message != tc.message || e.RetryAfter != tc.retryAfter || e.RequestID != tc.requestID || e.OperationID != "getEmail" {
				t.Fatalf("%#v", e)
			}
			if want := "getEmail: HTTP " + fmt.Sprint(tc.status) + " " + tc.code + ": " + tc.message; e.Error() != want {
				t.Fatalf("Error() = %q", e.Error())
			}
		})
	}
}

func TestTransportErrors(t *testing.T) {
	ctx := context.Background()
	t.Run("network", func(t *testing.T) {
		s := &stub{respond: func(int, *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial: %w", syscall.ECONNREFUSED)
		}}
		cl, _ := newTestClient(t, s, WithDefaultMaxRetries(0))
		_, err := cl.Emails.List(ctx, &ListEmailsParams{Recipient: new("user@example.com")})
		te, ok := errors.AsType[*TransportError](err)
		if !ok || te.Reason != ReasonNetwork || te.Method != http.MethodGet || te.Path != "/v1/emails" || te.OperationID != "listEmails" {
			t.Fatalf("%#v", err)
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			t.Fatal("cause not unwrappable")
		}
		if te.Error() != "GET /v1/emails (listEmails): network" {
			t.Fatalf("Error() = %q", te.Error())
		}
		for _, c := range te.Unwrap() {
			if strings.Contains(c.Error(), "user") || strings.Contains(c.Error(), "example.com") {
				t.Fatalf("cause holds the URL: %v", c)
			}
		}
	})
	t.Run("timeout", func(t *testing.T) {
		s := &stub{respond: func(_ int, r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}}
		cl, _ := newTestClient(t, s)
		_, err := cl.Emails.Get(ctx, "e1", WithTimeout(20*time.Millisecond))
		te, ok := errors.AsType[*TransportError](err)
		if !ok || te.Reason != ReasonTimeout || !errors.Is(err, context.DeadlineExceeded) || s.count() != 1 {
			t.Fatalf("%v after %d requests", err, s.count())
		}
	})
	t.Run("aborted during the request", func(t *testing.T) {
		cause := errors.New("shutting down")
		ctx, cancel := context.WithCancelCause(ctx)
		s := &stub{respond: func(int, *http.Request) (*http.Response, error) {
			cancel(cause)
			return nil, context.Canceled
		}}
		cl, _ := newTestClient(t, s)
		_, err := cl.Emails.Get(ctx, "e1")
		te, ok := errors.AsType[*TransportError](err)
		if !ok || te.Reason != ReasonAborted || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) || s.count() != 1 {
			t.Fatalf("%v after %d requests", err, s.count())
		}
	})
	t.Run("caller deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		defer cancel()
		s := &stub{respond: func(_ int, r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}}
		cl, _ := newTestClient(t, s)
		_, err := cl.Emails.Get(ctx, "e1")
		if te, ok := errors.AsType[*TransportError](err); !ok || te.Reason != ReasonAborted || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("injected client timeout is network", func(t *testing.T) {
		s := &stub{respond: func(n int, r *http.Request) (*http.Response, error) {
			if n <= 1 {
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			return respond(200, `{"id":"e1"}`), nil
		}}
		cl, _ := newTestClient(t, s, WithHTTPClient(&http.Client{Transport: s, Timeout: 10 * time.Millisecond}))
		_, err := cl.Emails.Get(ctx, "e1", WithMaxRetries(0))
		if te, ok := errors.AsType[*TransportError](err); !ok || te.Reason != ReasonNetwork {
			t.Fatalf("%v", err)
		}
		// Retried like any network failure on a read.
		if _, err := cl.Emails.Get(ctx, "e1"); err != nil || s.count() != 3 {
			t.Fatalf("%v after %d requests", err, s.count())
		}
	})
}

// slowBody is a response body that never ends.
type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestSuccessBodies(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		resp *http.Response
		want TransportReason
	}{
		"not JSON":           {respond(200, "whsec_secretvalue"), ReasonInvalidBody},
		"empty":              {respond(200, ""), ReasonInvalidBody},
		"204 with a result":  {respond(204, ""), ReasonInvalidBody},
		"trailing data":      {respond(200, `{"id":"e1"} {}`), ReasonInvalidBody},
		"invalid UTF-8":      {respond(200, "{\"id\":\"\xff\"}"), ReasonInvalidBody},
		"wrong type":         {respond(200, `{"id":1}`), ReasonInvalidBody},
		"too large (length)": {&http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), ContentLength: maxBodyBytes + 1}, ReasonBodyTooLarge},
		"too large (stream)": {&http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(endless{}), ContentLength: -1}, ReasonBodyTooLarge},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := &stub{respond: func(int, *http.Request) (*http.Response, error) { return tc.resp, nil }}
			cl, _ := newTestClient(t, s)
			_, err := cl.Emails.Get(ctx, "e1")
			te, ok := errors.AsType[*TransportError](err)
			if !ok || te.Reason != tc.want || s.count() != 1 {
				t.Fatalf("%v after %d requests, want %s once", err, s.count(), tc.want)
			}
			if len(te.Unwrap()) != 0 || strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), "whsec_") {
				t.Fatalf("error carries the body: %#v", err)
			}
		})
	}
	t.Run("result-less operations accept any 2xx", func(t *testing.T) {
		for _, resp := range []*http.Response{respond(204, ""), respond(200, "not json")} {
			s := &stub{respond: func(int, *http.Request) (*http.Response, error) { return resp, nil }}
			cl, _ := newTestClient(t, s)
			if err := cl.Contacts.Delete(ctx, "c1"); err != nil {
				t.Fatal(err)
			}
		}
	})
}

type closeCounter struct {
	io.Reader
	mu     sync.Mutex
	closed int
}

func (c *closeCounter) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	return nil
}

func TestEveryBodyIsClosed(t *testing.T) {
	var bodies []*closeCounter
	s := &stub{respond: func(n int, _ *http.Request) (*http.Response, error) {
		b := &closeCounter{Reader: strings.NewReader([]string{unavailable, "<html>", `{"id":"e1"}`}[min(n, 2)])}
		bodies = append(bodies, b)
		status := []int{503, 302, 200}[min(n, 2)]
		return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"1"}}, Body: b, ContentLength: -1}, nil
	}}
	cl, _ := newTestClient(t, s)
	_, _ = cl.Emails.Get(context.Background(), "e1") // 503 → retry → 302
	_, _ = cl.Emails.Get(context.Background(), "e1") // 200
	for i, b := range bodies {
		if b.closed != 1 {
			t.Errorf("body %d closed %d times", i, b.closed)
		}
	}
	// An unread body is drained (a little) before it is closed, so the
	// connection can be reused.
	unread := &closeCounter{Reader: strings.NewReader("ignored")}
	s2 := &stub{respond: func(int, *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: unread, ContentLength: -1}, nil
	}}
	cl2, _ := newTestClient(t, s2)
	if err := cl2.Contacts.Delete(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	if rest, _ := io.ReadAll(unread.Reader); len(rest) != 0 || unread.closed != 1 {
		t.Fatalf("left %q unread, closed %d times", rest, unread.closed)
	}
}

func TestWaits(t *testing.T) {
	rateLimited := `{"error":{"code":"rate_limited","message":"slow down"}}`
	cases := map[string]struct {
		header []string
		want   time.Duration
	}{
		"retry-after":     {[]string{"Retry-After", "2"}, 2*time.Second + 500*time.Millisecond},
		"retry-after 0":   {[]string{"Retry-After", "0"}, time.Second + 500*time.Millisecond},
		"retry-after 120": {[]string{"Retry-After", "120"}, 60*time.Second + 500*time.Millisecond},
		"no retry-after":  {nil, 375 * time.Millisecond},
		"date":            {[]string{"Retry-After", "Fri, 31 Oct 2026 12:00:00 GMT"}, 375 * time.Millisecond},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := &stub{respond: sequence(answer(429, rateLimited, tc.header...), answer(201, `{"id":"b1"}`))}
			cl, clk := newTestClient(t, s, WithDefaultTimeout(10*time.Minute))
			if _, err := cl.Broadcasts.Create(context.Background(), &CreateBroadcastRequest{}); err != nil {
				t.Fatal(err)
			}
			if len(clk.sleeps) != 1 || clk.sleeps[0] != tc.want {
				t.Fatalf("waits %v, want [%v]", clk.sleeps, tc.want)
			}
		})
	}
}

func TestBackoff(t *testing.T) {
	for n, ceiling := range []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second, 8 * time.Second} {
		if got := backoff(n, func() float64 { return 0 }); got != ceiling/2 {
			t.Errorf("backoff(%d) low = %v, want %v", n, got, ceiling/2)
		}
		if got := backoff(n, func() float64 { return 1 }); got != ceiling {
			t.Errorf("backoff(%d) high = %v, want %v", n, got, ceiling)
		}
	}
	if got := backoff(70, func() float64 { return 1 }); got != 8*time.Second {
		t.Errorf("backoff(70) = %v", got)
	}
	for _, r := range []float64{0, 0.999} {
		got := wait(0, 3*time.Second, true, func() float64 { return r })
		if got < 3*time.Second || got >= 4*time.Second {
			t.Errorf("jitter with %v: %v", r, got)
		}
	}
}

func TestNeverRetried(t *testing.T) {
	cases := map[string]func() *http.Response{
		"409":                           answer(409, `{"error":{"code":"idempotency_conflict","message":"x"}}`),
		"400":                           answer(400, `{"error":{"code":"bad","message":"x"}}`),
		"401":                           answer(401, `{"error":{"code":"unauthorized","message":"x"}}`),
		"403":                           answer(403, `{"error":{"code":"forbidden","message":"x"}}`),
		"404":                           answer(404, `{"error":{"code":"not_found","message":"x"}}`),
		"422":                           answer(422, `{"error":{"code":"validation_failed","message":"x"}}`),
		"webhook_registration_disabled": answer(503, `{"error":{"code":"webhook_registration_disabled","message":"x"}}`),
		"redirect":                      answer(302, ""),
		"invalid body":                  answer(202, "nope"),
	}
	for name, failure := range cases {
		t.Run(name, func(t *testing.T) {
			s := &stub{respond: sequence(failure, answer(202, `{"id":"e1"}`))}
			cl, _ := newTestClient(t, s)
			if _, err := cl.Emails.Send(context.Background(), &CreateEmailRequest{}); err == nil || s.count() != 1 {
				t.Fatalf("%v after %d requests, want no retry", err, s.count())
			}
		})
	}
}

func TestDeadlineAndRetryLimits(t *testing.T) {
	ctx := context.Background()
	t.Run("a wait past the deadline is not started", func(t *testing.T) {
		s := &stub{respond: always(429, `{"error":{"code":"rate_limited","message":"x"}}`, "Retry-After", "40")}
		cl, clk := newTestClient(t, s)
		_, err := cl.Emails.Get(ctx, "e1")
		e, ok := errors.AsType[*APIError](err)
		if !ok || e.RetryAfter != 40*time.Second || s.count() != 1 || len(clk.sleeps) != 0 {
			t.Fatalf("%v after %d requests, waits %v", err, s.count(), clk.sleeps)
		}
	})
	t.Run("at most maxRetries", func(t *testing.T) {
		for _, tc := range []struct {
			opts []RequestOption
			want int
		}{{nil, 3}, {[]RequestOption{WithMaxRetries(0)}, 1}, {[]RequestOption{WithMaxRetries(9)}, 6}} {
			s := &stub{respond: always(503, unavailable, "Retry-After", "1")}
			cl, _ := newTestClient(t, s, WithDefaultTimeout(time.Hour))
			_, err := cl.Emails.Get(ctx, "e1", tc.opts...)
			if err == nil || s.count() != tc.want {
				t.Fatalf("%v after %d requests, want %d", err, s.count(), tc.want)
			}
		}
	})
	t.Run("cancelled during a wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(ctx)
		s := &stub{respond: always(503, unavailable, "Retry-After", "1")}
		cl, clk := newTestClient(t, s)
		clk.onWait = cancel
		_, err := cl.Emails.Get(ctx, "e1")
		if te, ok := errors.AsType[*TransportError](err); !ok || te.Reason != ReasonAborted || s.count() != 1 {
			t.Fatalf("%v after %d requests", err, s.count())
		}
	})
	t.Run("already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		s := &stub{respond: always(200, `{"id":"e1"}`)}
		cl, _ := newTestClient(t, s)
		if _, err := cl.Emails.Get(ctx, "e1"); s.count() != 0 || err == nil {
			t.Fatalf("%v after %d requests", err, s.count())
		}
	})
	t.Run("a wait cut by the call's own deadline is a timeout", func(t *testing.T) {
		s := &stub{respond: always(503, unavailable, "Retry-After", "1")}
		cl, _ := newTestClient(t, s)
		cl.c.rt.sleep = realRuntime.sleep
		// The first reading fixes the deadline; later ones keep the wait
		// inside it, so only the real context timer can end the wait.
		start := time.Unix(1_800_000_000, 0)
		calls := 0
		cl.c.rt.now = func() time.Time {
			calls++
			if calls == 1 {
				return start
			}
			return start.Add(-time.Hour)
		}
		_, err := cl.Emails.Get(ctx, "e1", WithTimeout(50*time.Millisecond))
		if te, ok := errors.AsType[*TransportError](err); !ok || te.Reason != ReasonTimeout || s.count() != 1 {
			t.Fatalf("%v after %d requests", err, s.count())
		}
	})
}

func TestIdempotencyKeys(t *testing.T) {
	ctx := context.Background()
	t.Run("caller's key on every attempt, identical bodies", func(t *testing.T) {
		s := &stub{respond: sequence(answer(503, unavailable, "Retry-After", "1"), answer(502, ""), answer(202, `{"id":"e1"}`))}
		cl, _ := newTestClient(t, s)
		if _, err := cl.Emails.Send(ctx, &CreateEmailRequest{From: "a@example.com", To: "b@example.com"}, WithIdempotencyKey("order-7:receipt")); err != nil {
			t.Fatal(err)
		}
		if s.count() != 3 {
			t.Fatalf("%d requests", s.count())
		}
		for _, r := range s.reqs {
			if r.header.Get("Idempotency-Key") != "order-7:receipt" || r.body != s.reqs[0].body {
				t.Fatalf("attempt key %q body %q", r.header.Get("Idempotency-Key"), r.body)
			}
		}
	})
	t.Run("generated key per logical call", func(t *testing.T) {
		s := &stub{respond: sequence(answer(503, unavailable, "Retry-After", "1"), answer(202, `{"id":"e1"}`), answer(202, `{"id":"e2"}`))}
		cl, _ := newTestClient(t, s)
		for range 2 {
			if _, err := cl.ContactEvents.Create(ctx, &CreateContactEventRequest{}); err != nil {
				t.Fatal(err)
			}
		}
		keys := []string{s.reqs[0].header.Get("Idempotency-Key"), s.reqs[1].header.Get("Idempotency-Key"), s.reqs[2].header.Get("Idempotency-Key")}
		if keys[0] == "" || keys[0] != keys[1] || keys[2] == keys[0] {
			t.Fatalf("keys %v", keys)
		}
	})
	t.Run("real keys", func(t *testing.T) {
		a, b := realRuntime.newKey(), realRuntime.newKey()
		if len(a) != 32 || a == b || strings.Trim(a, "0123456789abcdef") != "" {
			t.Fatalf("keys %q %q", a, b)
		}
	})
	t.Run("invalid keys", func(t *testing.T) {
		s := &stub{respond: always(202, `{"id":"e1"}`)}
		cl, _ := newTestClient(t, s)
		for _, key := range []string{"", "has space", "tab\t", "ünicode", strings.Repeat("k", 256)} {
			_, err := cl.Emails.Send(ctx, &CreateEmailRequest{}, WithIdempotencyKey(key))
			if !errors.Is(err, ErrInvalidArgument) || (key != "" && strings.Contains(err.Error(), key)) {
				t.Errorf("%q: %v", key, err)
			}
		}
		if _, err := cl.Emails.Send(ctx, &CreateEmailRequest{}, WithIdempotencyKey(strings.Repeat("k", 255))); err != nil {
			t.Fatal(err)
		}
		if s.count() != 1 {
			t.Fatalf("%d requests", s.count())
		}
	})
}

func TestConcurrentUse(t *testing.T) {
	s := &stub{respond: always(200, `{"id":"e1"}`)}
	cl, _ := newTestClient(t, s)
	cl.c.rt = realRuntime
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := cl.Emails.Get(context.Background(), "e1"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if s.count() != 20 {
		t.Fatalf("%d requests", s.count())
	}
}
