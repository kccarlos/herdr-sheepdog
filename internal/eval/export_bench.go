package eval

import (
	"context"
)

// The voice bench (internal/bench) reuses the text sim's scenarios,
// rubric, judge and taxonomy; these are the pieces it needs.

// SetScreenDir sets where the scenario's bare screen names are looked
// up first (LoadSimFile sets the screens/ directory next to the file).
func (s *SimScenario) SetScreenDir(dir string) { s.screenDir = dir }

// Validate checks the scenario as LoadSimFile does.
func (s SimScenario) Validate() error { return s.validate() }

// RubricFor lists a scenario's judge criteria with their names (the
// default items it does not skip, then its own).
func RubricFor(s SimScenario) (names, criteria []string) { return rubricFor(s) }

// JudgeAll asks the judge every criterion about one conversation.
func JudgeAll(ctx context.Context, j Judge, criteria []string, transcript, facts string, final bool) []Verdict {
	return judgeAll(ctx, j, criteria, transcript, facts, final)
}

// ActionFailures checks what sheepdog sent to the workers against the
// scenario's expectations.
func ActionFailures(exp SimExpect, acts []SimLine) []Failure { return actionFailures(exp, acts) }

// RenderTranscript is a conversation as the CEO lived it, for the judge.
func RenderTranscript(convo []SimLine) string { return renderSim(convo) }

// StatusOf derives a run's status from its failures.
func StatusOf(fs []Failure) RunStatus { return statusOf(fs) }

// IsRateLimited reports whether an error text is a provider rate limit.
func IsRateLimited(s string) bool { return isRateLimited(errorString(s)) }

// IsTimeoutText reports whether an error text is a timeout.
func IsTimeoutText(s string) bool { return isTimeoutText(s) }

// FirstLine is the first line of s, trimmed.
func FirstLine(s string) string { return firstLine(s) }

// SortLines orders lines by time (stable).
func SortLines(lines []SimLine) { sortLines(lines) }

// SimWaitMarker is the system line a simulated user's wait leaves in
// the conversation.
const SimWaitMarker = simWaitMarker
