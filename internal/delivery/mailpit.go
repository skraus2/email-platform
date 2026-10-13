package delivery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"time"
)

// allowedMailpitHosts are the only SMTP targets the Mailpit adapter will talk
// to. A typo or copied production setting must never reach a real mail server.
var allowedMailpitHosts = map[string]bool{
	"localhost": true, "127.0.0.1": true, "::1": true,
	"mailpit": true, // Compose service name
}

// ValidateMailpitAddr checks that addr is host:port on an allowed local host.
func ValidateMailpitAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return errors.New("SMTP address must be host:port")
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return errors.New("SMTP port is invalid")
	}
	if !allowedMailpitHosts[host] {
		return fmt.Errorf("SMTP host %q is not a local Mailpit host (allowed: localhost, 127.0.0.1, ::1, mailpit)", host)
	}
	return nil
}

// Mailpit delivers to a local Mailpit instance over plain SMTP. Mailpit
// captures messages; it never delivers them, so results are "captured".
type Mailpit struct {
	addr string
	host string
}

// NewMailpit returns an adapter for addr, refusing non-local hosts.
func NewMailpit(addr string) (*Mailpit, error) {
	if err := ValidateMailpitAddr(addr); err != nil {
		return nil, err
	}
	host, _, _ := net.SplitHostPort(addr)
	return &Mailpit{addr: addr, host: host}, nil
}

// Send implements Provider.
func (m *Mailpit) Send(ctx context.Context, msg Message) (Result, error) {
	raw, err := buildMessage(msg, time.Now())
	if err != nil {
		return Result{}, &Error{Class: ClassInvalid}
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", m.addr)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, &Error{Class: ClassTimeout}
		}
		return Result{}, &Error{Class: ClassConnect}
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	// Unblock any pending read/write when ctx is cancelled without deadline.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	if err := m.converse(conn, msg, raw); err != nil {
		return Result{}, classify(ctx, err)
	}
	return Result{
		Provider:   "mailpit",
		ProviderID: "<" + msg.ID.String() + "@" + messageIDDomain + ">",
		Status:     StatusCaptured,
	}, nil
}

func (m *Mailpit) converse(conn net.Conn, msg Message, raw []byte) error {
	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello(messageIDDomain); err != nil {
		return err
	}
	if err := c.Mail(msg.From); err != nil {
		return err
	}
	if err := c.Rcpt(msg.To); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// classify maps an SMTP conversation error to a sanitized Error, dropping
// the server's text (it may contain addresses).
func classify(ctx context.Context, err error) *Error {
	var tpErr *textproto.Error
	if errors.As(err, &tpErr) {
		return &Error{Class: ClassRejected, Code: tpErr.Code}
	}
	if ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded) {
		return &Error{Class: ClassTimeout}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{Class: ClassTimeout}
	}
	return &Error{Class: ClassProtocol}
}
