package emailplatform

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testKey  = "ep_test_SECRETKEY123"
	testBase = "https://api.example.com"
)

// recorded is what the stub saw of one request.
type recorded struct {
	method string
	path   string // escaped
	query  string
	header http.Header
	body   string
}

// stub is an http.RoundTripper answering from respond (n is the 0-based
// request number).
type stub struct {
	mu      sync.Mutex
	reqs    []recorded
	respond func(n int, r *http.Request) (*http.Response, error)
}

func (s *stub) RoundTrip(r *http.Request) (*http.Response, error) {
	var body string
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}
	s.mu.Lock()
	n := len(s.reqs)
	s.reqs = append(s.reqs, recorded{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Clone(), body})
	s.mu.Unlock()
	return s.respond(n, r)
}

func (s *stub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

func (s *stub) last() recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reqs[len(s.reqs)-1]
}

func respond(status int, body string, header ...string) *http.Response {
	h := http.Header{}
	for i := 0; i+1 < len(header); i += 2 {
		h.Set(header[i], header[i+1])
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), ContentLength: -1}
}

// always answers every request the same way.
func always(status int, body string, header ...string) func(int, *http.Request) (*http.Response, error) {
	return func(int, *http.Request) (*http.Response, error) { return respond(status, body, header...), nil }
}

// sequence answers request n with answers[n] (the last one repeats).
func sequence(answers ...func() *http.Response) func(int, *http.Request) (*http.Response, error) {
	return func(n int, _ *http.Request) (*http.Response, error) {
		return answers[min(n, len(answers)-1)](), nil
	}
}

func answer(status int, body string, header ...string) func() *http.Response {
	return func() *http.Response { return respond(status, body, header...) }
}

const unavailable = `{"error":{"code":"service_unavailable","message":"try again"}}`

// clock is a fake runtime: sleeping advances now.
type clock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
	keys   int
	random float64
	onWait func()
}

func (c *clock) runtime() runtime {
	return runtime{
		now: func() time.Time {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.t
		},
		sleep: func(ctx context.Context, d time.Duration) error {
			c.mu.Lock()
			c.sleeps = append(c.sleeps, d)
			c.t = c.t.Add(d)
			hook := c.onWait
			c.mu.Unlock()
			if hook != nil {
				hook()
			}
			return ctx.Err()
		},
		random: func() float64 { return c.random },
		newKey: func() string {
			c.mu.Lock()
			defer c.mu.Unlock()
			c.keys++
			return "generated-" + strconv.Itoa(c.keys)
		},
	}
}

func newTestClient(t *testing.T, s *stub, opts ...ClientOption) (*Client, *clock) {
	t.Helper()
	cl, err := New(testKey, testBase, append([]ClientOption{WithHTTPClient(&http.Client{Transport: s})}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Unix(1_800_000_000, 0), random: 0.5}
	cl.c.rt = clk.runtime()
	return cl, clk
}

// methods maps every operation to its client method ("Service.Method").
var methods = map[string]string{
	"createEmail":            "Emails.Send",
	"listEmails":             "Emails.List",
	"getEmail":               "Emails.Get",
	"listEmailEvents":        "Emails.ListEvents",
	"cancelEmail":            "Emails.Cancel",
	"createContact":          "Contacts.Create",
	"listContacts":           "Contacts.List",
	"getContact":             "Contacts.Get",
	"updateContact":          "Contacts.Update",
	"deleteContact":          "Contacts.Delete",
	"createAudience":         "Audiences.Create",
	"listAudiences":          "Audiences.List",
	"getAudience":            "Audiences.Get",
	"addAudienceContacts":    "Audiences.AddContacts",
	"listAudienceContacts":   "Audiences.ListContacts",
	"addAudienceContact":     "Audiences.AddContact",
	"removeAudienceContact":  "Audiences.RemoveContact",
	"reportUnsubscribe":      "Unsubscribes.Report",
	"createBroadcast":        "Broadcasts.Create",
	"listBroadcasts":         "Broadcasts.List",
	"getBroadcast":           "Broadcasts.Get",
	"scheduleBroadcast":      "Broadcasts.Schedule",
	"cancelBroadcast":        "Broadcasts.Cancel",
	"createAutomation":       "Automations.Create",
	"listAutomations":        "Automations.List",
	"getAutomation":          "Automations.Get",
	"updateAutomation":       "Automations.Update",
	"listAutomationRuns":     "Automations.ListRuns",
	"getAutomationRun":       "AutomationRuns.Get",
	"cancelAutomationRun":    "AutomationRuns.Cancel",
	"createContactEvent":     "ContactEvents.Create",
	"getUsage":               "Usage.Get",
	"createTemplate":         "Templates.Create",
	"getTemplate":            "Templates.Get",
	"publishTemplateVersion": "Templates.PublishVersion",
	"getTemplateVersion":     "Templates.GetVersion",
	"createWebhookEndpoint":  "WebhookEndpoints.Create",
}

func methodOf(cl *Client, op *operation) reflect.Value {
	service, name, _ := strings.Cut(methods[op.id], ".")
	return reflect.ValueOf(cl).Elem().FieldByName(service).MethodByName(name)
}

// placeholders lists the {name}s of a path template.
func placeholders(path string) []string {
	var names []string
	for rest := path; ; {
		start := strings.IndexByte(rest, '{')
		if start < 0 {
			return names
		}
		end := strings.IndexByte(rest, '}')
		names = append(names, rest[start+1:end])
		rest = rest[end+1:]
	}
}

// invocation calls an operation's method with generated arguments: path
// parameters "p1", "p2" (version 7), an empty body, no query parameters.
type invocation struct {
	ctx     context.Context
	nilBody bool
	opts    []RequestOption
}

func invoke(cl *Client, op *operation, inv invocation) error {
	m := methodOf(cl, op)
	mt := m.Type()
	ctx := inv.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	args := []reflect.Value{reflect.ValueOf(ctx)}
	n := 0
	for i := 1; i < mt.NumIn()-1; i++ {
		in := mt.In(i)
		switch {
		case in.Kind() == reflect.String:
			n++
			args = append(args, reflect.ValueOf("p"+strconv.Itoa(n)))
		case in.Kind() == reflect.Int:
			args = append(args, reflect.ValueOf(7))
		case in.Kind() == reflect.Pointer && strings.HasSuffix(in.Elem().Name(), "Params"):
			args = append(args, reflect.Zero(in))
		case in.Kind() == reflect.Pointer && inv.nilBody:
			args = append(args, reflect.Zero(in))
		default:
			args = append(args, reflect.New(in.Elem()))
		}
	}
	variadic := reflect.MakeSlice(mt.In(mt.NumIn()-1), 0, len(inv.opts))
	for _, o := range inv.opts {
		v := reflect.ValueOf(o)
		if variadic.Type().Elem().Kind() == reflect.Interface {
			v = v.Convert(variadic.Type().Elem())
		}
		variadic = reflect.Append(variadic, v)
	}
	out := m.CallSlice(append(args, variadic))
	err, _ := reflect.TypeAssert[error](out[len(out)-1])
	return err
}

// filledPath is the operation's path with invoke's arguments filled in.
func filledPath(op *operation) string {
	path := op.path
	n := 0
	for _, name := range placeholders(op.path) {
		value := "7"
		if name != "version" {
			n++
			value = "p" + strconv.Itoa(n)
		}
		path = strings.Replace(path, "{"+name+"}", value, 1)
	}
	return path
}

// successStatus is a status every operation accepts with the body "{}".
func successStatus(op *operation) int {
	if op.result == "" {
		return http.StatusNoContent
	}
	return http.StatusOK
}
