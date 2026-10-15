package admin

import "time"

// UseParams lowers the hashing cost for tests (the dummy hash follows).
func (s *Service) UseParams(p Params) { s.params = p }

// Hashes reports how many password hashes were computed.
func (s *Service) Hashes() int64 { return s.hashes.Load() }

// SetHashLimits overrides the concurrent-hash limits for tests.
func (s *Service) SetHashLimits(slots int, wait time.Duration) {
	s.hashSlots = make(chan struct{}, slots)
	s.hashWait = wait
}

// HoldHashSlot occupies one hash slot until the returned func is called.
func (s *Service) HoldHashSlot() func() {
	s.hashSlots <- struct{}{}
	return func() { <-s.hashSlots }
}
