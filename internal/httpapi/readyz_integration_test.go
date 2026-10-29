//go:build integration

package httpapi_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skraus2/email-platform/internal/database"
	"github.com/skraus2/email-platform/internal/httpapi"
	"github.com/skraus2/email-platform/internal/testdb"
)

// TestReadyzAgainstRealDatabase exercises the API handler end to end over HTTP
// with the same database check wiring as cmd/api.
func TestReadyzAgainstRealDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := testdb.New(t)
	m, err := database.NewMigrator(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	api := httpapi.New(httpapi.Deps{
		Logger: slog.New(slog.DiscardHandler),
		Checks: []httpapi.Check{{
			Name: "database",
			Fn:   func(ctx context.Context) error { return database.Ready(ctx, pool, m) },
		}},
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	get := func(path string) int {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if code := get("/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("readyz before migrations = %d, want 503", code)
	}
	if code := get("/healthz"); code != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", code)
	}

	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if code := get("/readyz"); code != http.StatusOK {
		t.Fatalf("readyz after migrations = %d, want 200", code)
	}

	api.SetDraining()
	if code := get("/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("readyz while draining = %d, want 503", code)
	}
}
