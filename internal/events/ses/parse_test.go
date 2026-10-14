package ses

import (
	"errors"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	fixtureEmail   = uuid.MustParse("0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b")
	fixtureProject = uuid.MustParse("0199e39f-0000-7000-8000-000000000001")
)

const fixtureMessageID = "0107019a-1111-2222-3333-444444444444-000000"

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func at(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return ts
}

func TestParseFixtures(t *testing.T) {
	for name, want := range map[string]struct {
		typ, eventID string
		occurred     time.Time
		details      map[string]string
	}{
		"send":     {"send", "send:" + fixtureMessageID, at("2026-10-14T07:00:00Z"), map[string]string{}},
		"delivery": {"delivered", "delivery:" + fixtureMessageID, at("2026-10-14T07:00:02Z"), map[string]string{}},
		"bounce_permanent": {"bounced", "0107019a-bounce-feedback-0001", at("2026-10-14T07:00:03Z"),
			map[string]string{"bounce_type": "Permanent", "bounce_subtype": "General"}},
		"bounce_transient": {"bounced", "0107019a-bounce-feedback-0002", at("2026-10-14T07:00:04Z"),
			map[string]string{"bounce_type": "Transient", "bounce_subtype": "MailboxFull"}},
		"bounce_undetermined": {"bounced", "0107019a-bounce-feedback-0003", at("2026-10-14T07:00:05Z"),
			map[string]string{"bounce_type": "Undetermined", "bounce_subtype": "Undetermined"}},
		"complaint": {"complained", "0107019a-complaint-feedback-0001", at("2026-10-14T07:10:00Z"),
			map[string]string{"complaint_feedback_type": "abuse"}},
		"reject": {"rejected", "reject:" + fixtureMessageID, at("2026-10-14T07:00:00Z"), map[string]string{}},
		"delivery_delay": {"delivery_delayed", "delay:" + fixtureMessageID + ":2026-10-14T07:30:00Z", at("2026-10-14T07:30:00Z"),
			map[string]string{"delay_type": "MailboxFull"}},
	} {
		ev, err := Parse(fixture(t, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if ev.Provider != "ses" || ev.Type != want.typ || ev.ProviderEventID != want.eventID ||
			ev.ProviderMessageID != fixtureMessageID || ev.EmailID != fixtureEmail || ev.ProjectID != fixtureProject ||
			!ev.OccurredAt.Equal(want.occurred) || !maps.Equal(ev.Details, want.details) {
			t.Errorf("%s: parsed %+v", name, ev)
		}
		for _, v := range ev.Details {
			if strings.Contains(v, "@") || strings.Contains(v, "550") {
				t.Errorf("%s: detail %q carries personal or diagnostic data", name, v)
			}
		}
	}
}

func TestParseIgnoresEngagementAndUnknown(t *testing.T) {
	for _, msg := range []string{
		fixture(t, "open"),
		fixture(t, "click"),
		strings.Replace(fixture(t, "send"), `"eventType":"Send"`, `"eventType":"Rendering Failure"`, 1),
		strings.Replace(fixture(t, "send"), `"eventType":"Send"`, `"eventType":"Subscription"`, 1),
		strings.Replace(fixture(t, "send"), `"eventType":"Send"`, `"eventType":"Something New"`, 1),
	} {
		if _, err := Parse(msg); !errors.Is(err, ErrIgnored) {
			t.Errorf("err = %v, want ErrIgnored for %.40s", err, msg)
		}
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	send := fixture(t, "send")
	bounce := fixture(t, "bounce_permanent")
	for name, msg := range map[string]string{
		"not json":           "{",
		"no event type":      strings.Replace(send, `"eventType":"Send",`, ``, 1),
		"no tags":            strings.Replace(send, `"email_id":["0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b"],`, ``, 1),
		"tag not uuid":       strings.Replace(send, `"0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b"`, `"x"`, 1),
		"two email tags":     strings.Replace(send, `["0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b"]`, `["0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b","0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0c"]`, 1),
		"nil uuid tag":       strings.Replace(send, `"0199e39f-0000-7000-8000-000000000001"`, `"00000000-0000-0000-0000-000000000000"`, 1),
		"no message id":      strings.Replace(send, `"messageId":"0107019a-1111-2222-3333-444444444444-000000",`, ``, 1),
		"bad timestamp":      strings.Replace(send, `"timestamp":"2026-10-14T07:00:00.000Z"`, `"timestamp":"yesterday"`, 1),
		"bounce no feedback": strings.Replace(bounce, `"feedbackId":"0107019a-bounce-feedback-0001",`, ``, 1),
		"bounce bad type":    strings.Replace(bounce, `"bounceType":"Permanent"`, `"bounceType":"permanent"`, 1),
		"bounce no section":  `{"eventType":"Bounce",` + send[strings.Index(send, `"mail"`):strings.Index(send, `,"send"`)] + `}`,
		"long feedback id":   strings.Replace(bounce, `0107019a-bounce-feedback-0001`, strings.Repeat("f", 600), 1),
	} {
		if _, err := Parse(msg); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

// Optional details that are not plain short values are dropped, never stored.
func TestParseDropsOddDetails(t *testing.T) {
	msg := strings.Replace(fixture(t, "bounce_permanent"), `"bounceSubType":"General"`, `"bounceSubType":"line\nbreak"`, 1)
	ev, err := Parse(msg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ev.Details["bounce_subtype"]; ok || ev.Details["bounce_type"] != "Permanent" {
		t.Fatalf("details = %v", ev.Details)
	}
}

func FuzzParse(f *testing.F) {
	for _, name := range []string{"send", "delivery", "bounce_permanent", "complaint", "delivery_delay", "open"} {
		b, err := os.ReadFile("testdata/" + name + ".json")
		if err != nil {
			f.Fatal(err)
		}
		f.Add(string(b))
	}
	f.Add(`{"eventType":"Bounce","mail":{},"bounce":null}`)
	f.Fuzz(func(t *testing.T, msg string) {
		ev, err := Parse(msg)
		if err != nil {
			if !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrIgnored) {
				t.Fatalf("unexpected error kind: %v", err)
			}
			return
		}
		if ev.EmailID == uuid.Nil || ev.ProjectID == uuid.Nil || ev.ProviderEventID == "" || ev.OccurredAt.IsZero() {
			t.Fatalf("accepted an incomplete event: %+v", ev)
		}
	})
}
