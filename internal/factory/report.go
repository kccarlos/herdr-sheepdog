package factory

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/world"
)

// Refusal is a report or command the acceptance table turned down
// (design.md 7.2): a code and one plain sentence. Nothing changed.
type Refusal struct{ Code, Why string }

func (r *Refusal) Error() string { return r.Code + ": " + r.Why }

// owner marks the verbs only the owner may send, from a terminal with no
// run's token.
const owner = "owner"

// verbRoles is the acceptance table: who may send each verb.
var verbRoles = map[string]string{
	"start": owner, "stop": owner,
	"plan apply": flow.RoleCoordinator, "recap decide": flow.RoleCoordinator,
	"plan amend": flow.RoleCoordinator, "task": flow.RoleCoordinator,
	"ready": flow.RoleWorker,
}

// Report is the acceptance step for everything a run's terminal sends: the
// token names the run, and through it the role and the project.
func (e *Engine) Report(token, verb string, args json.RawMessage) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	want, ok := verbRoles[verb]
	if !ok {
		return e.refuse("", "kind_not_allowed", fmt.Sprintf("%q is not a report the factory knows.", verb))
	}
	id, s, role, ti := e.runByTokenLocked(token)
	if want == owner {
		return e.refuse(id, "kind_not_allowed", fmt.Sprintf("A run's terminal cannot %s the factory; the owner runs it from a terminal with no run's token.", verb))
	}
	if id == "" {
		return e.refuse("", "invalid_token", "The token matches no run the factory started.")
	}
	if role != want {
		return e.refuse(id, "kind_not_allowed", fmt.Sprintf("A %s may not send %s; only the %s may.", role, verb, want))
	}
	if ti >= 0 && flow.Ended(s.Tasks[ti]) {
		return e.refuse(id, "already_closed", fmt.Sprintf("Task %q has ended; it takes no more reports.", s.Tasks[ti].ID))
	}
	if verb == "ready" {
		return e.acceptReadyLocked(id, &s, role, ti, args)
	}
	if verb == "recap decide" {
		return e.acceptRecapDecideLocked(id, &s, role, args)
	}
	if verb == "task" {
		return e.acceptTaskLocked(id, &s, role, args)
	}
	if verb == "plan amend" {
		return e.acceptAmendLocked(id, &s, role, args)
	}
	if len(s.Tasks) > 0 {
		return e.refuse(id, "plan_exists", "A plan already exists; change it with plan amend.")
	}
	tasks, fails := checkPlan(args)
	if len(fails) > 0 {
		return e.refuse(id, "plan_invalid", strings.Join(fails, "; ")+".")
	}
	s.Tasks = tasks
	titles := make([]string, len(tasks))
	for i, t := range tasks {
		titles[i] = t.ID + " " + t.Title
	}
	return e.commitLocked(id, &s, LogLine{By: role, Input: map[string]any{"verb": verb, "tasks": tasks}, Rule: "plan apply",
		Decision: "plan applied", Why: fmt.Sprintf("%d task(s): %s", len(tasks), strings.Join(titles, ", "))})
}

func (e *Engine) acceptReadyLocked(id string, s *flow.State, role string, ti int, args json.RawMessage) error {
	var req struct {
		Head    string  `json:"head"`
		Reject  *string `json:"reject"`
		Nothing *string `json:"nothing_to_change"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if len(bytes.TrimSpace(args)) == 0 || dec.Decode(&req) != nil || strings.TrimSpace(req.Head) == "" {
		return e.refuse(id, "invalid_report", "A ready report must carry the worktree head commit.")
	}
	t := &s.Tasks[ti]
	a := &t.Attempts[len(t.Attempts)-1]
	h := flow.H(*a)
	ctx, cancel := context.WithTimeout(context.Background(), lockGitTimeout)
	defer cancel()
	tip, rel, clean, err := world.InspectWorktree(ctx, a.Worktree, h)
	if err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(req.Head) != tip:
		return e.refuse(id, "invalid_report", "Reported head does not match the task worktree tip; run report ready from the worktree.")
	case !clean:
		return e.refuse(id, "dirty_tree", "The worktree has uncommitted changes; commit or discard them before reporting ready.")
	case req.Reject != nil:
		why, err := e.cleanWhy(id, *req.Reject, "reject report", "--reject")
		if err != nil {
			return err
		}
		r := flow.RejectableRound(*a, h)
		if rel != flow.RelSame || r == nil || flow.Ladder(flow.In{Task: *t, Settings: s.Settings}) != flow.Fix {
			if rel == flow.RelSame && flow.Ladder(flow.In{Task: *t, Settings: s.Settings}) == flow.RecapAnswer {
				return e.refuse(id, "nothing_to_reject", "Nothing to reject: a recap is open and the coordinator decides; there is nothing for you to do.")
			}
			return e.refuse(id, "nothing_to_reject", "Nothing to reject: the newest round covering the current head must be an unrejected reviewer changes verdict with no new commit (after a commit, run report ready without --reject).")
		}
		a.Rejects = append(a.Rejects, flow.Reject{Head: h, Round: r.N, Why: why})
		return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "ready", "head": h, "round": r.N, "reject": why},
			Rule: "ready --reject", Decision: "rejected", Why: fmt.Sprintf("round %d at %s: %s", r.N, h, why)})
	case req.Nothing != nil:
		why, err := e.cleanWhy(id, *req.Nothing, "nothing-to-change report", "--why")
		if err != nil {
			return err
		}
		if tip != a.BaseCommit || rel != flow.RelSame { // at the base, and no ready moved H past it
			return e.refuse(id, "nothing_new", "The worktree has commits over the base; run report ready without --nothing-to-change.")
		}
		a.NothingToChange = why
		return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "ready", "nothing_to_change": why},
			Rule: "ready --nothing-to-change", Decision: "nothing to change", Why: why})
	case rel == flow.RelBehind || rel == flow.RelDiverged:
		return e.refuse(id, "pull_first", "The attempt head moved past the worktree tip; pull first.")
	case rel == flow.RelSame:
		return e.refuse(id, "nothing_new", "The worktree has no commit ahead of the current head.")
	}
	a.Ready, a.NothingToChange = append(a.Ready, flow.Ready{Head: tip}), ""
	return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "ready", "head": tip}, Rule: "ready", Decision: "ready", Why: tip})
}

const maxRejectWhyRunes = 500

// cleanWhy cleans a free-text reason and refuses it when empty or over the cap.
func (e *Engine) cleanWhy(id, raw, what, flag string) (string, error) {
	why := cleanRejectWhy(raw)
	if why == "" {
		return "", e.refuse(id, "why_missing", fmt.Sprintf("A %s must carry a non-empty reason (%s \"<why>\").", what, flag))
	}
	if utf8.RuneCountInString(why) > maxRejectWhyRunes {
		return "", e.refuse(id, "invalid_report", "the reason is longer than 500 characters; put details in a file and name it")
	}
	return why, nil
}

func cleanRejectWhy(raw string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, raw)), " ")
}

// acceptVerdictLocked checks a reviewer's terminal <report> tag against the
// acceptance table (design.md 7.2). Valid: writes findings to reports/, stops
// the reviewer, and records the round. Invalid: refuses with invalid_report,
// logged once per distinct output on that run, leaving the round unused.
func (e *Engine) acceptVerdictLocked(ctx context.Context, id string, s *flow.State, ti int, rd world.ReportRead) error {
	t := &s.Tasks[ti]
	a := &t.Attempts[len(t.Attempts)-1]
	rev := a.Review
	if rev == nil {
		return e.refuse(id, "invalid_report", "No review round is active for this task.")
	}
	rep, why, majors := rd.Report, "", 0
	for l := range strings.Lines(rd.Body) {
		// A list or bold mark may lead: "- major:", "**major:**", "1. major:".
		if strings.HasPrefix(strings.TrimLeft(l, " \t-*0123456789."), "major:") {
			majors++
		}
	}
	switch {
	case rd.Status != world.ReportFound || rep.Code == "" || rep.Code != rev.Code:
		why = "The report tag is malformed or does not carry this round's one-time code."
	case rep.Round != rev.Round || rep.Head != rev.Head || rev.Head != flow.H(*a) || rev.Round != len(a.Rounds)+1:
		why = "The report tag does not match this review run's round and commit."
	case (rep.Verdict != flow.VerdictPass || rep.Majors != 0) && (rep.Verdict != flow.VerdictChanges || rep.Majors < 1):
		why = "Verdict must be pass with 0 majors or changes with at least 1 major."
	case rep.Majors != majors:
		why = fmt.Sprintf("The tag says %d major(s) but %d findings line(s) start with major:.", rep.Majors, majors)
	}
	errKey := "verdict " + id + " " + t.ID + " " + rev.RunID
	if why != "" {
		if sig := rd.Raw + "\x00" + why; rev.RunID == "" || e.errs[errKey] != sig {
			if rev.RunID != "" {
				if e.errs == nil {
					e.errs = map[string]string{}
				}
				e.errs[errKey] = sig
			}
			return e.refuse(id, "invalid_report", why)
		}
		return &Refusal{Code: "invalid_report", Why: why}
	}
	delete(e.errs, errKey)
	path := e.Paths.Report(id, t.ID, rev.Round)
	body := fmt.Sprintf("# Review round %d: %s (%d major(s))\n\nTask: %s — %s\nCommit: %s\n\n## Findings\n\n%s\n",
		rev.Round, rep.Verdict, rep.Majors, t.ID, t.Title, rev.Head, cmp.Or(rd.Body, "(none)"))
	if err := writeAtomic(path, []byte(body)); err != nil {
		return err
	}
	if err := world.StopRun(ctx, e.HC, rev.RunID); err != nil {
		return err
	}
	rnd := flow.Round{N: rev.Round, Kind: cmp.Or(rev.Kind, flow.RoundFull), Head: rev.Head, Reviewer: rev.Reviewer, Verdict: rep.Verdict, Majors: rep.Majors, ReportPath: path}
	a.Rounds, a.Review = append(a.Rounds, rnd), nil
	return e.commitLocked(id, s, LogLine{
		By: flow.RoleReviewer, Task: t.ID,
		Input: map[string]any{"verb": "verdict", "round": rnd.N, "kind": rnd.Kind, "model": rnd.Reviewer.Model, "head": rnd.Head, "verdict": rnd.Verdict, "majors": rnd.Majors},
		Rule:  "verdict", Decision: rnd.Verdict, Why: fmt.Sprintf("round %d (%s, %s) at %s: %s (%d major(s))", rnd.N, rnd.Kind, rnd.Reviewer.Model, rnd.Head, rnd.Verdict, rnd.Majors),
	})
}

func (e *Engine) refuse(project, code, why string) error {
	e.logEvent("factory.refused", map[string]string{"project": project, "code": code, "why": why})
	return &Refusal{Code: code, Why: why}
}

// tokenHash is all state.json keeps of a run token: its SHA-256, hex.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// runByTokenLocked finds the run holding token: a project's coordinator or a
// task's open attempt worker.
func (e *Engine) runByTokenLocked(token string) (string, flow.State, string, int) {
	h := tokenHash(token)
	same := func(t string) bool {
		return token != "" && t != "" && subtle.ConstantTimeCompare([]byte(t), []byte(h)) == 1
	}
	for _, ent := range e.projectDirs() {
		s, err := LoadState(e.Paths.State(ent.Name()))
		if err != nil {
			continue
		}
		if same(s.Project.RunTokenHash) {
			return ent.Name(), s, flow.RoleCoordinator, -1
		}
		for ti, t := range s.Tasks {
			if n := len(t.Attempts); n > 0 {
				if a := t.Attempts[n-1]; same(a.RunTokenHash) {
					return ent.Name(), s, flow.RoleWorker, ti
				} else if a.Review != nil && same(a.Review.RunTokenHash) {
					return ent.Name(), s, flow.RoleReviewer, ti
				}
			}
		}
	}
	return "", flow.State{}, "", -1
}

// planTask is the part of design.md 3's plan record that slice 1 accepts.
type planTask struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Brief    string   `json:"brief"`
	Outcomes []string `json:"outcomes"`
	After    []string `json:"after"`
}

type planFile struct {
	Tasks []planTask `json:"tasks"`
}

// checkPlan runs the Shape check of design.md 11.7 and returns every failure.
func checkPlan(raw json.RawMessage) ([]flow.Task, []string) {
	return checkPlanWith(raw, nil, "")
}

func checkPlanWith(raw json.RawMessage, existing []flow.Task, parentID string) ([]flow.Task, []string) {
	var p planFile
	var fails []string
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		fails = append(fails, "shape: "+strings.TrimPrefix(err.Error(), "json: "))
		p = planFile{} // the strict decode may have stopped half way
		if json.Unmarshal(raw, &p) != nil {
			return nil, fails
		}
	}
	if len(p.Tasks) == 0 {
		fails = append(fails, "shape: the plan has no tasks")
	}
	existingIDs := map[string]bool{}
	for _, t := range existing {
		existingIDs[t.ID] = true
	}
	ids := map[string]bool{}
	var newIDs []string
	for i := range p.Tasks {
		t := &p.Tasks[i]
		t.ID = strings.TrimSpace(t.ID)
		for j := range t.After {
			t.After[j] = strings.TrimSpace(t.After[j])
		}
		if t.ID == "" || ids[t.ID] || existingIDs[t.ID] {
			fails = append(fails, fmt.Sprintf("shape: task %d needs an id no other task has", i+1))
		} else {
			ids[t.ID], newIDs = true, append(newIDs, t.ID)
		}
		if cleanRejectWhy(t.Title) == "" {
			fails = append(fails, fmt.Sprintf("shape: task %q has no title", t.ID))
		}
	}
	after := map[string][]string{}
	for _, et := range existing {
		if et.ID != parentID {
			after[et.ID] = replaceDep(et.After, parentID, newIDs)
		}
	}
	tasks := make([]flow.Task, len(p.Tasks))
	for i, t := range p.Tasks {
		for _, a := range t.After {
			if parentID != "" && a == parentID {
				fails = append(fails, fmt.Sprintf("shape: task %q is after %q, which is being split", t.ID, a))
			} else if !ids[a] && !existingIDs[a] {
				fails = append(fails, fmt.Sprintf("shape: task %q is after %q, which is not in the plan", t.ID, a))
			}
		}
		after[t.ID] = t.After
		tasks[i] = flow.Task{ID: t.ID, Title: cleanRejectWhy(t.Title), Brief: t.Brief, Outcomes: t.Outcomes, After: t.After, SplitFrom: parentID, Attempts: []flow.Attempt{}}
	}
	if id := cycleAt(p.Tasks, after); id != "" {
		fails = append(fails, fmt.Sprintf("shape: the after links make a cycle through %q", id))
	}
	return tasks, fails
}

func replaceDep(after []string, old string, repl []string) []string {
	if old == "" || !slices.Contains(after, old) {
		return after
	}
	var out []string
	add := func(id string) {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	for _, a := range after {
		if a == old {
			for _, r := range repl {
				add(r)
			}
		} else {
			add(a)
		}
	}
	return out
}

func (e *Engine) acceptRecapDecideLocked(id string, s *flow.State, role string, args json.RawMessage) error {
	var req struct {
		Task     string            `json:"task"`
		Choice   string            `json:"choice"`
		Why      string            `json:"why"`
		Children []json.RawMessage `json:"children,omitempty"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if len(bytes.TrimSpace(args)) == 0 || dec.Decode(&req) != nil || strings.TrimSpace(req.Task) == "" || strings.TrimSpace(req.Choice) == "" {
		return e.refuse(id, "invalid_report", "A recap decision must carry task and choice.")
	}
	taskID, choice := strings.TrimSpace(req.Task), strings.TrimSpace(req.Choice)
	ti := slices.IndexFunc(s.Tasks, func(t flow.Task) bool { return t.ID == taskID })
	if ti < 0 || flow.OpenRecap(s.Tasks[ti]) == nil {
		return e.refuse(id, "no_recap", fmt.Sprintf("Task %q has no open recap.", taskID))
	}
	t := &s.Tasks[ti]
	if flow.Ended(*t) {
		return e.refuse(id, "already_closed", fmt.Sprintf("Task %q is already ended; its recap no longer needs a decision.", taskID))
	}
	rc := flow.OpenRecap(*t)
	if !slices.Contains(rc.Allowed, choice) {
		return e.refuse(id, "not_allowed", fmt.Sprintf("Choice %q is not allowed for this recap; allowed: %s.", choice, strings.Join(rc.Allowed, ", ")))
	}
	why, err := e.cleanWhy(id, req.Why, "recap decision", "--why")
	if err != nil {
		return err
	}
	if choice == "drop" {
		if len(req.Children) > 0 {
			return e.refuse(id, "invalid_report", "drop does not take --child tasks.")
		}
		rc.Decided, t.Dropped = &flow.RecapDecision{Choice: choice, Why: why}, &flow.Dropped{Why: why}
		return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "recap decide", "choice": choice, "why": why},
			Rule: "recap decide", Decision: "dropped", Why: why})
	}
	if choice == "new" {
		if !rc.Amended {
			return e.refuse(id, "not_allowed", "new is allowed only after plan amend changed the task's brief or outcomes since the recap opened.")
		}
		note := fmt.Sprintf("Closed by sheepdog factory: task %s starts a new attempt (%s).", t.ID, why)
		if a := flow.OpenAttempt(*t); a != nil {
			ctx, cancel := context.WithTimeout(context.Background(), ghTimeout)
			defer cancel()
			if _, err := closeOpenPR(ctx, s.Project.Folder, *a, note); err != nil {
				return err // before any run is stopped: the task is as it was
			}
			if err := e.stopRuns(ctx, taskRuns(id, *t)); err != nil {
				return err
			}
			clearRuns(t)
			a.Closed = note
		}
		rc.Decided = &flow.RecapDecision{Choice: choice, Why: why}
		return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "recap decide", "choice": choice, "why": why},
			Rule: "recap decide", Decision: "attempt closed; a new one starts", Why: why})
	}
	raw, _ := json.Marshal(struct {
		Tasks []json.RawMessage `json:"tasks"`
	}{Tasks: req.Children})
	children, fails := checkPlanWith(raw, s.Tasks, t.ID)
	if len(fails) > 0 {
		return e.refuse(id, "plan_invalid", strings.Join(fails, "; ")+".")
	}
	childIDs := make([]string, len(children))
	for i, c := range children {
		childIDs[i] = c.ID
	}
	rc.Decided, t.Split = &flow.RecapDecision{Choice: choice, Why: why}, &flow.Split{Children: childIDs}
	for i := range s.Tasks {
		if i != ti {
			s.Tasks[i].After = replaceDep(s.Tasks[i].After, t.ID, childIDs)
		}
	}
	s.Tasks = append(s.Tasks, children...)
	return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "recap decide", "choice": choice, "why": why, "children": childIDs},
		Rule: "recap decide", Decision: "split into " + strings.Join(childIDs, ", "), Why: why})
}

// acceptTaskLocked is the coordinator's park, unpark, drop, close and
// send-back (design.md 4.9, 7.2). The rows then act on the record.
func (e *Engine) acceptTaskLocked(id string, s *flow.State, role string, args json.RawMessage) error {
	var req struct{ Action, Task, Why string }
	if json.Unmarshal(args, &req) != nil {
		return e.refuse(id, "invalid_report", "A task command must carry action, task and why.")
	}
	ti := slices.IndexFunc(s.Tasks, func(t flow.Task) bool { return t.ID == strings.TrimSpace(req.Task) })
	switch {
	case ti < 0:
		return e.refuse(id, "invalid_report", fmt.Sprintf("No task %q in the plan.", req.Task))
	case flow.Ended(s.Tasks[ti]):
		return e.refuse(id, "already_closed", fmt.Sprintf("Task %q has ended; it cannot be changed.", req.Task))
	}
	why, err := e.cleanWhy(id, req.Why, "task command", "--why")
	if err != nil {
		return err
	}
	t, verb := &s.Tasks[ti], "task "+req.Action
	a := flow.OpenAttempt(*t)
	line := LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": verb, "why": why}, Rule: verb, Decision: req.Action, Why: why}
	switch req.Action {
	case "park", "unpark":
		t.Parked = req.Action == "park"
	case "drop":
		t.Dropped = &flow.Dropped{Why: why}
		var waiting []string // nothing is unblocked silently: they keep waiting on the dropped id
		for _, o := range s.Tasks {
			if slices.Contains(o.After, t.ID) && !flow.Ended(o) {
				waiting = append(waiting, o.ID)
			}
		}
		if len(waiting) > 0 {
			line.Decision += "; still waiting on it: " + strings.Join(waiting, ", ")
		}
	case "close":
		if a == nil || a.NothingToChange == "" {
			return e.refuse(id, "not_allowed", "close is allowed only after the worker reported nothing to change.")
		}
		ctx, cancel := context.WithTimeout(context.Background(), lockGitTimeout)
		defer cancel()
		if tip, _, _, err := world.InspectWorktree(ctx, a.Worktree, flow.H(*a)); err != nil {
			return err
		} else if tip != a.BaseCommit {
			return e.refuse(id, "nothing_new", "The worker committed since it reported nothing to change; send it back or wait for its ready.")
		}
		t.Closed = &flow.Closed{Why: why}
	case "send-back":
		if a == nil {
			return e.refuse(id, "invalid_report", fmt.Sprintf("Task %q has no attempt to send back.", t.ID))
		}
		n := len(a.Rounds) + 1
		a.Rounds = append(a.Rounds, flow.Round{N: n, Kind: flow.RoundSendBack, Head: flow.H(*a), Verdict: flow.VerdictChanges, Why: why})
		a.NothingToChange, line.Decision = "", fmt.Sprintf("sent back (round %d)", n)
	default:
		return e.refuse(id, "invalid_report", fmt.Sprintf("%q is not a task command; use park, unpark, drop, close or send-back.", req.Action))
	}
	return e.commitLocked(id, s, line)
}

// amendTask is one entry of plan amend for an existing task: only the
// fields named change.
type amendTask struct {
	ID       string    `json:"id"`
	Title    *string   `json:"title"`
	Brief    *string   `json:"brief"`
	Outcomes *[]string `json:"outcomes"`
}

// acceptAmendLocked is plan amend (design.md 3.1, 4.9, 7.2): changes to
// existing tasks' title, brief and outcomes, and new tasks, with a why. An
// ended task is not changed. A changed brief or outcomes is a boundary:
// earlier rounds stop covering, an in-flight review is stopped, and an open
// recap may then decide new. A live worker is told once what changed.
func (e *Engine) acceptAmendLocked(id string, s *flow.State, role string, args json.RawMessage) error {
	var req struct {
		Why   string            `json:"why"`
		Tasks []json.RawMessage `json:"tasks"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return e.refuse(id, "plan_invalid", "shape: "+strings.TrimPrefix(err.Error(), "json: ")+".")
	}
	why, err := e.cleanWhy(id, req.Why, "plan amend", `"why"`)
	if err != nil {
		return err
	}
	var fails []string
	if len(req.Tasks) == 0 {
		fails = append(fails, "shape: the amend has no tasks")
	}
	type change struct {
		ti int
		c  amendTask
	}
	var changes []change
	var fresh []json.RawMessage
	for _, raw := range req.Tasks {
		var c amendTask
		_ = json.Unmarshal(raw, &c) // the id decides: an existing task or a new one
		ti := slices.IndexFunc(s.Tasks, func(t flow.Task) bool { return t.ID == strings.TrimSpace(c.ID) })
		if ti < 0 {
			fresh = append(fresh, raw)
			continue
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		switch err := d.Decode(&c); {
		case err != nil:
			fails = append(fails, fmt.Sprintf("shape: task %q: %s (only title, brief and outcomes change)", s.Tasks[ti].ID, strings.TrimPrefix(err.Error(), "json: ")))
		case flow.Ended(s.Tasks[ti]):
			fails = append(fails, fmt.Sprintf("task %q has ended and is not changed", s.Tasks[ti].ID))
		case c.Title != nil && cleanRejectWhy(*c.Title) == "":
			fails = append(fails, fmt.Sprintf("shape: task %q has no title", s.Tasks[ti].ID))
		default:
			changes = append(changes, change{ti, c})
		}
	}
	var added []flow.Task
	if len(fresh) > 0 {
		raw, _ := json.Marshal(map[string]any{"tasks": fresh})
		var f []string
		added, f = checkPlanWith(raw, s.Tasks, "")
		fails = append(fails, f...)
	}
	if len(fails) > 0 {
		return e.refuse(id, "plan_invalid", strings.Join(fails, "; ")+".")
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockGitTimeout)
	defer cancel()
	var done []string
	for _, ch := range changes {
		t, c, what := &s.Tasks[ch.ti], ch.c, []string{}
		if c.Title != nil && cleanRejectWhy(*c.Title) != t.Title {
			t.Title, what = cleanRejectWhy(*c.Title), append(what, "title")
		}
		if c.Brief != nil && *c.Brief != t.Brief {
			t.Brief, what = *c.Brief, append(what, "brief")
		}
		if c.Outcomes != nil && !slices.Equal(*c.Outcomes, t.Outcomes) {
			t.Outcomes, what = *c.Outcomes, append(what, "outcomes")
		}
		a := flow.OpenAttempt(*t)
		if slices.Contains(what, "brief") || slices.Contains(what, "outcomes") {
			if rc := flow.OpenRecap(*t); rc != nil {
				rc.Amended = true
			}
			if a != nil {
				a.Boundary = len(a.Rounds)
				if a.Review != nil { // it was given the old brief
					if err := world.StopRun(ctx, e.HC, a.Review.RunID); err != nil {
						return err
					}
					a.Review = nil
					forgetStarts(a, false)
				}
			}
		}
		if len(what) == 0 {
			continue
		}
		done = append(done, t.ID+" ("+strings.Join(what, ", ")+")")
		if a != nil && a.RunID != "" {
			bin := cmp.Or(e.Bin, "sheepdog")
			msg := fmt.Sprintf("The plan changed for task %s (%s): %s\nTitle: %s\nBrief:\n%s\nOutcomes:\n%s\nEarlier review rounds no longer count. If your work must change, commit and run %s factory report ready again; otherwise wait.",
				t.ID, strings.Join(what, ", "), why, t.Title, cmp.Or(strings.TrimSpace(t.Brief), "(none)"), bullets(t.Outcomes), bin)
			if err := world.SendRun(ctx, e.HC, a.RunID, msg); err != nil { // a restarted worker reads the new plan anyway
				e.logError("factory.exec_error", id, err)
			}
		}
	}
	for _, t := range added {
		done = append(done, t.ID+" (new)")
	}
	s.Tasks = append(s.Tasks, added...)
	return e.commitLocked(id, s, LogLine{By: role, Input: map[string]any{"verb": "plan amend", "why": why, "tasks": done},
		Rule: "plan amend", Decision: "amended " + cmp.Or(strings.Join(done, ", "), "nothing"), Why: why})
}

// cycleAt returns a task on a cycle of after links, or "" when there is none.
func cycleAt(tasks []planTask, after map[string][]string) string {
	seen := map[string]int{} // 1: on the current path, 2: finished
	var visit func(id string) string
	visit = func(id string) string {
		switch seen[id] {
		case 1:
			return id
		case 2:
			return ""
		}
		seen[id] = 1
		for _, a := range after[id] {
			if c := visit(a); c != "" {
				return c
			}
		}
		seen[id] = 2
		return ""
	}
	for _, t := range tasks {
		if c := visit(t.ID); c != "" {
			return c
		}
	}
	return ""
}
