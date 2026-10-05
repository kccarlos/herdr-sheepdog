package gemini

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// The shepherd's language setting ends the system instruction of every
// connection (read at each connect, so the next session has a change)
// and is on the voice.system_prompt event; with none set the prompt is
// exactly what it was before the setting.
func TestSystemPromptCarriesLanguage(t *testing.T) {
	var line atomic.Value
	line.Store("LANG-LINE-1")
	cfg := Config{Aliases: []string{"auth"}, Language: func() string { return line.Load().(string) }}

	c := newFakeConn("c1")
	d := &fakeDialer{results: []func() (Conn, error){ok(c)}}
	h := newHarness(t, cfg, d)
	h.waitEvent("connected", func(ev voice.Event) bool {
		st, ok := ev.(voice.EventStatus)
		return ok && st.Status == voice.StatusConnected
	})
	p := h.log.payloads("voice.system_prompt")
	if len(p) != 1 || !strings.HasSuffix(p[0]["text"].(string), "\n\nLANG-LINE-1") || p[0]["language"] != "LANG-LINE-1" {
		t.Fatalf("voice.system_prompt = %v", p)
	}

	line.Store("LANG-LINE-2")
	if s, _ := SystemPrompt(cfg); !strings.HasSuffix(s, "\n\nLANG-LINE-2") || strings.Contains(s, "LANG-LINE-1") {
		t.Errorf("next session's prompt ends %q", s[len(s)-40:])
	}

	plain, _ := SystemPrompt(Config{Aliases: []string{"auth"}})
	line.Store("")
	if s, _ := SystemPrompt(cfg); s != plain {
		t.Error("an empty setting changed the prompt")
	}
	over := Config{SystemPrompt: "custom {aliases}", Aliases: []string{"auth"}, Language: func() string { return "L" }}
	if s, _ := SystemPrompt(over); s != "custom auth\n\nL" {
		t.Errorf("override prompt = %q", s)
	}
}
