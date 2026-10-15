package app

import (
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

// Brain returns the attached orchestrator, or nil when it is off.
func (a *App) Brain() *brain.Brain { return a.brain }

// BrainOffReason says why the orchestrator is off ("" when it is on).
func (a *App) BrainOffReason() string { return a.brainOff }

// QuerierLazy reports whether the supervisor got the lazy CLI querier
// (not the bench's Options.Querier).
func (a *App) QuerierLazy() bool { return a.queryLazy != nil }

// SetVoiceRetry shortens the voice start backoff for a test.
func SetVoiceRetry(min, max time.Duration) (restore func()) {
	oldMin, oldMax := voiceRetryMin, voiceRetryMax
	voiceRetryMin, voiceRetryMax = min, max
	return func() { voiceRetryMin, voiceRetryMax = oldMin, oldMax }
}
