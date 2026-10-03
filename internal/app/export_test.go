package app

import "github.com/kccarlos/herdr-sheepdog/internal/brain"

// Brain returns the attached brain, or nil when it is off.
func (a *App) Brain() *brain.Brain { return a.brain }

// BrainOffReason says why the brain is off ("" when it is on).
func (a *App) BrainOffReason() string { return a.brainOff }

// QuerierLazy reports whether the supervisor got the lazy CLI querier
// (not the bench's Options.Querier).
func (a *App) QuerierLazy() bool { return a.queryLazy != nil }
