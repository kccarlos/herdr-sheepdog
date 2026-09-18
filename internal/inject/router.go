package inject

import (
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Router maps trivial utterances onto voice tool calls without a model.
// DEBUG ONLY: it stands in for the voice model when `sheepdog serve
// --voice=off`, so the tool path can be exercised from the /debug page. It
// is deliberately dumb; anything it does not recognise is rejected.
type Router struct {
	seq atomic.Uint64
}

var (
	reConfirm  = regexp.MustCompile(`^(yes|yeah|yep|yup|ok|okay|sure|go ahead|do it|proceed|approve|approved|confirm|accept|allow|fine|no|nope|nah|deny|decline|reject|option \d+|the (first|second|third|fourth|fifth) one|\d+)$`)
	reCancel   = regexp.MustCompile(`^(stop|cancel|abort|never mind|nevermind|halt)$`)
	reBriefing = regexp.MustCompile(`^(what'?s going on|what is going on|status|briefing|update|what'?s up|how are things|how's it going)\??$`)
	reAsk      = regexp.MustCompile(`^(ask|why|what|how|is|are|did|does|has|have|where|which|who)\b`)
	reNote     = regexp.MustCompile(`^(note that|note|remember that|remember)\b`)
	rePropose  = regexp.MustCompile(`^(tell|say to|instruct|have)\b`)
)

// Route returns the tool call for text, or false when the text is not one
// of the recognised shapes.
func (r *Router) Route(text string) (voice.ToolCall, bool) {
	norm := normalize(text)
	if norm == "" {
		return voice.ToolCall{}, false
	}
	call := voice.ToolCall{ID: "text-" + strconv.FormatUint(r.seq.Add(1), 10), Args: map[string]any{}}
	switch {
	case reCancel.MatchString(norm):
		call.Name = supervisor.ToolCancel
	case reConfirm.MatchString(norm):
		call.Name = supervisor.ToolConfirm
		call.Args["answer"] = norm
	case reBriefing.MatchString(norm):
		call.Name = supervisor.ToolBriefing
	case reNote.MatchString(norm):
		call.Name = supervisor.ToolNote
		call.Args["text"] = strings.TrimSpace(reNote.ReplaceAllString(strings.TrimSpace(text), ""))
	case rePropose.MatchString(norm):
		call.Name = supervisor.ToolPropose
		call.Args["instruction"] = strings.TrimSpace(text)
	case reAsk.MatchString(norm):
		call.Name = supervisor.ToolAsk
		q := strings.TrimSpace(text)
		if strings.HasPrefix(norm, "ask ") {
			q = strings.TrimSpace(q[4:])
		}
		call.Args["question"] = q
	default:
		return voice.ToolCall{}, false
	}
	return call, true
}

// normalize lowercases, collapses whitespace and strips trailing
// punctuation.
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimRight(s, ".!?,;: ")
}
