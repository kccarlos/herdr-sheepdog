//go:build !darwin && !linux

package procprio

// ApplyProcessGroup is a no-op on unsupported operating systems.
func ApplyProcessGroup(_ int) {}
