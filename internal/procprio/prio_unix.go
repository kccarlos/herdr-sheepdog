//go:build darwin || linux

package procprio

import "syscall"

// ApplyProcessGroup applies best-effort OS scheduling and OOM adjustments to a
// newly started process group leader. Errors (such as EPERM inside a restricted
// sandbox or container) are ignored so child execution never fails.
func ApplyProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Setpriority(syscall.PRIO_PGRP, pid, NiceDelta)
	applyOSExtra(pid)
}
