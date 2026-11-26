package inject

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// FactoryProject is the fixture for a project, its coordinator and investigation.
type FactoryProject struct {
	factory.ProjectHead
	Tasks             []factory.TaskView `json:"tasks,omitempty"`
	Log               []factory.LogEntry `json:"log,omitempty"`
	Asks              []flow.Ask         `json:"asks,omitempty"`
	CoordinatorKind   string             `json:"coordinator_kind,omitempty"`
	CoordinatorState  string             `json:"coordinator_state,omitempty"`
	CoordinatorScreen string             `json:"coordinator_screen,omitempty"`
	Answer            string             `json:"answer,omitempty"`
}

// Factory is an in-memory stand-in for *factory.Engine (app-factory.md
// §9): scripted projects for the eval simulator and tests, with the
// engine's ask, proof, say, start and stop rules and its factory.changed
// event, but no store, git or runs.
type Factory struct {
	log logging.Logger

	mu       sync.Mutex
	projects map[string]*FactoryProject
	proofs   []factory.AnswerProof
	factory.ChangeStream
}

// NewFactory returns an empty fake factory logging to log (nil: nowhere).
func NewFactory(log logging.Logger) *Factory {
	return &Factory{log: cmp.Or[logging.Logger](log, logging.NopLogger{}), projects: map[string]*FactoryProject{}}
}

func scriptedAsk(a flow.Ask) flow.Ask {
	status := a.Status
	a.Status, a.Kind = "", strings.TrimSpace(a.Kind)
	a, _, _ = flow.StepAsk(a, flow.AskEvent{Kind: flow.EvAskOpen}, a.At)
	a.Status = cmp.Or(status, a.Status)
	return a.Clone()
}

// SetProject replaces a fixture and publishes a change for a new head or log.
func (f *Factory) SetProject(p FactoryProject) {
	p.Tasks, p.Log, p.Asks = slices.Clone(p.Tasks), slices.Clone(p.Log), slices.Clone(p.Asks)
	p.ProjectHead = p.Clone()
	p.Name = factory.ProjectName(p.ID, p.Folder)
	if p.State != "running" {
		p.CoordinatorPane = ""
	}
	if p.Counts == nil {
		p.Counts = map[string]int{}
		for _, t := range p.Tasks {
			p.Counts[t.Stage]++
		}
	}
	for i, a := range p.Asks {
		p.Asks[i] = scriptedAsk(a)
	}
	var change factory.Change
	defer f.edit(&change)()
	old := f.projects[p.ID]
	if old == nil || !reflect.DeepEqual(old.ProjectHead, p.ProjectHead) || !slices.Equal(old.Log, p.Log) {
		change.Project = p.ID
	}
	f.projects[p.ID] = &p
}

// edit acquires the project lock and returns a function to finish the edit.
func (f *Factory) edit(c *factory.Change) func() {
	f.mu.Lock()
	return func() {
		if c.Kind != "" {
			factory.PublishChange(&f.ChangeStream, *c)
		}
		f.mu.Unlock()
		if c.Kind == "say" {
			f.log.Log("factory", "factory.say", "", *c)
		}
		if c.Project != "" {
			f.log.Log("factory", "factory.changed", "", map[string]string{"project": c.Project})
		}
	}
}

// ProjectHeads returns the compact project views in id order.
func (f *Factory) ProjectHeads() []factory.ProjectHead {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]factory.ProjectHead, 0, len(f.projects))
	for _, id := range slices.Sorted(maps.Keys(f.projects)) {
		out = append(out, f.projects[id].Clone())
	}
	return out
}

// ProjectName is the project's spoken name, or id when unknown.
func (f *Factory) ProjectName(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.projects[id]; p != nil {
		return p.Name
	}
	return id
}

// Projects returns the project views of the full device route.
func (f *Factory) Projects() []factory.ProjectView {
	var out []factory.ProjectView
	for _, h := range f.ProjectHeads() {
		out = append(out, factory.ProjectView{Name: h.Name, CoordinatorPane: h.CoordinatorPane, Alert: h.Alert, ID: h.ID, Folder: h.Folder,
			Goal: h.Goal, State: h.State, Running: h.State == "running", Counts: h.Counts})
	}
	return out
}

// Graph returns the project's tasks.
func (f *Factory) Graph(id string) (factory.GraphView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.projects[id]; p != nil {
		return factory.GraphView{Protocol: 1, Project: id, Folder: p.Folder, Tasks: append([]factory.TaskView{}, p.Tasks...)}, nil
	}
	return factory.GraphView{}, factory.ErrProjectNotFound
}

// LogLines returns the matching tail of the project's scripted log.
func (f *Factory) LogLines(id string, n int, task string, observations bool) ([]factory.LogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := f.projects[id]
	if p == nil {
		return nil, factory.ErrProjectNotFound
	}
	if n <= 0 {
		n = factory.DefaultLogLimit
	}
	n = min(n, factory.MaxLogLimit)
	var out []factory.LogEntry
	for _, l := range p.Log {
		if factory.MatchLogTask(task, l.Task, l.By) && (observations || l.Kind != "observation" && l.Decision != "") {
			out = append(out, l)
		}
	}
	return out[max(0, len(out)-n):], nil
}

// OpenAsk opens a validated ask on its scripted project.
func (f *Factory) OpenAsk(a flow.Ask) {
	var change factory.Change
	defer f.edit(&change)()
	a, code, _ := flow.ValidateAsk(a)
	if p := f.projects[a.Project]; code == "" && p != nil {
		p.Asks = append(p.Asks, scriptedAsk(a))
		change = factory.Change{Project: a.Project, Kind: flow.EvAskOpen, AskID: a.ID, Task: a.Task}
	}
}

// Asks returns asks across projects (open only when open) in creation order.
func (f *Factory) Asks(open bool) []flow.Ask {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []flow.Ask
	for _, p := range f.projects {
		out = factory.AppendAskViews(out, p.Asks, p.ID, p.Folder, open)
	}
	return factory.SortAskViews(out)
}

// MintProof records a validated in-memory owner proof.
func (f *Factory) MintProof(askID, option, inputKind, deviceID string) (factory.AnswerProof, error) {
	p, refusal := factory.PrepareAnswerProof(askID, option, inputKind, deviceID)
	if refusal != nil {
		return p, refusal
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p.ID, p.At = fmt.Sprintf("prf-%d", len(f.proofs)+1), time.Now()
	f.proofs = append(f.proofs, p)
	return p, nil
}

// Answer applies the engine's option, proof and ask transition rules.
func (f *Factory) Answer(askID, option string, proof factory.AnswerProof) error {
	askID = strings.TrimSpace(askID)
	var change factory.Change
	defer f.edit(&change)()
	for id, p := range f.projects {
		if i := slices.IndexFunc(p.Asks, func(a flow.Ask) bool { return a.ID == askID }); i >= 0 {
			a := p.Asks[i]
			resolved, err := factory.CheckAskAnswer(a, option, proof, func() ([]factory.AnswerProof, error) { return f.proofs, nil })
			if err != nil {
				return err
			}
			p.Asks[i], _, _ = flow.StepAsk(a, flow.AskEvent{Kind: flow.EvAskAnswer, Answer: resolved, By: "owner", ProofID: proof.ID, DeviceID: proof.DeviceID}, time.Now())
			change = factory.Change{Project: id, Kind: "ask_answer", AskID: askID, Task: a.Task}
			return nil
		}
	}
	return &factory.Refusal{Code: "not_found", Why: fmt.Sprintf("No ask %q exists.", askID)}
}

// Say logs the owner's words and wakes change subscribers.
func (f *Factory) Say(project, text string) error {
	_, err := f.SayByID(context.Background(), strings.TrimSpace(project), text, "")
	return err
}

// SayByID records the same accepted instruction as the engine's ID route.
func (f *Factory) SayByID(ctx context.Context, project, text, heard string) (factory.ProjectHead, error) {
	var change factory.Change
	defer f.edit(&change)()
	if err := ctx.Err(); err != nil {
		return factory.ProjectHead{}, err
	}
	p := f.projects[project]
	switch {
	case p == nil:
		return factory.ProjectHead{}, factory.ErrProjectNotFound
	case p.State == "finished":
		return p.Clone(), &factory.Refusal{Code: flow.RefuseClosed, Why: p.Name + " is finished. Give it a new goal."}
	}
	text, _, ref := factory.NormalizeSay(text, heard)
	if ref != nil {
		return p.Clone(), ref
	}
	change = factory.Change{Project: project, Kind: "say", Text: text}
	return p.Clone(), nil
}

// Start starts a scripted project in folder with the supplied goal.
func (f *Factory) Start(folder, goal string, _ int) error {
	folder, goal = strings.TrimSpace(folder), strings.TrimSpace(goal)
	if folder == "" || goal == "" {
		return fmt.Errorf("folder and goal are required")
	}
	var change factory.Change
	defer f.edit(&change)()
	for id, p := range f.projects {
		if p.Folder == folder {
			if p.State == "finished" && p.Goal == goal {
				return &factory.Refusal{Code: flow.RefuseClosed, Why: "This project is finished. Give it a new goal."}
			}
			p.State, p.Goal, change.Project = "running", goal, id
			return nil
		}
	}
	return factory.ErrProjectNotFound
}

// Stop stops a running scripted project.
func (f *Factory) Stop(project string) error {
	var change factory.Change
	defer f.edit(&change)()
	p := f.projects[strings.TrimSpace(project)]
	if p == nil {
		return factory.ErrProjectNotFound
	}
	if p.State == "running" {
		p.State, p.CoordinatorPane, change.Project = "stopped", "", p.ID
	}
	return nil
}
