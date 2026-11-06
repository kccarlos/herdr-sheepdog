package procprio

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fakeLookPath(present ...string) func(string) (string, error) {
	set := map[string]string{}
	for _, p := range present {
		if strings.Contains(p, "/") {
			set[p] = p
		} else {
			set[p] = "/usr/bin/" + p
		}
	}
	return func(file string) (string, error) {
		if got, ok := set[file]; ok {
			return got, nil
		}
		return "", errors.New("not found")
	}
}

func TestWrapArgvForOS(t *testing.T) {
	// Darwin with taskpolicy on PATH wraps an existing command in utility QoS,
	// never background (-b).
	got := WrapArgvForOS("darwin", fakeLookPath("taskpolicy", "/bin/sh"), []string{"/bin/sh", "-c", "echo hi"})
	want := []string{"/usr/bin/taskpolicy", "-c", "utility", "/bin/sh", "-c", "echo hi"}
	if !slices.Equal(got, want) {
		t.Errorf("darwin PATH taskpolicy = %v, want %v", got, want)
	}

	// Darwin under launchd (where /usr/sbin may be absent from PATH) falls
	// back to /usr/sbin/taskpolicy.
	got = WrapArgvForOS("darwin", fakeLookPath("/usr/sbin/taskpolicy", "codex"), []string{"codex", "exec"})
	want = []string{"/usr/sbin/taskpolicy", "-c", "utility", "codex", "exec"}
	if !slices.Equal(got, want) {
		t.Errorf("darwin /usr/sbin/taskpolicy = %v, want %v", got, want)
	}

	// Missing target binary is NOT wrapped so exec.Command.Start still fails
	// with a start error instead of running taskpolicy and exiting 1.
	orig := []string{"/nonexistent/codex", "exec"}
	got = WrapArgvForOS("darwin", fakeLookPath("/usr/sbin/taskpolicy"), orig)
	if !slices.Equal(got, orig) {
		t.Errorf("missing binary wrapped = %v, want %v", got, orig)
	}

	// Linux raises oom_score_adj before the target program runs: setting it
	// after Start races a fast child that reads its own value first.
	got = WrapArgvForOS("linux", fakeLookPath("/bin/sh", "codex"), []string{"codex", "exec"})
	if len(got) < 4 || got[0] != "/bin/sh" || got[1] != "-c" || !strings.Contains(got[2], "oom_score_adj") ||
		!slices.Equal(got[len(got)-2:], []string{"codex", "exec"}) {
		t.Errorf("linux WrapArgvForOS = %v, want an sh wrapper that sets oom_score_adj then execs codex exec", got)
	}
	if got = WrapArgvForOS("linux", fakeLookPath("/bin/sh"), []string{"codex", "exec"}); !slices.Equal(got, []string{"codex", "exec"}) {
		t.Errorf("linux missing binary wrapped = %v, want unchanged", got)
	}
}

func TestShellPrefixForOS(t *testing.T) {
	cases := []struct {
		name string
		goos string
		bins []string
		want []string
	}{
		{
			name: "darwin with nice and taskpolicy on PATH keeps resolved taskpolicy path",
			goos: "darwin",
			bins: []string{"nice", "taskpolicy"},
			want: []string{"nice", "-n", "10", "/usr/bin/taskpolicy", "-c", "utility"},
		},
		{
			name: "darwin with /usr/sbin/taskpolicy fallback",
			goos: "darwin",
			bins: []string{"nice", "/usr/sbin/taskpolicy"},
			want: []string{"nice", "-n", "10", "/usr/sbin/taskpolicy", "-c", "utility"},
		},
		{
			name: "linux with nice and ionice uses -t so restricted containers still run",
			goos: "linux",
			bins: []string{"nice", "ionice"},
			want: []string{"nice", "-n", "10", "ionice", "-c", "2", "-n", "7", "-t"},
		},
		{
			name: "linux with nice only",
			goos: "linux",
			bins: []string{"nice"},
			want: []string{"nice", "-n", "10"},
		},
		{
			name: "no tools available",
			goos: "darwin",
			bins: nil,
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ShellPrefixForOS(tc.goos, fakeLookPath(tc.bins...))
			if !slices.Equal(got, tc.want) {
				t.Errorf("ShellPrefixForOS = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnwrapArgv(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{
			in:   []string{"nice", "-n", "10", "/usr/sbin/taskpolicy", "-c", "utility", "codex", "--no-daemon"},
			want: []string{"codex", "--no-daemon"},
		},
		{
			in:   []string{"/usr/bin/nice", "-n", "10", "taskpolicy", "-c", "utility", "/opt/homebrew/bin/claude", "--resume"},
			want: []string{"/opt/homebrew/bin/claude", "--resume"},
		},
		{
			in:   []string{"/usr/sbin/taskpolicy", "-c", "utility", "codex", "--no-daemon"},
			want: []string{"codex", "--no-daemon"},
		},
		{
			in:   []string{"nice", "-n", "10", "ionice", "-c", "2", "-n", "7", "-t", "claude"},
			want: []string{"claude"},
		},
		{
			in:   []string{"ionice", "-c", "2", "-n", "7", "-t", "codex", "--no-daemon"},
			want: []string{"codex", "--no-daemon"},
		},
		{
			in:   []string{"claude", "--model", "sonnet"},
			want: []string{"claude", "--model", "sonnet"},
		},
		{
			in:   []string{"nice", "-n", "10"},
			want: nil,
		},
	}
	for _, tc := range cases {
		got := UnwrapArgv(tc.in)
		if !slices.Equal(got, tc.want) {
			t.Errorf("UnwrapArgv(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestWriteOOMScoreAdj(t *testing.T) {
	proc := t.TempDir()
	pidDir := filepath.Join(proc, "4242")
	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		t.Fatal(err)
	}
	adjFile := filepath.Join(pidDir, "oom_score_adj")
	if err := os.WriteFile(adjFile, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOOMScoreAdj(proc, 4242, OOMScoreAdj); err != nil {
		t.Fatalf("writeOOMScoreAdj: %v", err)
	}
	b, err := os.ReadFile(adjFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "500" {
		t.Errorf("oom_score_adj = %q, want 500", string(b))
	}

	// Never lower an already higher OOM score.
	if err := os.WriteFile(adjFile, []byte("800\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeOOMScoreAdj(proc, 4242, OOMScoreAdj); err != nil {
		t.Fatalf("writeOOMScoreAdj higher: %v", err)
	}
	b, _ = os.ReadFile(adjFile)
	if strings.TrimSpace(string(b)) != "800" {
		t.Errorf("higher oom_score_adj was lowered to %q", string(b))
	}
}
