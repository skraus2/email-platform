package emails

import "time"

// SetListLimits overrides the list query limits for tests.
func (s *Service) SetListLimits(slots int, wait, timeout time.Duration) {
	s.listSlots = make(chan struct{}, slots)
	s.listWait, s.listTimeout = wait, timeout
}

// HoldListSlot occupies one list slot until the returned func is called.
func (s *Service) HoldListSlot() func() {
	s.listSlots <- struct{}{}
	return func() { <-s.listSlots }
}
