package inject

import (
	"context"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// Done is SetState(done) with the pane text left as it is.
func (i *Injector) Done(ctx context.Context, paneID string) (state.Agent, error) {
	return i.SetState(ctx, paneID, herdr.StateDone, "")
}

// Sends returns how many prompts/keys were sent to a synthetic pane.
func (h *Herdr) Sends(paneID string) int {
	p, _ := h.get(paneID)
	return p.sends
}
