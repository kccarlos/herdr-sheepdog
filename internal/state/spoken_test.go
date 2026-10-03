package state

import (
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
			map[string]string{"p1": "Run suite and report failure Codex", "p2": "sheepdog Codex 1", "p3": "sheepdog Codex 2"}},
		{"two left without a unique title, one with", []Agent{ag("p1", "codex", "sheepdog", "Docs"), ag("p2", "codex", "sheepdog", "")},
			map[string]string{"p1": "Docs Codex", "p2": "sheepdog Codex"}},
		{"same title twice falls back to numbers", []Agent{ag("p1", "claude", "sheepdog", "README"), ag("p2", "claude", "sheepdog", "readme")},
			map[string]string{"p1": "sheepdog Claude 1", "p2": "sheepdog Claude 2"}},
		{"three of a kind across two projects", []Agent{ag("p1", "claude", "sheepdog", "README"), ag("p2", "claude", "sheepdog", "Tests"), ag("p3", "claude", "docs", "x")},
			map[string]string{"p1": "README Claude", "p2": "Tests Claude", "p3": "docs Claude"}},
		{"long title capped", []Agent{ag("p1", "claude", "s", "one two three four five six seven eight"), ag("p2", "claude", "s", "")},
			map[string]string{"p1": "one two three four five six Claude", "p2": "s Claude"}},
		{"exited agents ignored", []Agent{ag("p1", "claude", "sheepdog", "README"), exited(ag("p2", "claude", "sheepdog", "Tests"))},
			map[string]string{"p1": "Claude"}},
		{"CEO herdr name wins", []Agent{named(ag("p1", "claude", "sheepdog", "README"), "docs"), ag("p2", "claude", "sheepdog", "Tests")},
			map[string]string{"p1": "docs", "p2": "Claude"}},
		{"pinned alias is a CEO name", []Agent{pinned(ag("p1", "codex", "auth", ""), "auth"), ag("p2", "codex", "web", "")},
			map[string]string{"p1": "auth", "p2": "Codex"}},
		{"CEO name equal to a computed one", []Agent{named(ag("p1", "codex", "x", ""), "Claude"), ag("p2", "claude", "y", "")},
			map[string]string{"p1": "Claude", "p2": "Claude 2"}},
		{"agy and unknown kinds", []Agent{ag("p1", "agy", "x", ""), ag("p2", "pi", "x", "")},
			map[string]string{"p1": "Antigravity", "p2": "Pi"}},
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
