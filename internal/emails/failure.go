package emails

import (
	"errors"
	"slices"

	"github.com/skraus2/email-platform/internal/delivery"
)

// Failure codes say why a dispatch attempt failed. They are part of the API
// (error_code) and enforced by a database CHECK, so adding one needs both.
const (
	CodeProviderUnreachable   = "provider_unreachable"
	CodeProviderTimeout       = "provider_timeout"
	CodeProviderProtocolError = "provider_protocol_error"
	CodeProviderDeferred      = "provider_deferred"
	CodeProviderThrottled     = "provider_throttled"
	CodeProviderRejected      = "provider_rejected"
	CodeProviderAuthFailed    = "provider_auth_failed"
	CodeSenderNotVerified     = "sender_not_verified"
	CodeMessageInvalid        = "message_invalid"
	CodeUnsafeTarget          = "unsafe_target"
	CodeSuppressed            = "suppressed"
	CodeProjectDisabled       = "project_disabled"
	CodeContentMissing        = "content_missing"
	CodeExpired               = "expired"
	CodeInternalError         = "internal_error"
	// Broadcast emails only.
	CodeUnsubscribed      = "unsubscribed"
	CodeContactDeleted    = "contact_deleted"
	CodeBroadcastCanceled = "broadcast_canceled"
	CodeRenderFailed      = "render_failed"
)

// FailureCodes lists every failure code.
var FailureCodes = []string{
	CodeProviderUnreachable, CodeProviderTimeout, CodeProviderProtocolError, CodeProviderDeferred,
	CodeProviderThrottled, CodeProviderRejected, CodeProviderAuthFailed, CodeSenderNotVerified,
	CodeMessageInvalid, CodeUnsafeTarget, CodeSuppressed, CodeProjectDisabled,
	CodeContentMissing, CodeExpired, CodeInternalError,
	CodeUnsubscribed, CodeContactDeleted, CodeBroadcastCanceled, CodeRenderFailed,
}

var retryableCodes = []string{
	CodeProviderUnreachable, CodeProviderTimeout, CodeProviderProtocolError, CodeProviderDeferred,
	CodeProviderThrottled, CodeInternalError,
}

// Retryable reports whether an attempt that failed with code may succeed
// when retried.
func Retryable(code string) bool { return slices.Contains(retryableCodes, code) }

// failureCode maps a send error to its failure code. Errors that are not
// provider errors, and provider classes this code does not know, are
// internal errors and retried.
func failureCode(err error) (code string, retryable bool) {
	var de *delivery.Error
	if !errors.As(err, &de) {
		return CodeInternalError, true
	}
	switch de.Class {
	case delivery.ClassConnect:
		code = CodeProviderUnreachable
	case delivery.ClassTimeout:
		code = CodeProviderTimeout
	case delivery.ClassProtocol:
		code = CodeProviderProtocolError
	case delivery.ClassRejected:
		code = CodeProviderRejected
		if de.Code >= 400 && de.Code < 500 {
			code = CodeProviderDeferred
		}
	case delivery.ClassThrottled:
		code = CodeProviderThrottled
	case delivery.ClassAuth:
		code = CodeProviderAuthFailed
	case delivery.ClassSenderUnverified:
		code = CodeSenderNotVerified
	case delivery.ClassAPIRejected:
		code = CodeProviderRejected
	case delivery.ClassAPIUnavailable:
		code = CodeProviderUnreachable
	case delivery.ClassInvalid:
		code = CodeMessageInvalid
	case delivery.ClassUnsafeTarget:
		code = CodeUnsafeTarget
	default:
		code = CodeInternalError
	}
	return code, Retryable(code)
}

// notAccepted reports whether the provider certainly did not take the
// message: the connection failed, or the provider answered with a refusal
// (SMTP rejection reply, API error other than a server error).
func notAccepted(err error) bool {
	var de *delivery.Error
	if !errors.As(err, &de) {
		return false
	}
	switch de.Class {
	case delivery.ClassConnect, delivery.ClassRejected, delivery.ClassThrottled,
		delivery.ClassAuth, delivery.ClassSenderUnverified, delivery.ClassAPIRejected:
		return true
	}
	return false
}
