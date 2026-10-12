package overseer

// Purge clears all recorded checkpoints from the Overseer.
// It resets the checkpoint history while preserving the goal, constraints,
// rules, and pass threshold. After purging, the Overseer can continue to
// evaluate new reports with a clean slate.
func (o *Overseer) Purge() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.checkpoints = o.checkpoints[:0]
}
