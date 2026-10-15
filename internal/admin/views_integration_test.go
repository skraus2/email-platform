//go:build integration

package admin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/admin"
	"github.com/skraus2/email-platform/internal/testdb"
)

func TestSearchesAreBounded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	v := admin.NewViews(testdb.NewMigrated(t))
	release := v.HoldSearchSlots()
	if _, _, err := v.SearchEmails(ctx, admin.EmailFilter{}, 10); !errors.Is(err, admin.ErrViewBusy) {
		t.Fatalf("all search slots taken: %v", err)
	}
	if _, _, err := v.EmailDeliveries(ctx, uuid.New(), uuid.Nil, 10); !errors.Is(err, admin.ErrNotFound) {
		t.Fatalf("deliveries of an unknown email: %v", err)
	}
	release()
	if _, _, err := v.SearchEmails(ctx, admin.EmailFilter{}, 10); err != nil {
		t.Fatalf("after release: %v", err)
	}
}
