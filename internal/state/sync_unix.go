//go:build !windows

package state

import (
	"os"
	"syscall"
)

// syncFile flushes f's dirty pages to the OS via a plain fsync(2). This
// is the default per-persist sync: cheap everywhere, and on Darwin it
// deliberately bypasses os.File.Sync, whose internal/poll implementation
// transparently upgrades to F_FULLFSYNC (a full hardware cache flush,
// ~0.5s observed on this machine) — see fullSyncFile for that path,
// which state.full_fsync opts into.
func syncFile(f *os.File) error {
	return syscall.Fsync(int(f.Fd()))
}

// fullSyncFile is the hardware-durable sync: F_FULLFSYNC on Darwin (via
// os.File.Sync), a plain fsync(2) elsewhere (already durable enough on
// Linux, where there is no F_FULLFSYNC distinction).
func fullSyncFile(f *os.File) error {
	return f.Sync()
}
