package inject

import (
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Router stands in for the voice model when `sheepdog serve
// --voice=off`, so the tool path can be exercised from the /debug page.
// Since D1-revised the voice has only two tools and classifies nothing,
// so the router has almost nothing left to do: a bare stop word calls
// stop, everything else goes to the supervisor verbatim, exactly as the
// voice model would relay it.
type Router struct {
	seq atomic.Uint64
}

// Route returns the tool call for text, or false when there is nothing
// to relay. The bare-stop check reads supervisor.BareStopWords — the
// same list the supervisor itself checks on the relayed text (this only
// saves a hop) — so a word like "abort" stops both GPT-Live's Classify
// path (here) and Gemini's to_supervisor path identically (audit B8:
// this used to be its own regex, missing "abort" and "halt").
func (r *Router) Route(text string) (voice.ToolCall, bool) {
	norm := supervisor.BareWord(text)
	if norm == "" {
		return voice.ToolCall{}, false
	}
	call := voice.ToolCall{ID: "text-" + strconv.FormatUint(r.seq.Add(1), 10), Args: map[string]any{}}
	if supervisor.BareStopWords[norm] {
		call.Name = voice.ToolStop
		return call, true
	}
	call.Name = voice.ToolToSupervisor
	call.Args["text"] = strings.TrimSpace(text)
	return call, true
}
