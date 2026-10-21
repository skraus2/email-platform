//go:build integration

package contacts_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/contacts"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/testdb"
)

func setup(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	pool := testdb.NewMigrated(t)
	q := store.New(pool)
	a, err := auth.CreateProject(context.Background(), q, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := auth.CreateProject(context.Background(), q, "b")
	if err != nil {
		t.Fatal(err)
	}
	return pool, a.ID, b.ID
}

func ptr(s string) *string { return &s }

func subscribed(email string) contacts.CreateInput {
	return contacts.CreateInput{Email: email, Status: contacts.StatusSubscribed, ConsentSource: ptr("signup-form")}
}

func TestCreateAndGetContact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, b := setup(t)
	s := contacts.NewStore(pool, 1000)

	c, err := s.Create(ctx, a, contacts.CreateInput{
		Email: "Ada@Example.test", Status: contacts.StatusSubscribed, ConsentSource: ptr("signup-form"),
		Attributes: json.RawMessage(`{"name": "Ada", "vip": true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Email != "Ada@Example.test" || c.Status != "subscribed" || *c.ConsentSource != "signup-form" ||
		string(c.Attributes) != `{"name":"Ada","vip":true}` || c.ProjectID != a || c.StatusChangedAt.IsZero() {
		t.Fatalf("created %+v", c)
	}
	got, err := s.Get(ctx, a, c.ID)
	if err != nil || got.ID != c.ID || string(got.Attributes) != `{"name":"Ada","vip":true}` {
		t.Fatalf("get: %+v %v", got, err)
	}
	// Isolation: another project sees nothing.
	if _, err := s.Get(ctx, b, c.ID); !errors.Is(err, contacts.ErrNotFound) {
		t.Fatalf("foreign get: %v", err)
	}
	// Unique per project, case-insensitively; same address elsewhere is fine.
	if _, err := s.Create(ctx, a, subscribed("ada@example.TEST")); !errors.Is(err, contacts.ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := s.Create(ctx, b, subscribed("ada@example.test")); err != nil {
		t.Fatalf("same address in another project: %v", err)
	}
	// Tokens are unique per contact and never part of the contact view.
	var tokens []string
	rows, err := pool.Query(ctx, "SELECT unsubscribe_token FROM contacts")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var tok string
		if err := rows.Scan(&tok); err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, tok)
	}
	if rows.Err() != nil || len(tokens) != 2 || tokens[0] == tokens[1] || len(tokens[0]) != 43 {
		t.Fatalf("tokens %v %v", tokens, rows.Err())
	}
	if _, err := s.Create(ctx, uuid.New(), subscribed("x@example.test")); !errors.Is(err, contacts.ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
}

func TestCreateContactValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, _ := setup(t)
	s := contacts.NewStore(pool, 1000)
	cases := []struct {
		name  string
		in    contacts.CreateInput
		field string
	}{
		{"no address", contacts.CreateInput{Status: "subscribed", ConsentSource: ptr("x")}, "email"},
		{"display name", contacts.CreateInput{Email: "Ada <a@example.test>", Status: "unsubscribed"}, "email"},
		{"unicode", contacts.CreateInput{Email: "ü@example.test", Status: "unsubscribed"}, "email"},
		{"no status", contacts.CreateInput{Email: "a@example.test"}, "status"},
		{"bad status", contacts.CreateInput{Email: "a@example.test", Status: "pending"}, "status"},
		{"subscribed without source", contacts.CreateInput{Email: "a@example.test", Status: "subscribed"}, "consent_source"},
		{"bad source", contacts.CreateInput{Email: "a@example.test", Status: "subscribed", ConsentSource: ptr("")}, "consent_source"},
		{"bad attributes", contacts.CreateInput{Email: "a@example.test", Status: "unsubscribed", Attributes: json.RawMessage(`{"A":1}`)}, "attributes"},
	}
	for _, c := range cases {
		_, err := s.Create(ctx, a, c.in)
		var e *contacts.Error
		if !errors.As(err, &e) || e.Field != c.field {
			t.Errorf("%s: %v (want field %s)", c.name, err, c.field)
		}
	}
	// Numbers are stored written out; what passes validation stays small.
	nums := make([]string, 20)
	for i := range nums {
		nums[i] = `"n` + string(rune('a'+i)) + `": 1.` + strings.Repeat("9", 40) + `e-20`
	}
	c, err := s.Create(ctx, a, contacts.CreateInput{Email: "n@example.test", Status: "unsubscribed", Attributes: json.RawMessage(`{` + strings.Join(nums, ",") + `}`)})
	if err != nil {
		t.Fatal(err)
	}
	var stored int
	if err := pool.QueryRow(ctx, "SELECT octet_length(attributes::text) FROM contacts WHERE id = $1", c.ID).Scan(&stored); err != nil || stored > contacts.MaxAttributesSize {
		t.Fatalf("stored attributes take %d bytes (%v)", stored, err)
	}
	if err := s.Delete(ctx, a, c.ID); err != nil {
		t.Fatal(err)
	}
	// Values at the bound survive a read and write back unchanged.
	edge, err := s.Create(ctx, a, contacts.CreateInput{Email: "e@example.test", Status: "unsubscribed",
		Attributes: json.RawMessage(`{"a":1e63,"b":1e-63,"c":-1e63,"d":0e-63}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, a, edge.ID, contacts.UpdateInput{Attributes: edge.Attributes}); err != nil {
		t.Fatalf("round trip of %s: %v", edge.Attributes, err)
	}
	// Unsubscribed contacts need no source.
	if _, err := s.Create(ctx, a, contacts.CreateInput{Email: "u@example.test", Status: "unsubscribed"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM contacts").Scan(&n); err != nil || n != 2 {
		t.Fatalf("stored %d contacts, %v", n, err)
	}
}

func TestContactCapConcurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, b := setup(t)
	s := contacts.NewStore(pool, 5)
	for i := range 4 {
		if _, err := s.Create(ctx, a, subscribed("c"+string(rune('0'+i))+"@example.test")); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range errs {
		wg.Go(func() {
			_, errs[i] = s.Create(ctx, a, subscribed("p"+string(rune('a'+i))+"@example.test"))
		})
	}
	wg.Wait()
	created := 0
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, contacts.ErrLimit):
			t.Errorf("unexpected: %v", err)
		}
	}
	if created != 1 {
		t.Fatalf("created %d at the cap, want 1", created)
	}
	// The cap is per project.
	if _, err := s.Create(ctx, b, subscribed("x@example.test")); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateContact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, b := setup(t)
	s := contacts.NewStore(pool, 1000)
	c, err := s.Create(ctx, a, contacts.CreateInput{Email: "a@example.test", Status: "unsubscribed", Attributes: json.RawMessage(`{"x":1}`)})
	if err != nil {
		t.Fatal(err)
	}

	// Attributes only: status time unchanged.
	u, err := s.Update(ctx, a, c.ID, contacts.UpdateInput{Attributes: json.RawMessage(`{"y":"z"}`)})
	if err != nil || string(u.Attributes) != `{"y":"z"}` || !u.StatusChangedAt.Equal(c.StatusChangedAt) || u.Status != "unsubscribed" {
		t.Fatalf("attributes update: %+v %v", u, err)
	}
	// Subscribing needs a source in the same request.
	var e *contacts.Error
	if _, err := s.Update(ctx, a, c.ID, contacts.UpdateInput{Status: ptr("subscribed")}); !errors.As(err, &e) || e.Field != "consent_source" {
		t.Fatalf("subscribe without source: %v", err)
	}
	u, err = s.Update(ctx, a, c.ID, contacts.UpdateInput{Status: ptr("subscribed"), ConsentSource: ptr("double-opt-in")})
	if err != nil || u.Status != "subscribed" || *u.ConsentSource != "double-opt-in" || !u.StatusChangedAt.After(c.StatusChangedAt) {
		t.Fatalf("subscribe: %+v %v", u, err)
	}
	// Same status again: no status change time.
	again, err := s.Update(ctx, a, c.ID, contacts.UpdateInput{Status: ptr("subscribed"), ConsentSource: ptr("double-opt-in")})
	if err != nil || !again.StatusChangedAt.Equal(u.StatusChangedAt) {
		t.Fatalf("repeat: %+v %v", again, err)
	}
	if _, err := s.Update(ctx, a, c.ID, contacts.UpdateInput{}); !errors.As(err, &e) {
		t.Fatalf("empty update: %v", err)
	}
	if _, err := s.Update(ctx, a, c.ID, contacts.UpdateInput{Status: ptr("gone")}); !errors.As(err, &e) || e.Field != "status" {
		t.Fatalf("bad status: %v", err)
	}
	if _, err := s.Update(ctx, b, c.ID, contacts.UpdateInput{Status: ptr("unsubscribed")}); !errors.Is(err, contacts.ErrNotFound) {
		t.Fatalf("foreign update: %v", err)
	}
	got, _ := s.Get(ctx, a, c.ID)
	if got.Status != "subscribed" {
		t.Fatalf("foreign update changed the contact: %+v", got)
	}
}

func TestDeleteContact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, b := setup(t)
	s := contacts.NewStore(pool, 1000)
	c, err := s.Create(ctx, a, subscribed("a@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, b, c.ID); !errors.Is(err, contacts.ErrNotFound) {
		t.Fatalf("foreign delete: %v", err)
	}
	if err := s.Delete(ctx, a, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, a, c.ID); !errors.Is(err, contacts.ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.Delete(ctx, a, c.ID); !errors.Is(err, contacts.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	// Erased: the address can be added again (fresh consent).
	if _, err := s.Create(ctx, a, subscribed("A@example.test")); err != nil {
		t.Fatal(err)
	}
}

func TestListContacts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, b := setup(t)
	s := contacts.NewStore(pool, 1000)
	var ids []uuid.UUID
	for i := range 7 {
		in := subscribed("c" + string(rune('0'+i)) + "@example.test")
		if i%2 == 1 {
			in = contacts.CreateInput{Email: in.Email, Status: "unsubscribed"}
		}
		c, err := s.Create(ctx, a, in)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	if _, err := s.Create(ctx, b, subscribed("c0@example.test")); err != nil {
		t.Fatal(err)
	}

	var seen []uuid.UUID
	f := contacts.ListFilter{}
	for {
		page, more, err := s.List(ctx, a, f, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page {
			if c.ProjectID != a {
				t.Fatalf("foreign contact listed")
			}
			seen = append(seen, c.ID)
		}
		if !more {
			break
		}
		f.After = page[len(page)-1].ID
	}
	if len(seen) != 7 || seen[0] != ids[6] || seen[6] != ids[0] {
		t.Fatalf("paged %v, want newest first %v", seen, ids)
	}

	page, more, err := s.List(ctx, a, contacts.ListFilter{Status: "unsubscribed"}, 100)
	if err != nil || more || len(page) != 3 {
		t.Fatalf("status filter: %d %v %v", len(page), more, err)
	}
	page, _, err = s.List(ctx, a, contacts.ListFilter{Email: "C3@EXAMPLE.test"}, 100)
	if err != nil || len(page) != 1 || page[0].ID != ids[3] {
		t.Fatalf("email filter: %+v %v", page, err)
	}
	page, _, err = s.List(ctx, a, contacts.ListFilter{Email: "c3@example.test", Status: "subscribed"}, 100)
	if err != nil || len(page) != 0 {
		t.Fatalf("email+status filter: %+v %v", page, err)
	}
	// A cursor of another project only positions the scan.
	page, _, err = s.List(ctx, b, contacts.ListFilter{After: ids[6]}, 100)
	if err != nil || len(page) != 0 {
		t.Fatalf("foreign cursor: %d %v", len(page), err)
	}
	page, _, err = s.List(ctx, b, contacts.ListFilter{}, 100)
	if err != nil || len(page) != 1 {
		t.Fatalf("other project: %d %v", len(page), err)
	}
}
