//go:build linux

package panelsync

import "golang.org/x/sys/unix"

func swapDirs(stage, dst string) error {
	return unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, dst, unix.RENAME_EXCHANGE)
}
