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

// Compiles reports how many stored template versions were compiled.
func (s *Service) Compiles() int64 { return s.compiles.Load() }

// SetRenderLimits overrides the template render limits for tests.
func (s *Service) SetRenderLimits(slots int, wait time.Duration) {
	s.renderSlots = make(chan struct{}, slots)
	s.renderWait = wait
}

// HoldRenderSlot occupies one render slot until the returned func is called.
func (s *Service) HoldRenderSlot() func() {
	s.renderSlots <- struct{}{}
	return func() { <-s.renderSlots }
}

// SetAfterReconcileRead makes the dispatcher call f after it read the proof
// of an earlier attempt, before it acts on it.
func SetAfterReconcileRead(d *Dispatcher, f func()) { d.afterReconcileRead = f }
