package voice

// Current returns the running provider session (nil before Start).
func (sw *Switcher) Current() Session {
	sw.mu.RLock()
	defer sw.mu.RUnlock()
	return sw.inner
}
