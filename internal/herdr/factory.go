package herdr

// IsFactoryQuiet reports whether a factory terminal transition should be
// silenced: non-coordinator roles always, coordinator except when blocked.
func IsFactoryQuiet(labels map[string]string, to State) bool {
	return labels["sheepdog.project"] != "" && (labels["sheepdog.role"] != "coordinator" || to != StateBlocked)
}
