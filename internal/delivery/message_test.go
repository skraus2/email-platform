package delivery

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var fixedTime = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func sample() Message {
	return Message{
		ID:   uuid.MustParse("01a13601-4b3e-72cd-9dcd-4d9159938763"),
		From: "noreply@example.test", To: "customer@example.test",
		Subject: "Willkommen bei Ä-Ö-Ü", Text: "Hallo Welt\nZeile zwei", HTML: "<p>Hallo <b>Welt</b></p>",
	}
}

func parse(t *testing.T, raw []byte) *mail.Message {
	t.Helper()
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("not a valid RFC 5322 message: %v\n%s", err, raw)
	}
	return m
}

func decodeQP(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(quotedprintable.NewReader(r))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

func TestBuildMessageMultipartRoundTrip(t *testing.T) {
	msg := sample()
	raw, err := buildMessage(msg, fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.ReplaceAll(raw, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatal("bare LF in message; all line endings must be CRLF")
	}
	m := parse(t, raw)
	h := m.Header
	if h.Get("Message-Id") != "<"+msg.ID.String()+"@email-platform.local>" {
		t.Errorf("Message-ID = %q", h.Get("Message-Id"))
	}
	if h.Get("From") != msg.From || h.Get("To") != msg.To || h.Get("Mime-Version") != "1.0" {
		t.Errorf("headers = %v", h)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(h.Get("Subject"))
	if err != nil || subject != msg.Subject {
		t.Errorf("subject = %q (%v), want %q", subject, err, msg.Subject)
	}
	if d, err := h.Date(); err != nil || !d.Equal(fixedTime) {
		t.Errorf("date = %v (%v)", d, err)
	}

	mediaType, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("content-type = %q (%v)", mediaType, err)
	}
	mr := multipart.NewReader(m.Body, params["boundary"])
	var parts []string
	for {
		p, err := mr.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if p.Header.Get("Content-Transfer-Encoding") != "quoted-printable" {
			t.Errorf("part encoding = %q", p.Header.Get("Content-Transfer-Encoding"))
		}
		parts = append(parts, p.Header.Get("Content-Type")+"|"+decodeQP(t, p))
	}
	want := []string{
		"text/plain; charset=utf-8|" + msg.Text,
		"text/html; charset=utf-8|" + msg.HTML,
	}
	if len(parts) != 2 || parts[0] != want[0] || parts[1] != want[1] {
		t.Fatalf("parts = %q, want %q", parts, want)
	}
}

func TestBuildMessageSinglePart(t *testing.T) {
	for _, tc := range []struct{ text, html, ct, body string }{
		{"only text", "", "text/plain; charset=utf-8", "only text"},
		{"", "<p>only html</p>", "text/html; charset=utf-8", "<p>only html</p>"},
	} {
		msg := sample()
		msg.Text, msg.HTML = tc.text, tc.html
		raw, err := buildMessage(msg, fixedTime)
		if err != nil {
			t.Fatal(err)
		}
		m := parse(t, raw)
		if m.Header.Get("Content-Type") != tc.ct {
			t.Errorf("content-type = %q, want %q", m.Header.Get("Content-Type"), tc.ct)
		}
		if got := decodeQP(t, m.Body); got != tc.body {
			t.Errorf("body = %q, want %q", got, tc.body)
		}
	}
}

func TestBuildMessageFoldsLongSubject(t *testing.T) {
	msg := sample()
	msg.Subject = strings.Repeat("Ünïcödé ", 30) // 240 chars, non-ASCII
	raw, err := buildMessage(msg, fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line longer than 998 octets: %d", len(line))
		}
	}
	m := parse(t, raw)
	subject, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || subject != msg.Subject {
		t.Fatalf("folded subject did not round-trip: %q (%v)", subject, err)
	}
}

func TestBuildMessageRejectsHeaderInjection(t *testing.T) {
	cases := map[string]func(*Message){
		"subject CRLF": func(m *Message) { m.Subject = "Hi\r\nBcc: victim@example.test" },
		"subject LF":   func(m *Message) { m.Subject = "Hi\nX: y" },
		"from CR":      func(m *Message) { m.From = "a@example.test\rBcc: x@example.test" },
		"to NUL":       func(m *Message) { m.To = "a@example.test\x00" },
		"to list":      func(m *Message) { m.To = "a@example.test, b@example.test" },
		"no body":      func(m *Message) { m.Text, m.HTML = "", "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			msg := sample()
			mutate(&msg)
			if _, err := buildMessage(msg, fixedTime); err == nil {
				t.Fatal("buildMessage accepted invalid message")
			}
		})
	}
}

func TestBuildMessageListUnsubscribe(t *testing.T) {
	const u = "https://app.example.test/unsubscribe?t=AbC-_0123456789abcdefghijklmnopqrstuvwxyzABC"
	msg := sample()
	msg.ListUnsubscribeURL = u
	raw, err := buildMessage(msg, fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	h := parse(t, raw).Header
	if h.Get("List-Unsubscribe") != "<"+u+">" || h.Get("List-Unsubscribe-Post") != "List-Unsubscribe=One-Click" {
		t.Fatalf("headers = %q / %q", h.Get("List-Unsubscribe"), h.Get("List-Unsubscribe-Post"))
	}

	// Without a URL neither header exists.
	raw, err = buildMessage(sample(), fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	if h := parse(t, raw).Header; h.Get("List-Unsubscribe") != "" || h.Get("List-Unsubscribe-Post") != "" {
		t.Fatal("unsubscribe headers on a message without URL")
	}

	// The longest accepted URL keeps the header line within 998 bytes.
	long := "https://a.test/" + strings.Repeat("x", MaxListUnsubscribeURL-len("https://a.test/"))
	msg.ListUnsubscribeURL = long
	raw, err = buildMessage(msg, fixedTime)
	if err != nil {
		t.Fatalf("URL of %d bytes: %v", len(long), err)
	}
	for line := range strings.SplitSeq(string(raw), "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line of %d bytes", len(line))
		}
	}

	for name, bad := range map[string]string{
		"too long":    long + "x",
		"http":        "http://app.example.test/u?t=x",
		"relative":    "/unsubscribe?t=x",
		"mailto":      "mailto:u@example.test",
		"userinfo":    "https://user:pw@app.example.test/u",
		"no host":     "https:///u",
		"CRLF":        "https://app.example.test/u\r\nBcc: victim@example.test",
		"LF":          "https://app.example.test/u\nX: y",
		"angle":       "https://app.example.test/u>, <https://evil.test/",
		"space":       "https://app.example.test/u x",
		"quote":       `https://app.example.test/u"x`,
		"non-ASCII":   "https://app.example.test/ü",
		"NUL":         "https://app.example.test/u\x00",
		"control DEL": "https://app.example.test/u\x7f",
	} {
		msg := sample()
		msg.ListUnsubscribeURL = bad
		if _, err := buildMessage(msg, fixedTime); err == nil {
			t.Errorf("%s: accepted %q", name, bad)
		}
		if ValidateListUnsubscribeURL(bad) == nil {
			t.Errorf("%s: ValidateListUnsubscribeURL accepted %q", name, bad)
		}
	}
}
