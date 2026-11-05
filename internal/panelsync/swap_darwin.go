//go:build darwin

package panelsync

import "golang.org/x/sys/unix"

func swapDirs(stage, dst string) error {
	return unix.RenamexNp(stage, dst, unix.RENAME_SWAP)
}
