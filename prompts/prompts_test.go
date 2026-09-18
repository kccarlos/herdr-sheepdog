package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderAssistant(t *testing.T) {
	s, _ := (*Loader)(nil).Render(Assistant, map[string]string{"aliases": "auth, docs"})
	if !strings.Contains(s, "aliases: auth, docs.") || strings.Contains(s, "{aliases}") {
		t.Errorf("aliases not substituted: %q", s)
	}
	for _, tool := range []string{"briefing", "ask", "propose", "confirm", "cancel", "note"} {
		if !strings.Contains(s, tool) {
			t.Errorf("prompt does not mention %s", tool)
		}
	}
	// The push prefixes of internal/voice/gemini (pushTurn) must be
	// explained to the model: system events are spoken, context is not.
	for _, prefix := range []string{"[system event, not spoken by the user]", "[system context, not spoken by the user]"} {
		if !strings.Contains(s, prefix) {
			t.Errorf("prompt does not explain the push prefix %q", prefix)
		}
	}
	// Rules from the headset sessions and docs/roles.md §Assistant (cheap
	// guard against the prompt drifting): status questions always go
	// through a tool, propose / ask never guess the agent, doubts about a
	// status are ask (a fresh screen read, never the cached briefing
	// again), "idle" is never inflated into "not finished", only the
	// latest tool result may be reused for a follow-up, a mis-heard
	// utterance is repeated back rather than guessed, and the copilot
	// manner (offer the next step, read options as a choice) is present.
	for _, rule := range []string{
		"MUST start with a tool call",
		"never answer it from memory",
		"If a tool has not answered yet, say so",
		`"Idle" means ready for input`,
		"Fill agent only when the user said an alias",
		"leave it empty, never guess",
		`"what did it say"`,
		`"check again"`,
		"tells an agent to DO something",
		"Unsure whether it's a question or an instruction: ask",
		"uses the latest spoken, detail and options",
		"Repeat it and ask",
		"offer the next step",
		"read them as a choice",
		"invent nothing",
		"shepherd's register",
	} {
		if !strings.Contains(s, rule) {
			t.Errorf("prompt lost the rule %q", rule)
		}
	}
	if words := len(strings.Fields(s)); words > 400 {
		t.Errorf("prompt is %d words; keep it short (<= 400)", words)
	}
	if _, err := Render("missing.txt", nil); err == nil {
		t.Error("missing template accepted")
	}
}

func TestRenderMissingTemplate(t *testing.T) {
	if _, err := Render("missing.txt", nil); err == nil {
		t.Error("missing template accepted")
	}
}

// TestRULESProtectedSentences guards against the rail or the provenance
// rule silently drifting out of the embedded RULES.md.
func TestRULESProtectedSentences(t *testing.T) {
	text, err := embeddedText(SVPRules)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, RailSentence) {
		t.Errorf("RULES.md lost the rail sentence %q", RailSentence)
	}
	if !strings.Contains(text, ProvenanceSentence) {
		t.Errorf("RULES.md lost the provenance sentence %q", ProvenanceSentence)
	}
}

func TestLoaderEmbeddedOnly(t *testing.T) {
	l := NewLoader("", nil)
	for _, name := range WorkspaceFiles {
		text, source := l.Load(name)
		if source != SourceEmbedded {
			t.Errorf("%s: source = %q, want embedded", name, source)
		}
		if text == "" {
			t.Errorf("%s: empty text", name)
		}
	}
}

func TestLoaderNilBehavesEmbedded(t *testing.T) {
	var l *Loader
	text, source := l.Load(Assistant)
	if source != SourceEmbedded || text == "" {
		t.Errorf("nil loader: got (%q, %q)", text, source)
	}
}

func TestLoaderOverridePartial(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, Assistant), []byte("custom assistant text"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := NewLoader(dir, nil)
	text, source := l.Load(Assistant)
	if source != SourceOverride || text != "custom assistant text" {
		t.Errorf("assistant.md: got (%q, %q)", text, source)
	}
	// USER.md was not overridden: still embedded.
	_, source = l.Load(User)
	if source != SourceEmbedded {
		t.Errorf("USER.md: source = %q, want embedded (no override present)", source)
	}
}

func TestLoaderRejectsRulesOverrideMissingRail(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "svp"), 0o700); err != nil {
		t.Fatal(err)
	}
	bad := "## Tools\n\nSome replacement text with no rail sentence at all.\n"
	if err := os.WriteFile(filepath.Join(dir, SVPRules), []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	var rejected []map[string]any
	log := recorderFunc(func(source, typ, paneID string, payload any) {
		if typ == "prompts.override_rejected" {
			m, _ := payload.(map[string]any)
			rejected = append(rejected, m)
		}
	})
	l := NewLoader(dir, log)
	text, source := l.Load(SVPRules)
	if source != SourceEmbedded {
		t.Errorf("rejected override: source = %q, want embedded", source)
	}
	if !hasProtectedSentences(text) {
		t.Error("fallback text lacks the protected sentences")
	}
	if len(rejected) != 1 {
		t.Fatalf("got %d prompts.override_rejected events, want 1", len(rejected))
	}
}

func TestLoaderReload(t *testing.T) {
	dir := t.TempDir()
	l := NewLoader(dir, nil)
	if _, source := l.Load(Assistant); source != SourceEmbedded {
		t.Fatalf("expected embedded before the override file exists")
	}
	if err := os.WriteFile(filepath.Join(dir, Assistant), []byte("reloaded text"), 0o600); err != nil {
		t.Fatal(err)
	}
	files := l.Reload()
	text, source := l.Load(Assistant)
	if source != SourceOverride || text != "reloaded text" {
		t.Errorf("after reload: got (%q, %q)", text, source)
	}
	if len(files) != len(WorkspaceFiles) {
		t.Errorf("Reload() returned %d files, want %d", len(files), len(WorkspaceFiles))
	}
}

func TestExportRefusesWithoutForce(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Export(dir, false); err != nil {
		t.Fatalf("first export: %v", err)
	}
	if _, _, err := Export(dir, false); err == nil {
		t.Error("second export without --force should refuse")
	}
	if _, _, err := Export(dir, true); err != nil {
		t.Errorf("export --force: %v", err)
	}
}

// recorderFunc adapts a func to logging.Logger without importing the
// logging package's test helpers.
type recorderFunc func(source, typ, paneID string, payload any)

func (f recorderFunc) Log(source, typ, paneID string, payload any) { f(source, typ, paneID, payload) }
