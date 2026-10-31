package emailplatform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestEveryOperationHasAMethodWithTheContractsTypes(t *testing.T) {
	cl, _ := newTestClient(t, &stub{respond: always(200, "{}")})
	if len(methods) != len(operations) {
		t.Fatalf("%d methods for %d operations", len(methods), len(operations))
	}
	seen := map[string]bool{}
	ctxType := reflect.TypeFor[context.Context]()
	errType := reflect.TypeFor[error]()
	for i := range operations {
		op := &operations[i]
		t.Run(op.id, func(t *testing.T) {
			name, ok := methods[op.id]
			if !ok || seen[name] {
				t.Fatalf("no method, or %q is mapped twice", name)
			}
			seen[name] = true
			m := methodOf(cl, op)
			if !m.IsValid() {
				t.Fatalf("%s does not exist", name)
			}
			mt := m.Type()
			want := []reflect.Type{ctxType}
			for _, p := range placeholders(op.path) {
				if p == "version" {
					want = append(want, reflect.TypeFor[int]())
				} else {
					want = append(want, reflect.TypeFor[string]())
				}
			}
			var named []string
			if op.body != "" {
				named = append(named, op.body)
			}
			if op.params != "" {
				named = append(named, op.params)
			}
			got := make([]reflect.Type, 0, mt.NumIn())
			for i := range mt.NumIn() - 1 {
				got = append(got, mt.In(i))
			}
			if len(got) != len(want)+len(named) {
				t.Fatalf("%s takes %v, want %v plus pointers to %v", name, got, want, named)
			}
			for i, w := range want {
				if got[i] != w {
					t.Errorf("argument %d is %v, want %v", i, got[i], w)
				}
			}
			for i, n := range named {
				g := got[len(want)+i]
				if g.Kind() != reflect.Pointer || g.Elem().Name() != n || g.Elem().PkgPath() != reflect.TypeFor[Client]().PkgPath() {
					t.Errorf("argument %d is %v, want *%s", len(want)+i, g, n)
				}
			}
			option := reflect.TypeFor[RequestOption]()
			if keyed(op) {
				option = reflect.TypeFor[IdempotentOption]()
			}
			if !mt.IsVariadic() || mt.In(mt.NumIn()-1).Elem() != option {
				t.Errorf("options are %v, want ...%v", mt.In(mt.NumIn()-1), option)
			}
			if op.result == "" {
				if mt.NumOut() != 1 || mt.Out(0) != errType {
					t.Errorf("returns %v, want error only", mt)
				}
				return
			}
			if mt.NumOut() != 2 || mt.Out(1) != errType || mt.Out(0).Kind() != reflect.Pointer || mt.Out(0).Elem().Name() != op.result {
				t.Errorf("returns %v, want (*%s, error)", mt, op.result)
			}
		})
	}
}

func TestEveryMethodSendsItsOperation(t *testing.T) {
	for i := range operations {
		op := &operations[i]
		t.Run(op.id, func(t *testing.T) {
			s := &stub{respond: always(successStatus(op), "{}")}
			cl, _ := newTestClient(t, s)
			if err := invoke(cl, op, invocation{}); err != nil {
				t.Fatal(err)
			}
			if s.count() != 1 {
				t.Fatalf("%d requests, want 1", s.count())
			}
			r := s.last()
			if r.method != op.method || r.path != filledPath(op) || r.query != "" {
				t.Errorf("sent %s %s?%s, want %s %s", r.method, r.path, r.query, op.method, filledPath(op))
			}
			if op.body == "" {
				if r.body != "" || r.header.Get("Content-Type") != "" {
					t.Errorf("sent a body %q (%s)", r.body, r.header.Get("Content-Type"))
				}
				return
			}
			// invoke passes a new zero value of the body type.
			mt := methodOf(cl, op).Type()
			var bodyType reflect.Type
			for in := range mt.Ins() {
				if in.Kind() == reflect.Pointer && in.Elem().Name() == op.body {
					bodyType = in.Elem()
				}
			}
			want, err := json.Marshal(reflect.New(bodyType).Interface())
			if err != nil {
				t.Fatal(err)
			}
			if r.header.Get("Content-Type") != "application/json" || r.body != string(want) {
				t.Errorf("body %q (%s), want %s", r.body, r.header.Get("Content-Type"), want)
			}
		})
	}
}

func TestBodiesAreSentAsMarshalled(t *testing.T) {
	s := &stub{respond: always(202, `{"id":"e1","status":"queued"}`)}
	cl, _ := newTestClient(t, s)
	subject := "Hi"
	req := &CreateEmailRequest{From: "app@example.com", To: "user@example.com", Subject: &subject, TemplateVersion: new(TemplateVersionPublished())}
	if _, err := cl.Emails.Send(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.last().body; got != string(want) {
		t.Fatalf("body %s, want %s", got, want)
	}
	if !strings.Contains(string(want), `"template_version":"published"`) {
		t.Fatalf("wire names lost: %s", want)
	}
}

func TestResultsAreDecoded(t *testing.T) {
	s := &stub{respond: always(200, `{"data":[{"id":"e1","to":"u@example.com","status":"sent"}],"next_cursor":"c2"}`)}
	cl, _ := newTestClient(t, s)
	page, err := cl.Emails.List(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Data) != 1 || page.Data[0].Id != "e1" || page.Data[0].Status != "sent" || page.NextCursor == nil || *page.NextCursor != "c2" {
		t.Fatalf("decoded %+v", page)
	}
}

func TestBodies(t *testing.T) {
	s := &stub{respond: always(200, `{"id":"b1"}`)}
	cl, _ := newTestClient(t, s)
	ctx := context.Background()

	// Optional body: nil sends no body at all (schedule now).
	if _, err := cl.Broadcasts.Schedule(ctx, "b1", nil); err != nil {
		t.Fatal(err)
	}
	if r := s.last(); r.body != "" || r.header.Get("Content-Type") != "" {
		t.Fatalf("nil optional body sent %q (%s)", r.body, r.header.Get("Content-Type"))
	}
	if _, err := cl.Broadcasts.Schedule(ctx, "b1", &ScheduleBroadcastRequest{}); err != nil {
		t.Fatal(err)
	}
	if r := s.last(); r.body != "{}" {
		t.Fatalf("empty optional body sent %q", r.body)
	}

	// Required bodies: nil is an argument error, nothing is sent.
	before := s.count()
	for i := range operations {
		op := &operations[i]
		if op.bodyMode != "required" {
			continue
		}
		err := invoke(cl, op, invocation{nilBody: true})
		if !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%s with a nil body: %v", op.id, err)
		}
	}
	if s.count() != before {
		t.Fatalf("%d requests sent for nil bodies", s.count()-before)
	}
}

func TestPerCallOptionsReachEveryMethod(t *testing.T) {
	for i := range operations {
		op := &operations[i]
		t.Run(op.id, func(t *testing.T) {
			s := &stub{respond: always(503, unavailable, "Retry-After", "2")}
			cl, _ := newTestClient(t, s)
			err := invoke(cl, op, invocation{opts: []RequestOption{WithMaxRetries(0)}})
			if _, ok := errors.AsType[*APIError](err); !ok || s.count() != 1 {
				t.Fatalf("WithMaxRetries(0): %v after %d requests", err, s.count())
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err = invoke(cl, op, invocation{ctx: ctx})
			if te, ok := errors.AsType[*TransportError](err); !ok || te.Reason != ReasonAborted || s.count() != 1 {
				t.Fatalf("done context: %v after %d requests", err, s.count())
			}
			err = invoke(cl, op, invocation{opts: []RequestOption{WithTimeout(0)}})
			if !errors.Is(err, ErrInvalidArgument) || s.count() != 1 {
				t.Fatalf("WithTimeout(0): %v after %d requests", err, s.count())
			}
		})
	}
}

func TestRetriesFollowTheOperationAllowlist(t *testing.T) {
	var eligible []string
	for i := range operations {
		op := &operations[i]
		if retryable(op) {
			eligible = append(eligible, op.id)
		}
		failures := map[string]func() *http.Response{
			"503 service_unavailable": answer(503, unavailable, "Retry-After", "2"),
			"503 without a body":      answer(503, "<html>"),
			"500":                     answer(500, `{"error":{"code":"internal_error","message":"x"}}`),
			"502":                     answer(502, ""),
			"504":                     answer(504, ""),
		}
		for name, failure := range failures {
			t.Run(op.id+" "+name, func(t *testing.T) {
				s := &stub{respond: sequence(failure, answer(successStatus(op), "{}"))}
				cl, _ := newTestClient(t, s)
				err := invoke(cl, op, invocation{})
				want := 1
				if retryable(op) {
					want = 2
				}
				if s.count() != want || (want == 2) != (err == nil) {
					t.Fatalf("%d requests (err %v), want %d", s.count(), err, want)
				}
			})
		}
		t.Run(op.id+" network", func(t *testing.T) {
			s := &stub{respond: func(n int, _ *http.Request) (*http.Response, error) {
				if n == 0 {
					return nil, errors.New("connection reset")
				}
				return respond(successStatus(op), "{}"), nil
			}}
			cl, _ := newTestClient(t, s)
			err := invoke(cl, op, invocation{})
			want := 1
			if retryable(op) {
				want = 2
			}
			if s.count() != want || (want == 2) != (err == nil) {
				t.Fatalf("%d requests (err %v), want %d", s.count(), err, want)
			}
		})
		t.Run(op.id+" 429", func(t *testing.T) {
			s := &stub{respond: sequence(answer(429, `{"error":{"code":"rate_limited","message":"slow"}}`, "Retry-After", "1"), answer(successStatus(op), "{}"))}
			cl, _ := newTestClient(t, s)
			if err := invoke(cl, op, invocation{}); err != nil || s.count() != 2 {
				t.Fatalf("429: %v after %d requests, want a retry", err, s.count())
			}
		})
	}
	slices.Sort(eligible)
	want := []string{"createContactEvent", "createEmail", "getAudience", "getAutomation", "getAutomationRun", "getBroadcast", "getContact", "getEmail", "getTemplate", "getTemplateVersion", "getUsage", "listAudienceContacts", "listAudiences", "listAutomationRuns", "listAutomations", "listBroadcasts", "listContacts", "listEmailEvents", "listEmails"}
	if !slices.Equal(eligible, want) {
		t.Fatalf("eligible %v, want %v", eligible, want)
	}
}

func TestListAll(t *testing.T) {
	pages := map[string]string{
		"":   `{"data":[{"id":"e1"},{"id":"e2"}],"next_cursor":"c2"}`,
		"c2": `{"data":[{"id":"e3"}],"next_cursor":null}`,
	}
	s := &stub{respond: func(_ int, r *http.Request) (*http.Response, error) {
		return respond(200, pages[r.URL.Query().Get("cursor")]), nil
	}}
	cl, _ := newTestClient(t, s)
	ctx := context.Background()
	stale := "stale"
	params := &ListEmailsParams{Recipient: new("user@example.com"), Cursor: &stale}
	seq := cl.Emails.ListAll(ctx, params, WithMaxRetries(0))
	if s.count() != 0 {
		t.Fatal("requested before iterating")
	}
	collect := func() []string {
		var ids []string
		for e, err := range seq {
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, e.Id)
		}
		return ids
	}
	if got := collect(); !slices.Equal(got, []string{"e1", "e2", "e3"}) {
		t.Fatalf("items %v", got)
	}
	if s.count() != 2 {
		t.Fatalf("%d requests, want 2", s.count())
	}
	for _, r := range s.reqs {
		if !strings.Contains(r.query, "recipient=user%40example.com") || strings.Contains(r.query, "stale") {
			t.Errorf("page query %q", r.query)
		}
	}
	if *params.Cursor != "stale" || params.Recipient == nil {
		t.Fatal("params modified")
	}
	// Every range starts again at the first page.
	if got := collect(); len(got) != 3 || s.count() != 4 {
		t.Fatalf("second range: %v after %d requests", got, s.count())
	}
	// Breaking after the first item sends exactly one request.
	before := s.count()
	for _, err := range seq {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if s.count() != before+1 {
		t.Fatalf("break sent %d requests", s.count()-before)
	}
}

func TestListAllErrors(t *testing.T) {
	cases := map[string]struct {
		pages []string
		want  error
	}{
		"repeated cursor": {[]string{`{"data":[{"id":"a"}],"next_cursor":"c"}`, `{"data":[],"next_cursor":"c"}`}, errRepeatedCursor},
		"empty cursor":    {[]string{`{"data":[],"next_cursor":""}`}, errEmptyCursor},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var answers []func() *http.Response
			for _, p := range tc.pages {
				answers = append(answers, answer(200, p))
			}
			s := &stub{respond: sequence(answers...)}
			cl, _ := newTestClient(t, s)
			var errs []error
			for _, err := range cl.Contacts.ListAll(context.Background(), nil) {
				if err != nil {
					errs = append(errs, err)
				}
			}
			if len(errs) != 1 || !errors.Is(errs[0], tc.want) {
				t.Fatalf("errors %v, want one %v", errs, tc.want)
			}
		})
	}
	t.Run("page error", func(t *testing.T) {
		s := &stub{respond: always(404, `{"error":{"code":"not_found","message":"no"}}`)}
		cl, _ := newTestClient(t, s)
		n := 0
		for item, err := range cl.Automations.ListAllRuns(context.Background(), "a1", nil) {
			n++
			if _, ok := errors.AsType[*APIError](err); !ok || item.Id != "" {
				t.Fatalf("yielded %v, %v", item, err)
			}
		}
		if n != 1 || s.count() != 1 {
			t.Fatalf("%d yields after %d requests", n, s.count())
		}
	})
}

func TestEveryListHasAnIterator(t *testing.T) {
	all := map[string]string{
		"listEmails": "Emails.ListAll", "listEmailEvents": "Emails.ListAllEvents", "listContacts": "Contacts.ListAll",
		"listAudiences": "Audiences.ListAll", "listAudienceContacts": "Audiences.ListAllContacts",
		"listBroadcasts": "Broadcasts.ListAll", "listAutomations": "Automations.ListAll", "listAutomationRuns": "Automations.ListAllRuns",
	}
	n := 0
	for i := range operations {
		op := &operations[i]
		if !strings.HasSuffix(op.result, "List") {
			continue
		}
		n++
		t.Run(all[op.id], func(t *testing.T) {
			// Two pages; a stale cursor and a filter in the caller's params.
			s := &stub{respond: func(_ int, r *http.Request) (*http.Response, error) {
				if r.URL.Query().Get("cursor") == "c2" {
					return respond(200, `{"data":[{"id":"i3"}],"next_cursor":null}`), nil
				}
				return respond(200, `{"data":[{"id":"i1"},{"id":"i2"}],"next_cursor":"c2"}`), nil
			}}
			cl, _ := newTestClient(t, s)
			service, name, ok := strings.Cut(all[op.id], ".")
			m := reflect.ValueOf(cl).Elem().FieldByName(service).MethodByName(name)
			if !ok || !m.IsValid() {
				t.Fatalf("%s has no iterator", op.id)
			}
			mt := m.Type()
			args := []reflect.Value{reflect.ValueOf(context.Background())}
			for range placeholders(op.path) {
				args = append(args, reflect.ValueOf("p1"))
			}
			params := reflect.New(mt.In(len(args)).Elem())
			params.Elem().FieldByName("Cursor").Set(reflect.ValueOf(new("stale")))
			params.Elem().FieldByName("Limit").Set(reflect.ValueOf(new(2)))
			args = append(args, params)
			seq := m.Call(args)[0]
			var ids []string
			for item, err := range seq.Seq2() {
				if !err.IsNil() {
					t.Fatal(err.Interface())
				}
				ids = append(ids, item.FieldByName("Id").String())
			}
			if !slices.Equal(ids, []string{"i1", "i2", "i3"}) || s.count() != 2 {
				t.Fatalf("items %v after %d requests", ids, s.count())
			}
			if q := s.reqs[1].query; q != "cursor=c2&limit=2" {
				t.Errorf("second page query %q", q)
			}
			if q := s.reqs[0].query; q != "limit=2" {
				t.Errorf("first page query %q", q)
			}
			if c := params.Elem().FieldByName("Cursor").Elem().String(); c != "stale" {
				t.Errorf("params modified: cursor %q", c)
			}
		})
	}
	if n != len(all) {
		t.Fatalf("%d list operations, %d iterators", n, len(all))
	}
}
