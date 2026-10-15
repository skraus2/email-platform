//go:build integration

package ses

import "context"

// NewPollerWithCertFetch is NewPoller with the signing certificates served
// by fetch instead of the pinned SNS host. It exists only in integration
// test builds, so no binary can trust another certificate source.
func NewPollerWithCertFetch(ctx context.Context, c PollerConfig, fetch Fetcher) (*Poller, error) {
	p, err := NewPoller(ctx, c)
	if err != nil {
		return nil, err
	}
	p.verifier.fetch = fetch
	return p, nil
}
