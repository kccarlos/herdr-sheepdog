package flow

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// UrgentExtraSlots is the extra slot beyond max_running for an urgent task (design.md 4.10).
const UrgentExtraSlots = 1

// UnmetDeps returns t's dependencies in t.After that are not done, split into
// still-open dependencies and dropped ones (design.md 4.2, N9). A dropped
// dependency stays unmet until the plan is amended (no silent unblock).
func UnmetDeps(t Task, tasks []Task) (notDone, dropped []string) {
	byID := make(map[string]Task, len(tasks))
	for _, x := range tasks {
		byID[x.ID] = x
	}
	for _, id := range t.After {
		dep, ok := byID[id]
		switch {
		case ok && dep.Dropped != nil:
			dropped = append(dropped, id)
		case !ok || (dep.Done == nil && dep.Closed == nil):
			notDone = append(notDone, id)
		}
	}
	return notDone, dropped
}

// RunningTasks counts tasks holding a slot (design.md 4.8): an open attempt
// whose worker run is not gone (idle/done/error hold the slot; an ended worker
// frees it).
func RunningTasks(tasks []Task, workerState map[string]RunState) int {
	n := 0
	for _, t := range tasks {
		if a := OpenAttempt(t); !Ended(t) && !t.Parked && a != nil && cmp.Or(workerState[t.ID], a.Run.TaskRun().State) != RunGone {
			n++
		}
	}
	return n
}

// GrantSlots decides which tasks that reached N10 this pass get a slot
// (design.md 4.10, KB-TSK-12): owner-answered first, then urgent, then longest
// remaining critical path of estimate_lines through after, then oldest (index
// in tasks). Gives up to maxRunning - running slots, plus one extra slot at the
// cap (running == maxRunning) for an owner-answered or urgent task.
func GrantSlots(candidates []string, tasks []Task, running, maxRunning int, asks ...Ask) map[string]bool {
	byID, age := make(map[string]Task, len(tasks)), make(map[string]int, len(tasks))
	for i, t := range tasks {
		byID[t.ID], age[t.ID] = t, i
	}
	ownerAns := make(map[string]bool)
	for _, a := range asks {
		if a.Task != "" && a.Gated && a.Status == AskStatusAnswered && a.AnsweredBy == "owner" {
			if last := lastSlotAt(byID[a.Task]); last.IsZero() || a.AnsweredAt.After(last) {
				ownerAns[a.Task] = true
			}
		}
	}
	crit := criticalPaths(tasks)
	ordered := slices.Clone(candidates)
	slices.SortStableFunc(ordered, func(a, b string) int {
		if oa, ob := ownerAns[a], ownerAns[b]; oa != ob {
			return map[bool]int{true: -1, false: 1}[oa]
		}
		if ua, ub := byID[a].Priority == PriorityUrgent, byID[b].Priority == PriorityUrgent; ua != ub {
			return map[bool]int{true: -1, false: 1}[ua]
		}
		if c := cmp.Compare(crit[b], crit[a]); c != 0 {
			return c
		}
		return cmp.Compare(age[a], age[b])
	})
	granted, used := make(map[string]bool), running
	for _, id := range ordered {
		limit := maxRunning
		if running == maxRunning && (ownerAns[id] || byID[id].Priority == PriorityUrgent) {
			limit += UrgentExtraSlots
		}
		if used < limit {
			granted[id] = true
			used++
		}
	}
	return granted
}

func lastSlotAt(t Task) time.Time {
	var at time.Time
	scan := func(acts []Act) {
		for _, act := range acts {
			if act.Result == "done" && (act.Key == AskNewAttempt || strings.HasPrefix(act.Key, AskStartWorker)) && act.At.After(at) {
				at = act.At
			}
		}
	}
	scan(t.Acts)
	for _, a := range t.Attempts {
		scan(a.Acts)
	}
	return at
}

// criticalPaths computes, for each non-ended task, the longest remaining chain
// of EstimateLines starting at that task and following after links downstream.
func criticalPaths(tasks []Task) map[string]int {
	est, down := make(map[string]int, len(tasks)), make(map[string][]string, len(tasks))
	for _, t := range tasks {
		if Ended(t) {
			continue
		}
		est[t.ID] = max(0, t.EstimateLines)
		for _, dep := range t.After {
			down[dep] = append(down[dep], t.ID)
		}
	}
	memo, visiting := make(map[string]int, len(est)), make(map[string]bool, len(est))
	var longest func(string) int
	longest = func(id string) int {
		if v, ok := memo[id]; ok {
			return v
		}
		if visiting[id] {
			return 0
		}
		visiting[id] = true
		best := 0
		for _, nxt := range down[id] {
			best = max(best, longest(nxt))
		}
		delete(visiting, id)
		memo[id] = est[id] + best
		return memo[id]
	}
	for _, t := range tasks {
		if !Ended(t) {
			longest(t.ID)
		}
	}
	return memo
}
