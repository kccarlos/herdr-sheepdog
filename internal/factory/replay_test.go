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

var planReviewerTmpl = func() string { b, _ := os.ReadFile("plan_reviewer.md"); return string(b) }()

var templates = map[string]*string{
	"coordinator":    &coordinatorTemplate,
	"worker":         &workerTemplate,
	"reviewer":       &reviewerCmdTemplate,
	"reviewer-codex": &reviewerTemplate,
	"retro":          &retroTemplate,
	"plan_reviewer":  &planReviewerTmpl,
}

// filled builds role's prompt from tmpl through the engine's own fill, on a
// fixed task whose round 1 at c1 was rejected (so the reviewer gets R2's facts).
func filled(role, tmpl string) string {
	t := flow.Task{ID: "T1", Title: "feat: add Greet", Brief: "Add Greet(name).", NotDoing: "localization", EstimateLines: 50,
		Outcomes: []flow.Outcome{{Text: "Greet works", ProvedBy: "cmd: go test ./..."}}}
	a := flow.Attempt{Worktree: "/wt/T1", Branch: "factory/T1", SizeCap: 100, Review: &flow.ReviewRun{Code: "abc123", Round: 4, Kind: flow.RoundRetro, Head: "c1"},
		Rounds:  []flow.Round{{N: 1, Kind: flow.RoundFull, Head: "c1", Verdict: flow.VerdictChanges, Majors: 1, ReportPath: "/r/1.md"}},
		Rejects: []flow.Reject{{Head: "c1", Round: 1, Why: "the cache key is right"}}}
	switch role {
	case "coordinator":
		return coordinatorPrompt(tmpl, flow.Project{Folder: "/repo", Goal: "Add a greeting."}, "sheepdog")
	case "worker":
		return workerPrompt(tmpl, t, a, "sheepdog")
	case "retro":
		e := &Engine{}
		return e.retroPrompt(tmpl, t, a, flow.DefaultSettings(), "c1", "git diff b..c1", 80, 4, "sheepdog", "/tmp/draft.md")
	case "plan_reviewer":
		facts := "Goal: Add a greeting.\nTasks under review:\n- T1 feat: add Greet (for_whom: CLI callers; if_skipped: no greeting; smallest: one Greet func; not_doing: i18n; estimate: 50)\n- T2 feat: verbose flag (for_whom: debuggers; if_skipped: an optional item from the backlog; smallest: --verbose; not_doing: log levels; estimate: 40)\n- T3 feat: persist count (for_whom: users; if_skipped: no count; smallest: write count to state.json; not_doing: history; estimate: 60)\nAccepted tasks: (none)"
		return fill(tmpl, "{facts}", facts, "{findings_path}", "/tmp/plan-draft.md", "{sheepdog}", "sheepdog", "{code}", "abc123")
	}
	e := &Engine{}
	prompt, err := e.reviewerPromptWith(context.Background(), tmpl, t, a, "c1", flow.RoundFull, "git diff b..c1", e.rejectionPrompt("p", "T1", a, "c1"), 2, "sheepdog", "/tmp/draft.md")
	if err != nil {
		panic(err)
	}
	return prompt
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
		if strings.TrimSpace(*tmpl) == "" {
			t.Errorf("%s: template is empty", role)
			continue
		}
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
		if strings.TrimSpace(p) == "" {
			t.Errorf("%s: filled prompt is empty", role)
			continue
		}
		if m := regexp.MustCompile(`\{[a-z_]+\}`).FindString(p) + sourceMark.FindString(p); m != "" {
			t.Errorf("%s: filled prompt keeps %q:\n%s", role, m, p)
		}
	}
}

// TestRetroTemplateCauseSetMatchesFlow checks design.md 7.2, prompts.md 5.5,
// and KB-RET-15: retro.md lists the exact 8-cause set from flow.RetroCauses
// on one rule line with no missing, misspelled, reordered or extra causes.
func TestRetroTemplateCauseSetMatchesFlow(t *testing.T) {
	if strings.TrimSpace(retroTemplate) == "" {
		t.Fatal("retroTemplate is empty; retro.md is not embedded yet")
	}
	var line string
	for _, raw := range strings.Split(retroTemplate, "\n") {
		if strings.Contains(raw, flow.RetroCauses[0]) {
			line = strings.TrimSpace(sourceMark.ReplaceAllString(raw, ""))
			break
		}
	}
	if line == "" {
		t.Fatalf("retro.md has no line naming %q (flow.RetroCauses = %v)", flow.RetroCauses[0], flow.RetroCauses)
	}
	// Take the cause list after the last ':' before flow.RetroCauses[0],
	// trim trailing sentence punctuation, and split by ',' or '|'.
	prefixIdx := strings.Index(line, flow.RetroCauses[0])
	listPart := line
	if colon := strings.LastIndex(line[:prefixIdx], ":"); colon >= 0 {
		listPart = line[colon+1:]
	}
	listPart = strings.TrimSpace(strings.TrimRight(listPart, ".)"))
	sep := ","
	if !strings.Contains(listPart, ",") && strings.Contains(listPart, "|") {
		sep = "|"
	}
	var got []string
	for _, part := range strings.Split(listPart, sep) {
		tok := strings.Trim(strings.TrimSpace(part), "`\"'.")
		if tok != "" {
			got = append(got, tok)
		}
	}
	if !slices.Equal(got, flow.RetroCauses) {
		t.Fatalf("retro.md cause line = %v (from %q), want exact flow.RetroCauses %v", got, line, flow.RetroCauses)
	}
}

// TestPlanReviewerTemplateKindsMatchFlow checks plan.md 11.2, design.md 7.2,
// and prompts.md 5.2: plan_reviewer.md lists exact flow.PlanFindingKinds.
func TestPlanReviewerTemplateKindsMatchFlow(t *testing.T) {
	if strings.TrimSpace(planReviewerTmpl) == "" {
		t.Fatal("plan_reviewer.md is missing or empty")
	}
	var got []string
	for _, raw := range strings.Split(planReviewerTmpl, "\n") {
		line := strings.TrimSpace(sourceMark.ReplaceAllString(raw, ""))
		if _, after, ok := strings.Cut(line, "<"+flow.PlanFindingKinds[0]+"|"); ok {
			if body, _, ok := strings.Cut(after, ">"); ok {
				got = append([]string{flow.PlanFindingKinds[0]}, strings.Split(body, "|")...)
				break
			}
		}
	}
	if !slices.Equal(got, flow.PlanFindingKinds) {
		t.Fatalf("plan_reviewer.md finding kinds = %v, want exact flow.PlanFindingKinds %v", got, flow.PlanFindingKinds)
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
