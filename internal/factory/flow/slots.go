package flow

import (
	"cmp"
	"slices"
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
		case !ok || dep.Done == nil:
			notDone = append(notDone, id)
		}
	}
	return notDone, dropped
}

// RunningTasks counts tasks currently holding a slot: open attempt, not ended,
// not parked. (Design 4.8 refines this to worker starting/working in slice 5a.)
func RunningTasks(tasks []Task) int {
	n := 0
	for _, t := range tasks {
		if !Ended(t) && !t.Parked && OpenAttempt(t) != nil {
			n++
		}
	}
	return n
}

// GrantSlots decides which tasks that reached N10 this pass get a slot
// (design.md 4.10): urgent first (owner-answered comes in slice 6), then
// longest remaining critical path of estimate_lines through after, then oldest
// (index in tasks). Gives up to maxRunning - running slots, plus one extra
// slot beyond maxRunning for an urgent task.
func GrantSlots(candidates []string, tasks []Task, running, maxRunning int) map[string]bool {
	byID, age := make(map[string]Task, len(tasks)), make(map[string]int, len(tasks))
	for i, t := range tasks {
		byID[t.ID], age[t.ID] = t, i
	}
	crit := criticalPaths(tasks)
	ordered := slices.Clone(candidates)
	slices.SortStableFunc(ordered, func(a, b string) int {
		ua, ub := byID[a].Priority == PriorityUrgent, byID[b].Priority == PriorityUrgent
		if ua != ub {
			if ua {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(crit[b], crit[a]); c != 0 {
			return c
		}
		return cmp.Compare(age[a], age[b])
	})
	granted, used := make(map[string]bool), running
	for _, id := range ordered {
		limit := maxRunning
		if byID[id].Priority == PriorityUrgent {
			limit += UrgentExtraSlots
		}
		if used < limit {
			granted[id] = true
			used++
		}
	}
	return granted
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
	for id := range est {
		longest(id)
	}
	return memo
}
