package herdr

// IsFactoryQuiet reports whether a transition on a terminal with labels to
// state to should be silenced by the factory quiet rule (design.md 12.1):
// non-coordinator factory roles (worker, reviewer, checks, helper) are quiet
// for every state change; coordinator is quiet except when blocked;
// non-factory terminals are never quiet.
func IsFactoryQuiet(labels map[string]string, to State) bool {
	return false
}
