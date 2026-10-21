// Package identity is this computer's identity for the apps: the daemon
// id (docs/dev/design/security.md "The daemon's identity"), made once and
// kept in the data directory, and the computer's name.
package identity

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Computer is what GET /api/hello says about this computer.
type Computer struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// idPattern is a daemon id: 16 random bytes, base64url without padding.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{22}$`)

// DaemonID returns the daemon id in <dataDir>/tls/daemon_id, making it
// on first use (the directory 0700, the file 0600). A file that does not
// hold an id is an error, never silently replaced: the apps know the
// computer by it.
func DaemonID(dataDir string) (string, error) {
	dir := filepath.Join(dataDir, "tls")
	path := filepath.Join(dir, "daemon_id")
	if b, err := os.ReadFile(path); err == nil {
		id := strings.TrimSpace(string(b))
		if !idPattern.MatchString(id) {
			return "", fmt.Errorf("identity: %s does not hold a daemon id", path)
		}
		return id, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("identity: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("identity: %w", err)
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("identity: %w", err)
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		// Another start made it first: use that one.
		return DaemonID(dataDir)
	}
	if err != nil {
		return "", fmt.Errorf("identity: %w", err)
	}
	if _, err := f.WriteString(id + "\n"); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("identity: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("identity: %w", err)
	}
	return id, nil
}

// Name is the computer's name: configured (computer.name) when set, else
// the system's (macOS ComputerName, else the host name).
func Name(configured string) string {
	if n := strings.TrimSpace(configured); n != "" {
		return n
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("scutil", "--get", "ComputerName").Output(); err == nil {
			if n := strings.TrimSpace(string(out)); n != "" {
				return n
			}
		}
	}
	if h, err := os.Hostname(); err == nil {
		return strings.TrimSuffix(strings.TrimSpace(h), ".local")
	}
	return "This computer"
}
