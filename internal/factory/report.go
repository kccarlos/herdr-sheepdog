package factory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

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

// verbRoles is slice 1's acceptance table: who may send each verb.
var verbRoles = map[string]string{"start": owner, "stop": owner, "plan apply": flow.RoleCoordinator, "ready": flow.RoleWorker}

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
	if verb == "ready" {
		return e.acceptReadyLocked(id, &s, role, ti, args)
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
		Head string `json:"head"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if len(bytes.TrimSpace(args)) == 0 || dec.Decode(&req) != nil || strings.TrimSpace(req.Head) == "" {
		return e.refuse(id, "invalid_report", "A ready report must carry the worktree head commit.")
	}
	t := &s.Tasks[ti]
	a := &t.Attempts[len(t.Attempts)-1]
	ctx, cancel := context.WithTimeout(context.Background(), lockGitTimeout)
	defer cancel()
	tip, rel, clean, err := world.InspectWorktree(ctx, a.Worktree, flow.H(*a))
	if err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(req.Head) != tip:
		return e.refuse(id, "invalid_report", "Reported head does not match the task worktree tip; run report ready from the worktree.")
	case !clean:
		return e.refuse(id, "dirty_tree", "The worktree has uncommitted changes; commit or discard them before reporting ready.")
	case rel == flow.RelBehind || rel == flow.RelDiverged:
		return e.refuse(id, "pull_first", "The attempt head moved past the worktree tip; pull first.")
	case rel == flow.RelSame:
		return e.refuse(id, "nothing_new", "The worktree has no commit ahead of the current head.")
	}
	a.Ready = append(a.Ready, flow.Ready{Head: tip})
	return e.commitLocked(id, s, LogLine{By: role, Task: t.ID, Input: map[string]any{"verb": "ready", "head": tip}, Rule: "ready", Decision: "ready", Why: tip})
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
			if n := len(t.Attempts); n > 0 && same(t.Attempts[n-1].RunTokenHash) {
				return ent.Name(), s, flow.RoleWorker, ti
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
	ids := map[string]bool{}
	for i := range p.Tasks {
		t := &p.Tasks[i]
		t.ID = strings.TrimSpace(t.ID)
		for j := range t.After {
			t.After[j] = strings.TrimSpace(t.After[j])
		}
		if t.ID == "" || ids[t.ID] {
			fails = append(fails, fmt.Sprintf("shape: task %d needs an id no other task has", i+1))
		} else {
			ids[t.ID] = true
		}
		if strings.TrimSpace(t.Title) == "" {
			fails = append(fails, fmt.Sprintf("shape: task %q has no title", t.ID))
		}
	}
	after := map[string][]string{}
	tasks := make([]flow.Task, len(p.Tasks))
	for i, t := range p.Tasks {
		for _, a := range t.After {
			if !ids[a] {
				fails = append(fails, fmt.Sprintf("shape: task %q is after %q, which is not in the plan", t.ID, a))
			}
		}
		after[t.ID] = t.After
		tasks[i] = flow.Task{ID: t.ID, Title: strings.TrimSpace(t.Title), Brief: t.Brief, Outcomes: t.Outcomes, After: t.After, Attempts: []flow.Attempt{}}
	}
	if id := cycleAt(p.Tasks, after); id != "" {
		fails = append(fails, fmt.Sprintf("shape: the after links make a cycle through %q", id))
	}
	return tasks, fails
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
