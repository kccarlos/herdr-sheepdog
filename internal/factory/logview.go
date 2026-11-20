package factory

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/factory/flow"
)

// ProjectLog reads the last n lines (all when n <= 0) of a project's
// log.jsonl and its formatted first-look pass rate summary (empty when no task
// has a reviewed round): folder's project, or the one started project when
// folder is empty. A torn last line (an append that did not finish) is skipped.
func ProjectLog(ctx context.Context, paths Paths, folder string, n int) ([]LogLine, string, error) {
	id, s, err := (&Engine{Paths: paths}).findProjectLocked(ctx, folder)
	if err != nil {
		return nil, "", err
	}
	lines, err := readLogFile(paths.Log(id), false)
	if err != nil {
		return nil, "", err
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, FormatFirstLook(s.Tasks), nil
}

func readLogFile(path string, missingOK bool) ([]LogLine, error) {
	if err := guardTestPath(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if missingOK && os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var lines []LogLine
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var l LogLine
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			lines = append(lines, l)
		}
	}
	return lines, sc.Err()
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
}

// StatusViewOf derives each task's status from stored state and log.jsonl.
// Stage comes from GraphViewOf so `sheepdog factory status` and `/factory`
// always agree from one function.
func StatusViewOf(s flow.State, logs []LogLine) []TaskStatus {
	last := make(map[string]LogLine, len(s.Tasks))
	for _, l := range logs {
		if l.Task != "" && slices.ContainsFunc(flow.TaskRows, func(r flow.Row[flow.In]) bool { return r.ID == l.Rule }) {
			last[l.Task] = l
		}
	}
	g := GraphViewOf(s, logs)
	out := make([]TaskStatus, len(g.Tasks))
	for i, tv := range g.Tasks {
		ts := TaskStatus{ID: tv.Ref, Title: tv.Title, Stage: tv.Stage, Why: "no pass yet"}
		if l, ok := last[tv.Ref]; ok {
			ts.Rule, ts.Why = l.Rule, l.Why
		}
		out[i] = ts
	}
	return out
}

// ProjectStatus reads stored state.json and log.jsonl for folder (or the single
// started/stored project when folder is empty). It makes no hc, git or gh calls
// and works when the daemon is stopped.
func ProjectStatus(paths Paths, folder string) ([]TaskStatus, error) {
	id, s, err := findStoredProject(paths, strings.TrimSpace(folder))
	if err != nil {
		return nil, err
	}
	logs, err := readLogFile(paths.Log(id), true)
	if err != nil {
		return nil, err
	}
	return StatusViewOf(s, logs), nil
}

func findStoredProject(paths Paths, target string) (string, flow.State, error) {
	dir := filepath.Join(paths.DataDir, "factory", "projects")
	if err := guardTestPath(dir); err != nil {
		return "", flow.State{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return "", flow.State{}, err
	}
	var started, all []string
	states := map[string]flow.State{}
	for _, ent := range entries {
		s, err := LoadState(paths.State(ent.Name()))
		if err != nil {
			continue
		}
		if target != "" {
			if ent.Name() == target || sameFolder(s.Project.Folder, target) {
				return ent.Name(), s, nil
			}
			continue
		}
		states[ent.Name()] = s
		all = append(all, ent.Name())
		if s.Project.Started {
			started = append(started, ent.Name())
		}
	}
	switch {
	case target != "":
		return "", flow.State{}, fmt.Errorf("no factory project for %s", target)
	case len(started) == 1:
		return started[0], states[started[0]], nil
	case len(started) > 1:
		return "", flow.State{}, errors.New("multiple projects started; pass sheepdog factory status <folder>")
	case len(all) == 1:
		return all[0], states[all[0]], nil
	case len(all) > 1:
		return "", flow.State{}, errors.New("multiple factory projects; pass sheepdog factory status <folder>")
	default:
		return "", flow.State{}, errors.New("no factory project found")
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

// FormatStatusLine formats one task status line: id, short title, stage, last
// matched row and its why. Full commit ids are cut to 7.
func FormatStatusLine(ts TaskStatus) string {
	title := strings.Join(strings.Fields(ts.Title), " ")
	if r := []rune(title); len(r) > 36 {
		title = string(r[:35]) + "…"
	}
	why := cmp.Or(strings.Join(strings.Fields(ts.Why), " "), "-")
	s := fmt.Sprintf("%-6s %-36s %-17s %-4s %s", ts.ID, title, ts.Stage, cmp.Or(ts.Rule, "-"), why)
	return fullSHA.ReplaceAllStringFunc(s, func(m string) string { return m[:7] })
}
