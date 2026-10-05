package state

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

func TestSpokenNames(t *testing.T) {
	ag := func(pane, kind, project, title string) Agent {
		return Agent{PaneID: pane, Kind: kind, WorkspaceLabel: project, Title: title, State: herdr.StateWorking, AliasAuto: true, Alias: Slug(project + "-" + kind + "-" + pane)}
	}
	exited := func(a Agent) Agent { a.State = herdr.StateUnknown; return a }
	named := func(a Agent, name string) Agent { a.Name = name; return a }
	pinned := func(a Agent, alias string) Agent { a.Alias, a.AliasAuto = alias, false; return a }
	cases := []struct {
		name   string
		agents []Agent
		want   map[string]string
	}{
		{"one of a kind", []Agent{ag("p1", "claude", "herdr-sheepdog", "README")}, map[string]string{"p1": "Claude"}},
		{"one of each kind", []Agent{ag("p1", "claude", "sheepdog", ""), ag("p2", "codex", "sheepdog", "")},
			map[string]string{"p1": "Claude", "p2": "Codex"}},
		{"two of a kind across projects", []Agent{ag("p1", "claude", "sheepdog", "README"), ag("p2", "claude", "docs-site", "")},
			map[string]string{"p1": "sheepdog Claude", "p2": "docs site Claude"}},
		{"two of a kind in one project, titles", []Agent{ag("p1", "claude", "sheepdog", "README"), ag("p2", "claude", "sheepdog", "Fix flaky test")},
			map[string]string{"p1": "README Claude", "p2": "Fix flaky test Claude"}},
		{"three of a kind, one project, one title", []Agent{ag("p1", "codex", "sheepdog", "Run suite and report failure"), ag("p2", "codex", "sheepdog", ""), ag("p3", "codex", "sheepdog", "")},
			map[string]string{"p1": "Run suite Codex", "p2": "sheepdog Codex 1", "p3": "sheepdog Codex 2"}},
		{"two left without a unique title, one with", []Agent{ag("p1", "codex", "sheepdog", "Docs"), ag("p2", "codex", "sheepdog", "")},
			map[string]string{"p1": "Docs Codex", "p2": "sheepdog Codex"}},
		{"same title twice falls back to numbers", []Agent{ag("p1", "claude", "sheepdog", "README"), ag("p2", "claude", "sheepdog", "readme")},
			map[string]string{"p1": "sheepdog Claude 1", "p2": "sheepdog Claude 2"}},
		{"three of a kind across two projects", []Agent{ag("p1", "claude", "sheepdog", "README"), ag("p2", "claude", "sheepdog", "Tests"), ag("p3", "claude", "docs", "x")},
			map[string]string{"p1": "README Claude", "p2": "Tests Claude", "p3": "docs Claude"}},
		{"long title capped", []Agent{ag("p1", "claude", "s", "one two three four five six seven eight"), ag("p2", "claude", "s", "")},
			map[string]string{"p1": "one two three Claude", "p2": "s Claude"}},
		{"exited agents ignored", []Agent{ag("p1", "claude", "sheepdog", "README"), exited(ag("p2", "claude", "sheepdog", "Tests"))},
			map[string]string{"p1": "Claude"}},
		{"CEO herdr name wins", []Agent{named(ag("p1", "claude", "sheepdog", "README"), "docs"), ag("p2", "claude", "sheepdog", "Tests")},
			map[string]string{"p1": "docs", "p2": "Claude"}},
		{"pinned alias is a CEO name", []Agent{pinned(ag("p1", "codex", "auth", ""), "auth"), ag("p2", "codex", "web", "")},
			map[string]string{"p1": "auth", "p2": "Codex"}},
		{"CEO name equal to a computed one", []Agent{named(ag("p1", "codex", "x", ""), "Claude"), ag("p2", "claude", "y", "")},
			map[string]string{"p1": "Claude", "p2": "y Claude"}},
		{"pinned name stays when a second of its kind arrives", []Agent{heard(ag("p1", "claude", "s", "README"), "Claude"), ag("p2", "claude", "s", "docs pass")},
			map[string]string{"p1": "Claude", "p2": "s Claude"}},
		{"pinned title name stays when the title changes", []Agent{heard(ag("p1", "claude", "s", "项目总结和核心功能"), "README Claude"), ag("p2", "claude", "s", "docs pass")},
			map[string]string{"p1": "README Claude", "p2": "Claude"}},
		{"agy and unknown kinds", []Agent{ag("p1", "agy", "x", ""), ag("p2", "pi", "x", "")},
			map[string]string{"p1": "Antigravity", "p2": "Pi"}},
		{"chinese title capped by runes", []Agent{ag("p1", "claude", "s", "修复语音延迟修复语音延迟修复语音延迟修复语音延迟修复语音延迟修复语音延迟修复语音延迟"), ag("p2", "claude", "s", "")},
			map[string]string{"p1": "修复语音延迟修复语音延迟修复语音延迟修复语音延迟修复语音延迟修复 Claude", "p2": "s Claude"}},
		{"no project", []Agent{ag("p1", "codex", "", "A"), ag("p2", "codex", "", "")},
			map[string]string{"p1": "A Codex", "p2": "Codex"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SpokenNames(c.agents)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for k, v := range c.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
				}
			}
		})
	}
}

// The store recomputes spoken names as agents come and go, and records
// the old name when one changes.
func TestStoreSpokenNameChanges(t *testing.T) {
	s, err := Open("", Options{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	tr := func(pane, to string) herdr.Transition {
		return herdr.Transition{PaneID: pane, To: herdr.State(to), Agent: "claude", WorkspaceID: "w1", WorkspaceLabel: "sheepdog"}
	}
	if _, _, err := s.Apply(tr("p1", "working")); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Get("p1")
	if a.Spoken != "Claude" || a.SpokenWas != "" {
		t.Fatalf("first agent: %+v", a)
	}
	if _, err := s.SetTitle("p1", " \"README\". "); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, _, err := s.Apply(tr("p2", "working")); err != nil {
		t.Fatal(err)
	}
	a, _ = s.Get("p1")
	if a.Title != "README" || a.Spoken != "README Claude" || a.SpokenWas != "Claude" || !a.SpokenChangedAt.Equal(now) {
		t.Fatalf("after a second claude: %+v", a)
	}
	b, _ := s.Get("p2")
	if b.Spoken != "sheepdog Claude" || b.SpokenWas != "" {
		t.Fatalf("second agent: %+v", b)
	}
	// The second one exits: the first is the only Claude again.
	if _, _, err := s.Apply(tr("p2", "unknown")); err != nil {
		t.Fatal(err)
	}
	a, _ = s.Get("p1")
	if a.Spoken != "Claude" || a.SpokenWas != "README Claude" {
		t.Fatalf("after the second exited: %+v", a)
	}
	b, _ = s.Get("p2")
	if b.Spoken != "sheepdog Claude" || Say(b) != "sheepdog Claude" {
		t.Fatalf("an exited agent keeps its last name: %+v", b)
	}
	// A CEO rename wins and the derived alias follows it.
	if err := s.SetName("p1", "docs"); err != nil {
		t.Fatal(err)
	}
	a, _ = s.Get("p1")
	if a.Spoken != "docs" || a.SpokenWas != "Claude" || a.Alias != "sheepdog-docs" || a.AliasAuto != true {
		t.Fatalf("after rename: %+v", a)
	}
	if _, _, err := s.Apply(tr("p2", "working")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetName("p2", "Docs"); err == nil {
		t.Error("a name another running agent has was accepted")
	}
	if err := s.SetName("p2", "  "); err == nil {
		t.Error("an empty name was accepted")
	}
}

// TestSpokenNamesFromRealTitles: the session titles two Claudes in one
// project really had in the tier-3 bench (finding 5: the names were
// "shop/cart.go Cart type Claude" and "TODO.md tasks Claude", and the
// brain fell back to "the first Claude"). Paths, file names and code
// identifiers are dropped from the spoken name.
func TestSpokenNamesFromRealTitles(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "herdr", "claude-session-titles.tier3.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	var runs [][][2]string
	cur := [][2]string{}
	for _, line := range strings.Split(string(b)+"\n", "\n") {
		switch {
		case strings.HasPrefix(line, "#"):
		case strings.TrimSpace(line) == "":
			if len(cur) > 0 {
				runs = append(runs, cur)
				cur = [][2]string{}
			}
		default:
			title, want, _ := strings.Cut(line, "\t")
			cur = append(cur, [2]string{title, want})
		}
	}
	if len(runs) != 3 {
		t.Fatalf("runs = %d", len(runs))
	}
	check := func(t *testing.T, rows [][2]string) {
		t.Helper()
		var agents []Agent
		for i, r := range rows {
			agents = append(agents, Agent{PaneID: "w1:p" + strconv.Itoa(i+1), Kind: "claude", WorkspaceLabel: "bench", Title: r[0], State: herdr.StateWorking, AliasAuto: true})
		}
		got := SpokenNames(agents)
		for i, r := range rows {
			if n := got["w1:p"+strconv.Itoa(i+1)]; n != r[1] {
				t.Errorf("title %q: spoken %q, want %q", r[0], n, r[1])
			}
		}
	}
	for _, run := range runs {
		check(t, run)
	}
	// Code identifiers go too; nothing speakable left numbers the agents.
	check(t, [][2]string{{"fix internal/herdr/prompt.go", "fix Claude"}, {"update CartItem addItem", "update Claude"}})
	check(t, [][2]string{{"shop/cart.go", "bench Claude 1"}, {"TODO.md", "bench Claude 2"}})
}

// HerdrName is herdr's spelling of a spoken name (agent.rename's rule).
func TestHerdrName(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"kvoice explainer", "kvoice-explainer", true},
		{"Front End", "front-end", true},
		{"docs_2", "docs-2", true},
		{"文档", "", false},
		{"2nd claude", "", false},
		{"", "", false},
		{"a very long name that goes on and on past the limit", "a-very-long-name-that-goes-on-an", true},
	} {
		got, ok := HerdrName(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("HerdrName(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// heard marks a row's spoken name as heard by the CEO (SpokenPinned).
func heard(a Agent, spoken string) Agent {
	a.Spoken, a.SpokenPinned = spoken, true
	return a
}
