package supervisor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The displacement rail (displaced.go): the user heard auth's question
// and answers it, but docs' question took the voice's slot in the
// seconds before docs' announcement. Their words must not reach docs.

const (
	authPane = "w5:p1"
	docsPane = "w6:p1"
)

// toldAuth blocks auth and has its announcement spoken, then puts docs to
// work: auth's question is the one the user heard.
func toldAuth(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.addAgent(authPane, "auth", "codex", herdr.StateWorking)
	h.addAgent(docsPane, "docs", "codex", herdr.StateWorking)
	h.block(authPane, "codex-approval-prompt.screen.txt")
	line := h.lastPushText()
	if !strings.Contains(line, "auth") {
		t.Fatalf("auth's announcement = %q", line)
	}
	h.speak(line)
	h.clock.Advance(2 * time.Second)
	return h
}

// blockDocs blocks docs; its announcement is queued, not yet spoken. It
// returns that announcement.
func blockDocs(t *testing.T, h *harness) string {
	t.Helper()
	h.block(docsPane, "codex-approval-prompt.screen.txt")
	p, ok := h.pending()
	if !ok || p.PaneID != docsPane {
		t.Fatalf("pending = %+v, want docs' question", p)
	}
	line := h.lastPushText()
	if !strings.Contains(line, "docs") {
		t.Fatalf("docs' announcement = %q", line)
	}
	return line
}

func (h *harness) keysTo(pane string) int {
	n := 0
	for _, r := range h.sendKeysRequests() {
		if r[0] == pane {
			n++
		}
	}
	return n
}

func (h *harness) displacedBlocks() int {
	n := 0
	for _, e := range h.log.ofType("brain.rail_blocked") {
		if r, _ := payload(e)["reason"].(string); strings.Contains(r, "was told to them") {
			n++
		}
	}
	return n
}

// B replaced A: docs blocks while auth's question is open, and the user's
// yes comes before docs' announcement was spoken. Once it was, a yes
// answers docs.
func TestDisplacedQuestionIgnoresYesBeforeItsAnnouncement(t *testing.T) {
	h := toldAuth(t)
	docsLine := blockDocs(t, h)
	h.clock.Advance(500 * time.Millisecond)
	h.say("Yes.")
	if n := h.keysTo(docsPane); n != 0 {
		t.Fatalf("a yes said before docs' announcement sent docs %d key presses", n)
	}
	if h.displacedBlocks() != 1 {
		t.Fatalf("rail_blocked = %v", h.log.ofType("brain.rail_blocked"))
	}
	// The orchestrator's send_keys meets the same rail.
	p, _ := h.pending()
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), docsPane, p.YesKeys); err == nil {
		t.Fatal("send_keys to docs passed on words said before its announcement")
	}

	h.speak(docsLine)
	h.clock.Advance(time.Second)
	h.say("Yes.")
	if n := h.keysTo(docsPane); n != 1 {
		t.Fatalf("keys to docs after its announcement = %d, want 1", n)
	}
}

// B blocked after A closed: the user answered auth, and docs blocks a
// second later. A repeated yes before docs' announcement does not reach it.
func TestQuestionAfterAnAnsweredOneIgnoresYesBeforeItsAnnouncement(t *testing.T) {
	h := toldAuth(t)
	h.say("Yes.")
	if n := h.keysTo(authPane); n != 1 {
		t.Fatalf("keys to auth = %d, want 1", n)
	}
	h.srv.SetScreen(authPane, "running the tests")
	h.transition(authPane, herdr.StateBlocked, herdr.StateWorking)
	h.clock.Advance(time.Second)
	blockDocs(t, h)
	h.clock.Advance(500 * time.Millisecond)
	h.say("Yes.")
	if n := h.keysTo(docsPane); n != 0 {
		t.Fatalf("a repeated yes sent docs %d key presses", n)
	}
	if h.displacedBlocks() != 1 {
		t.Fatalf("rail_blocked = %v", h.log.ofType("brain.rail_blocked"))
	}
}

// A answered at the same moment B blocks: the user's "yes" for auth began
// while auth's question was open; docs blocked before the words ended
// (and, in the second case, its announcement went out mid-utterance).
func TestYesBegunBeforeADisplacingBlockDoesNotReachIt(t *testing.T) {
	for _, toldMidway := range []bool{false, true} {
		name := "unannounced"
		if toldMidway {
			name = "announced mid-utterance"
		}
		t.Run(name, func(t *testing.T) {
			h := toldAuth(t)
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: "Yes", Final: false})
			docsLine := blockDocs(t, h)
			if toldMidway {
				h.playText(docsLine)
			}
			h.clock.Advance(300 * time.Millisecond)
			h.sup.OnVoiceEvent(voice.EventTranscript{Dir: voice.DirIn, Text: ".", Final: true})
			h.relay("Yes.")
			if n := h.keysTo(docsPane); n != 0 {
				t.Fatalf("auth's yes sent docs %d key presses", n)
			}
			if h.displacedBlocks() != 1 {
				t.Fatalf("rail_blocked = %v", h.log.ofType("brain.rail_blocked"))
			}
			// The words were about auth: send_keys to auth still answers it.
			q, ok := h.store.Question(firstQuestionOf(h, authPane))
			if !ok {
				t.Fatal("auth's question is gone")
			}
			if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, q.YesKeys); err != nil {
				t.Fatalf("send_keys to auth: %v", err)
			}
			if n := h.keysTo(authPane); n != 1 {
				t.Fatalf("keys to auth = %d, want 1", n)
			}
		})
	}
}

func firstQuestionOf(h *harness, pane string) string {
	for _, q := range h.store.Questions() {
		if q.PaneID == pane {
			return q.ID
		}
	}
	return ""
}

// Even when the previous question closed long ago so nothing else is open or
// recently told, a bare yes on an untold question never sends keys: it re-asks
// once with SpokenSummary, and once that re-ask plays the next bare yes works.
func TestQuestionAfterALongClosedOneReasksUntilPlayed(t *testing.T) {
	h := toldAuth(t)
	h.srv.SetScreen(authPane, "running the tests")
	h.transition(authPane, herdr.StateBlocked, herdr.StateWorking)
	h.clock.Advance(2 * time.Minute)
	blockDocs(t, h)
	h.clock.Advance(500 * time.Millisecond)
	res := h.say("Yes.")
	if n := h.keysTo(docsPane); n != 0 {
		t.Fatalf("untold sole question got %d key presses on bare yes", n)
	}
	if !strings.Contains(res.Spoken, "hadn't finished reading") || !strings.Contains(res.Spoken, "docs asks to run printf") {
		t.Fatalf("re-ask = %q, want untold re-ask with SpokenSummary", res.Spoken)
	}
	h.playText(res.Spoken)
	h.clock.Advance(500 * time.Millisecond)
	h.say("Yes.")
	if n := h.keysTo(docsPane); n != 1 {
		t.Fatalf("keys to docs after re-ask played = %d, want 1", n)
	}
}

// A question the user was told about stays theirs to answer after another
// one was told: send_keys revives auth's question (a new id) and the rail
// lets the words through, since they began after auth's was told.
func TestEarlierToldQuestionStaysAnswerable(t *testing.T) {
	h := toldAuth(t)
	h.speak(blockDocs(t, h))
	h.clock.Advance(time.Second)
	h.hear("Yes to auth.")
	q, ok := h.store.Question(firstQuestionOf(h, authPane))
	if !ok {
		t.Fatal("auth's question is gone")
	}
	if _, err := (brainHost{h.sup}).AnswerPrompt(context.Background(), authPane, q.YesKeys); err != nil {
		t.Fatalf("send_keys to auth: %v", err)
	}
	if n := h.keysTo(authPane); n != 1 {
		t.Fatalf("keys to auth = %d, want 1", n)
	}
}
