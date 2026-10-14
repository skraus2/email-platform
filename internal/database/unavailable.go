package database

import (
	"errors"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Unavailable reports whether err means the database could not be reached
// or went away (connection failure, server shutdown or restart), as opposed
// to an error in the statement itself. Callers answer such errors with
// "service unavailable, retry later".
func Unavailable(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// Class 08: connection exception; 57P01-57P03: admin shutdown,
		// crash shutdown, cannot connect now (starting up or recovering).
		return strings.HasPrefix(pgErr.Code, "08") ||
			pgErr.Code == "57P01" || pgErr.Code == "57P02" || pgErr.Code == "57P03"
	}
	var connectErr *pgconn.ConnectError
	var netErr net.Error
	return errors.As(err, &connectErr) || errors.As(err, &netErr) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		pgconn.SafeToRetry(err)
}
