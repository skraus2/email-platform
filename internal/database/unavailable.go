package database

import (
	"errors"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/puddle/v2"
)

// Unavailable reports whether err means the database could not be reached
// or went away (connection failure, server shutdown or restart), as opposed
// to an error in the statement itself. Callers answer such errors with
// "service unavailable, retry later".
func Unavailable(err error) bool {
	if err == nil {
		return false
	}
	// Any failure to establish a connection, whatever the server said
	// (e.g. too many connections right after a restart).
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) || errors.Is(err, puddle.ErrClosedPool) {
		return true
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		// Class 08: connection exception; 53300: too many connections;
		// 57P01-57P03: admin shutdown, crash shutdown, cannot connect now
		// (starting up or recovering).
		switch pgErr.Code {
		case "53300", "57P01", "57P02", "57P03":
			return true
		}
		return strings.HasPrefix(pgErr.Code, "08")
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		pgconn.SafeToRetry(err)
}
