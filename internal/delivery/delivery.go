// Package delivery hands finished messages to a mail provider. Providers
// implement Provider; the only one today is the local Mailpit capture sink.
// Errors are sanitized: they carry a class and an SMTP code, never provider
// text, which can contain recipient addresses.
package delivery

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Message is one email for one recipient. Addresses are bare local@domain.
type Message struct {
	ID      uuid.UUID // becomes the Message-ID
	From    string
	To      string
	Subject string
	Text    string
	HTML    string
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
)

// Error is a sanitized provider error.
type Error struct {
	Class string
	Code  int // SMTP reply code, 0 if none
}

func (e *Error) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("delivery failed: %s (smtp %d)", e.Class, e.Code)
	}
	return "delivery failed: " + e.Class
}

// Temporary reports whether a retry can succeed.
func (e *Error) Temporary() bool {
	switch e.Class {
	case ClassConnect, ClassTimeout, ClassProtocol:
		return true
	case ClassRejected:
		return e.Code >= 400 && e.Code < 500
	default:
		return false
	}
}
