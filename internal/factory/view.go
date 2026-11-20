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
	Goal    string         `json:"goal,omitempty"`
	Running bool           `json:"running"`
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

func projectViewOf(s flow.State) ProjectView {
	counts := map[string]int{}
	for _, tv := range GraphViewOf(s, nil).Tasks {
		counts[tv.Stage]++
	}
	return ProjectView{ID: s.Project.ID, Folder: s.Project.Folder, Goal: s.Project.Goal, Running: s.Project.Started, Counts: counts}
}

func GraphViewOf(s flow.State, logs []LogLine) GraphView {
	done, idx, deps, lastLog := make(map[string]bool, len(s.Tasks)), make(map[string]int, len(s.Tasks)), map[string][]string{}, map[string]LogLine{}
	for _, l := range logs {
		if l.Task != "" {
			lastLog[l.Task] = l
		}
	}
	for i, t := range s.Tasks {
		idx[t.ID], done[t.ID] = i, t.Done != nil
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
		stage, why := taskStageWhy(t, s.Settings, s.Project.Started, done)
		l, model, term, rounds := lastLog[t.ID], "", "", 0
		if n := len(t.Attempts); n > 0 {
			rounds = len(slices.DeleteFunc(slices.Clone(flow.Counted(t.Attempts[n-1])), func(r flow.Round) bool { return r.Kind == flow.RoundSendBack }))
		}
		if a := flow.OpenAttempt(t); a != nil && a.Review != nil {
			model, term = a.Review.Reviewer.Model, cmp.Or(a.Review.RunID, a.RunID)
		} else if a != nil && a.RunID != "" {
			model, term = workerModel, a.RunID
		}
		tasks = append(tasks, TaskView{
			Ref: t.ID, Title: t.Title, Stage: stage, WhyWaiting: why, After: append([]string{}, t.After...), Dependents: append([]string{}, deps[t.ID]...),
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
		if json.Unmarshal(raw, &l) == nil {
			logs = append(logs, l)
		}
	}
	return s, logs, nil
}

// stateStage maps slice-2 engine states onto design.md 4.11 stages and default why text.
var stateStage = map[string][2]string{
	"done": {"done", ""}, "parked": {"blocked_or_failed", "Parked, waiting on the coordinator"}, "nothing_to_change": {"blocked_or_failed", "Worker reported nothing to change, waiting on the coordinator"},
	"waiting_on_dep": {"waiting_on_dep", ""}, "queued": {"queued", "Queued to start"}, "working": {"working", "Worker is working"},
	flow.Review: {"working", ""}, flow.Fix: {"working", "Waiting on worker to fix review findings"}, flow.RecapAnswer: {"blocked_or_failed", "Review did not converge, waiting on recap"},
	flow.Passed: {"ci", "Waiting on CI and merge"}, flow.NoLadderRow: {"blocked_or_failed", "Waiting on the coordinator"},
}

func taskStageWhy(t flow.Task, settings flow.Settings, started bool, done map[string]bool) (string, string) {
	a, st, why := flow.OpenAttempt(t), "", ""
	switch {
	case flow.Ended(t):
		st = "done"
	case t.Parked:
		st = "parked"
	case flow.OpenRecap(t) != nil:
		st, why = flow.RecapAnswer, "At "+cmp.Or(flow.OpenRecap(t).Trigger, "a")+" recap, waiting on the coordinator"
	case a == nil && slices.ContainsFunc(t.After, func(d string) bool { return !done[d] }):
		st, why = "waiting_on_dep", "Waiting on "+strings.Join(slices.DeleteFunc(slices.Clone(t.After), func(d string) bool { return done[d] }), ", ")
	case a == nil:
		st, why = "queued", map[bool]string{true: "Queued to start", false: "Project is stopped"}[started]
	case a.NothingToChange != "":
		st = "nothing_to_change"
	case len(a.Ready) == 0:
		st = "working"
	default:
		st = flow.Ladder(flow.In{Task: t, Settings: settings})
		if st == flow.Review && a.Review != nil && a.Review.Round > 0 {
			why = fmt.Sprintf("In review round %d", a.Review.Round)
		} else if st == flow.Review {
			why = fmt.Sprintf("In review round %d", len(a.Rounds)+1)
		}
	}
	if info, ok := stateStage[st]; ok {
		return info[0], cmp.Or(why, info[1])
	}
	return "blocked_or_failed", "Waiting on the coordinator"
}
