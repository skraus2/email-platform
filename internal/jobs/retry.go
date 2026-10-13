package jobs

import "time"

// RetryPolicy spaces dispatch retries: capped exponential backoff with
// "equal jitter". Retry n waits between d/2 and d, d = min(Max, Base·2^(n-1)).
// The guaranteed half keeps a recovering provider from being hammered; the
// random half spreads retries of many jobs that failed together.
type RetryPolicy struct {
	Base time.Duration
	Max  time.Duration
}

// Delay returns the wait before retry n (1 = first retry). rnd returns a
// number in [0, 1).
func (p RetryPolicy) Delay(n int, rnd func() float64) time.Duration {
	d := p.ceiling(n)
	half := d / 2
	return half + time.Duration(rnd()*float64(d-half))
}

// ceiling is min(Max, Base·2^(n-1)), computed without overflow.
func (p RetryPolicy) ceiling(n int) time.Duration {
	d := p.Base
	for i := 1; i < n && d < p.Max; i++ {
		d *= 2
	}
	return min(d, p.Max)
}

// WorstCase is the longest total wait of all retries of a job with
// maxAttempts attempts.
func (p RetryPolicy) WorstCase(maxAttempts int) time.Duration {
	var total time.Duration
	for n := 1; n < maxAttempts; n++ {
		total += p.ceiling(n)
	}
	return total
}

// AttemptBudget is how long one dispatch attempt may run: one provider call
// plus a few database round trips. It is the worker's job timeout.
func AttemptBudget(providerTimeout time.Duration) time.Duration {
	return providerTimeout + 30*time.Second
}
