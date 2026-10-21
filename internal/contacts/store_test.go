package contacts

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestDatabaseRefusalsMap(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"23514", "22003", "22P02"} {
		var e *Error
		if err := rejected(fmt.Errorf("x: %w", &pgconn.PgError{Code: code})); !errors.As(err, &e) || e.Field != "attributes" {
			t.Errorf("%s: %v", code, err)
		}
	}
	other := errors.New("other")
	if err := rejected(other); !errors.Is(err, other) {
		t.Errorf("other error changed: %v", err)
	}
	if rejected(nil) != nil {
		t.Error("nil changed")
	}
	for _, code := range []string{"55P03", "57014"} {
		if err := busy(&pgconn.PgError{Code: code}); !errors.Is(err, ErrBusy) {
			t.Errorf("%s: %v", code, err)
		}
	}
}
