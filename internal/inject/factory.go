package inject

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// FactoryProject is one scripted project of the fake Factory: its head as
// /api/state shows it, its tasks, its log and its asks, in the real
// factory types.
type FactoryProject struct {
	factory.ProjectHead
	Tasks []factory.TaskView
	Log   []factory.LogEntry
	Asks  []flow.Ask
}

// Factory is an in-memory stand-in for *factory.Engine (app-factory.md
// §9): scripted projects for the eval simulator and tests, with the
// engine's ask, proof, say, start and stop rules and its factory.changed
// event, but no store, git or runs.
type Factory struct {
	log logging.Logger

	mu       sync.Mutex
	projects map[string]*FactoryProject
	proofs   map[string]factory.AnswerProof
	subs     []chan factory.Change
}

// NewFactory returns an empty fake factory logging to log (nil: nowhere).
func NewFactory(log logging.Logger) *Factory {
	return &Factory{log: log, projects: map[string]*FactoryProject{}, proofs: map[string]factory.AnswerProof{}}
}

// headOf completes a head as the engine would: the name from the folder,
// no coordinator unless running, counts from the task stages.
func headOf(p FactoryProject) factory.ProjectHead {
	h := p.ProjectHead
	h.Name = factory.ProjectName(h.ID, h.Folder)
	if h.State != "running" {
		h.CoordinatorPane = ""
	}
	if h.Counts == nil {
		h.Counts = map[string]int{}
		for _, t := range p.Tasks {
			h.Counts[t.Stage]++
		}
	}
	return h
}

// SetProject adds or replaces a project; factory.changed is logged only
// when its head changed (app-factory.md §4).
func (f *Factory) SetProject(p FactoryProject) {
	p.Tasks, p.Log, p.Asks = slices.Clone(p.Tasks), slices.Clone(p.Log), slices.Clone(p.Asks)
	p.ProjectHead = headOf(p)
	f.mu.Lock()
	old, ok := f.projects[p.ID]
	f.projects[p.ID] = &p
	f.mu.Unlock()
	if !ok || !reflect.DeepEqual(old.ProjectHead, p.ProjectHead) {
		f.changed(p.ID)
	}
}

func (f *Factory) changed(id string) {
	if f.log != nil {
		f.log.Log("factory", "factory.changed", "", map[string]string{"project": id})
	}
}

func (f *Factory) notifyLocked(c factory.Change) {
	for _, ch := range f.subs {
		select {
		case ch <- c:
		default:
		}
	}
}

// Changes returns a buffered stream of asks, answers and says.
func (f *Factory) Changes() <-chan factory.Change {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan factory.Change, 64)
	f.subs = append(f.subs, ch)
	return ch
}

// ProjectHeads returns the compact project views in id order.
func (f *Factory) ProjectHeads() []factory.ProjectHead {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]factory.ProjectHead, 0, len(f.projects))
	for _, id := range slices.Sorted(maps.Keys(f.projects)) {
		h := f.projects[id].ProjectHead
		h.Counts = maps.Clone(h.Counts)
		out = append(out, h)
	}
	return out
}

// ProjectName is the project's spoken name, or id when unknown.
func (f *Factory) ProjectName(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.projects[id]; p != nil {
		return cmp.Or(p.Name, id)
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

func (f *Factory) project(id string) (*FactoryProject, error) {
	if p := f.projects[id]; p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("%w: %s", factory.ErrProjectNotFound, id)
}

// Graph returns the project's tasks.
func (f *Factory) Graph(id string) (factory.GraphView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.project(id)
	if err != nil {
		return factory.GraphView{}, err
	}
	return factory.GraphView{Protocol: 1, Project: id, Folder: p.Folder, Tasks: slices.Clone(p.Tasks)}, nil
}

// LogLines returns the last n log lines (of task, when set); observations
// (lines with no decision) only when asked for.
func (f *Factory) LogLines(id string, n int, task string, observations bool) ([]factory.LogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.project(id)
	if err != nil {
		return nil, err
	}
	n = min(cmp.Or(max(n, 0), factory.DefaultLogLimit), factory.MaxLogLimit)
	out := slices.DeleteFunc(slices.Clone(p.Log), func(l factory.LogEntry) bool {
		return (task != "" && l.Task != task) || (!observations && l.Decision == "")
	})
	return out[max(0, len(out)-n):], nil
}

// OpenAsk opens a on its project (a.Project), as a run's ask report would.
func (f *Factory) OpenAsk(a flow.Ask) {
	f.mu.Lock()
	p := f.projects[a.Project]
	if p == nil {
		f.mu.Unlock()
		return
	}
	a.Status = cmp.Or(a.Status, flow.AskStatusOpen)
	p.Asks = append(p.Asks, a)
	f.notifyLocked(factory.Change{Project: a.Project, Kind: flow.EvAskOpen, AskID: a.ID, Task: a.Task})
	f.mu.Unlock()
	f.changed(a.Project)
}

// Asks returns asks across projects (open only when open) in creation order.
func (f *Factory) Asks(open bool) []flow.Ask {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []flow.Ask
	for id, p := range f.projects {
		for _, a := range p.Asks {
			if !open || a.Status == flow.AskStatusOpen {
				a.Project, a.Options = cmp.Or(a.Project, id), slices.Clone(a.Options)
				out = append(out, a)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b flow.Ask) int { return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.Project, b.Project)) })
	return out
}

// MintProof records an owner proof; like the engine's, only a tap or
// voice from a paired device (never "local") counts.
func (f *Factory) MintProof(askID, option, inputKind, deviceID string) (factory.AnswerProof, error) {
	if (inputKind != factory.ProofInputTap && inputKind != factory.ProofInputVoice) || deviceID == "" || deviceID == "local" || askID == "" || option == "" {
		return factory.AnswerProof{}, &factory.Refusal{Code: "proof_rejected", Why: "Owner answer proof requires tap or voice input from a paired device or live voice session."}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p := factory.AnswerProof{ID: fmt.Sprintf("prf-%d", len(f.proofs)+1), AskID: askID, Option: option, InputKind: inputKind, DeviceID: deviceID}
	f.proofs[p.ID] = p
	return p, nil
}

// Answer answers an open ask; every answer needs a minted proof for that
// ask and option.
func (f *Factory) Answer(askID, option string, proof factory.AnswerProof) error {
	f.mu.Lock()
	for id, p := range f.projects {
		i := slices.IndexFunc(p.Asks, func(a flow.Ask) bool { return a.ID == askID })
		if i < 0 {
			continue
		}
		a := &p.Asks[i]
		if a.Status != flow.AskStatusOpen {
			f.mu.Unlock()
			return &factory.Refusal{Code: flow.RefuseClosed, Why: "The ask is already closed."}
		}
		if got, ok := f.proofs[proof.ID]; !ok || got != proof || got.AskID != askID || got.Option != option {
			f.mu.Unlock()
			return &factory.Refusal{Code: "proof_rejected", Why: "Gated asks require an owner AnswerProof from a paired device or live voice."}
		}
		a.Status, a.Answer = flow.AskStatusAnswered, option
		f.notifyLocked(factory.Change{Project: id, Kind: "ask_answer", AskID: askID, Task: a.Task})
		f.mu.Unlock()
		f.changed(id)
		return nil
	}
	f.mu.Unlock()
	return &factory.Refusal{Code: "not_found", Why: fmt.Sprintf("No ask %q exists.", askID)}
}

// Say logs the owner's words on project; a finished project refuses them.
func (f *Factory) Say(project, text string) error {
	f.mu.Lock()
	p, err := f.project(project)
	switch msg := strings.TrimSpace(text); {
	case err != nil:
	case p.State == "finished":
		err = &factory.Refusal{Code: flow.RefuseClosed, Why: "the project is finished; start it with a different goal"}
	case msg == "":
		err = &factory.Refusal{Code: "why_missing", Why: "Say requires non-empty text."}
	default:
		p.Log = append(p.Log, factory.LogEntry{By: "owner", Decision: "owner said", Why: msg})
		f.notifyLocked(factory.Change{Project: project, Kind: "say", Text: msg})
	}
	f.mu.Unlock()
	if err == nil {
		f.changed(project)
	}
	return err
}

func (f *Factory) setState(match func(*FactoryProject) bool, st string) error {
	f.mu.Lock()
	for _, p := range f.projects {
		if match(p) {
			p.State = st
			p.ProjectHead = headOf(*p)
			f.mu.Unlock()
			f.changed(p.ID)
			return nil
		}
	}
	f.mu.Unlock()
	return factory.ErrProjectNotFound
}

// Start resumes the project in folder (the fake makes no new projects).
func (f *Factory) Start(folder, _ string, _ int) error {
	return f.setState(func(p *FactoryProject) bool { return p.Folder == folder }, "running")
}

// Stop stops project; the plan is kept and its coordinator goes.
func (f *Factory) Stop(project string) error {
	return f.setState(func(p *FactoryProject) bool { return p.ID == project }, "stopped")
}

// Report refuses every run report: the fake has no runs.
func (f *Factory) Report(_, verb string, _ json.RawMessage) error {
	return &factory.Refusal{Code: "invalid_report", Why: "The fake factory takes no " + verb + " report."}
}
