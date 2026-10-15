package ses

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/skraus2/email-platform/internal/events"
)

// Parse outcomes besides success.
var (
	// ErrIgnored: a valid event the platform does not track (engagement,
	// rendering failures, subscriptions, unknown types).
	ErrIgnored = errors.New("ses: event type not tracked")
	// ErrInvalid: the message is not a usable SES event.
	ErrInvalid = errors.New("ses: invalid event")
)

// Message tags the platform sets on every SES send; events carry them back.
const (
	TagEmailID   = "email_id"
	TagProjectID = "project_id"
)

type sesEvent struct {
	EventType string `json:"eventType"`
	Mail      struct {
		Timestamp        string              `json:"timestamp"`
		MessageID        string              `json:"messageId"`
		SendingAccountID string              `json:"sendingAccountId"`
		Tags             map[string][]string `json:"tags"`
	} `json:"mail"`
	Bounce *struct {
		BounceType    string `json:"bounceType"`
		BounceSubType string `json:"bounceSubType"`
		Timestamp     string `json:"timestamp"`
		FeedbackID    string `json:"feedbackId"`
	} `json:"bounce"`
	Complaint *struct {
		ComplaintFeedbackType string `json:"complaintFeedbackType"`
		Timestamp             string `json:"timestamp"`
		FeedbackID            string `json:"feedbackId"`
	} `json:"complaint"`
	Delivery *struct {
		Timestamp string `json:"timestamp"`
	} `json:"delivery"`
	DeliveryDelay *struct {
		DelayType string `json:"delayType"`
		Timestamp string `json:"timestamp"`
	} `json:"deliveryDelay"`
}

const maxIDLen = 512

func invalid(reason string) error { return fmt.Errorf("%w: %s", ErrInvalid, reason) }

// Parse turns an SES event-publishing message into a provider event. Only
// non-personal details are kept: no addresses, diagnostic codes, SMTP
// responses or links.
func Parse(message string) (events.ProviderEvent, error) {
	ev, _, err := parse(message)
	return ev, err
}

// parse also returns the AWS account that sent the message (empty if the
// event does not name one).
func parse(message string) (events.ProviderEvent, string, error) {
	var e sesEvent
	if err := json.Unmarshal([]byte(message), &e); err != nil {
		return events.ProviderEvent{}, "", invalid("not JSON")
	}
	ev, err := convert(e)
	return ev, e.Mail.SendingAccountID, err
}

func convert(e sesEvent) (events.ProviderEvent, error) {
	ev := events.ProviderEvent{Provider: "ses", Details: map[string]string{}}
	switch e.EventType {
	case "Send", "Delivery", "Bounce", "Complaint", "Reject", "DeliveryDelay":
	case "":
		return events.ProviderEvent{}, invalid("no event type")
	default:
		return events.ProviderEvent{}, ErrIgnored
	}

	msgID := e.Mail.MessageID
	if msgID == "" || len(msgID) > maxIDLen-64 {
		return events.ProviderEvent{}, invalid("missing or oversized message id")
	}
	ev.ProviderMessageID = msgID
	var err error
	if ev.EmailID, err = tag(e.Mail.Tags, TagEmailID); err != nil {
		return events.ProviderEvent{}, err
	}
	if ev.ProjectID, err = tag(e.Mail.Tags, TagProjectID); err != nil {
		return events.ProviderEvent{}, err
	}
	mailTime, err := timestamp(e.Mail.Timestamp)
	if err != nil {
		return events.ProviderEvent{}, err
	}

	switch e.EventType {
	case "Send":
		ev.Type, ev.ProviderEventID, ev.OccurredAt = "send", "send:"+msgID, mailTime
	case "Reject":
		ev.Type, ev.ProviderEventID, ev.OccurredAt = "rejected", "reject:"+msgID, mailTime
	case "Delivery":
		if e.Delivery == nil {
			return events.ProviderEvent{}, invalid("no delivery section")
		}
		ev.Type, ev.ProviderEventID = "delivered", "delivery:"+msgID
		if ev.OccurredAt, err = timestamp(e.Delivery.Timestamp); err != nil {
			return events.ProviderEvent{}, err
		}
	case "Bounce":
		b := e.Bounce
		if b == nil || b.FeedbackID == "" || len(b.FeedbackID) > maxIDLen {
			return events.ProviderEvent{}, invalid("bounce without a usable feedback id")
		}
		switch b.BounceType {
		case "Permanent", "Transient", "Undetermined":
		default:
			return events.ProviderEvent{}, invalid("unknown bounce type")
		}
		ev.Type, ev.ProviderEventID = "bounced", b.FeedbackID
		ev.Details["bounce_type"] = b.BounceType
		optional(ev.Details, "bounce_subtype", b.BounceSubType, bounceSubTypes)
		if ev.OccurredAt, err = timestamp(b.Timestamp); err != nil {
			return events.ProviderEvent{}, err
		}
	case "Complaint":
		c := e.Complaint
		if c == nil || c.FeedbackID == "" || len(c.FeedbackID) > maxIDLen {
			return events.ProviderEvent{}, invalid("complaint without a usable feedback id")
		}
		ev.Type, ev.ProviderEventID = "complained", c.FeedbackID
		optional(ev.Details, "complaint_feedback_type", c.ComplaintFeedbackType, feedbackTypes)
		if ev.OccurredAt, err = timestamp(c.Timestamp); err != nil {
			return events.ProviderEvent{}, err
		}
	case "DeliveryDelay":
		d := e.DeliveryDelay
		if d == nil {
			return events.ProviderEvent{}, invalid("no delivery delay section")
		}
		if ev.OccurredAt, err = timestamp(d.Timestamp); err != nil {
			return events.ProviderEvent{}, err
		}
		ev.Type = "delivery_delayed"
		ev.ProviderEventID = "delay:" + msgID + ":" + ev.OccurredAt.UTC().Format(time.RFC3339)
		optional(ev.Details, "delay_type", d.DelayType, delayTypes)
	}
	return ev, nil
}

// tag returns the single UUID value of a message tag.
func tag(tags map[string][]string, name string) (uuid.UUID, error) {
	v := tags[name]
	if len(v) != 1 {
		return uuid.Nil, invalid("tag " + name + " missing or repeated")
	}
	id, err := uuid.Parse(v[0])
	if err != nil || id == uuid.Nil || len(v[0]) != 36 {
		return uuid.Nil, invalid("tag " + name + " is not a UUID")
	}
	return id, nil
}

func timestamp(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.IsZero() {
		return time.Time{}, invalid("bad timestamp")
	}
	return t.UTC(), nil
}

// Documented SES values; anything else (a future value, free text) is
// dropped rather than stored and forwarded.
var (
	bounceSubTypes = set("Undetermined", "General", "NoEmail", "Suppressed", "OnAccountSuppressionList",
		"MailboxFull", "MessageTooLarge", "ContentRejected", "AttachmentRejected")
	feedbackTypes = set("abuse", "auth-failure", "fraud", "not-spam", "other", "virus") // RFC 5965
	delayTypes    = set("InternalFailure", "General", "MailboxFull", "SpamDetected", "RecipientServerError",
		"IPFailure", "TransientCommunicationFailure", "BYOIPHostNameLookupUnavailable", "Undetermined", "SendingDeferral")
)

func set(values ...string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

// optional keeps a detail only if it is one of the documented values.
func optional(details map[string]string, key, v string, allowed map[string]bool) {
	if allowed[v] {
		details[key] = v
	}
}
