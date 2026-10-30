package herdrcore_test

import (
	"errors"
	"maps"
	"os/exec"
	"slices"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

type sendParams struct {
	Terminal string `json:"terminal"`
	Text     string `json:"text"`
	Force    bool   `json:"force"`
}

func TestStartAgentTypesTheCommand(t *testing.T) {
	cases := []struct {
		kind string
		args []string
		want string
	}{
		{"codex", []string{"-m", "x"}, "codex --no-daemon -m x"},
		{"claude", []string{"--model", "y"}, "claude --model y"},
		{"claude", nil, "claude"},
		{"claude", []string{"say it's fine", ""}, `claude 'say it'\''s fine' ''`},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			b, srv := newBackend(t)
			srv.Add(liveTerminal("t1", "zsh"))
			if err := b.StartAgent(testContext(t), "t1", "placeholder", c.kind, c.args, 0); err != nil {
				t.Fatal(err)
			}
			sent := requestParams[sendParams](t, srv, "terminal.send")
			if len(sent) != 1 || sent[0].Terminal != "t1" || sent[0].Text != c.want || sent[0].Force {
				t.Fatalf("send params = %+v, want text %q", sent, c.want)
			}
		})
	}
}

func TestStartAgentRefusesUnknownKind(t *testing.T) {
	b, srv := newBackend(t)
	srv.Add(liveTerminal("t1", "zsh"))
	if err := b.StartAgent(testContext(t), "t1", "x", "rm", nil, 0); err == nil {
		t.Fatal("unknown kind was accepted")
	}
	if n := countRequests(srv, "terminal.send"); n != 0 {
		t.Fatalf("%d sends for an unknown kind", n)
	}
}

func TestOpenShell(t *testing.T) {
	b, srv := newBackend(t)
	ctx := testContext(t)

	created, err := b.CreateWorkspace(ctx, "/work/a", "ignored label")
	if err != nil {
		t.Fatal(err)
	}
	split, err := b.SplitPane(ctx, created.PaneID, "/work/b")
	if err != nil {
		t.Fatal(err)
	}
	if created.PaneID == "" || split.PaneID == "" || created.PaneID == split.PaneID {
		t.Fatalf("pane ids = %q, %q", created.PaneID, split.PaneID)
	}
	if created.Cwd != "/work/a" || split.Cwd != "/work/b" || created.Agent != "" {
		t.Fatalf("panes = %+v, %+v", created, split)
	}

	runs := requestParams[hc.RunParams](t, srv, "terminal.run")
	if len(runs) != 2 {
		t.Fatalf("terminal.run requests = %d", len(runs))
	}
	for i, cwd := range []string{"/work/a", "/work/b"} {
		r := runs[i]
		if r.Cwd != cwd || len(r.Argv) != 0 || r.Line != "" || !r.NoParent ||
			!maps.Equal(r.Env, map[string]string{"DISABLE_AUTO_UPDATE": "true"}) ||
			!maps.Equal(r.Labels, map[string]string{"sheepdog": "spawn"}) {
			t.Errorf("run %d params = %+v", i, r)
		}
	}
}

func TestProcessInfo(t *testing.T) {
	b, srv := newBackend(t)
	ctx := testContext(t)
	shellPID, childPID := 100, 200

	idle := liveTerminal("t1", "zsh")
	idle.PID = &shellPID
	idle.Foreground = &hc.Foreground{PID: shellPID, Name: "zsh", Argv: []string{"zsh"}}
	srv.Add(idle)

	busy := liveTerminal("t2", "zsh")
	busy.PID = &shellPID
	busy.Foreground = &hc.Foreground{PID: childPID, Name: "codex", Argv: []string{"codex", "--no-daemon"}}
	srv.Add(busy)

	srv.Add(liveTerminal("t3", "zsh"))

	info, err := b.ProcessInfo(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if info.PaneID != "t1" || info.ShellPID != 100 || info.ForegroundProcessGroup != 100 || !info.ShellForeground() {
		t.Fatalf("idle shell info = %+v", info)
	}

	info, err = b.ProcessInfo(ctx, "t2")
	if err != nil {
		t.Fatal(err)
	}
	if info.ShellForeground() || info.ForegroundProcessGroup != 200 || len(info.ForegroundProcesses) != 1 ||
		info.ForegroundProcesses[0].Name != "codex" ||
		!slices.Equal(info.ForegroundProcesses[0].Argv, []string{"codex", "--no-daemon"}) {
		t.Fatalf("busy shell info = %+v", info)
	}

	info, err = b.ProcessInfo(ctx, "t3")
	if err != nil || info.ShellForeground() || len(info.ForegroundProcesses) != 0 {
		t.Fatalf("no foreground info = %+v, %v", info, err)
	}
	if _, err := b.ProcessInfo(ctx, "t99"); !errors.Is(err, herdr.ErrPaneNotFound) {
		t.Fatalf("ProcessInfo on missing = %v", err)
	}
}

func TestAgentKindsOnPath(t *testing.T) {
	b, _ := newBackend(t)
	b.LookPath = func(file string) (string, error) {
		if file == "claude" || file == "codex" || file == "pi" {
			return "/bin/" + file, nil
		}
		return "", exec.ErrNotFound
	}
	kinds, err := b.AgentKinds(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"claude", "codex", "pi"}; !slices.Equal(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
}
