//go:build linux

package procprio

import (
	"runtime"
	"syscall"
)

func applyOSExtra(pid int) {
	_ = writeOOMScoreAdj("/proc", pid, OOMScoreAdj)
	setIOPrioBestEffortLow(pid)
}

func setIOPrioBestEffortLow(pid int) {
	var trap uintptr
	switch runtime.GOARCH {
	case "amd64":
		trap = 251 // __NR_ioprio_set on x86_64
	case "arm64":
		trap = 30 // __NR_ioprio_set on aarch64
	default:
		return
	}
	const (
		ioprioWhoPgrp = 2
		ioprioClassBE = 2
		ioprioLowest  = (ioprioClassBE << 13) | 7
	)
	_, _, _ = syscall.Syscall(trap, ioprioWhoPgrp, uintptr(pid), ioprioLowest)
}
