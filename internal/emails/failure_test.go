package emails

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/skraus2/email-platform/internal/delivery"
)

func TestFailureCodes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		err       error
		code      string
		retryable bool
	}{
		{"connect", &delivery.Error{Class: delivery.ClassConnect}, CodeProviderUnreachable, true},
		{"timeout", &delivery.Error{Class: delivery.ClassTimeout}, CodeProviderTimeout, true},
		{"protocol", &delivery.Error{Class: delivery.ClassProtocol}, CodeProviderProtocolError, true},
		{"rejected 421", &delivery.Error{Class: delivery.ClassRejected, Code: 421}, CodeProviderDeferred, true},
		{"rejected 452", &delivery.Error{Class: delivery.ClassRejected, Code: 452}, CodeProviderDeferred, true},
		{"rejected 550", &delivery.Error{Class: delivery.ClassRejected, Code: 550}, CodeProviderRejected, false},
		{"rejected without code", &delivery.Error{Class: delivery.ClassRejected}, CodeProviderRejected, false},
		{"rejected 2xx", &delivery.Error{Class: delivery.ClassRejected, Code: 250}, CodeProviderRejected, false},
		{"invalid", &delivery.Error{Class: delivery.ClassInvalid}, CodeMessageInvalid, false},
		{"unsafe target", &delivery.Error{Class: delivery.ClassUnsafeTarget}, CodeUnsafeTarget, false},
		{"throttled", &delivery.Error{Class: delivery.ClassThrottled, HTTPStatus: 429}, CodeProviderThrottled, true},
		{"auth", &delivery.Error{Class: delivery.ClassAuth, HTTPStatus: 403}, CodeProviderAuthFailed, false},
		{"sender unverified", &delivery.Error{Class: delivery.ClassSenderUnverified, HTTPStatus: 400}, CodeSenderNotVerified, false},
		{"api rejected", &delivery.Error{Class: delivery.ClassAPIRejected, HTTPStatus: 400}, CodeProviderRejected, false},
		{"api unavailable", &delivery.Error{Class: delivery.ClassAPIUnavailable, HTTPStatus: 503}, CodeProviderUnreachable, true},
		{"unknown class", &delivery.Error{Class: "something_new"}, CodeInternalError, true},
		{"wrapped", fmt.Errorf("send: %w", &delivery.Error{Class: delivery.ClassConnect}), CodeProviderUnreachable, true},
		{"not a delivery error", errors.New("boom"), CodeInternalError, true},
	}
	for _, tt := range tests {
		code, retryable := failureCode(tt.err)
		if code != tt.code || retryable != tt.retryable {
			t.Errorf("%s: failureCode = (%q, %v), want (%q, %v)", tt.name, code, retryable, tt.code, tt.retryable)
		}
		if !slices.Contains(FailureCodes, code) {
			t.Errorf("%s: code %q is not in FailureCodes", tt.name, code)
		}
		// Retryability of a delivery error must agree with the provider's
		// own classification.
		var de *delivery.Error
		if errors.As(tt.err, &de) && de.Class != "something_new" && retryable != de.Temporary() {
			t.Errorf("%s: retryable = %v but delivery.Error.Temporary() = %v", tt.name, retryable, de.Temporary())
		}
	}
}

func TestFailureCodesRetryable(t *testing.T) {
	t.Parallel()
	retryable := []string{CodeProviderUnreachable, CodeProviderTimeout, CodeProviderProtocolError, CodeProviderDeferred, CodeProviderThrottled, CodeInternalError}
	for _, c := range FailureCodes {
		if got, want := Retryable(c), slices.Contains(retryable, c); got != want {
			t.Errorf("Retryable(%q) = %v, want %v", c, got, want)
		}
	}
	if len(FailureCodes) != 19 {
		t.Errorf("FailureCodes has %d entries, want the 19 documented codes", len(FailureCodes))
	}
}

func TestNotAccepted(t *testing.T) {
	t.Parallel()
	// A provider answer that refuses the request means the message was not
	// taken; only connection-level and server-side failures leave the
	// outcome open.
	for class, want := range map[string]bool{
		delivery.ClassConnect: true, delivery.ClassRejected: true, delivery.ClassThrottled: true,
		delivery.ClassAuth: true, delivery.ClassSenderUnverified: true, delivery.ClassAPIRejected: true,
		delivery.ClassTimeout: false, delivery.ClassProtocol: false, delivery.ClassAPIUnavailable: false,
		delivery.ClassInvalid: false, delivery.ClassUnsafeTarget: false,
	} {
		if got := notAccepted(&delivery.Error{Class: class}); got != want {
			t.Errorf("notAccepted(%s) = %v, want %v", class, got, want)
		}
	}
	if notAccepted(errors.New("boom")) {
		t.Error("an unknown error counts as not accepted")
	}
}
