package supervisor

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/factory"
	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
	"github.com/kccarlos/herdr-sheepdog/internal/workerlog"
)

var errNoCoordinatorDialog = fmt.Errorf("%w: the factory coordinator has no pending dialog to answer", brain.ErrNotPermitted)

type brainRoster struct {
	listed, finished []state.Agent
	heads            map[string]factory.ProjectHead
	coordinators     map[string]state.Agent
}

func (h brainHost) roster() brainRoster {
	rows := h.s.store.List()
	heads := h.projectHeads()
	r := brainRoster{heads: map[string]factory.ProjectHead{}, coordinators: map[string]state.Agent{}}
	r.listed = slices.DeleteFunc(rows, func(row state.Agent) bool {
		return row.Labels["sheepdog.project"] != "" || slices.ContainsFunc(heads, func(p factory.ProjectHead) bool {
			return p.CoordinatorPane == row.PaneID
		})
	})
	for _, p := range heads {
		pane := factoryPanePrefix + p.ID
		name := cmp.Or(p.Name, factory.ProjectName(p.ID, p.Folder))
		// Keep the entire id suffix so projects with the same name and
		// first few hex digits still have stable, different aliases.
		alias := state.Slug(name+"-factory") + "-" + state.Slug(strings.TrimPrefix(p.ID, "proj-"))
		row := state.Agent{PaneID: pane, Alias: alias, Title: name, Spoken: "the " + name + " factory", Kind: "factory"}
		r.heads[pane] = p
		if p.State == "finished" {
			r.finished = append(r.finished, row)
			continue
		}
		if coord, _ := h.s.store.Get(p.CoordinatorPane); blockedCoordinator(p, coord) {
			r.coordinators[pane] = coord
		}
		r.listed = append(r.listed, row)
	}
	return r
}

func blockedCoordinator(p factory.ProjectHead, row state.Agent) bool {
	return p.State == "running" && row.PaneID != "" && p.CoordinatorPane == row.PaneID &&
		row.State == herdr.StateBlocked && cmp.Or(row.Labels["sheepdog.project"], p.ID) == p.ID &&
		cmp.Or(row.Labels["sheepdog.role"], flow.RoleCoordinator) == flow.RoleCoordinator
}

func (h brainHost) projectHeads() []factory.ProjectHead {
	if f, ok := h.s.cfg.Factory.(factoryBoard); ok {
		return f.ProjectHeads()
	}
	return nil
}

func (h brainHost) projectHead(id string) factory.ProjectHead {
	for _, head := range h.projectHeads() {
		if head.ID == id {
			return head
		}
	}
	return factory.ProjectHead{}
}

func (h brainHost) openAsks(projects bool) []flow.Ask {
	if projects {
		return h.s.cfg.Factory.Asks(true)
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
		if a.Project == id && a.Gated {
			out = append(out, a)
		}
	}
	return out
}

func projectCounts(p factory.ProjectHead, openAsks int) string {
	total := 0
	for _, n := range p.Counts {
		total += n
	}
	waiting := max(openAsks, p.Counts["owner_answer"]+p.Counts["owner_gate"])
	return fmt.Sprintf("%d working, %d waiting on you, %d of %d done", p.Counts["working"], waiting, p.Counts["done"], total)
}

func (h brainHost) readBoard(id string) (string, error) {
	p := h.projectHead(id)
	f, ok := h.s.cfg.Factory.(factoryBoard)
	if p.ID == "" || !ok {
		return "", factory.ErrProjectNotFound
	}
	graph, err := f.Graph(id)
	if err != nil {
		return "", err
	}
	logs, err := f.LogLines(id, 3, factory.CoordinatorProjectLog, false)
	if err != nil {
		return "", err
	}
	asks := projectAsks(h.s.cfg.Factory.Asks(true), id)
	var b strings.Builder
	name := cmp.Or(p.Name, factory.ProjectName(p.ID, p.Folder))
	fmt.Fprintf(&b, "%s: %s. %s. Goal: %q.\n", boardField(name), boardField(p.State), projectCounts(p, len(asks)), boardField(p.Goal))
	for _, l := range logs {
		fmt.Fprintf(&b, "Coordinator, %s: %q\n", boardField(l.Decision), boardField(l.Why))
	}
	const boardLimit = 8 << 10
	for i, a := range asks {
		label := strings.TrimSpace("Open ask " + a.Task)
		line := fmt.Sprintf("%s: %q. Options: %s\n", boardField(label), boardField(a.Question), boardField(strings.Join(a.Options, " / ")))
		if b.Len()+len(line) > 6<<10 {
			fmt.Fprintf(&b, "… (%d more asks omitted)\n", len(asks)-i)
			break
		}
		b.WriteString(line)
	}
	shown := 0
	for _, done := range []bool{false, true} {
		for _, t := range graph.Tasks {
			if (t.Stage == "done") != done {
				continue
			}
			line := boardTask(t, h.s.cfg.Now())
			if b.Len()+len(line) > boardLimit-64 {
				break
			}
			b.WriteString(line)
			shown++
		}
	}
	if omitted := len(graph.Tasks) - shown; omitted > 0 {
		fmt.Fprintf(&b, "… (%d more tasks omitted)\n", omitted)
	}
	h.s.setSubject(factoryPanePrefix + id)
	return strings.TrimSpace(b.String()), nil
}

func boardField(s string) string {
	return prose.TruncateRunes(strings.Join(strings.Fields(s), " "), 90)
}

func boardTask(t factory.TaskView, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %q — %s", boardField(t.Ref), boardField(t.Title), boardField(t.Stage))
	for _, step := range t.Steps {
		if step.State == "now" || step.State == "ended" {
			fmt.Fprintf(&b, ", %s", boardField(step.Name))
			if step.Text != "" && step.Text != t.WhyWaiting {
				fmt.Fprintf(&b, " (%s)", boardField(step.Text))
			}
		}
	}
	if t.RunCLI != "" {
		fmt.Fprintf(&b, ", %s", boardField(t.RunCLI))
	}
	if !t.RunStartedAt.IsZero() {
		fmt.Fprintf(&b, ", %d min", max(0, int(now.Sub(t.RunStartedAt)/time.Minute)))
	}
	if t.WhyWaiting != "" {
		fmt.Fprintf(&b, ". %s", boardField(t.WhyWaiting))
	}
	if l := t.LastActorDecision; l != nil {
		fmt.Fprintf(&b, ". Last (%s): %s; why: %q", boardField(l.By), boardField(l.Decision), boardField(l.Why))
	}
	return b.String() + "\n"
}
