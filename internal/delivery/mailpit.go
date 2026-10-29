package delivery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"os"
	"strconv"
	"strings"
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

// converse runs the SMTP dialogue. It reads the greeting itself and stops
// before MAIL FROM unless the server identifies as Mailpit: a real MTA
// listening on a local port (often relaying to the internet) must never get a
// message, whatever SMTP_ADDR says.
func (m *Mailpit) converse(conn net.Conn, msg Message, raw []byte) error {
	tp := textproto.NewConn(conn)
	defer func() { _ = tp.Close() }()

	_, greeting, err := tp.ReadResponse(220)
	if err != nil {
		return fmt.Errorf("read greeting: %w", err)
	}
	if !strings.Contains(greeting, mailpitBanner) {
		return &Error{Class: ClassUnsafeTarget}
	}
	steps := []struct {
		code int
		cmd  string
	}{
		{250, "EHLO " + messageIDDomain},
		{250, "MAIL FROM:<" + msg.From + ">"},
		{25, "RCPT TO:<" + msg.To + ">"}, // 250 or 251
		{354, "DATA"},
	}
	for _, s := range steps {
		if err := command(tp, s.code, s.cmd); err != nil {
			return err
		}
	}
	w := tp.DotWriter()
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("end message: %w", err)
	}
	// Any 2xx means the message was accepted.
	if _, _, err := tp.ReadResponse(2); err != nil {
		return fmt.Errorf("message not accepted: %w", err)
	}
	// The server has accepted the message. A QUIT failure now must not be
	// reported as a send failure, or the retry would send it twice.
	_ = command(tp, 221, "QUIT")
	return nil
}

// mailpitBanner is part of Mailpit's SMTP greeting ("220 <host> Mailpit ESMTP
// Service ready").
const mailpitBanner = " Mailpit ESMTP"

func command(tp *textproto.Conn, expect int, line string) error {
	verb, _, _ := strings.Cut(line, " ")
	id, err := tp.Cmd("%s", line)
	if err != nil {
		return fmt.Errorf("send %s: %w", verb, err)
	}
	tp.StartResponse(id)
	defer tp.EndResponse(id)
	if _, _, err := tp.ReadResponse(expect); err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	return nil
}

// classify maps an SMTP conversation error to a sanitized Error, dropping
// the server's text (it may contain addresses).
func classify(ctx context.Context, err error) *Error {
	if de, ok := errors.AsType[*Error](err); ok {
		return de
	}
	if tpErr, ok := errors.AsType[*textproto.Error](err); ok {
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
