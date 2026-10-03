package tier3

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

func TestScenarioFilesLoad(t *testing.T) {
	scs, err := LoadGlob("../../../testdata/bench/tier3/*.json", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(scs) < 10 {
		t.Fatalf("%d scenarios, want at least 10", len(scs))
	}
	codex := 0
	for _, s := range scs {
		for _, w := range s.Workers {
			if w.Kind == KindCodex {
				codex++
				break
			}
		}
	}
	// Codex is on a scarce free plan: at most two scenarios use it.
	if codex > 2 {
		t.Fatalf("%d scenarios use Codex, want at most 2", codex)
	}
}

func TestValidateRejects(t *testing.T) {
	base := func() Scenario {
		return Scenario{ID: "x", Goal: "g", Workers: []Worker{{Alias: "a", Workspace: "w", Kind: KindClaude}}}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("base: %v", err)
	}
	for name, mut := range map[string]func(*Scenario){
		"no workers":     func(s *Scenario) { s.Workers = nil },
		"bad kind":       func(s *Scenario) { s.Workers[0].Kind = "gemini" },
		"bad trust":      func(s *Scenario) { s.Workers[0].Trust = "maybe" },
		"no workspace":   func(s *Scenario) { s.Workers[0].Workspace = "" },
		"unknown setup":  func(s *Scenario) { s.Setup = []Step{{Worker: "b"}} },
		"bad wait":       func(s *Scenario) { s.Setup = []Step{{Worker: "a", WaitFor: []string{"busy"}}} },
		"bad regexp":     func(s *Scenario) { s.Expect.Events = []EventCheck{{Type: "x", Matches: "("}} },
		"screen worker":  func(s *Scenario) { s.Expect.Screens = []ScreenCheck{{Worker: "b", Matches: "x"}} },
		"tier":           func(s *Scenario) { s.Tier = 2 },
		"no goal":        func(s *Scenario) { s.Goal = "" },
		"action unknown": func(s *Scenario) { s.Expect.Actions = []eval.SimAction{{Kind: "keys", Agent: "zz"}} },
	} {
		s := base()
		mut(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestHerdrRefusesDefaultSession(t *testing.T) {
	for _, name := range []string{"", "default"} {
		h := &Herdr{Bin: "/nonexistent/herdr", Session: name}
		if _, err := h.run(t.Context(), "pane", "list"); err != errDefaultSession {
			t.Errorf("session %q: err = %v", name, err)
		}
		if err := h.Stop(); err != errDefaultSession {
			t.Errorf("stop %q: err = %v", name, err)
		}
	}
}

func TestParseSessionSocket(t *testing.T) {
	list := "name                 status   directory                                        socket\n" +
		"default              running  /Users/x/.config/herdr                          /Users/x/.config/herdr/herdr.sock\n" +
		"sdbench-ab12cd       running  /Users/x/.config/herdr/sessions/sdbench-ab12cd   /Users/x/.config/herdr/sessions/sdbench-ab12cd/herdr.sock\n" +
		"old                  stopped  /Users/x/.config/herdr/sessions/old              /Users/x/.config/herdr/sessions/old/herdr.sock\n"
	got, err := parseSessionSocket(list, "sdbench-ab12cd")
	if err != nil || got != "/Users/x/.config/herdr/sessions/sdbench-ab12cd/herdr.sock" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := parseSessionSocket(list, "old"); err == nil {
		t.Fatal("a stopped session must not count")
	}
}

func TestEventLines(t *testing.T) {
	start := time.Now()
	w := &world{start: start, aliasOf: map[string]string{"w1:p1": "bench-claude"}, pushes: make(chan eval.SimLine, 8)}
	ev := func(typ, pane string, p map[string]any) Event {
		b, _ := json.Marshal(p)
		return Event{ID: 1, TS: start.Add(2 * time.Second), Type: typ, PaneID: pane, Payload: b}
	}
	w.event(ev("voice.push", "", map[string]any{"text": "names line", "urgency": "silent"}))
	w.event(ev("voice.push", "w1:p1", map[string]any{"text": "Claude asks to run go test.", "urgency": "interrupt"}))
	w.event(ev("herdr.send", "w1:p1", map[string]any{"kind": "prompt", "text": "fix the test"}))
	w.event(ev("herdr.send", "w1:p1", map[string]any{"kind": "keys", "keys": []string{"ctrl+c"}}))
	w.event(ev("herdr.send", "w1:p1", map[string]any{"kind": "keys", "keys": []string{"Down", "Enter"}}))
	w.event(ev("herdr.transition", "w1:p1", map[string]any{"from": "working", "to": "blocked"}))
	pushes := w.drain()
	if len(pushes) != 1 || pushes[0].Who != "push" || pushes[0].AtMS != 2000 {
		t.Fatalf("pushes = %+v", pushes)
	}
	if len(w.actions) != 3 {
		t.Fatalf("actions = %+v", w.actions)
	}
	kinds := []string{w.actions[0].Kind, w.actions[1].Kind, w.actions[2].Kind}
	if strings.Join(kinds, ",") != "prompt,interrupt,keys" || w.actions[2].Detail != "Down Enter" || w.actions[0].Agent != "bench-claude" {
		t.Fatalf("actions = %+v", w.actions)
	}
	fs := eval.ActionFailures(eval.SimExpect{Actions: []eval.SimAction{{Kind: "interrupt", Agent: "bench-claude"}, {Kind: "keys", Agent: "bench-claude", Matches: "Enter"}}}, w.actions)
	if len(fs) != 0 {
		t.Fatalf("failures = %+v", fs)
	}
	if len(w.timeline) != 1 || !strings.Contains(w.timeline[0].Text, "working → blocked") {
		t.Fatalf("timeline = %+v", w.timeline)
	}
}

func TestCommandLine(t *testing.T) {
	w := &world{o: Options{CodexEffort: "low"}}
	got := w.command(&worker{Worker: Worker{Kind: KindClaude, Model: "haiku", Args: []string{"--allowedTools", "Bash(go test:*)"}}})
	if got != `claude --model haiku --allowedTools 'Bash(go test:*)'` {
		t.Fatalf("claude: %s", got)
	}
	got = w.command(&worker{Worker: Worker{Kind: KindCodex, Model: "gpt-6-luna"}})
	if got != "codex -m gpt-6-luna -c model_reasoning_effort=low" {
		t.Fatalf("codex: %s", got)
	}
}

func TestTrustPrompt(t *testing.T) {
	claude := " Quick safety check: Is this a project you created or one you trust?\n ❯ No, exit\n   Yes, I trust this folder\n"
	if !trustPrompt.MatchString(claude) {
		t.Fatal("claude trust prompt not matched")
	}
	if k := trustKeys(KindClaude, claude); strings.Join(k, " ") != "Down Enter" {
		t.Fatalf("keys = %v", k)
	}
}

func TestUpdatePrompt(t *testing.T) {
	screen := "✨ Update available! 0.158.0 -> 0.160.0\n› 1. Update now (runs the installer)\n  2. Skip\n  3. Skip until next version\n"
	if !updatePrompt.MatchString(screen) {
		t.Fatal("codex update menu not matched")
	}
	if updatePrompt.MatchString("❯ Try \"create a util logging.py that...\"") {
		t.Fatal("an idle Claude Code matched")
	}
}

func TestChecks(t *testing.T) {
	w := &world{sc: Scenario{Expect: Expect{
		Events:  []EventCheck{{Type: "pending.created"}, {Type: "voice.push", Matches: "Type something", Absent: true}},
		Screens: []ScreenCheck{{Worker: "a", Matches: "readme"}},
	}}}
	evs := []Event{{Type: "pending.created", Payload: json.RawMessage(`{}`)}, {Type: "voice.push", Payload: json.RawMessage(`{"text":"Pick one: README or CLI."}`)}}
	lines, fails := w.checks(t.Context(), evs, map[string]string{"a": "You picked Write a README"})
	if len(fails) != 0 || len(lines) != 3 {
		t.Fatalf("lines %v fails %v", lines, fails)
	}
	evs = append(evs, Event{Type: "voice.push", Payload: json.RawMessage(`{"text":"Options: Type something."}`)})
	_, fails = w.checks(t.Context(), evs, map[string]string{"a": "nothing"})
	if len(fails) != 2 {
		t.Fatalf("fails %v", fails)
	}
}

func TestFilterEnvFile(t *testing.T) {
	dir := t.TempDir()
	src := dir + "/src.env"
	body := "GEMINI_API_KEY=g\nVERTEX_API_KEY=v\nexport VERTEX_PROJECT=p\nOPENAI_API_KEY=o\n# DEEPSEEK_API_KEY=c\nDEEPSEEK_API_KEY=d\n"
	if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	dst, err := filterEnvFile(src, dir+"/dst.env")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	if got := string(b); got != "VERTEX_API_KEY=v\nexport VERTEX_PROJECT=p\nDEEPSEEK_API_KEY=d\n" {
		t.Fatalf("filtered = %q", got)
	}
	env := scrubEnv([]string{"PATH=/bin", "SHEEPDOG_WEB_PORT=7777", "GEMINI_API_KEY=g", "GEMINI_API_KEY_2=h", "OPENAI_API_KEY=o", "HOME=/h"})
	if strings.Join(env, ",") != "PATH=/bin,HOME=/h" {
		t.Fatalf("scrubbed = %v", env)
	}
}

// The scratch daemon may start agents only in the run's own clones,
// never under the config default (~/Documents).
func TestProjectRootsStayInTheRun(t *testing.T) {
	if got := projectRoots(DaemonOptions{Dir: "/x/run-1/daemon"}); got != "/x/run-1/repos" {
		t.Fatalf("project roots = %q", got)
	}
	if got := projectRoots(DaemonOptions{Dir: "/x/run-1/daemon", ProjectRoots: "/y"}); got != "/y" {
		t.Fatalf("project roots = %q", got)
	}
}
