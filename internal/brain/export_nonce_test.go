package brain

// SetNonceSource swaps the marker nonce generator for an external test and
// returns the restore function.
func SetNonceSource(f func() string) (restore func()) {
	old := nonceSource
	nonceSource = f
	return func() { nonceSource = old }
}
