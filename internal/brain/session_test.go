package brain_test

import (
	"errors"
	"strings"
	"testing"

	"google.golang.org/genai"

	. "github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// sevenGlitches is a worker's last message listing what it found
// (docs/history/dive-deep.md: 2026-09-24 18:16:17, "what's the seven app
// glitches"; synthetic, not the shepherd's transcript).
const sevenGlitches = `Found one small bug and seven app glitches.
The bug: after a failed reply the app says nothing changed.
The glitches:
1. The mic button stays red after a call ends.
2. The listening banner flickers on reconnect.
3. The haptic buzz fires twice on a push.
4. Hold-to-talk drops the first word.
5. The transcript jumps to the top on a new line.
6. Dark mode loses the status colour.
7. The reconnect spinner never stops after a timeout.`

func sessionRead() SessionRead {
	return SessionRead{
		LastAssistant: sevenGlitches,
		Instruction:   "Review the iOS app for glitches",
		Files:         []string{"notes.md"},
		Tools:         map[string]int{"Read": 4, "Bash": 2},
	}
}

// read_screen with source session hands the model the worker's own
// words inside the provenance delimiters: the last instruction, one line
// of what it did, and its last message whole (docs/history/dive-deep.md WP8a).
func TestReadScreenSessionSource(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "auth", "source": "session"})}},
		braintest.Step{Text: braintest.Reply("It found a mic that stays red and six more.", "")},
		braintest.Step{Text: braintest.Reply("Still the same.", "")},
		braintest.Step{Text: braintest.Reply("Yes, still.", "")},
	), func(c *Config) { c.PruneAfterTurns = 1 })
	h.host.SetSession("w5:p1", sessionRead())
	if _, err := h.ask("what exactly did it say?", ""); err != nil {
		t.Fatal(err)
	}
	h.brain.Quiesce()
	second := unnonce(h.model.Calls()[1].Text())
	body := second[strings.Index(second, "<<<BEGIN_WORKER_OUTPUT agent=auth source=session>>>"):]
	want := "<<<BEGIN_WORKER_OUTPUT agent=auth source=session>>>\n" +
		"Last instruction it received: Review the iOS app for glitches\n" +
		"What it did for it: edited notes.md; tool calls: Bash 2, Read 4\n" +
		"Its last message, whole:\n" + sevenGlitches + "\n<<<END_WORKER_OUTPUT>>>"
	if !strings.HasPrefix(body, want) {
		t.Errorf("session result:\n%s\nwant:\n%s", body, want)
	}
	if tr := h.log.ofType("brain.tool_result"); len(tr) != 1 || payload(tr[0])["error"] != nil {
		t.Errorf("tool_result = %v", tr)
	}
	// Pruned like any other tool result: the turn after next sees the
	// stub (TestPruningKeepsDiskComplete).
	for i := 0; i < 2; i++ {
		if _, err := h.ask("and now?", ""); err != nil {
			t.Fatal(err)
		}
		h.brain.Quiesce()
	}
	third := h.model.Calls()[3].Text()
	if strings.Contains(third, "hold-to-talk") || strings.Contains(third, "Hold-to-talk") || !strings.Contains(third, "[session of auth, last message ") || !strings.Contains(third, "pruned]") {
		t.Errorf("the session read was not pruned to its stub:\n%s", third)
	}
}

// A last message over the cap loses its oldest part, never its end (the
// findings and the sign-off), and says so.
func TestReadSessionCutsTheOldestPart(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "auth", "source": "session"})}},
		braintest.Step{Text: braintest.Reply("Done.", "")},
	), nil)
	long := "OLDEST line.\n" + strings.Repeat("Some working notes about the app.\n", 150) + "NEWEST: what changed, what needs a decision, what is next."
	h.host.SetSession("w5:p1", SessionRead{LastAssistant: long})
	if _, err := h.ask("tell me more", "auth"); err != nil {
		t.Fatal(err)
	}
	second := unnonce(h.model.Calls()[1].Text())
	start, end := strings.Index(second, "<<<BEGIN_WORKER_OUTPUT"), strings.Index(second, "<<<END_WORKER_OUTPUT>>>")
	if start < 0 || end < 0 {
		t.Fatalf("no wrapped result:\n%s", second)
	}
	body := second[start:end]
	if strings.Contains(body, "OLDEST") || !strings.Contains(body, "NEWEST: what changed") || !strings.Contains(body, "characters are cut; this is its end") {
		t.Errorf("cut result:\n%s", body)
	}
	if strings.Contains(body, "Last instruction") || strings.Contains(body, "What it did") {
		t.Errorf("empty instruction and did-line should be left out:\n%s", body)
	}
	if n := len(body); n > 3400 {
		t.Errorf("result body = %d bytes, want about 3,000", n)
	}
}

// A session that cannot be read is an error the model reads, naming the
// reason and pointing back at the screen; an unknown source is refused.
func TestReadSessionMissingAndBadSource(t *testing.T) {
	h := newHarness(t, braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "docs", "source": "session"})}},
		braintest.Step{Calls: []*genai.FunctionCall{braintest.FC(ToolReadScreen, map[string]any{"agent": "docs", "source": "diary"})}},
		braintest.Step{Text: braintest.Reply("I cannot read its record.", "")},
	), nil)
	h.host.SessionErr = errors.New("no session record for docs: unsupported_kind (this kind of agent keeps no session record sheepdog can read)")
	if _, err := h.ask("what exactly did docs say?", ""); err != nil {
		t.Fatal(err)
	}
	calls := h.model.Calls()
	if got := calls[1].Text(); !strings.Contains(got, "unsupported_kind") || !strings.Contains(got, "source screen still shows") {
		t.Errorf("model did not see the missing reason:\n%s", got)
	}
	if got := calls[2].Text(); !strings.Contains(got, `read_screen source "diary"`) {
		t.Errorf("bad source not refused:\n%s", got)
	}
	if tr := h.log.ofType("brain.tool_result"); len(tr) != 2 || payload(tr[0])["error"] == nil || payload(tr[1])["error"] == nil {
		t.Errorf("tool results = %v", tr)
	}
}

// The declaration tells the model which source is for what, in both
// the CEO and the read-only (event) surface.
func TestReadScreenDeclaresSource(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		var decl *genai.FunctionDeclaration
		for _, d := range ToolDeclarations(readOnly)[0].FunctionDeclarations {
			if d.Name == ToolReadScreen {
				decl = d
			}
		}
		if decl == nil {
			t.Fatalf("readOnly=%v: no read_screen", readOnly)
		}
		src := decl.Parameters.Properties["source"]
		if src == nil || strings.Join(src.Enum, ",") != "screen,session" {
			t.Errorf("readOnly=%v: source = %+v", readOnly, src)
		}
		for _, want := range []string{"what it is doing right now", "what it said, found or changed"} {
			if !strings.Contains(decl.Description, want) {
				t.Errorf("readOnly=%v: description lacks %q: %s", readOnly, want, decl.Description)
			}
		}
	}
}

// The reflex model (--brain=fake) reads the session for a detail
// question and the screen otherwise.
func TestReflexReadsTheSessionForDetail(t *testing.T) {
	h := newHarness(t, braintest.Reflex(), nil)
	h.host.SetSession("w5:p1", sessionRead())
	rep, err := h.ask("what exactly did it say?", "auth")
	if err != nil {
		t.Fatal(err)
	}
	tc := h.log.ofType("brain.tool_call")
	if len(tc) != 1 {
		t.Fatalf("tool calls = %v", tc)
	}
	if args, _ := payload(tc[0])["args"].(map[string]any); args["source"] != SourceSession {
		t.Errorf("reflex read args = %v", args)
	}
	if !strings.HasPrefix(rep.Spoken, "It wrote: Found one small bug and seven app glitches.") {
		t.Errorf("reply = %q", rep.Spoken)
	}
}
