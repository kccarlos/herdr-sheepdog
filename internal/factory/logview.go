package factory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// ProjectLog reads the last n lines (all when n <= 0) of a project's
// log.jsonl and its formatted first-look pass rate summary (empty when no task
// has a reviewed round). It uses the same stored project lookup as
// ProjectStatus and skips an incomplete last line.
func ProjectLog(_ context.Context, paths Paths, folder string, n int) ([]LogLine, string, error) {
	id, err := findStoredProject(paths, strings.TrimSpace(folder))
	if err != nil {
		return nil, "", err
	}
	s, lines, err := (&Engine{Paths: paths}).loadProjectCopy(id)
	if err != nil {
		return nil, "", err
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, FormatFirstLook(s.Tasks), nil
}

// FormatFirstLook formats the project's first-look pass rate (tasks whose
// first reviewed round passed / tasks with at least one reviewed round), or ""
// when no task has been reviewed yet.
func FormatFirstLook(tasks []flow.Task) string {
	passed, reviewed := flow.FirstLookPassRate(tasks)
	if reviewed == 0 {
		return ""
	}
	return fmt.Sprintf("first-look pass rate: %d/%d (%d%%)", passed, reviewed, (passed*100+reviewed/2)/reviewed)
}

var fullSHA = regexp.MustCompile(`\b[0-9a-f]{40}\b`)

// FormatLogLine is one log line for the owner: time, task, rule, what was
// asked, the decision and why (design.md 14). Full commit ids are cut to 7.
func FormatLogLine(l LogLine) string {
	s := fmt.Sprintf("%s  %-4s %-11s asked: %s | decision: %s | why: %s",
		l.At.Format("2006-01-02 15:04:05"), cmp.Or(l.Task, "-"), l.Rule,
		cmp.Or(strings.Join(l.Asked, ", "), "-"), cmp.Or(l.Decision, "-"), cmp.Or(l.Why, "-"))
	return fullSHA.ReplaceAllStringFunc(s, func(m string) string { return m[:7] })
}

// TaskStatus is one task's status for `sheepdog factory status` (design.md 4.11).
type TaskStatus struct {
	ID, Title, Stage, Rule, Why string
	At                          time.Time
	Stale                       bool
}

// StatusViewOf derives each task's status from stored state and log.jsonl.
// Stage comes from the rows via GraphViewOf so `sheepdog factory status`,
// `/factory`, and the stale check always agree from one source.
func StatusViewOf(s flow.State, logs []LogLine) []TaskStatus {
	lastLog, lastRow := lastTaskRows(logs)
	g := graphViewOf(s, lastLog)
	out := make([]TaskStatus, 0, len(g.Tasks)+len(s.Project.Alerts))
	for _, tv := range g.Tasks {
		why := "no logged row"
		if tv.WhyWaiting != "" {
			why = tv.WhyWaiting + " (no logged row)"
		}
		ts := TaskStatus{ID: tv.Ref, Title: tv.Title, Stage: tv.Stage, Why: why}
		if l, ok := lastRow[tv.Ref]; ok {
			ts.Rule, ts.Why, ts.At, ts.Stale = l.Rule, tv.WhyWaiting, l.At, taskRows[l.Rule].Stage != tv.Stage && !strings.Contains(" "+strings.Join(l.Asked, " ")+" ", " "+flow.AskWake+"@worker_failed ")
		}
		out = append(out, ts)
	}
	for _, k := range slices.Sorted(maps.Keys(s.Project.Alerts)) {
		a := s.Project.Alerts[k]
		why := cmp.Or(a.Cause, a.Kind)
		if a.Fix != "" {
			why += " — " + a.Fix
		}
		out = append(out, TaskStatus{ID: "ALERT", Title: a.Kind, Stage: map[bool]string{true: "stopped", false: "alert"}[a.Stopping], Rule: a.Rule, Why: why, At: a.At})
	}
	return out
}

// ProjectStatus reads stored state.json and log.jsonl for folder (or the single
// started/stored project when folder is empty). It makes no hc, git or gh calls
// and works when the daemon is stopped.
func ProjectStatus(paths Paths, folder string) ([]TaskStatus, error) {
	id, err := findStoredProject(paths, strings.TrimSpace(folder))
	if err != nil {
		return nil, err
	}
	s, logs, err := (&Engine{Paths: paths}).loadProjectCopy(id)
	if err != nil {
		return nil, err
	}
	return StatusViewOf(s, logs), nil
}

func findStoredProject(paths Paths, target string) (string, error) {
	dir := filepath.Join(paths.DataDir, "factory", "projects")
	if err := guardTestPath(dir); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	var started, all []string
	var loadErr error
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		s, err := LoadState(paths.State(ent.Name()))
		if err != nil {
			err = fmt.Errorf("load project %s: %w", ent.Name(), err)
			if target == ent.Name() {
				return "", err
			}
			loadErr = cmp.Or(loadErr, err)
			continue
		}
		if target != "" {
			if ent.Name() == target || underFolderLocal(s.Project.Folder, target) {
				return ent.Name(), nil
			}
			continue
		}
		all = append(all, ent.Name())
		if s.Project.Started {
			started = append(started, ent.Name())
		}
	}
	switch {
	case target != "" && loadErr != nil:
		return "", loadErr
	case target != "":
		return "", fmt.Errorf("no factory project for %s", target)
	case len(started) == 1:
		return started[0], nil
	case len(started) > 1:
		return "", errors.New("multiple projects started; pass sheepdog factory status <folder>")
	case len(all) == 1:
		return all[0], nil
	case len(all) > 1:
		return "", errors.New("multiple factory projects; pass sheepdog factory status <folder>")
	case loadErr != nil:
		return "", loadErr
	default:
		return "", errors.New("no factory project found")
	}
}

func underFolder(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return root != "" && target != "" && err == nil && filepath.IsLocal(rel)
}

// underFolderLocal is used only by commands in the owner's login session.
func underFolderLocal(root, target string) bool {
	if root == "" || target == "" {
		return false
	}
	for dir := filepath.Clean(target); ; {
		if sameFolder(root, dir) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func sameFolder(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if ca, cb := filepath.Clean(a), filepath.Clean(b); ca == cb {
		return true
	} else if ra, errA := filepath.EvalSymlinks(ca); errA == nil {
		rb, errB := filepath.EvalSymlinks(cb)
		return errB == nil && ra == rb
	}
	return false
}

func formatAge(now, at time.Time) string {
	if at.IsZero() {
		return ""
	}
	d := max(0, now.Sub(at))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

// FormatStatusLine formats one task status line: id, short title, stage, last
// logged row, its age and why. Full commit ids are cut to 7.
func FormatStatusLine(ts TaskStatus) string {
	id, title, rule, why := cmp.Or(flow.CleanText(ts.ID), "-"), flow.CleanText(ts.Title), flow.CleanText(ts.Rule), cmp.Or(flow.CleanText(ts.Why), "-")
	if r := []rune(title); len(r) > 36 {
		title = string(r[:35]) + "…"
	}
	age := formatAge(time.Now(), ts.At)
	colRule, tail := cmp.Or(rule, "-"), why
	switch {
	case rule == "":
	case ts.Stale && age != "":
		colRule, tail = "-", fmt.Sprintf("(last logged %s %s) %s", rule, age, why)
	case ts.Stale:
		colRule, tail = "-", fmt.Sprintf("(last logged %s) %s", rule, why)
	case age != "":
		tail = age + " " + why
	}
	s := fmt.Sprintf("%-6s %-36s %-17s %-4s %s", id, title, ts.Stage, colRule, tail)
	return fullSHA.ReplaceAllStringFunc(s, func(m string) string { return m[:7] })
}
