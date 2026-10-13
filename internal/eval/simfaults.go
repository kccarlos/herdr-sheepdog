package eval

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// SimFaults force, on the user's turns (1-based, the Nth line the user
// says), what the orchestrator and the voice get wrong now and then, so
// a tier-1 run can exercise the daemon's record of unanswered questions
// (harness iteration 7). Tier 1 has no voice layer: the cut and the
// unvoiced answer are stood in for here, with the events the voice
// layer would report (its own tests cover how it detects them).
type SimFaults struct {
	// FailTurns: every orchestrator model call during the turn fails, so
	// the turn falls back and the user hears the daemon's fallback line.
	FailTurns []int `json:"fail_turns,omitempty"`
	// CutTurns: the user talks over the answer: the voice reports a cut
	// after its first CutWords words (default 3), and the user hears only
	// those.
	CutTurns []int `json:"cut_turns,omitempty"`
	CutWords int   `json:"cut_words,omitempty"`
	// UnvoicedTurns: the voice never says the answer: the user hears
	// nothing, and the voice layer reports the answer not voiced.
	UnvoicedTurns []int `json:"unvoiced_turns,omitempty"`
}

// fault is what SimFaults force on one turn.
type fault string

const (
	faultNone     fault = ""
	faultFail     fault = "fail"
	faultCut      fault = "cut"
	faultUnvoiced fault = "unvoiced"
)

// on is the fault forced on user turn n.
func (f *SimFaults) on(n int) fault {
	switch {
	case f == nil:
		return faultNone
	case slices.Contains(f.FailTurns, n):
		return faultFail
	case slices.Contains(f.CutTurns, n):
		return faultCut
	case slices.Contains(f.UnvoicedTurns, n):
		return faultUnvoiced
	}
	return faultNone
}

func (f *SimFaults) cutWords() int {
	if f == nil || f.CutWords <= 0 {
		return 3
	}
	return f.CutWords
}

// cutPrefix is the first n words of said, or for text in Chinese
// (no spaces between words) its first 2n characters.
func cutPrefix(said string, n int) string {
	if strings.IndexFunc(said, func(r rune) bool { return unicode.Is(unicode.Han, r) }) >= 0 {
		r := []rune(said)
		return string(r[:min(2*n, len(r))])
	}
	words := strings.Fields(said)
	return strings.Join(words[:min(n, len(words))], " ")
}

// errInjected is the model error a FailTurns turn gets.
var errInjected = errors.New("eval: injected model failure (sim faults)")

// faultSwitch fails every model call while it is on.
type faultSwitch struct {
	mu sync.Mutex
	on bool
}

func (s *faultSwitch) set(on bool) {
	s.mu.Lock()
	s.on = on
	s.mu.Unlock()
}

func (s *faultSwitch) failing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on
}

// faultModel is a brain.Model behind a faultSwitch.
type faultModel struct {
	brain.Model
	sw *faultSwitch
}

func (m faultModel) Generate(ctx context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	if m.sw.failing() {
		return nil, errInjected
	}
	return m.Model.Generate(ctx, model, contents, cfg)
}

// faultMessageModel keeps a brain.MessageModel one (DeepSeek's
// reasoning_content replay).
type faultMessageModel struct {
	faultModel
	mm brain.MessageModel
}

func (m faultMessageModel) GenerateMessages(ctx context.Context, model string, msgs []brain.Message, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, json.RawMessage, error) {
	if m.sw.failing() {
		return nil, nil, errInjected
	}
	return m.mm.GenerateMessages(ctx, model, msgs, cfg)
}

// withFaults wraps m so that sw fails its calls.
func withFaults(m brain.Model, sw *faultSwitch) brain.Model {
	fm := faultModel{Model: m, sw: sw}
	if mm, ok := m.(brain.MessageModel); ok {
		return faultMessageModel{faultModel: fm, mm: mm}
	}
	return fm
}

// voiceAnswer reports to the supervisor what the voice did with the
// answer res to text, as the voice layer would, and returns what the
// user heard of it. Without a fault the voice says it all.
func (w *simWorld) voiceAnswer(text string, res voice.ToolResult, f fault, cutWords int) string {
	said := strings.TrimSpace(res.Spoken)
	if said == "" {
		return ""
	}
	switch f {
	case faultUnvoiced:
		w.sup.OnVoiceEvent(voice.EventPushUnheard{Push: voice.UnheardPush{
			Text:   said,
			Tags:   map[string]any{voice.TagAnswersUtterance: res.UtteranceID, "answers_text": text},
			Reason: voice.UnheardNotVoiced,
			SentAt: time.Now(),
		}})
		return ""
	case faultCut:
		heard := cutPrefix(said, cutWords)
		w.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: said, Final: true})
		w.sup.OnVoiceEvent(voice.EventTurnComplete{})
		// Speech time in proportion to the characters said.
		total := utf8.RuneCountInString(said)
		runMs := 60 * total
		w.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: heard, Final: true, Cut: &voice.TranscriptCut{
			UtteranceID: res.UtteranceID, HeardMs: 60 * utf8.RuneCountInString(heard), RunMs: runMs, Full: said,
		}})
		return heard
	}
	w.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirOut, Text: said, Final: true})
	w.sup.OnVoiceEvent(voice.EventTurnComplete{})
	return said
}
