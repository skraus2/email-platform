package emailplatform

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

const (
	maxRetriesLimit    = 5
	maxRetryAfter      = 60 * time.Second
	retryAfterJitter   = time.Second
	backoffBase        = 500 * time.Millisecond
	backoffCap         = 8 * time.Second
	defaultTimeout     = 30 * time.Second
	defaultMaxRetries  = 2
	maxBodyBytes       = 10 << 20
	maxMessageBytes    = 1024
	userAgent          = "email-platform-sdk-go"
	createEmailID      = "createEmail"
	createContactEvtID = "createContactEvent"
)

var retryAfterPattern = regexp.MustCompile(`^[0-9]{1,9}$`)

// retryAfter reads Retry-After in seconds; HTTP dates are not used.
func retryAfter(h http.Header) (time.Duration, bool) {
	raw := h.Get("Retry-After")
	if !retryAfterPattern.MatchString(raw) {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}

// backoff for retry n (0-based): exponential, half of it jittered.
func backoff(n int, random func() float64) time.Duration {
	ceiling := backoffCap
	if n < 5 {
		ceiling = min(backoffCap, backoffBase<<n)
	}
	return time.Duration(float64(ceiling) * (0.5 + 0.5*random()))
}

// wait before retry n: the server's Retry-After, clamped to 1–60 s, plus
// jitter so clients refused together do not come back together; without
// one, backoff.
func wait(n int, after time.Duration, sent bool, random func() float64) time.Duration {
	if !sent {
		return backoff(n, random)
	}
	after = min(maxRetryAfter, max(time.Second, after))
	return after + time.Duration(random()*float64(retryAfterJitter))
}

// retryable lists the operations a failure with unknown outcome may repeat:
// reads, and the two creates whose Idempotency-Key the API honours.
func retryable(op *operation) bool {
	return op.method == http.MethodGet || keyed(op)
}

func keyed(op *operation) bool {
	return op.id == createEmailID || op.id == createContactEvtID
}

// shouldRetry: 429 is refused before anything is stored, so any operation
// may repeat it. Other failures may have left the request applied; only
// operations that cannot apply twice are repeated.
func shouldRetry(op *operation, err error) bool {
	if e, ok := errors.AsType[*APIError](err); ok {
		if e.Status == http.StatusTooManyRequests {
			return true
		}
		unknown := e.Status == http.StatusInternalServerError ||
			e.Status == http.StatusBadGateway ||
			e.Status == http.StatusGatewayTimeout ||
			(e.Status == http.StatusServiceUnavailable && e.Code == "service_unavailable")
		return unknown && retryable(op)
	}
	if e, ok := errors.AsType[*TransportError](err); ok {
		return e.Reason == ReasonNetwork && retryable(op)
	}
	return false
}
