//go:build integration

package delivery_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/delivery"
	"github.com/skraus2/email-platform/internal/testmailpit"
)

func TestMailpitCapturesExactlyTheMessage(t *testing.T) {
	t.Parallel()
	mp, err := delivery.NewMailpit(testmailpit.SMTPAddr())
	if err != nil {
		t.Fatal(err)
	}
	msg := delivery.Message{
		ID: uuid.Must(uuid.NewV7()), From: "noreply@example.test", To: "customer@example.test",
		Subject: "Integration ✓ " + t.Name(), Text: "Plain body\nsecond line", HTML: "<p>HTML <b>body</b></p>",
	}
	res, err := mp.Send(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != delivery.StatusCaptured || res.Provider != "mailpit" {
		t.Fatalf("result = %+v; a local sink must report captured, never submitted/delivered", res)
	}

	got := testmailpit.FindByMessageID(t, strings.Trim(res.ProviderID, "<>"), 5*time.Second)
	if len(got) != 1 {
		t.Fatalf("Mailpit has %d messages with this Message-ID, want exactly 1", len(got))
	}
	m := got[0]
	if m.From.Address != msg.From || len(m.To) != 1 || m.To[0].Address != msg.To || m.Subject != msg.Subject {
		t.Errorf("headers = from %q to %v subject %q", m.From.Address, m.To, m.Subject)
	}
	if strings.TrimSpace(strings.ReplaceAll(m.Text, "\r\n", "\n")) != msg.Text {
		t.Errorf("text = %q, want %q", m.Text, msg.Text)
	}
	if strings.TrimSpace(m.HTML) != msg.HTML {
		t.Errorf("html = %q, want %q", m.HTML, msg.HTML)
	}
}
