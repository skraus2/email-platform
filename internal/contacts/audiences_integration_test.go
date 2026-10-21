//go:build integration

package contacts_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/skraus2/email-platform/internal/contacts"
)

func TestCreateAndGetAudience(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, b := setup(t)
	s := contacts.NewAudiences(pool, 3, 100)

	au, err := s.Create(ctx, a, "Newsletter")
	if err != nil || au.Name != "Newsletter" || au.ProjectID != a {
		t.Fatalf("create: %+v %v", au, err)
	}
	if _, err := s.Create(ctx, a, "newsLETTER"); !errors.Is(err, contacts.ErrAudienceExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := s.Create(ctx, b, "Newsletter"); err != nil {
		t.Fatalf("same name elsewhere: %v", err)
	}
	for _, bad := range []string{"", strings.Repeat("n", 201), "a\nb", "rtl\u202e"} {
		var e *contacts.Error
		if _, err := s.Create(ctx, a, bad); !errors.As(err, &e) || e.Field != "name" {
			t.Errorf("name %q: %v", bad, err)
		}
	}
	got, n, err := s.Get(ctx, a, au.ID)
	if err != nil || got.ID != au.ID || n != 0 {
		t.Fatalf("get: %+v %d %v", got, n, err)
	}
	if _, _, err := s.Get(ctx, b, au.ID); !errors.Is(err, contacts.ErrNotFound) {
		t.Fatalf("foreign get: %v", err)
	}
	// Cap per project, concurrent creates at limit-1.
	if _, err := s.Create(ctx, a, "second"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() { _, errs[i] = s.Create(ctx, a, "p"+string(rune('a'+i))) })
	}
	wg.Wait()
	created := 0
	for _, err := range errs {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, contacts.ErrAudienceLimit):
			t.Errorf("unexpected: %v", err)
		}
	}
	if created != 1 {
		t.Fatalf("created %d at the cap", created)
	}

	page, more, err := s.List(ctx, a, uuid.Nil, 2)
	if err != nil || !more || len(page) != 2 || page[0].Name == "Newsletter" {
		t.Fatalf("list page 1: %+v %v %v", page, more, err)
	}
	rest, more, err := s.List(ctx, a, page[1].ID, 2)
	if err != nil || more || len(rest) != 1 || rest[0].ID != au.ID {
		t.Fatalf("list page 2: %+v %v %v", rest, more, err)
	}
}

func TestAudienceMembership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, b := setup(t)
	cs := contacts.NewStore(pool, 1000)
	s := contacts.NewAudiences(pool, 10, 5)
	au, err := s.Create(ctx, a, "list")
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for i := range 6 {
		c, err := cs.Create(ctx, a, subscribed("m"+string(rune('0'+i))+"@example.test"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	foreign, err := cs.Create(ctx, b, subscribed("f@example.test"))
	if err != nil {
		t.Fatal(err)
	}

	// Bulk input: 1–1000 distinct ids.
	tooMany := make([]uuid.UUID, contacts.MaxAddBatch+1)
	for i := range tooMany {
		tooMany[i] = uuid.New()
	}
	for name, in := range map[string][]uuid.UUID{"none": nil, "too many": tooMany, "repeated": {ids[0], ids[0]}} {
		var e *contacts.Error
		if _, err := s.Add(ctx, a, au.ID, in); !errors.As(err, &e) || e.Field != "contact_ids" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if res, err := s.Add(ctx, a, au.ID, tooMany[:contacts.MaxAddBatch]); err != nil || res.NotFound != contacts.MaxAddBatch {
		t.Fatalf("1000 unknown ids: %+v %v", res, err)
	}
	// Single add is idempotent.
	for range 2 {
		if err := s.AddOne(ctx, a, au.ID, ids[0]); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddOne(ctx, a, au.ID, foreign.ID); !errors.Is(err, contacts.ErrNotFound) {
		t.Fatalf("foreign contact: %v", err)
	}
	if err := s.AddOne(ctx, b, au.ID, foreign.ID); !errors.Is(err, contacts.ErrNotFound) {
		t.Fatalf("foreign audience: %v", err)
	}

	res, err := s.Add(ctx, a, au.ID, []uuid.UUID{ids[0], ids[1], ids[2], foreign.ID, uuid.New()})
	if err != nil || res != (contacts.AddResult{Added: 2, AlreadyMembers: 1, NotFound: 2}) {
		t.Fatalf("bulk add: %+v %v", res, err)
	}
	// Cap 5: 3 members, adding 3 new ones would make 6 → nothing added.
	if _, err := s.Add(ctx, a, au.ID, ids[3:6]); !errors.Is(err, contacts.ErrAudienceFull) {
		t.Fatalf("over the cap: %v", err)
	}
	if _, n, _ := s.Get(ctx, a, au.ID); n != 3 {
		t.Fatalf("members after refused add: %d", n)
	}
	// Re-adding members never counts against the cap.
	if res, err := s.Add(ctx, a, au.ID, append(ids[0:3], ids[3], ids[4])); err != nil || res.Added != 2 || res.AlreadyMembers != 3 {
		t.Fatalf("fill to cap: %+v %v", res, err)
	}
	if err := s.AddOne(ctx, a, au.ID, ids[5]); !errors.Is(err, contacts.ErrAudienceFull) {
		t.Fatalf("single over cap: %v", err)
	}
	if err := s.AddOne(ctx, a, au.ID, ids[4]); err != nil {
		t.Fatalf("existing member at cap: %v", err)
	}

	// Listing by contact id.
	var seen []uuid.UUID
	after := uuid.Nil
	for {
		page, more, err := s.Members(ctx, a, au.ID, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range page {
			seen = append(seen, c.ID)
		}
		if !more {
			break
		}
		after = page[len(page)-1].ID
	}
	if len(seen) != 5 || seen[0] != ids[0] || seen[4] != ids[4] {
		t.Fatalf("members %v", seen)
	}
	if _, _, err := s.Members(ctx, b, au.ID, uuid.Nil, 10); !errors.Is(err, contacts.ErrNotFound) {
		t.Fatalf("foreign members: %v", err)
	}

	// Remove: idempotent, foreign ids are 404.
	if err := s.Remove(ctx, a, au.ID, ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, a, au.ID, ids[0]); err != nil {
		t.Fatalf("second remove: %v", err)
	}
	if err := s.Remove(ctx, a, au.ID, foreign.ID); !errors.Is(err, contacts.ErrNotFound) {
		t.Fatalf("remove foreign contact: %v", err)
	}
	if err := s.Remove(ctx, b, au.ID, foreign.ID); !errors.Is(err, contacts.ErrNotFound) {
		t.Fatalf("remove from foreign audience: %v", err)
	}
	// Deleting a contact removes its memberships.
	if err := cs.Delete(ctx, a, ids[1]); err != nil {
		t.Fatal(err)
	}
	if _, n, _ := s.Get(ctx, a, au.ID); n != 3 {
		t.Fatalf("members after contact delete: %d", n)
	}
	// Composite keys: a membership across projects is impossible.
	_, err = pool.Exec(ctx, "INSERT INTO audience_members (project_id, audience_id, contact_id) VALUES ($1, $2, $3)", a, au.ID, foreign.ID)
	if pgErr := (*pgconn.PgError)(nil); !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("cross-project membership: %v, want a foreign key violation", err)
	}
}

func TestAudienceCapConcurrent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, _ := setup(t)
	cs := contacts.NewStore(pool, 1000)
	s := contacts.NewAudiences(pool, 10, 4)
	au, err := s.Create(ctx, a, "list")
	if err != nil {
		t.Fatal(err)
	}
	var ids []uuid.UUID
	for i := range 10 {
		c, err := cs.Create(ctx, a, subscribed("c"+string(rune('a'+i))+"@example.test"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(ids))
	for i := range ids {
		wg.Go(func() { errs[i] = s.AddOne(ctx, a, au.ID, ids[i]) })
	}
	wg.Wait()
	ok, full := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, contacts.ErrAudienceFull):
			full++
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if ok != 4 || full != 6 {
		t.Fatalf("%d added, %d full; want 4 and 6", ok, full)
	}
	if _, n, err := s.Get(ctx, a, au.ID); err != nil || n != 4 {
		t.Fatalf("members after concurrent adds: %d %v", n, err)
	}
}
