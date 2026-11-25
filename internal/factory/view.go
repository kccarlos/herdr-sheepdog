package factory

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// ErrProjectNotFound is returned when a project id has no stored state.
var ErrProjectNotFound = errors.New("factory project not found")

const DefaultLogLimit, MaxLogLimit = 50, 500

type ProjectView struct {
	ID      string         `json:"id"`
	Folder  string         `json:"folder,omitempty"`
	RepoURL string         `json:"repo_url,omitempty"`
	Goal    string         `json:"goal,omitempty"`
	State   string         `json:"state,omitempty"`
	Running bool           `json:"running"`
	Live    bool           `json:"live,omitempty"`
	Status  string         `json:"status,omitempty"`
	Counts  map[string]int `json:"counts,omitempty"`
	Error   string         `json:"error,omitempty"`
}

type GraphView struct {
	Protocol int        `json:"protocol"`
	Project  string     `json:"project"`
	Folder   string     `json:"folder"`
	Tasks    []TaskView `json:"tasks"`
}

type TaskView struct {
	Ref          string   `json:"ref"`
	Title        string   `json:"title"`
	Brief        string   `json:"brief,omitempty"`
	Stage        string   `json:"stage"`
	WhyWaiting   string   `json:"why_waiting,omitempty"`
	After        []string `json:"after"`
	Dependents   []string `json:"dependents"`
	Layer        int      `json:"layer"`
	Rounds       int      `json:"rounds"`
	Model        string   `json:"model,omitempty"`
	Terminal     string   `json:"terminal,omitempty"`
	LastRule     string   `json:"last_rule,omitempty"`
	LastDecision string   `json:"last_decision,omitempty"`
}

// LogEntry is the read-only log projection (design.md 12.1); Wake/Model await core writers.
type LogEntry struct {
	At       time.Time `json:"at"`
	By       string    `json:"by"`
	Task     string    `json:"task,omitempty"`
	Decision string    `json:"decision,omitempty"`
	Why      string    `json:"why,omitempty"`
}

func ProjectsView(states []flow.State) []ProjectView {
	out := make([]ProjectView, len(states))
	for i, s := range states {
		out[i] = projectViewOf(s)
	}
	slices.SortFunc(out, func(a, b ProjectView) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func projectLive(s flow.State) bool {
	return s.Project.RunID != "" || flow.RunLive("", s.Project.Run) ||
		slices.ContainsFunc(s.Project.PlanReviews, func(pr flow.PlanReview) bool {
			return pr.Run != nil && (pr.Run.RunID != "" || flow.RunLive("", pr.Run.Run))
		}) ||
		slices.ContainsFunc(s.Tasks, func(t flow.Task) bool {
			return slices.ContainsFunc(t.Attempts, func(a flow.Attempt) bool {
				return a.RunID != "" || flow.RunLive("", a.Run) || a.Review != nil && (a.Review.RunID != "" || flow.RunLive("", a.Review.Run))
			})
		})
}

func projectViewOf(s flow.State) ProjectView {
	counts, status, state := map[string]int{}, "", "stopped"
	if s.Project.Finished {
		state = "finished"
	} else if s.Project.Started {
		state = "running"
	}
	for _, tv := range GraphViewOf(s, nil).Tasks {
		counts[tv.Stage]++
	}
	if s.Project.Started && !s.Project.Finished && !flow.ProjectFinished(s.Project, s.Tasks) {
		if open := len(flow.OpenPlanFindings(s.Project)); open > 0 {
			status = fmt.Sprintf("%d plan findings open, waiting on the coordinator", open)
		} else if n := len(s.Project.PlanReviews); n > 0 {
			if pr := s.Project.PlanReviews[n-1]; pr.Accepted.IsZero() && pr.Verdict == "" && pr.Run != nil && flow.RunLive(pr.Run.RunID, pr.Run.Run) {
				status = "Plan review"
			}
		}
	}
	return ProjectView{ID: s.Project.ID, Folder: s.Project.Folder, RepoURL: s.Project.RepoURL, Goal: s.Project.Goal, State: state, Running: s.Project.Started, Live: projectLive(s), Status: status, Counts: counts}
}

var taskRows = func() map[string]flow.Row[flow.In] {
	m := make(map[string]flow.Row[flow.In], len(flow.TaskRows))
	for _, r := range flow.TaskRows {
		m[r.ID] = r
	}
	return m
}()

func lastTaskRows(logs []LogLine) map[string]LogLine {
	lastRow := map[string]LogLine{}
	for _, l := range logs {
		if l.Task != "" {
			if _, ok := taskRows[l.Rule]; ok {
				lastRow[l.Task] = l
			}
		}
	}
	return lastRow
}

func GraphViewOf(s flow.State, logs []LogLine) GraphView {
	return graphViewOf(s, lastTaskRows(logs))
}

func revActive(a *flow.Attempt) bool {
	return a != nil && a.Review != nil && (a.Review.Run == nil || a.Review.Run.State != flow.PhaseEnded)
}

func graphViewOf(s flow.State, lastRow map[string]LogLine) GraphView {
	idx, deps := make(map[string]int, len(s.Tasks)), map[string][]string{}
	for i, t := range s.Tasks {
		idx[t.ID] = i
		for _, after := range t.After {
			deps[after] = append(deps[after], t.ID)
		}
	}
	layers := make([]int, len(s.Tasks))
	for range len(s.Tasks) {
		for i, t := range s.Tasks {
			for _, dep := range t.After {
				if j, ok := idx[dep]; ok {
					layers[i] = max(layers[i], layers[j]+1)
				}
			}
		}
	}
	tasks := make([]TaskView, 0, len(s.Tasks))
	for i, t := range s.Tasks {
		stage, why := taskStageWhy(t, s.Tasks, s.Asks, s.Settings, s.Project.Started)
		l, model, term, rounds := lastRow[t.ID], "", "", 0
		if n := len(t.Attempts); n > 0 {
			rounds = len(slices.DeleteFunc(slices.Clone(flow.Counted(t.Attempts[n-1])), func(r flow.Round) bool { return r.Kind == flow.RoundSendBack || r.Kind == flow.RoundRetro }))
		}
		if a := flow.OpenAttempt(t); revActive(a) {
			model, term = a.Review.Reviewer.Model, cmp.Or(a.Review.RunID, a.RunID)
		} else if a != nil && a.RunID != "" {
			model, term = storedWorkerModel(t).Model, a.RunID
		}
		tasks = append(tasks, TaskView{
			Ref: t.ID, Title: t.Title, Brief: t.Brief, Stage: stage, WhyWaiting: why, After: append([]string{}, t.After...), Dependents: append([]string{}, deps[t.ID]...),
			Layer: layers[i], Rounds: rounds, Model: model, Terminal: term, LastRule: l.Rule, LastDecision: l.Decision,
		})
	}
	return GraphView{Protocol: 1, Project: s.Project.ID, Folder: s.Project.Folder, Tasks: tasks}
}

func LogView(lines []LogLine, n int) []LogEntry {
	if n <= 0 {
		n = DefaultLogLimit
	}
	if n = min(n, MaxLogLimit); len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]LogEntry, len(lines))
	for i, l := range lines {
		out[i] = LogEntry{At: l.At, By: l.By, Task: l.Task, Decision: l.Decision, Why: l.Why}
	}
	return out
}

func (e *Engine) Projects() []ProjectView {
	out := []ProjectView{}
	for _, ent := range e.projectDirs() {
		if !ent.IsDir() {
			continue
		}
		if s, err := LoadState(e.Paths.State(ent.Name())); err != nil {
			out = append(out, ProjectView{ID: ent.Name(), Error: err.Error()})
		} else {
			out = append(out, projectViewOf(s))
		}
	}
	slices.SortFunc(out, func(a, b ProjectView) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func (e *Engine) Graph(project string) (GraphView, error) {
	s, logs, err := e.loadProjectCopy(project)
	if err != nil {
		return GraphView{}, err
	}
	return GraphViewOf(s, logs), nil
}

func (e *Engine) LogLines(project string, n int) ([]LogEntry, error) {
	_, logs, err := e.loadProjectCopy(project)
	if err != nil {
		return nil, err
	}
	return LogView(logs, n), nil
}

func (e *Engine) loadProjectCopy(project string) (flow.State, []LogLine, error) {
	if project == "" || project == "." || project == ".." || strings.ContainsAny(project, `/\`) {
		return flow.State{}, nil, ErrProjectNotFound
	}
	// Read state.json before log.jsonl (the order matters for lockless CLI reads, S4).
	s, err := LoadState(e.Paths.State(project))
	if os.IsNotExist(err) {
		return flow.State{}, nil, ErrProjectNotFound
	} else if err != nil {
		return flow.State{}, nil, err
	}
	data, err := os.ReadFile(e.Paths.Log(project))
	if err != nil && !os.IsNotExist(err) {
		return flow.State{}, nil, err
	}
	var logs []LogLine
	for _, raw := range bytes.Split(data, []byte("\n")) {
		var l LogLine
		if json.Unmarshal(raw, &l) == nil && l.Seq <= s.Seq {
			logs = append(logs, l)
		}
	}
	return s, logs, nil
}

func taskStageWhy(t flow.Task, tasks []flow.Task, asks []flow.Ask, settings flow.Settings, started bool) (string, string) {
	if a := flow.OpenOwnerAskForTask(asks, t.ID); a != nil {
		if a.Kind == flow.KindGate {
			return "owner_gate", a.Question
		}
		return "owner_answer", a.Question
	}
	if flow.Ended(t) {
		return "done", ""
	}
	if t.Parked {
		return "blocked_or_failed", "Parked, waiting on the coordinator"
	}
	notDone, dropped := flow.UnmetDeps(t, tasks)
	unmet := append(notDone, dropped...)
	a := flow.OpenAttempt(t)
	r, ok := taskRows[t.LastRow]
	stage := r.Stage
	if a != nil && a.RunID == "" && flow.Done(a.Acts, flow.AskWake+"@worker_failed") {
		stage = "blocked_or_failed"
	} else if !ok {
		switch {
		case flow.OpenRecap(t) != nil:
			stage = "blocked_or_failed"
		case a == nil && len(unmet) > 0:
			stage = "waiting_on_dep"
		case a == nil:
			stage = "queued"
		case a.NothingToChange != "":
			stage = "blocked_or_failed"
		case len(a.Ready) == 0:
			stage = "working"
		default:
			r, _ = flow.FirstMatch(flow.LadderRows, flow.In{Task: t, Settings: settings})
			stage = r.Stage
		}
	}
	return stage, taskWhy(t, a, unmet, started, stage, r)
}

func taskWhy(t flow.Task, a *flow.Attempt, unmet []string, started bool, stage string, r flow.Row[flow.In]) string {
	switch {
	case stage == "done":
		return ""
	case flow.OpenRecap(t) != nil:
		return "At " + cmp.Or(flow.OpenRecap(t).Trigger, "a") + " recap, waiting on the coordinator"
	case a != nil && a.NothingToChange != "":
		return "Worker reported nothing to change, waiting on the coordinator"
	case !started:
		return "Project is stopped"
	case a != nil && a.RunID == "" && flow.Done(a.Acts, flow.AskWake+"@worker_failed"):
		return "Worker failed, waiting on the coordinator"
	case stage == "waiting_on_dep" && len(unmet) > 0:
		return "Waiting on " + strings.Join(unmet, ", ")
	case t.HoldWhy != "":
		return t.HoldWhy
	case a == nil && t.Unreviewed:
		return "Waiting on the plan review"
	case r.ID == "N19b":
		return "Not merged: checks got worse after the override card was made"
	case r.ID == "N15" || r.ID == "N19":
		return "Merging now, by your override"
	case r.ID == "N16":
		return "PR conflicts with base; telling worker to merge base"
	case r.ID == "N17":
		return "Waiting for another merge in flight"
	case stage == "working" && revActive(a):
		if a.Review.Role() == flow.RoleRetro {
			return "In retro"
		}
		return fmt.Sprintf("In review round %d", len(a.Rounds)+1)
	case r.Why == flow.WhyCI:
		return "Waiting on CI and merge"
	case r.ID == "N23":
		return "Worker is working"
	case r.WaitsOn != "":
		return "Waiting on " + r.WaitsOn
	case a == nil:
		return "Queued to start"
	default:
		return "Worker is working"
	}
}
