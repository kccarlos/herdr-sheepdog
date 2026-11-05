//go:build !darwin && !linux

package panelsync

import "os"

func swapDirs(string, string) error {
	return os.ErrInvalid
}
