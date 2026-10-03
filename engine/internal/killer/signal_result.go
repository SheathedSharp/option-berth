package killer

// Signal acceptance is an event, not a planned method or final kill row. An
// interrupted escalation can turn an OK row into an error after SIGTERM was
// accepted; a failed initial signal can carry the same method without that
// acceptance. Notify at the adapter boundary instead of guessing from rows.
func (e *engine) signalResult(u *unit, err error) error {
	if err == nil && e.onSignal != nil && u.root > 0 {
		e.onSignal(u.root)
	}
	return err
}
