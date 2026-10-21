// Package delivery hands finished messages to a mail provider. Providers
// implement Provider: the local Mailpit capture sink, and Amazon SES (only
// with the explicit SES opt-in configuration).
// Errors are sanitized: they carry a class and an SMTP or HTTP status code,
// never provider text, which can contain recipient addresses.
package delivery

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Message is one email for one recipient. Addresses are bare local@domain.
type Message struct {
	ID        uuid.UUID // becomes the Message-ID
	ProjectID uuid.UUID
	From      string
	To        string
	Subject   string
	Text      string
	HTML      string
	// ListUnsubscribeURL, when set, becomes the RFC 8058 one-click
	// List-Unsubscribe header (see ValidateListUnsubscribeURL).
	ListUnsubscribeURL string
}

// Status values a provider can report for an accepted message.
const (
	StatusCaptured  = "captured"  // local test sink; never a real send
	StatusSubmitted = "submitted" // real provider accepted it
)

// Result describes an accepted message.
type Result struct {
	Provider   string
	ProviderID string
	Status     string
}

// Provider sends messages.
type Provider interface {
	Send(ctx context.Context, msg Message) (Result, error)
}

// Error classes.
const (
	ClassInvalid  = "invalid"  // message could not be built; never retry
	ClassConnect  = "connect"  // could not reach the provider
	ClassTimeout  = "timeout"  // deadline exceeded mid-conversation
	ClassRejected = "rejected" // provider answered with an SMTP error
	ClassProtocol = "protocol" // unexpected protocol failure
	// ClassUnsafeTarget: the server is not the expected test sink (e.g. a
	// real MTA on localhost). Nothing was sent; never retry.
	ClassUnsafeTarget = "unsafe_target"

	// Classes of an API provider's error answer. Each means the request was
	// refused, except ClassAPIUnavailable (server error: outcome unknown).
	ClassThrottled        = "throttled"         // rate or quota exceeded; retry later
	ClassAuth             = "auth"              // credentials refused or not permitted
	ClassSenderUnverified = "sender_unverified" // sender or MAIL FROM domain not verified
	ClassAPIRejected      = "api_rejected"      // any other refusal
	ClassAPIUnavailable   = "api_unavailable"   // 5xx: the provider may have accepted it
)

// Error is a sanitized provider error.
type Error struct {
	Class      string
	Code       int // SMTP reply code, 0 if none
	HTTPStatus int // API response status, 0 if none
}

func (e *Error) Error() string {
	switch {
	case e.Code != 0:
		return fmt.Sprintf("delivery failed: %s (smtp %d)", e.Class, e.Code)
	case e.HTTPStatus != 0:
		return fmt.Sprintf("delivery failed: %s (http %d)", e.Class, e.HTTPStatus)
	}
	return "delivery failed: " + e.Class
}

// Temporary reports whether a retry can succeed.
func (e *Error) Temporary() bool {
	switch e.Class {
	case ClassConnect, ClassTimeout, ClassProtocol, ClassThrottled, ClassAPIUnavailable:
		return true
	case ClassRejected:
		return e.Code >= 400 && e.Code < 500
	default:
		return false
	}
}
