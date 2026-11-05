package files

import "testing"

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
