package files

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A workspace or agent can sit in $HOME, so the folders and files where
// tools keep tokens and history are hidden like .ssh.
func TestHomeSecretsAreSensitive(t *testing.T) {
	for _, name := range []string{
		".codex", ".claude", "Library", ".credentials.json", "auth.json", ".gcloud",
		".bash_history", ".zsh_history", ".python_history", ".psql_history",
		".mysql_history", ".node_repl_history", ".CODEX",
	} {
		if !Sensitive(name) {
			t.Errorf("Sensitive(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"config", "local", "cache", ".config", ".local", ".cache", "claude.md", "history.txt"} {
		if Sensitive(name) {
			t.Errorf("Sensitive(%q) = true, want false", name)
		}
	}
}

// Home, folders above home, and private daemon folders each get their own
// error instead of ErrSensitive, including through symlinks.
func TestCheckRootReasons(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	project := filepath.Join(home, "project")
	sshDir := filepath.Join(home, ".ssh")
	data := filepath.Join(base, "var", "sheepdog")
	dataSub := filepath.Join(data, "brain")
	for _, d := range []string{project, sshDir, dataSub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	homeLink := filepath.Join(base, "home-link")
	if err := os.Symlink(home, homeLink); err != nil {
		t.Fatal(err)
	}
	dataLink := filepath.Join(home, "data-link")
	if err := os.Symlink(data, dataLink); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	tests := []struct {
		dir  string
		want error
	}{
		{home, ErrHomeRoot},
		{filepath.Dir(home), ErrHomeRoot},
		{homeLink, ErrHomeRoot},
		{data, ErrPrivateRoot},
		{dataSub, ErrPrivateRoot},
		{filepath.Join(data, "missing"), ErrPrivateRoot},
		{filepath.Dir(data), ErrPrivateRoot},
		{dataLink, ErrPrivateRoot},
		{sshDir, ErrSensitive},
		{project, nil},
	}
	for _, tc := range tests {
		if err := CheckRoot(tc.dir, data); !errors.Is(err, tc.want) {
			t.Errorf("CheckRoot(%q) = %v, want %v", tc.dir, err, tc.want)
		}
	}

	if r, err := Open(home); !errors.Is(err, ErrHomeRoot) {
		if r != nil {
			_ = r.Close()
		}
		t.Fatalf("Open(home) = %v, want ErrHomeRoot", err)
	}
	r, err := Open(project)
	if err != nil {
		t.Fatalf("Open(project): %v", err)
	}
	_ = r.Close()
}
