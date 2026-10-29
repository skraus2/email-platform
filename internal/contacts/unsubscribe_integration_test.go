//go:build integration

package contacts_test

import (
	"context"
	"errors"
	"testing"

	"github.com/skraus2/email-platform/internal/contacts"
)

func TestUnsubscribeByToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, b := setup(t)
	s := contacts.NewStore(pool, 1000)
	c, err := s.Create(ctx, a, subscribed("a@example.test"))
	if err != nil {
		t.Fatal(err)
	}
	var tok string
	if err := pool.QueryRow(ctx, "SELECT unsubscribe_token FROM contacts WHERE id = $1", c.ID).Scan(&tok); err != nil {
		t.Fatal(err)
	}

	// Another project's key cannot use the token.
	if _, err := s.Unsubscribe(ctx, b, tok); !errors.Is(err, contacts.ErrNotFound) {
		t.Fatalf("foreign project: %v", err)
	}
	for _, bad := range []string{"", "short", tok[:42] + "?", tok + "x"} {
		if _, err := s.Unsubscribe(ctx, a, bad); !errors.Is(err, contacts.ErrNotFound) {
			t.Errorf("token %q: %v", bad, err)
		}
	}
	if got, _ := s.Get(ctx, a, c.ID); got.Status != contacts.StatusSubscribed {
		t.Fatal("refused reports changed the contact")
	}

	id, err := s.Unsubscribe(ctx, a, tok)
	if err != nil || id != c.ID {
		t.Fatalf("unsubscribe: %v %v", id, err)
	}
	first, _ := s.Get(ctx, a, c.ID)
	if first.Status != contacts.StatusUnsubscribed || !first.StatusChangedAt.After(c.StatusChangedAt) {
		t.Fatalf("after report: %+v", first)
	}
	// A repeat is a no-op with the same answer.
	id, err = s.Unsubscribe(ctx, a, tok)
	again, _ := s.Get(ctx, a, c.ID)
	if err != nil || id != c.ID || !again.StatusChangedAt.Equal(first.StatusChangedAt) || !again.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("repeat: %v %v %+v", id, err, again)
	}
	// The token survives re-subscription.
	if _, err := s.Update(ctx, a, c.ID, contacts.UpdateInput{Status: new("subscribed"), ConsentSource: new("re-opt-in")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Unsubscribe(ctx, a, tok); err != nil {
		t.Fatalf("second unsubscribe: %v", err)
	}
}
