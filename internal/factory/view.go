package factory

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// ErrProjectNotFound is returned when a project id has no stored state.
var ErrProjectNotFound = errors.New("factory project not found")

// ProjectView is one project's read-only summary (design.md 12.1, 12.3).
type ProjectView struct {
	ID      string         `json:"id"`
	Folder  string         `json:"folder"`
	Goal    string         `json:"goal"`
	Running bool           `json:"running"`
	Counts  map[string]int `json:"counts"`
}

// GraphView is a project's tasks grouped by dependency layer (design.md 12.1).
type GraphView struct {
	Protocol int          `json:"protocol"`
	Project  string       `json:"project"`
	Folder   string       `json:"folder"`
	Goal     string       `json:"goal"`
	Running  bool         `json:"running"`
	Layers   [][]TaskView `json:"layers"`
}

// TaskView is one task row in GraphView.
type TaskView struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Stage      string   `json:"stage"`
	Layer      int      `json:"layer"`
	After      []string `json:"after,omitempty"`
	Rule       string   `json:"rule,omitempty"`
	Decision   string   `json:"decision,omitempty"`
	WhyWaiting string   `json:"why_waiting,omitempty"`
	Rounds     int      `json:"rounds"`
	Model      string   `json:"model,omitempty"`
	Terminal   string   `json:"terminal,omitempty"`
}

// ProjectsView derives project summaries purely from copies of stored state.
func ProjectsView(states []flow.State) []ProjectView {
	out := make([]ProjectView, 0, len(states))
	for _, s := range states {
		done, counts := doneSet(s.Tasks), map[string]int{}
		for _, t := range s.Tasks {
			counts[taskStage(t, s.Settings, done)]++
		}
		out = append(out, ProjectView{ID: s.Project.ID, Folder: s.Project.Folder, Goal: s.Project.Goal, Running: s.Project.Started, Counts: counts})
	}
	slices.SortFunc(out, func(a, b ProjectView) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// GraphViewOf builds a project's layered task graph purely from stored state
// and its log lines (no git, gh or hc calls).
func GraphViewOf(s flow.State, logs []LogLine) GraphView {
	done, lastLog := doneSet(s.Tasks), map[string]LogLine{}
	for _, l := range logs {
		if l.Task != "" {
			lastLog[l.Task] = l
		}
	}
	layers, maxLayer := taskLayers(s.Tasks)
	grouped := make([][]TaskView, maxLayer+1)
	for i, t := range s.Tasks {
		l, layer := lastLog[t.ID], layers[i]
		model, term, rounds := taskAttemptInfo(t, l)
		grouped[layer] = append(grouped[layer], TaskView{
			ID: t.ID, Title: t.Title, Stage: taskStage(t, s.Settings, done), Layer: layer, After: slices.Clone(t.After),
			Rule: l.Rule, Decision: l.Decision, WhyWaiting: cmp.Or(l.Why, l.Decision), Rounds: rounds, Model: model, Terminal: term,
		})
	}
	return GraphView{Protocol: 1, Project: s.Project.ID, Folder: s.Project.Folder, Goal: s.Project.Goal, Running: s.Project.Started, Layers: grouped}
}

// LogView returns a copy of the last n log lines (all when n <= 0).
func LogView(lines []LogLine, n int) []LogLine {
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return append([]LogLine{}, lines...)
}

// Projects locks the engine, copies stored project states, and returns their summaries.
func (e *Engine) Projects() []ProjectView {
	e.mu.Lock()
	var states []flow.State
	for _, ent := range e.projectDirs() {
		if s, err := LoadState(e.Paths.State(ent.Name())); ent.IsDir() && err == nil {
			states = append(states, s)
		}
	}
	e.mu.Unlock()
	return ProjectsView(states)
}

// Graph locks the engine, copies the project's stored state and log, and builds its GraphView.
func (e *Engine) Graph(project string) (GraphView, error) {
	s, logs, err := e.loadProjectCopy(project)
	if err != nil {
		return GraphView{}, err
	}
	return GraphViewOf(s, logs), nil
}

// LogLines locks the engine, copies the project's log.jsonl, and returns the last n lines.
// Named LogLines because Engine.Log holds the daemon logger field.
func (e *Engine) LogLines(project string, n int) ([]LogLine, error) {
	_, logs, err := e.loadProjectCopy(project)
	if err != nil {
		return nil, err
	}
	return LogView(logs, n), nil
}

func (e *Engine) loadProjectCopy(project string) (flow.State, []LogLine, error) {
	if project == "" || project == "." || project == ".." || filepath.Base(project) != project || strings.ContainsAny(project, `/\`) {
		return flow.State{}, nil, ErrProjectNotFound
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := LoadState(e.Paths.State(project))
	if os.IsNotExist(err) {
		return flow.State{}, nil, ErrProjectNotFound
	} else if err != nil {
		return flow.State{}, nil, err
	}
	logs, err := readLogFile(e.Paths.Log(project), true)
	return s, logs, err
}

func doneSet(tasks []flow.Task) map[string]bool {
	done := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		done[t.ID] = t.Done != nil
	}
	return done
}

var ladderStages = map[string]string{flow.Review: "review", flow.Fix: "fix", flow.RecapAnswer: "recap", flow.Passed: "landing"}

func taskStage(t flow.Task, settings flow.Settings, done map[string]bool) string {
	a := flow.OpenAttempt(t)
	switch {
	case t.Done != nil:
		return "done"
	case t.Closed != nil:
		return "closed"
	case t.Dropped != nil:
		return "dropped"
	case t.Split != nil:
		return "split"
	case t.Parked:
		return "parked"
	case flow.OpenRecap(t) != nil:
		return "recap"
	case a == nil && slices.ContainsFunc(t.After, func(dep string) bool { return !done[dep] }):
		return "waiting_on_dep"
	case a == nil:
		return "planned"
	case a.NothingToChange != "":
		return "waiting_on_coordinator"
	case len(a.Ready) == 0:
		return "working"
	default:
		return cmp.Or(ladderStages[flow.Ladder(flow.In{Task: t, Settings: settings})], "waiting_on_coordinator")
	}
}

func taskLayers(tasks []flow.Task) ([]int, int) {
	idx := make(map[string]int, len(tasks))
	for i, t := range tasks {
		idx[t.ID] = i
	}
	layers, visit, maxL := make([]int, len(tasks)), make([]uint8, len(tasks)), -1
	var depth func(int) int
	depth = func(i int) int {
		if visit[i] != 0 {
			return layers[i]
		}
		visit[i] = 1
		for _, dep := range tasks[i].After {
			if j, ok := idx[dep]; ok {
				layers[i] = max(layers[i], depth(j)+1)
			}
		}
		visit[i] = 2
		return layers[i]
	}
	for i := range tasks {
		maxL = max(maxL, depth(i))
	}
	return layers, maxL
}

func taskAttemptInfo(t flow.Task, last LogLine) (model, term string, rounds int) {
	if len(t.Attempts) > 0 {
		for _, r := range t.Attempts[len(t.Attempts)-1].Rounds {
			if r.Kind != flow.RoundSendBack {
				rounds++
			}
		}
	}
	if a := flow.OpenAttempt(t); a != nil {
		if a.Review != nil {
			return cmp.Or(a.Review.Reviewer.Model, last.Model), cmp.Or(a.Review.RunID, a.RunID), rounds
		}
		if a.RunID != "" {
			return workerModel, a.RunID, rounds
		}
	}
	return last.Model, "", rounds
}
