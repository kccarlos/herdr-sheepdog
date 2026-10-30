package herdrcore

import (
	"context"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// screenState is the screen fallback for an agent that does not report
// status (it has not started to, as at a first-run trust question): a
// question on its screen is blocked, anything else unknown. ok is false
// when the screen could not be read, and the state then says nothing.
func (b *Backend) screenState(ctx context.Context, address string) (state herdr.State, ok bool) {
	res, err := b.client.Read(ctx, address, 0)
	if err != nil {
		return herdr.StateUnknown, false
	}
	if _, found := herdr.ParsePrompt(res.Text); found {
		return herdr.StateBlocked, true
	}
	return herdr.StateUnknown, true
}

// stateOf is the herdr state of t, reading its screen when it needs the
// fallback (unknown when the read fails).
func (b *Backend) stateOf(ctx context.Context, t hc.Terminal) herdr.State {
	if needsFallback(t) {
		state, _ := b.screenState(ctx, t.Address)
		return state
	}
	return mapState(t.Status)
}
