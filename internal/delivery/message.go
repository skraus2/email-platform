package delivery

import (
	"bytes"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

const messageIDDomain = "email-platform.local"

// buildMessage renders msg as an RFC 5322 message with CRLF line endings.
// It re-validates header fields itself so a validation gap upstream can never
// turn into header injection.
func buildMessage(msg Message, now time.Time) ([]byte, error) {
	for name, v := range map[string]string{"from": msg.From, "to": msg.To, "subject": msg.Subject} {
		if strings.ContainsAny(v, "\r\n\x00") {
			return nil, fmt.Errorf("%s contains a control character", name)
		}
	}
	for name, v := range map[string]string{"from": msg.From, "to": msg.To} {
		a, err := mail.ParseAddress(v)
		if err != nil || a.Name != "" || a.Address != v {
			return nil, fmt.Errorf("%s is not a single bare address", name)
		}
	}
	if msg.Text == "" && msg.HTML == "" {
		return nil, errors.New("message has no body")
	}
	if msg.ListUnsubscribeURL != "" {
		if err := ValidateListUnsubscribeURL(msg.ListUnsubscribeURL); err != nil {
			return nil, err
		}
	}

	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("Message-ID", "<"+msg.ID.String()+"@"+messageIDDomain+">")
	header("Date", now.UTC().Format(time.RFC1123Z))
	header("From", msg.From)
	header("To", msg.To)
	header("Subject", encodeSubject(msg.Subject))
	if msg.ListUnsubscribeURL != "" {
		// RFC 2369 + RFC 8058 one-click: the receiver's mailbox provider
		// POSTs "List-Unsubscribe=One-Click" to the URL.
		header("List-Unsubscribe", "<"+msg.ListUnsubscribeURL+">")
		header("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
	}
	header("MIME-Version", "1.0")

	switch {
	case msg.Text != "" && msg.HTML != "":
		mw := multipart.NewWriter(&b)
		header("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
		b.WriteString("\r\n")
		for _, p := range []struct{ ct, body string }{
			{"text/plain; charset=utf-8", msg.Text},
			{"text/html; charset=utf-8", msg.HTML},
		} {
			pw, err := mw.CreatePart(textproto.MIMEHeader{
				"Content-Type":              {p.ct},
				"Content-Transfer-Encoding": {"quoted-printable"},
			})
			if err != nil {
				return nil, err
			}
			if err := writeQP(pw, p.body); err != nil {
				return nil, err
			}
		}
		if err := mw.Close(); err != nil {
			return nil, err
		}
	default:
		ct, body := "text/plain; charset=utf-8", msg.Text
		if msg.Text == "" {
			ct, body = "text/html; charset=utf-8", msg.HTML
		}
		header("Content-Type", ct)
		header("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeQP(&b, body); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}

// MaxListUnsubscribeURL keeps the unfolded List-Unsubscribe header line
// ("List-Unsubscribe: <" + URL + ">") within the 998-byte line limit.
const MaxListUnsubscribeURL = 998 - len("List-Unsubscribe: <>")

// ValidateListUnsubscribeURL accepts an absolute https URL with a host, no
// userinfo, of printable ASCII without space, quotes and angle brackets, at
// most MaxListUnsubscribeURL bytes — safe to place between < and > in a
// header line.
func ValidateListUnsubscribeURL(v string) error {
	if len(v) > MaxListUnsubscribeURL {
		return fmt.Errorf("unsubscribe URL must be at most %d bytes", MaxListUnsubscribeURL)
	}
	for i := range len(v) {
		if c := v[i]; c <= 0x20 || c >= 0x7f || c == '<' || c == '>' || c == '"' {
			return errors.New("unsubscribe URL must be printable ASCII without spaces, quotes or angle brackets")
		}
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return errors.New("unsubscribe URL must be an absolute https URL without user information")
	}
	return nil
}

// encodeSubject returns the subject as RFC 2047 encoded-words when needed,
// folded onto continuation lines so no line exceeds the SMTP limit.
func encodeSubject(s string) string {
	enc := mime.QEncoding.Encode("utf-8", s)
	return strings.ReplaceAll(enc, "?= =?", "?=\r\n =?")
}

type byteWriter interface{ Write([]byte) (int, error) }

func writeQP(w byteWriter, body string) error {
	qp := quotedprintable.NewWriter(w)
	// Normalize to LF; the text-mode writer emits CRLF line breaks.
	if _, err := qp.Write([]byte(strings.ReplaceAll(body, "\r\n", "\n"))); err != nil {
		return err
	}
	return qp.Close()
}
