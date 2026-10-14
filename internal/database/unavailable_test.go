package database

import (
	"errors"
	"fmt"
	"io"
	"net"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/puddle/v2"
)

func TestUnavailable(t *testing.T) {
	t.Parallel()
	yes := map[string]error{
		"connect error":        &pgconn.ConnectError{},
		"connection exception": &pgconn.PgError{Code: "08006"},
		"admin shutdown":       &pgconn.PgError{Code: "57P01"},
		"crash shutdown":       &pgconn.PgError{Code: "57P02"},
		"cannot connect now":   &pgconn.PgError{Code: "57P03"},
		"too many connections": &pgconn.PgError{Code: "53300"},
		"pool closed":          fmt.Errorf("acquire: %w", puddle.ErrClosedPool),
		"network error":        &net.OpError{Op: "read", Err: errors.New("connection reset by peer")},
		"unexpected EOF":       io.ErrUnexpectedEOF,
		"EOF":                  io.EOF,
		"wrapped":              fmt.Errorf("insert email: %w", &pgconn.PgError{Code: "57P01"}),
	}
	for name, err := range yes {
		if !Unavailable(err) {
			t.Errorf("%s: Unavailable = false, want true", name)
		}
	}
	no := map[string]error{
		"nil":              nil,
		"unique violation": &pgconn.PgError{Code: "23505"},
		"check violation":  &pgconn.PgError{Code: "23514"},
		"query cancelled":  &pgconn.PgError{Code: "57014"},
		"plain error":      errors.New("boom"),
	}
	for name, err := range no {
		if Unavailable(err) {
			t.Errorf("%s: Unavailable = true, want false", name)
		}
	}
}
