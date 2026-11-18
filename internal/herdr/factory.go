package herdr

import (
	"maps"
	"strings"
)

// Factory terminal label keys (docs/dev/factory/owner-contract.md §6.1).
const (
	LabelFactoryProject = "sheepdog.project"
	LabelFactoryRole    = "sheepdog.role"
	LabelFactoryTask    = "sheepdog.task"
)

// IsFactoryQuiet reports whether a terminal's sheepdog.* labels mark it as a
// factory worker, reviewer, checks, helper, or task pane whose transitions
// must remain completely silent to the user (zero announce turns, zero voice
// pushes, and zero APNs notifications). Coordinator panes and non-factory
// terminals return false.
func IsFactoryQuiet(labels map[string]string) bool {
	if len(labels) == 0 {
		return false
	}
	role := strings.TrimSpace(labels[LabelFactoryRole])
	switch role {
	case "worker", "reviewer", "checks", "helper":
		return true
	case "coordinator":
		return false
	}
	return strings.TrimSpace(labels[LabelFactoryTask]) != ""
}

// CloneLabels returns a shallow copy of labels, or nil when labels is empty.
func CloneLabels(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	return maps.Clone(labels)
}
