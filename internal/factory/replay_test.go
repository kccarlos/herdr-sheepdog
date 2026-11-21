package factory

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// The template tests of prompts.md 7 and the replay of
// testdata/replay/situations.json. No model is in the loop: a situation
// passes when one line (one rule) of the filled prompt holds its `run` and
// `says` and none of its `never`. Each template rule is one line.

type situation struct {
	ID, Role, Notes, Message string
	Run, Says, Never         []string
}

var templates = map[string]*string{"coordinator": &coordinatorTemplate, "worker": &workerTemplate, "reviewer": &reviewerTemplate}

// filled builds role's prompt from tmpl through the engine's own fill, on a
// fixed task whose round 1 at c1 was rejected (so the reviewer gets R2's facts).
func filled(role, tmpl string) string {
	t := flow.Task{ID: "T1", Title: "feat: add Greet", Brief: "Add Greet(name).", NotDoing: "localization",
		Outcomes: []flow.Outcome{{Text: "Greet works", ProvedBy: "cmd: go test ./..."}}}
	a := flow.Attempt{Worktree: "/wt/T1", Branch: "factory/T1", Review: &flow.ReviewRun{Code: "abc123"},
		Rounds:  []flow.Round{{N: 1, Kind: flow.RoundFull, Head: "c1", Verdict: flow.VerdictChanges, Majors: 1, ReportPath: "/r/1.md"}},
		Rejects: []flow.Reject{{Head: "c1", Round: 1, Why: "the cache key is right"}}}
	switch role {
	case "coordinator":
		return coordinatorPrompt(tmpl, flow.Project{Folder: "/repo", Goal: "Add a greeting."}, "sheepdog")
	case "worker":
		return workerPrompt(tmpl, t, a, "sheepdog")
	}
	e := &Engine{}
	return e.reviewerPrompt(context.Background(), tmpl, t, a, "c1", flow.RoundFull, "git diff b..c1", e.rejectionPrompt("p", "T1", a, "c1"), 2)
}

func loadSituations(t *testing.T) []situation {
	t.Helper()
	raw, err := os.ReadFile("testdata/replay/situations.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ Situations []situation }
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f.Situations
}

// held reports whether one line of prompt holds s.
func held(s situation, prompt string) bool {
	for _, line := range strings.Split(prompt, "\n") {
		has := func(w string) bool { return strings.Contains(line, w) }
		if !slices.ContainsFunc(slices.Concat(s.Run, s.Says), func(w string) bool { return !has(w) }) && !slices.ContainsFunc(s.Never, has) {
			return true
		}
	}
	return false
}

func failing(sits []situation, role, tmpl string) []string {
	var ids []string
	for _, s := range sits {
		if s.Role == role && !held(s, filled(role, tmpl)) {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

func TestReplaySituationsPass(t *testing.T) {
	sits := loadSituations(t)
	for role, tmpl := range templates {
		if ids := failing(sits, role, *tmpl); len(ids) > 0 {
			t.Errorf("%s: situations fail: %v", role, ids)
		}
	}
}

// TestReplayCanFail deletes each template line in turn from a copy: every
// situation a template holds fails when exactly its line goes, and that
// deletion fails no other situation. R2's rule is the engine's rejection
// block, so only deleting the line with {rejections} fails it.
func TestReplayCanFail(t *testing.T) {
	sits := loadSituations(t)
	alone := map[string]bool{}
	for role, tmpl := range templates {
		lines := strings.Split(*tmpl, "\n")
		for i := range lines {
			ids := failing(sits, role, strings.Join(slices.Delete(slices.Clone(lines), i, i+1), "\n"))
			if len(ids) > 1 {
				t.Errorf("deleting %s line %d fails %v, want at most one situation", role, i+1, ids)
			}
			for _, id := range ids {
				alone[id] = true
			}
		}
	}
	for _, s := range sits {
		if !alone[s.ID] {
			t.Errorf("%s: no single template line holds it", s.ID)
		}
	}
}

// label is a facts heading such as "Brief:" or "Before you write:".
var label = regexp.MustCompile(`^[A-Z][A-Za-z ]*:$`)

// TestTemplatesShortAndSourced: at most 40 lines; every rule line (not a
// heading, a placeholder or facts line, the plan example or the tag) ends with
// its source in brackets; the filled prompt keeps no source mark or placeholder.
func TestTemplatesShortAndSourced(t *testing.T) {
	for role, tmpl := range templates {
		lines := strings.Split(strings.TrimSpace(*tmpl), "\n")
		if len(lines) > 40 {
			t.Errorf("%s: %d lines, want at most 40", role, len(lines))
		}
		for i, l := range lines {
			if l != "" && !label.MatchString(l) && !strings.HasPrefix(l, "{") && !strings.HasPrefix(l, "<") && !sourceMark.MatchString(l) {
				t.Errorf("%s line %d has no [source]: %q", role, i+1, l)
			}
		}
		p := filled(role, *tmpl)
		if m := regexp.MustCompile(`\{[a-z_]+\}`).FindString(p) + sourceMark.FindString(p); m != "" {
			t.Errorf("%s: filled prompt keeps %q:\n%s", role, m, p)
		}
	}
}

// TestW1ChoiceRoute: the worker's route for W1 and W8 without `report ask`.
// A nothing-to-change report at the base with the question, then the
// coordinator's send-back, reaches the live worker with the answer.
func TestW1ChoiceRoute(t *testing.T) {
	l := newLanding(t).withTokens()
	base := l.state().Tasks[0].Attempts[0].BaseCommit
	gitIn(t, l.wt, "reset", "--hard", base)
	l.edit(func(s *flow.State) {
		a := &s.Tasks[0].Attempts[0]
		a.Ready, a.Rounds, a.PR = []flow.Ready{}, []flow.Round{}, 0
	})
	l.report("tok-worker", "ready", `{"head":"`+base+`","nothing_to_change":"layout A or B? I pick A"}`, "")
	l.report("tok-coord", "task", `{"action":"send-back","task":"T1","why":"take layout B"}`, "")
	l.pass()
	if sends := sendsTo(t, l.srv.Requests(), l.worker); len(sends) != 1 || !strings.Contains(sends[0], "take layout B") {
		t.Fatalf("worker sends = %q, want one with the coordinator's answer", sends)
	}
}
