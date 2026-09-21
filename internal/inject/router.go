package inject

import (
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

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

// reStop is the whole-utterance stop shape (the supervisor runs the
// same check on the relayed text, so this only saves a hop).
var reStop = regexp.MustCompile(`^(stop|stop stop|wait|cancel|abort|never ?mind|halt|no no|hold on)$`)

// Route returns the tool call for text, or false when there is nothing
// to relay.
func (r *Router) Route(text string) (voice.ToolCall, bool) {
	norm := normalize(text)
	if norm == "" {
		return voice.ToolCall{}, false
	}
	call := voice.ToolCall{ID: "text-" + strconv.FormatUint(r.seq.Add(1), 10), Args: map[string]any{}}
	if reStop.MatchString(norm) {
		call.Name = voice.ToolStop
		return call, true
	}
	call.Name = voice.ToolToSupervisor
	call.Args["text"] = strings.TrimSpace(text)
	return call, true
}

// normalize lowercases, collapses whitespace and strips trailing
// punctuation.
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimRight(s, ".!?,;: ")
}
