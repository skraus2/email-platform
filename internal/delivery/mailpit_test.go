package delivery

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestNewMailpitRejectsNonLocalHosts(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:1025", "localhost:1025", "[::1]:1025", "mailpit:1025"} {
		if _, err := NewMailpit(addr); err != nil {
			t.Errorf("NewMailpit(%q) = %v, want ok", addr, err)
		}
	}
	for _, addr := range []string{
		"smtp.gmail.com:587", "email-smtp.eu-central-1.amazonaws.com:587", "10.0.0.5:25",
		"127.0.0.2:1025", "mailpit.evil.example:1025", "localhost", "", ":1025",
	} {
		if _, err := NewMailpit(addr); err == nil {
			t.Errorf("NewMailpit(%q) accepted a non-local SMTP target", addr)
		}
	}
}

// fakeSMTP speaks just enough SMTP to reach RCPT and answer it with reply.
func fakeSMTP(t *testing.T, rcptReply string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		write := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		write("220 fake ESMTP")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch cmd := strings.ToUpper(strings.Fields(line)[0]); cmd {
			case "EHLO", "HELO":
				write("250 fake")
			case "MAIL":
				write("250 ok")
			case "RCPT":
				write(rcptReply)
			case "QUIT":
				write("221 bye")
				return
			default:
				write("250 ok")
			}
		}
	}()
	return ln.Addr().String()
}

func TestSendErrorsAreSanitized(t *testing.T) {
	addr := fakeSMTP(t, "550 5.1.1 <customer@example.test>: Recipient address rejected: user unknown")
	mp, err := NewMailpit(addr)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mp.Send(context.Background(), sample())
	var de *Error
	if !errors.As(err, &de) {
		t.Fatalf("err = %v (%T), want *delivery.Error", err, err)
	}
	if de.Class != ClassRejected || de.Code != 550 || de.Temporary() {
		t.Errorf("error = %+v, want permanent rejected/550", de)
	}
	if strings.Contains(err.Error(), "customer@example.test") || strings.Contains(err.Error(), "user unknown") {
		t.Fatalf("provider text leaked into error: %q", err)
	}
}

func TestSendTemporaryRejection(t *testing.T) {
	addr := fakeSMTP(t, "451 4.7.1 <customer@example.test> try later")
	mp, _ := NewMailpit(addr)
	_, err := mp.Send(context.Background(), sample())
	var de *Error
	if !errors.As(err, &de) || !de.Temporary() || de.Code != 451 {
		t.Fatalf("err = %v, want temporary 451", err)
	}
}

func TestSendConnectErrorAndTimeout(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close() // nothing listens any more
	mp, _ := NewMailpit(addr)
	_, err := mp.Send(context.Background(), sample())
	var de *Error
	if !errors.As(err, &de) || de.Class != ClassConnect || !de.Temporary() {
		t.Fatalf("err = %v, want temporary connect error", err)
	}

	// A server that accepts but never greets must not hang past the deadline.
	silent, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { _ = silent.Close() })
	go func() {
		c, err := silent.Accept()
		if err == nil {
			time.Sleep(5 * time.Second)
			_ = c.Close()
		}
	}()
	mp, _ = NewMailpit(silent.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = mp.Send(ctx, sample())
	if !errors.As(err, &de) || de.Class != ClassTimeout {
		t.Fatalf("err = %v, want timeout", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("Send ignored the context deadline (%v)", time.Since(start))
	}
}
