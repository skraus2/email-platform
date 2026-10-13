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
	CodeProviderRejected      = "provider_rejected"
	CodeMessageInvalid        = "message_invalid"
	CodeUnsafeTarget          = "unsafe_target"
	CodeSuppressed            = "suppressed"
	CodeProjectDisabled       = "project_disabled"
	CodeContentMissing        = "content_missing"
	CodeExpired               = "expired"
	CodeInternalError         = "internal_error"
)

// FailureCodes lists every failure code.
var FailureCodes = []string{
	CodeProviderUnreachable, CodeProviderTimeout, CodeProviderProtocolError, CodeProviderDeferred,
	CodeProviderRejected, CodeMessageInvalid, CodeUnsafeTarget, CodeSuppressed, CodeProjectDisabled,
	CodeContentMissing, CodeExpired, CodeInternalError,
}

var retryableCodes = []string{
	CodeProviderUnreachable, CodeProviderTimeout, CodeProviderProtocolError, CodeProviderDeferred, CodeInternalError,
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
	case delivery.ClassInvalid:
		code = CodeMessageInvalid
	case delivery.ClassUnsafeTarget:
		code = CodeUnsafeTarget
	default:
		code = CodeInternalError
	}
	return code, Retryable(code)
}
