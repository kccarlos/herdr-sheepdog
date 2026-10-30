package hc

import (
	"os"
	"path/filepath"
	"strconv"
)

// DefaultSocket resolves the Unix socket using herdr-core's endpoint rules.
//
// getenv supplies environment variables and home the user's home directory,
// so the function reads neither itself; isDir probes /run/user/<uid>
// (nil: os.Stat).
func DefaultSocket(getenv func(string) string, home string, uid int, isDir func(string) bool) string {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if socket := getenv("HC_SOCKET"); socket != "" {
		return socket
	}

	session := getenv("HC_SESSION")
	if session == "" {
		session = "default"
	}
	if runtimeDir := getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "herdr-core", session+".sock")
	}

	if isDir == nil {
		isDir = isDirectory
	}
	runDir := filepath.Join("/run/user", strconv.Itoa(uid), "herdr-core")
	if isDir(runDir) {
		return filepath.Join(runDir, session+".sock")
	}

	stateHome := getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "herdr-core", session, session+".sock")
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
