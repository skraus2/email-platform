package templates

import "time"

// SetWriteLimits overrides the write limits for tests.
func (s *Store) SetWriteLimits(slots int, wait, timeout time.Duration) {
	s.writeSlots = make(chan struct{}, slots)
	s.writeWait, s.writeTimeout = wait, timeout
}

// HoldWriteSlot occupies one write slot until the returned func is called.
func (s *Store) HoldWriteSlot() func() {
	s.writeSlots <- struct{}{}
	return func() { <-s.writeSlots }
}
