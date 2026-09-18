//go:build windows

package state

import "os"

// syncFile and fullSyncFile: Windows has no macOS-style F_FULLFSYNC
// distinction, so both are a plain os.File.Sync.
func syncFile(f *os.File) error     { return f.Sync() }
func fullSyncFile(f *os.File) error { return f.Sync() }
