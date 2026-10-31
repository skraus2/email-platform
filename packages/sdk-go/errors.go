package emailplatform

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// ErrInvalidArgument is wrapped by every error about an argument or option
// of this package, returned before any request is sent. The messages name
// the argument, never its value.
var ErrInvalidArgument = errors.New("emailplatform: invalid argument")

func invalid(what string) error {
	return &argumentError{what: what}
}

type argumentError struct{ what string }

func (e *argumentError) Error() string { return "emailplatform: " + e.what }
func (e *argumentError) Unwrap() error { return ErrInvalidArgument }

// APIError is a response with a status outside 2xx.
type APIError struct {
	// Status is the HTTP status.
	Status int
	// Code is the API's stable snake_case error code, or one of the client's
	// own: "redirect" (a 3xx, never followed) and "http_error" (no error
	// body, e.g. from a proxy). A 503 without an error body is
	// "service_unavailable".
	Code string
	// Message is the server's text, cut to 1024 bytes.
	Message string
	// RetryAfter is the response's Retry-After, zero when it had none.
	RetryAfter time.Duration
	// RequestID is the response's X-Request-Id, for the server's logs.
	RequestID string
	// OperationID names the operation that failed, e.g. "createEmail".
	OperationID string

	retryAfterSent bool
}

func (e *APIError) Error() string {
	return e.OperationID + ": HTTP " + strconv.Itoa(e.Status) + " " + e.Code + ": " + e.Message
}

// GoString keeps %#v to the documented fields.
func (e *APIError) GoString() string {
	return "&emailplatform.APIError{Status:" + strconv.Itoa(e.Status) +
		", Code:" + strconv.Quote(e.Code) +
		", Message:" + strconv.Quote(e.Message) +
		", RetryAfter:" + strconv.Quote(e.RetryAfter.String()) +
		", RequestID:" + strconv.Quote(e.RequestID) +
		", OperationID:" + strconv.Quote(e.OperationID) + "}"
}

// TransportReason says why no usable response arrived.
type TransportReason string

const (
	// ReasonNetwork: the connection failed or broke; the request may have
	// reached the API.
	ReasonNetwork TransportReason = "network"
	// ReasonTimeout: the call's timeout ran out; the request may have
	// reached the API.
	ReasonTimeout TransportReason = "timeout"
	// ReasonAborted: the caller's context ended (cancelled or past its own
	// deadline).
	ReasonAborted TransportReason = "aborted"
	// ReasonInvalidBody: a 2xx response whose body is not the expected JSON.
	ReasonInvalidBody TransportReason = "invalid_body"
	// ReasonBodyTooLarge: the response body exceeded the client's limit.
	ReasonBodyTooLarge TransportReason = "body_too_large"
)

// TransportError is a call that got no usable response.
type TransportError struct {
	Reason      TransportReason
	OperationID string
	Method      string
	// Path is the operation's path template, e.g. "/v1/emails/{id}"; the
	// request URL is never kept (list filters carry recipient addresses).
	Path string

	causes []error
}

func (e *TransportError) Error() string {
	return e.Method + " " + e.Path + " (" + e.OperationID + "): " + string(e.Reason)
}

// Unwrap gives the reason's underlying errors: for ReasonAborted the
// caller context's error and cause, for ReasonTimeout
// context.DeadlineExceeded, for ReasonNetwork the transport's error (for
// errors.Is with e.g. syscall.ECONNREFUSED); nothing for the others.
func (e *TransportError) Unwrap() []error { return e.causes }

// GoString keeps %#v to the documented fields.
func (e *TransportError) GoString() string {
	return "&emailplatform.TransportError{Reason:" + strconv.Quote(string(e.Reason)) +
		", OperationID:" + strconv.Quote(e.OperationID) +
		", Method:" + strconv.Quote(e.Method) +
		", Path:" + strconv.Quote(e.Path) + "}"
}

func transportError(op *operation, reason TransportReason, causes ...error) *TransportError {
	return &TransportError{Reason: reason, OperationID: op.id, Method: op.method, Path: op.path, causes: causes}
}

func abortedError(op *operation, ctx context.Context) *TransportError {
	return transportError(op, ReasonAborted, ctx.Err(), context.Cause(ctx))
}
