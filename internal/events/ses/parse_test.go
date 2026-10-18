package ses

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
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
		"not json":              "{",
		"no event type":         strings.Replace(send, `"eventType":"Send",`, ``, 1),
		"no tags":               strings.Replace(send, `"email_id":["0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b"],`, ``, 1),
		"tag not uuid":          strings.Replace(send, `"0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b"`, `"x"`, 1),
		"two email tags":        strings.Replace(send, `["0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b"]`, `["0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0b","0199e3a0-1c2d-7e3f-8a4b-5c6d7e8f9a0c"]`, 1),
		"nil uuid tag":          strings.Replace(send, `"0199e39f-0000-7000-8000-000000000001"`, `"00000000-0000-0000-0000-000000000000"`, 1),
		"no message id":         strings.Replace(send, `"messageId":"0107019a-1111-2222-3333-444444444444-000000",`, ``, 1),
		"bad timestamp":         strings.Replace(send, `"timestamp":"2026-10-14T07:00:00.000Z"`, `"timestamp":"yesterday"`, 1),
		"bounce no feedback":    strings.Replace(bounce, `"feedbackId":"0107019a-bounce-feedback-0001",`, ``, 1),
		"bounce bad type":       strings.Replace(bounce, `"bounceType":"Permanent"`, `"bounceType":"permanent"`, 1),
		"bounce no section":     `{"eventType":"Bounce",` + send[strings.Index(send, `"mail"`):strings.Index(send, `,"send"`)] + `}`,
		"long feedback id":      strings.Replace(bounce, `0107019a-bounce-feedback-0001`, strings.Repeat("f", 600), 1),
		"zero time":             strings.Replace(send, `"timestamp":"2026-10-14T07:00:00.000Z"`, `"timestamp":"0001-01-01T00:00:00Z"`, 1),
		"delivery no section":   strings.Replace(fixture(t, "delivery"), `"delivery":{`, `"x":{`, 1),
		"delay no section":      strings.Replace(fixture(t, "delivery_delay"), `"deliveryDelay":{`, `"x":{`, 1),
		"complaint no feedback": strings.Replace(fixture(t, "complaint"), `"feedbackId":"0107019a-complaint-feedback-0001",`, ``, 1),
	} {
		if _, err := Parse(msg); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

// Optional details that are not documented SES values are dropped, never
// stored or forwarded.
func TestParseDropsOddDetails(t *testing.T) {
	for _, odd := range []string{`line\nbreak`, `mx.example.test`, `Unknown`} {
		msg := strings.Replace(fixture(t, "bounce_permanent"), `"bounceSubType":"General"`, `"bounceSubType":"`+odd+`"`, 1)
		ev, err := Parse(msg)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := ev.Details["bounce_subtype"]; ok || ev.Details["bounce_type"] != "Permanent" {
			t.Fatalf("%s: details = %v", odd, ev.Details)
		}
	}
	msg := strings.Replace(fixture(t, "complaint"), `"complaintFeedbackType":"abuse"`, `"complaintFeedbackType":"spam from a@b"`, 1)
	if ev, err := Parse(msg); err != nil || len(ev.Details) != 0 {
		t.Fatalf("complaint details = %v, %v", ev.Details, err)
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

// The sending account comes back with every event, so the poller can refuse
// events of another AWS account published to the platform's topic.
func TestParseSendingAccount(t *testing.T) {
	_, account, err := parse(fixture(t, "delivery"))
	if err != nil || account != "123456789012" {
		t.Fatalf("account = %q, %v; want 123456789012", account, err)
	}
	msg := strings.Replace(fixture(t, "delivery"), `"sendingAccountId":"123456789012",`, ``, 1)
	if _, account, err := parse(msg); err != nil || account != "" {
		t.Fatalf("without the field: account = %q, %v; want empty", account, err)
	}
}

func TestParseRecipients(t *testing.T) {
	// Fixtures: only complaints and permanent bounces carry recipients.
	for name, want := range map[string][]string{
		"complaint":           {"customer@example.test"},
		"bounce_permanent":    {"customer@example.test"},
		"bounce_transient":    nil,
		"bounce_undetermined": nil,
		"delivery":            nil,
		"send":                nil,
		"reject":              nil,
		"delivery_delay":      nil,
	} {
		ev, err := Parse(fixture(t, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !slices.Equal(ev.Recipients, want) {
			t.Errorf("%s: recipients = %v, want %v", name, ev.Recipients, want)
		}
		for k, v := range ev.Details {
			if strings.Contains(v, "@") {
				t.Errorf("%s: detail %s carries an address", name, k)
			}
		}
	}

	// Unusable addresses are dropped without refusing the event.
	list := func(addrs ...string) string {
		var parts []string
		for _, a := range addrs {
			b, _ := json.Marshal(a)
			parts = append(parts, `{"emailAddress":`+string(b)+`}`)
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	complaint := fixture(t, "complaint")
	withRecipients := func(arr string) string {
		return strings.Replace(complaint, `[{"emailAddress":"customer@example.test"}]`, arr, 1)
	}
	ev, err := Parse(withRecipients(list("ok@example.test", "Name <named@example.test>", "ünï@example.test",
		"a@b@c", "", strings.Repeat("x", 250)+"@example.test", "two@example.test, three@example.test", "Second@Example.test")))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ok@example.test", "Second@Example.test"}; !slices.Equal(ev.Recipients, want) {
		t.Fatalf("recipients = %v, want %v", ev.Recipients, want)
	}
	if ev.Type != "complained" || ev.ProviderEventID != "0107019a-complaint-feedback-0001" {
		t.Fatalf("event = %+v", ev)
	}

	// At most 50 entries are read.
	many := make([]string, 60)
	for i := range many {
		many[i] = fmt.Sprintf("r%d@example.test", i)
	}
	if ev, err = Parse(withRecipients(list(many...))); err != nil || len(ev.Recipients) != 50 || ev.Recipients[49] != "r49@example.test" {
		t.Fatalf("capped: %d recipients, %v", len(ev.Recipients), err)
	}

	// No recipients section at all: still a valid complaint, nothing to suppress.
	if ev, err = Parse(strings.Replace(complaint, `"complainedRecipients":[{"emailAddress":"customer@example.test"}],`, "", 1)); err != nil || ev.Recipients != nil {
		t.Fatalf("no recipients: %v, %v", ev.Recipients, err)
	}
}
