package factory

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// ErrProjectNotFound is returned when a project id has no stored state.
var ErrProjectNotFound = errors.New("factory project not found")

const DefaultLogLimit, MaxLogLimit = 50, 500

type ProjectView struct {
	Name            string         `json:"name"`
	CoordinatorPane string         `json:"coordinator_pane,omitempty"`
	Alert           *ProjectAlert  `json:"alert,omitempty"`
	ID              string         `json:"id"`
	Folder          string         `json:"folder,omitempty"`
	RepoURL         string         `json:"repo_url,omitempty"`
	Goal            string         `json:"goal,omitempty"`
	State           string         `json:"state,omitempty"`
	Running         bool           `json:"running"`
	Live            bool           `json:"live,omitempty"`
	Status          string         `json:"status,omitempty"`
	Counts          map[string]int `json:"counts,omitempty"`
	Error           string         `json:"error,omitempty"`
}

type ProjectHead struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Goal            string         `json:"goal"`
	State           string         `json:"state"`
	Counts          map[string]int `json:"counts"`
	Folder          string         `json:"folder,omitempty"`
	CoordinatorPane string         `json:"coordinator_pane,omitempty"`
	Alert           *ProjectAlert  `json:"alert,omitempty"`
}

type ProjectAlert struct {
	Kind  string `json:"kind"`
	Cause string `json:"cause"`
	Fix   string `json:"fix,omitempty"`
}

type GraphView struct {
	Protocol int        `json:"protocol"`
	Project  string     `json:"project"`
	Folder   string     `json:"folder"`
	Tasks    []TaskView `json:"tasks"`
}

type StepView struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Text  string `json:"text,omitempty"`
}

type TaskView struct {
	RunRole           string     `json:"run_role,omitempty"`
	RunCLI            string     `json:"run_cli,omitempty"`
	RunStartedAt      time.Time  `json:"run_started_at,omitzero"`
	LastActorDecision *LogEntry  `json:"last_actor_decision,omitempty"`
	Ref               string     `json:"ref"`
	Title             string     `json:"title"`
	Brief             string     `json:"brief,omitempty"`
	Stage             string     `json:"stage"`
	WhyWaiting        string     `json:"why_waiting,omitempty"`
	After             []string   `json:"after"`
	Dependents        []string   `json:"dependents"`
	Layer             int        `json:"layer"`
	Rounds            int        `json:"rounds"`
	Model             string     `json:"model,omitempty"`
	Terminal          string     `json:"terminal,omitempty"`
	LastRule          string     `json:"last_rule,omitempty"`
	LastDecision      string     `json:"last_decision,omitempty"`
	Steps             []StepView `json:"steps"`
}

// LogEntry is the read-only log projection (design.md 12.1); Wake/Model await core writers.
type LogEntry struct {
	At       time.Time `json:"at"`
	By       string    `json:"by"`
	Task     string    `json:"task,omitempty"`
	Decision string    `json:"decision,omitempty"`
	Why      string    `json:"why,omitempty"`
	Kind     string    `json:"kind,omitempty"`
}

var askLogKind = map[string]string{
	flow.AskMarkDone:    "merged",
	flow.AskNotifyOwner: "asked_you", flow.AskOpenGate: "asked_you", flow.AskOpenN24: "asked_you", flow.AskOwnerNoRecap: "asked_you",
	flow.AskOpenRecap: "needs_decision", flow.AskSizeRecap: "needs_decision", flow.AskCIRecap: "needs_decision", flow.AskTimeRecap: "needs_decision", flow.AskFailUpdate: "needs_decision",
}

func logKind(l LogLine) string {
	switch {
	case l.Decision == "":
		return "observation"
	case l.By == "owner":
		return "yours"
	}
	best := ""
	for _, a := range l.Asked {
		k, _, _ := strings.Cut(a, "@")
		if v := askLogKind[k]; v == "merged" {
			return v
		} else if v == "asked_you" || (v != "" && best == "") {
			best = v
		}
	}
	return best
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

func ProjectName(id, folder string) string {
	base := strings.TrimSpace(filepath.Base(strings.TrimSpace(folder)))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return id
	}
	return base
}

func projectAlert(alerts map[string]flow.Alert) *ProjectAlert {
	a := flow.StoppingAlert(alerts)
	if a == nil {
		for _, key := range slices.Sorted(maps.Keys(alerts)) {
			candidate := alerts[key]
			if a == nil || candidate.Kind == flow.AlertAgentsDown {
				a = &candidate
			}
			if candidate.Kind == flow.AlertAgentsDown {
				break
			}
		}
	}
	if a == nil {
		return nil
	}
	return &ProjectAlert{Kind: a.Kind, Cause: a.Cause, Fix: a.Fix}
}

func currentCoordinator(p flow.Project) bool {
	return p.RunID != "" && flow.RunLive(p.RunID, p.Run)
}

func projectHeadOf(s flow.State) ProjectHead {
	p := s.Project
	h := ProjectHead{ID: p.ID, Name: ProjectName(p.ID, p.Folder), Goal: p.Goal, Folder: p.Folder,
		State: "stopped", Counts: map[string]int{}, Alert: projectAlert(p.Alerts)}
	if p.Finished {
		h.State = "finished"
	} else if p.Started {
		h.State = "running"
	}
	if currentCoordinator(p) {
		h.CoordinatorPane = p.RunID
	}
	for _, t := range s.Tasks {
		stage, _, _ := taskStageWhy(t, s.Tasks, s.Asks, s.Settings, p.Started)
		h.Counts[stage]++
	}
	return h
}

func projectViewOf(s flow.State) ProjectView {
	h, status := projectHeadOf(s), ""
	if s.Project.Started && !s.Project.Finished && !flow.ProjectFinished(s.Project, s.Tasks) {
		if open := len(flow.OpenPlanFindings(s.Project)); open > 0 {
			status = fmt.Sprintf("%d plan findings open, waiting on the coordinator", open)
		} else if n := len(s.Project.PlanReviews); n > 0 {
			if pr := s.Project.PlanReviews[n-1]; pr.Accepted.IsZero() && pr.Verdict == "" && pr.Run != nil && flow.RunLive(pr.Run.RunID, pr.Run.Run) {
				status = "Plan review"
			}
		}
	}
	return ProjectView{ID: h.ID, Name: h.Name, Folder: h.Folder, RepoURL: s.Project.RepoURL, Goal: h.Goal,
		State: h.State, Running: s.Project.Started, Live: projectLive(s), Status: status, Counts: h.Counts,
		CoordinatorPane: h.CoordinatorPane, Alert: h.Alert}
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
	lastActor := map[string]*LogEntry{}
	for _, l := range logs {
		if l.Task != "" && l.Decision != "" && (l.By == "coordinator" || l.By == "owner") {
			lastActor[l.Task] = &LogEntry{At: l.At, By: l.By, Decision: l.Decision, Why: l.Why}
		}
	}
	return graphViewOf(s, lastTaskRows(logs), lastActor)
}

func revActive(a *flow.Attempt) bool {
	return a != nil && a.Review != nil && (a.Review.Run == nil || a.Review.Run.State != flow.PhaseEnded) &&
		!slices.ContainsFunc(a.Rounds, func(r flow.Round) bool { return r.N == a.Review.Round && r.Verdict != "" })
}

func graphViewOf(s flow.State, lastRow map[string]LogLine, lastActor map[string]*LogEntry) GraphView {
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
		stage, why, row := taskStageWhy(t, s.Tasks, s.Asks, s.Settings, s.Project.Started)
		l, model, term, rounds, att := lastRow[t.ID], "", "", 0, flow.OpenAttempt(t)
		if att != nil {
			rounds = len(slices.DeleteFunc(slices.Clone(flow.Counted(*att)), func(r flow.Round) bool { return r.Kind == flow.RoundSendBack || r.Kind == flow.RoundRetro }))
		}
		var run *flow.RunRecord
		if revActive(att) {
			model, term = att.Review.Reviewer.Model, cmp.Or(att.Review.RunID, att.RunID)
			run = att.Review.Run
		} else if att != nil && att.RunID != "" {
			model, term = storedWorkerModel(t).Model, att.RunID
			run = att.Run
		} else if att != nil {
			run = att.Run
		}
		tv := TaskView{
			Ref: t.ID, Title: t.Title, Brief: t.Brief, Stage: stage, WhyWaiting: why, After: append([]string{}, t.After...), Dependents: append([]string{}, deps[t.ID]...),
			Layer: layers[i], Rounds: rounds, Model: model, Terminal: term, LastRule: l.Rule, LastDecision: l.Decision, Steps: taskSteps(t, s, att, stage, why, row, rounds),
			LastActorDecision: lastActor[t.ID],
		}
		if run != nil && flow.RunLive("", run) {
			tv.RunRole, tv.RunCLI, tv.RunStartedAt = run.Role, run.CLI, run.StartedAt
		}
		tasks = append(tasks, tv)
	}
	return GraphView{Protocol: 1, Project: s.Project.ID, Folder: s.Project.Folder, Tasks: tasks}
}

func LogView(lines []LogLine, n int, observations ...bool) []LogEntry {
	if n <= 0 {
		n = DefaultLogLimit
	}
	n, showObs := min(n, MaxLogLimit), len(observations) > 0 && observations[0]
	out := make([]LogEntry, 0, min(len(lines), n))
	for _, l := range lines {
		if k := logKind(l); showObs || k != "observation" {
			out = append(out, LogEntry{At: l.At, By: l.By, Task: l.Task, Decision: l.Decision, Why: l.Why, Kind: k})
		}
	}
	return out[max(0, len(out)-n):]
}

func (e *Engine) Projects() []ProjectView {
	out := []ProjectView{}
	for _, ent := range e.projectDirs() {
		if !ent.IsDir() {
			continue
		}
		if s, err := LoadState(e.Paths.State(ent.Name())); err != nil {
			out = append(out, ProjectView{ID: ent.Name(), Name: ent.Name(), Error: err.Error()})
		} else {
			out = append(out, projectViewOf(s))
		}
	}
	slices.SortFunc(out, func(a, b ProjectView) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

func (e *Engine) initProjectHeads() {
	e.projectHeadsOnce.Do(func() {
		e.projectHeadsMu.Lock()
		defer e.projectHeadsMu.Unlock()
		e.projectHeads = map[string]ProjectHead{}
		e.projectPanes = map[string]map[string]Pane{}
		entries, _ := e.readProjectDirs()
		for _, ent := range entries {
			if ent.IsDir() {
				if s, err := LoadState(e.Paths.State(ent.Name())); err == nil {
					e.projectHeads[ent.Name()] = projectHeadOf(s)
					e.projectPanes[ent.Name()] = projectPanesOf(s)
				}
			}
		}
	})
}

// ProjectHeads returns the compact project views for /api/state in id order.
func (e *Engine) ProjectHeads() []ProjectHead {
	e.initProjectHeads()
	e.projectHeadsMu.RLock()
	defer e.projectHeadsMu.RUnlock()
	out := make([]ProjectHead, 0, len(e.projectHeads))
	for _, id := range slices.Sorted(maps.Keys(e.projectHeads)) {
		h := e.projectHeads[id]
		h.Counts = maps.Clone(h.Counts)
		if h.Alert != nil {
			a := *h.Alert
			h.Alert = &a
		}
		out = append(out, h)
	}
	return out
}

func (e *Engine) ProjectName(id string) string {
	e.initProjectHeads()
	e.projectHeadsMu.RLock()
	defer e.projectHeadsMu.RUnlock()
	return cmp.Or(e.projectHeads[id].Name, id)
}

// Pane describes a live terminal named by the factory's saved run records.
type Pane struct {
	Name, CLI, State   string
	StateSince         time.Time
	CurrentCoordinator bool
}

func projectPanesOf(s flow.State) map[string]Pane {
	p := s.Project
	if !currentCoordinator(p) {
		return nil
	}
	info := Pane{Name: ProjectName(p.ID, p.Folder), CurrentCoordinator: true}
	if r := p.Run; r != nil {
		info.CLI, info.State, info.StateSince = r.CLI, r.State, r.StatusSince
	}
	return map[string]Pane{p.RunID: info}
}

// Pane looks up a run by terminal address using the project-head cache lock.
func (e *Engine) Pane(id string) (Pane, bool) {
	e.initProjectHeads()
	e.projectHeadsMu.RLock()
	defer e.projectHeadsMu.RUnlock()
	var best Pane
	var project string
	found := false
	for key, panes := range e.projectPanes {
		if pane, ok := panes[id]; ok && (!found || key < project) {
			best, project, found = pane, key, true
		}
	}
	return best, found
}

func (e *Engine) publishProjectHead(id string, s flow.State, logLine bool) {
	h, panes := projectHeadOf(s), projectPanesOf(s)
	e.projectHeadsMu.Lock()
	changed := !reflect.DeepEqual(e.projectHeads[id], h)
	e.projectHeads[id], e.projectPanes[id] = h, panes
	e.projectHeadsMu.Unlock()
	if logLine || changed {
		e.logEvent("factory.changed", map[string]string{"project": id})
	}
}

func (e *Engine) Graph(project string) (GraphView, error) {
	s, logs, err := e.loadProjectCopy(project)
	if err != nil {
		return GraphView{}, err
	}
	return GraphViewOf(s, logs), nil
}

func (e *Engine) LogLines(project string, n int, task string, observations bool) ([]LogEntry, error) {
	_, logs, err := e.loadProjectCopy(project)
	if err != nil {
		return nil, err
	}
	if task != "" {
		logs = slices.DeleteFunc(logs, func(l LogLine) bool { return l.Task != task })
	}
	return LogView(logs, n, observations), nil
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

func taskStageWhy(t flow.Task, tasks []flow.Task, asks []flow.Ask, settings flow.Settings, started bool) (string, string, flow.Row[flow.In]) {
	r, ok := taskRows[t.LastRow]
	if a := flow.OpenOwnerAskForTask(asks, t.ID); a != nil {
		if a.Kind == flow.KindGate {
			return "owner_gate", a.Question, r
		}
		return "owner_answer", a.Question, r
	}
	if flow.Ended(t) {
		return "done", "", r
	}
	if t.Parked {
		return "blocked_or_failed", "Parked, waiting on the coordinator", r
	}
	notDone, dropped := flow.UnmetDeps(t, tasks)
	unmet := append(notDone, dropped...)
	a := flow.OpenAttempt(t)
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
	return stage, taskWhy(t, a, unmet, started, stage, r), r
}

func stepFromRow(t flow.Task, st flow.State, att *flow.Attempt, r flow.Row[flow.In], hasRetro bool) string {
	if t.Unreviewed && st.Settings.PlanReviewRounds > 0 {
		return "plan review"
	}
	if att == nil {
		return "planned"
	}
	rec := flow.OpenRecap(t)
	switch {
	case r.Stage == "ci" || r.ID == "N17":
		return "CI"
	case r.Stage == "working":
		return cmp.Or(map[string]string{"the reviewer": "review", "the retro reviewer": "retro"}[r.WaitsOn], "implementing")
	case len(att.Ready) == 0 || att.NothingToChange != "" || r.ID == "N12" || r.ID == "N14" || rec != nil && (rec.Trigger == flow.TriggerSize || rec.Trigger == flow.TriggerTime):
		return "implementing"
	case r.ID == "N24" || r.ID == "N25" || rec != nil && rec.Trigger == flow.TriggerReview:
		return map[bool]string{true: "retro", false: "review"}[hasRetro]
	}
	lr, _ := flow.FirstMatch(flow.LadderRows, flow.In{Task: t, Settings: st.Settings})
	switch {
	case lr.Stage == "ci":
		return "CI"
	case lr.WaitsOn == "the worker":
		return "implementing"
	case hasRetro && (lr.ID == "L2" || lr.ID == "L3" || lr.WaitsOn == "the retro reviewer"):
		return "retro"
	default:
		return "review"
	}
}

func taskSteps(t flow.Task, st flow.State, att *flow.Attempt, stage, why string, r flow.Row[flow.In], rounds int) []StepView {
	cfg := st.Settings
	prDone := cfg.PlanReviewRounds > 0 && !t.Unreviewed && slices.ContainsFunc(st.Project.PlanReviews, func(pr flow.PlanReview) bool {
		return slices.Contains(pr.Tasks, t.ID) && (!pr.Accepted.IsZero() || pr.Verdict == flow.VerdictUnavailable)
	})
	hasPR := cfg.PlanReviewRounds > 0 && (t.Unreviewed || prDone)
	h, nc, hasRetro, revPassed, revSkipped := "", (*flow.Round)(nil), false, false, false
	if att != nil {
		h = flow.H(*att)
		nc = flow.NewestCovering(*att, h)
		hasRetro = att.Review != nil && att.Review.Role() == flow.RoleRetro || len(flow.AttemptRetros(*att)) > 0
		revPassed = nc != nil && (nc.Verdict == flow.VerdictPass && len(flow.PassingCovering(*att, h)) >= cfg.ReviewRounds || nc.Kind == flow.RoundRetro && nc.Verdict == flow.VerdictShip)
		revSkipped = !revPassed && (r.ID == "N19b" || flow.CoveringOverride(*att, h) != nil || slices.ContainsFunc(att.Accepts, func(ac flow.Accept) bool { return ac.By == "override" && flow.Covers(*att, ac.Head, h) }))
	}
	term, unmerged, now := flow.Ended(t) || r.Stage == "done", t.Dropped != nil || t.Closed != nil || t.Split != nil, ""
	if !term {
		now = stepFromRow(t, st, att, r, hasRetro)
	}
	names := slices.DeleteFunc([]string{"plan review", "planned", "implementing", "review", "retro", "CI", "merged"}, func(x string) bool {
		return x == "plan review" && !hasPR || x == "retro" && !hasRetro && now != "retro"
	})
	nowIdx := len(names)
	if !term {
		nowIdx = slices.Index(names, now)
	} else if unmerged {
		last := cmp.Or(map[bool]string{true: "CI"}[att != nil && len(att.Ready) > 0 && (revPassed || revSkipped || slices.ContainsFunc(att.Accepts, func(ac flow.Accept) bool { return flow.Covers(*att, ac.Head, h) }))],
			map[bool]string{true: "retro"}[att != nil && hasRetro], map[bool]string{true: "review"}[att != nil && (len(att.Rounds) > 0 || att.Review != nil || revSkipped)],
			map[bool]string{true: "implementing"}[att != nil], map[bool]string{true: "planned"}[!hasPR || prDone], "plan review")
		names = names[:slices.Index(names, last)+1]
	}
	rnd, needs := cmp.Or(map[int]string{1: "1 round done"}[rounds], map[bool]string{true: fmt.Sprintf("%d rounds done", rounds)}[rounds > 1]), fmt.Sprintf("needs %d passing, at most %d", cfg.ReviewRounds, cfg.MaxRounds)
	liveRev, steps := (stage == "working" || r.Stage == "working") && !t.Parked && revActive(att), make([]StepView, len(names))
	for i, name := range names {
		s, txt := cmp.Or(map[bool]string{true: "skipped"}[name == "review" && i < nowIdx && revSkipped], map[int]string{-1: "done", 0: "now", 1: "todo"}[cmp.Compare(i, nowIdx)]), ""
		switch {
		case s == "now" && t.Parked:
			txt = "parked here, waiting on the coordinator"
		case name == "implementing" && s == "now" && rounds > 0 && att != nil && flow.CoveringOverride(*att, h) == nil && nc != nil && (nc.Kind == flow.RoundFull || nc.Kind == flow.RoundDelta) && nc.Verdict == flow.VerdictChanges:
			txt = fmt.Sprintf("sent back from review round %d; %s", nc.N, rnd)
		case name == "review" && s == "now" && liveRev && att.Review.Role() == flow.RoleReviewer:
			txt = fmt.Sprintf("review round %d; %s", cmp.Or(att.Review.Round, len(att.Rounds)+1), needs)
		case name == "review" && s == "now":
			txt = cmp.Or(map[bool]string{true: rnd}[revPassed], map[bool]string{true: rnd + "; " + needs}[rnd != ""], needs)
		case name == "review":
			txt = map[string]string{"todo": needs, "done": rnd, "skipped": "skipped by your override"}[s]
		case name == "retro" && s == "now" && liveRev && att.Review.Role() == flow.RoleRetro:
			txt = "a retro reviewer is looking at why review keeps failing"
		case name == "CI" && s == "now":
			txt = why
		case name == "merged" && s == "done" && att != nil && att.PR > 0:
			txt = fmt.Sprintf("PR #%d", att.PR)
		}
		steps[i] = StepView{Name: name, State: s, Text: txt}
	}
	if unmerged {
		steps = append(steps, map[bool]StepView{false: {Name: map[bool]string{true: "split", false: "dropped"}[t.Split != nil], State: "ended", Text: "ended without merging"}, true: {Name: "closed", State: "ended", Text: "ended without merging: its PR was closed"}}[t.Closed != nil])
	}
	return steps
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
		return fmt.Sprintf("In review round %d", cmp.Or(a.Review.Round, len(a.Rounds)+1))
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
