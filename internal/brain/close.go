package brain

import (
	"context"
	"errors"
)

// Closing an agent (docs/dev/roles.md "Closing an agent"): the interrupt
// tool with close true ends the agent by closing its herdr pane, instead
// of sending Ctrl-C. Which agent the user meant is the orchestrator's judgement
// (it asks when unsure); the daemon holds the rails: the user's own
// unused words behind it, and the agent running in the daemon's own
// directory (the user's orchestrator, most likely) only when the orchestrator
// says the user named that very agent (protected_ok).

// CloseRequest is one agent the orchestrator asked to close.
type CloseRequest struct {
	// PaneID is the agent's pane.
	PaneID string
	// ProtectedOK is the orchestrator saying the user named this very agent, for
	// an agent the daemon protects.
	ProtectedOK bool
}

// Closer is a Host that can close an agent's pane. A Host without it
// refuses close.
type Closer interface {
	// CloseAgent closes the agent's pane and returns the spoken echo. A
	// refusal by a rail wraps ErrNotPermitted.
	CloseAgent(ctx context.Context, req CloseRequest) (string, error)
}

// closeAgent runs an interrupt call with close true.
func (b *Brain) closeAgent(ctx context.Context, row AgentInfo, protectedOK bool, out toolOutcome) toolOutcome {
	c, ok := b.host.(Closer)
	if !ok {
		out.err = errors.New("closing an agent is not available here")
		return out
	}
	echo, err := c.CloseAgent(ctx, CloseRequest{PaneID: row.PaneID, ProtectedOK: protectedOK})
	if err != nil {
		out.err = err
		if errors.Is(err, ErrNotPermitted) {
			out.railBlocked = err.Error()
		}
		return out
	}
	out.acted, out.result = echo, echo
	return out
}
