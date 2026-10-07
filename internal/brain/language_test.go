package brain

import (
	"strings"
	"testing"
	"time"
)

// langHost is a stubHost that knows the user's language setting.
type langHost struct {
	stubHost
	line string
}

func (h *langHost) LanguageLine() string { return h.line }

// The user's language setting is a section of the state block (after
// the window, never in the cached system text), just before the time;
// with no setting, or a host that does not know it, there is none.
func TestLanguageInStateBlock(t *testing.T) {
	agents, notes, promises, invs := fullFlock()
	line := "The user's language setting, in order of preference: Chinese (Simplified) 简体中文. Answer in the language they use; write Chinese in Simplified characters."
	b := &Brain{cfg: Config{Now: func() time.Time { return speedNow }}.withDefaults(), log: nopLog{}, tools: toolDeclarations(false)}
	b.host = &langHost{stubHost: stubHost{agents: agents, notes: notes, promises: promises, invs: invs}, line: line}
	sys, _, state := b.systemPrompt()
	if strings.Contains(sys, line) {
		t.Error("language line in the system text")
	}
	i, j := strings.Index(state, "## Language\n\n"+line), strings.Index(state, "## Now")
	if i < 0 || j < i {
		t.Fatalf("state block lacks the language section before the time:\n%s", state)
	}
	if !strings.Contains(b.stateNow(), line) {
		t.Error("stateNow lacks the language line")
	}

	b.host = &langHost{stubHost: stubHost{agents: agents}}
	if _, _, state := b.systemPrompt(); strings.Contains(state, "## Language") {
		t.Error("empty setting rendered a section")
	}
	b.host = &stubHost{agents: agents}
	if _, _, state := b.systemPrompt(); strings.Contains(state, "## Language") {
		t.Error("a host without LanguageLine rendered a section")
	}
}
