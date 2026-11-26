package supervisor

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

var errNoCoordinatorDialog = fmt.Errorf("%w: the factory coordinator has no pending dialog to answer", brain.ErrNotPermitted)

type brainRoster struct {
	factory          Factory
	listed, finished []state.Agent
	heads            map[string]factory.ProjectHead
	coordinators     map[string]state.Agent
	alternates       map[string]string
}

func (h brainHost) roster() brainRoster {
	rows := h.s.store.List()
	f, ok := h.s.cfg.Factory.(Factory)
	if !ok {
		return brainRoster{listed: rows}
	}
	heads := f.ProjectHeads()
	if len(heads) == 0 {
		return brainRoster{listed: rows}
	}
	r := brainRoster{factory: f, heads: map[string]factory.ProjectHead{},
		coordinators: map[string]state.Agent{}, alternates: map[string]string{}}
	byPane := make(map[string]state.Agent, len(rows))
	for _, row := range rows {
		if row.Labels["sheepdog.project"] == "" {
			r.listed = append(r.listed, row)
		} else {
			byPane[row.PaneID] = row
		}
	}
	for _, p := range heads {
		pane := factoryPanePrefix + p.ID
		// Keep the entire id suffix so projects with the same name and
		// first few hex digits still have stable, different aliases.
		alias := state.Slug(p.Name+"-factory") + "-" + strings.TrimPrefix(p.ID, "proj-")
		row := state.Agent{PaneID: pane, Alias: alias, Spoken: "the " + p.Name + " factory", Kind: "factory", State: herdr.State(p.State)}
		r.heads[pane] = p
		if p.State == "finished" {
			r.finished = append(r.finished, row)
			continue
		}
		if coord := byPane[p.CoordinatorPane]; blockedCoordinator(p, coord) {
			r.coordinators[pane], r.alternates[pane] = coord, coord.Alias
		}
		r.listed = append(r.listed, row)
	}
	return r
}

func blockedCoordinator(p factory.ProjectHead, row state.Agent) bool {
	return p.State == "running" && row.PaneID != "" && p.CoordinatorPane == row.PaneID &&
		row.State == herdr.StateBlocked && row.Labels["sheepdog.project"] == p.ID && row.Labels["sheepdog.role"] == flow.RoleCoordinator
}

func (h brainHost) projectHead(id string) factory.ProjectHead {
	if f, ok := h.s.cfg.Factory.(Factory); ok {
		for _, head := range f.ProjectHeads() {
			if head.ID == id {
				return head
			}
		}
	}
	return factory.ProjectHead{}
}

func (r brainRoster) openAsks(rows []state.Agent) []flow.Ask {
	if slices.ContainsFunc(rows, func(a state.Agent) bool { return r.heads[a.PaneID].ID != "" }) {
		return r.factory.Asks(true)
	}
	return nil
}

func (h brainHost) rosterInfo(r brainRoster, row state.Agent, asks []flow.Ask, pending state.PendingAction, has bool) brain.AgentInfo {
	p, project := r.heads[row.PaneID]
	if !project {
		return h.info(row, pending, has)
	}
	asks = projectAsks(asks, p.ID)
	redact := h.s.cfg.Redactor.String
	text := fmt.Sprintf("Goal: %q. %s.", workerlog.Clip(redact(p.Goal), 80), projectCounts(p, len(asks)))
	if len(asks) > 0 {
		text += fmt.Sprintf(" Open ask: %q", workerlog.Clip(redact(asks[0].Question), 75))
	}
	info := brain.AgentInfo{PaneID: row.PaneID, Alias: row.Alias, Spoken: row.Spoken, Kind: row.Kind, State: p.State, Project: true, Task: text}
	if coord, ok := r.coordinators[row.PaneID]; ok {
		info.Pending = h.info(coord, pending, has).Pending
	}
	return info
}

func projectAsks(asks []flow.Ask, id string) []flow.Ask {
	var out []flow.Ask
	for _, a := range asks {
		if a.Project == id {
			out = append(out, a)
		}
	}
	return out
}

func projectCounts(p factory.ProjectHead, openAsks int) string {
	total := 0
	for n := range maps.Values(p.Counts) {
		total += n
	}
	waiting := max(openAsks, p.Counts["owner_answer"]+p.Counts["owner_gate"])
	return fmt.Sprintf("%d working, %d waiting on you, %d of %d done", p.Counts["working"], waiting, p.Counts["done"], total)
}

func (h brainHost) readBoard(id string) (string, error) {
	p := h.projectHead(id)
	if p.ID == "" {
		return "", factory.ErrProjectNotFound
	}
	f := h.s.cfg.Factory.(Factory)
	graph, err := f.Graph(id)
	if err != nil {
		return "", err
	}
	logs, err := f.LogLines(id, 3, factory.CoordinatorProjectLog, false)
	if err != nil {
		return "", err
	}
	asks := projectAsks(f.Asks(true), id)
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s. %s. Goal: %q.\n", p.Name, p.State, projectCounts(p, len(asks)), p.Goal)
	for _, l := range logs {
		fmt.Fprintf(&b, "Coordinator, %s: %q\n", l.Decision, l.Why)
	}
	for _, t := range graph.Tasks {
		fmt.Fprintf(&b, "%s %q — %s", t.Ref, t.Title, t.Stage)
		for _, step := range t.Steps {
			if step.State == "now" || step.State == "ended" {
				fmt.Fprintf(&b, ", %s", step.Name)
				if step.Text != "" && step.Text != t.WhyWaiting {
					fmt.Fprintf(&b, " (%s)", step.Text)
				}
			}
		}
		if t.RunCLI != "" {
			fmt.Fprintf(&b, ", %s", t.RunCLI)
		}
		if !t.RunStartedAt.IsZero() {
			fmt.Fprintf(&b, ", %d min", max(0, int(h.s.cfg.Now().Sub(t.RunStartedAt)/time.Minute)))
		}
		if t.WhyWaiting != "" {
			fmt.Fprintf(&b, ". %s", t.WhyWaiting)
		}
		if l := t.LastActorDecision; l != nil {
			fmt.Fprintf(&b, ". Last (%s): %s; why: %q", l.By, l.Decision, l.Why)
		}
		b.WriteByte('\n')
	}
	for _, a := range asks {
		fmt.Fprintf(&b, "Open ask %s: %q. Options: %q\n", a.Task, a.Question, a.Options)
	}
	return strings.TrimSpace(b.String()), nil
}
