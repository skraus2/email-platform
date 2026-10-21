package broadcasts

// SetBatch overrides the batch size for tests.
func (f *Fanout) SetBatch(n int) { f.batch = n }
