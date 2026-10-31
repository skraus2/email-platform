package emailplatform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTheKeyIsNeverPrinted(t *testing.T) {
	cl, _ := newTestClient(t, &stub{respond: always(200, "{}")})
	values := []any{cl, *cl}
	v := reflect.ValueOf(cl).Elem()
	for i := range v.NumField() {
		if v.Type().Field(i).IsExported() {
			values = append(values, v.Field(i).Interface(), v.Field(i).Elem().Interface())
		}
	}
	for _, value := range values {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%d", "%x", "%q"} {
			if out := fmt.Sprintf(format, value); strings.Contains(out, testKey) {
				t.Errorf("%s of %T shows the key: %s", format, value, out)
			}
		}
	}
	data, _ := json.Marshal(cl) //nolint:musttag // marshalling a client by accident must not reveal the key
	if bytes.Contains(data, []byte(testKey)) {
		t.Errorf("json: %s", data)
	}
	for _, h := range []func(*bytes.Buffer) slog.Handler{
		func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	} {
		var b bytes.Buffer
		slog.New(h(&b)).Info("client", "client", cl)
		if strings.Contains(b.String(), testKey) || !strings.Contains(b.String(), "api.example.com") {
			t.Errorf("slog: %s", b.String())
		}
	}
	if got := cl.String(); got != "emailplatform.Client(https://api.example.com)" {
		t.Errorf("String() = %q", got)
	}
}

func TestErrorsHoldNoSecrets(t *testing.T) {
	const (
		recipient = "private.person@example.org"
		secret    = "whsec_topsecretvalue"
	)
	answers := map[string]func(int, *http.Request) (*http.Response, error){
		"non-JSON 2xx":       always(200, secret),
		"non-envelope error": always(500, "<html>"+secret+"</html>"),
		"envelope":           always(404, `{"error":{"code":"not_found","message":"no such email"}}`),
		"redirect":           always(302, secret, "Location", "https://evil.example/"+secret),
		"network":            func(int, *http.Request) (*http.Response, error) { return nil, fmt.Errorf("refused") },
		"too large": func(int, *http.Request) (*http.Response, error) {
			return respond(200, strings.Repeat(secret, maxBodyBytes/len(secret)+1)), nil
		},
		"error with a cookie": always(500, "", "Set-Cookie", "s="+secret),
	}
	for name, a := range answers {
		t.Run(name, func(t *testing.T) {
			s := &stub{respond: a}
			cl, _ := newTestClient(t, s, WithDefaultMaxRetries(0))
			ctx := context.Background()
			_, err1 := cl.Emails.List(ctx, &ListEmailsParams{Recipient: new(recipient)})
			_, err2 := cl.Contacts.Create(ctx, &CreateContactRequest{Email: recipient, Attributes: new(ContactAttributes{"token": secret})})
			for _, err := range []error{err1, err2} {
				if err == nil {
					t.Fatal("no error")
				}
				out := fmt.Sprintf("%v | %+v | %#v | %s", err, err, err, err.Error())
				for _, needle := range []string{testKey, recipient, "private.person", secret} {
					if strings.Contains(out, needle) {
						t.Errorf("error shows %q: %s", needle, out)
					}
				}
			}
		})
	}
}

func TestEveryQueryParamsFieldIsEncoded(t *testing.T) {
	cl, _ := newTestClient(t, &stub{respond: always(200, "{}")})
	n := 0
	for i := range operations {
		op := &operations[i]
		if op.params == "" {
			continue
		}
		mt := methodOf(cl, op).Type()
		var typ reflect.Type
		for in := range mt.Ins() {
			if in.Kind() == reflect.Pointer && in.Elem().Name() == op.params {
				typ = in.Elem()
			}
		}
		if typ == nil {
			t.Fatalf("%s has no %s argument", op.id, op.params)
		}
		n++
		params := reflect.New(typ)
		for j := range typ.NumField() {
			fill(params.Elem().Field(j))
		}
		values, err := encodeQuery(params.Interface())
		if err != nil {
			t.Fatalf("%s: %v", op.params, err)
		}
		for field := range typ.Fields() {
			name, _, _ := strings.Cut(field.Tag.Get("form"), ",")
			if len(values[name]) == 0 {
				t.Errorf("%s.%s (%s) not encoded", op.params, field.Name, name)
			}
		}
	}
	if n != 9 {
		t.Fatalf("%d query parameter types, want 9 (8 lists and getUsage)", n)
	}
}

// fill sets a field to a non-zero value of its type.
func fill(f reflect.Value) {
	if f.Kind() == reflect.Pointer {
		f.Set(reflect.New(f.Type().Elem()))
		f = f.Elem()
	}
	switch {
	case f.Type() == timeType:
		f.Set(reflect.ValueOf(time.Unix(1_800_000_000, 0).UTC()))
	case f.Kind() == reflect.String:
		f.SetString("x")
	case f.Kind() == reflect.Int:
		f.SetInt(1)
	case f.Kind() == reflect.Bool:
		f.SetBool(true)
	case f.Kind() == reflect.Slice:
		f.Set(reflect.MakeSlice(f.Type(), 1, 1))
		fill(f.Index(0))
	}
}
