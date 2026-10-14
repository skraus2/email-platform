package events

// SetAfterInsert makes Ingest call f after inserting an event, before its
// transaction commits.
func SetAfterInsert(r *Recorder, f func()) { r.afterInsert = f }
