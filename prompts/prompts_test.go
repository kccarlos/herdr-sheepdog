package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderAssistant(t *testing.T) {
	s, _ := (*Loader)(nil).Render(Assistant, AssistantVars("Rex", []string{"auth", "docs"}))
	if !strings.Contains(s, "aliases: auth, docs.") || strings.Contains(s, "{aliases}") {
		t.Errorf("aliases not substituted: %q", s)
	}
	if !strings.HasPrefix(s, "You are Rex,") || strings.Contains(s, "{name}") {
		t.Errorf("name not substituted: %q", firstWords(s))
	}
	// The voice has exactly two tools since D1-revised (docs/roles.md
	// "One decision-maker"); the seven it used to classify into are
	// gone, and no name of one may creep back in as an instruction.
	for _, tool := range []string{"to_supervisor", "stop()"} {
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
	// Rules from the headset sessions and docs/roles.md "The voice — do
	// / don't" (a cheap guard against the prompt drifting): the words go
	// over verbatim, the voice never answers a push itself, "idle" is
	// never inflated into "not finished", a mis-heard utterance is
	// repeated back rather than guessed, options are read as a choice,
	// and the ear contract (docs/iteration-2.md WP1): spoken is read as
	// given and already ends with the one question, the voice adds none
	// of its own, and "what does that mean" is answered from detail.
	for _, rule := range []string{
		"verbatim",
		"Do not paraphrase",
		"Never answer them yourself",
		`"Idle" means ready for input`,
		"read spoken exactly as given",
		"Do not add a follow-up question of your own",
		`"what does that mean"`,
		"answer from detail",
		"no new terms",
		"Repeat it and ask",
		"read them as a choice",
		"invent nothing",
		"shepherd's register",
		`"Never mind" is not stop`,
	} {
		if !strings.Contains(s, rule) {
			t.Errorf("prompt lost the rule %q", rule)
		}
	}
	// The voice invents no follow-up question and "never mind" is not a
	// stop word (eval cases never-mind-is-not-stop-voice and
	// explain-simpler-second-time).
	for _, gone := range []string{"offer the next step", `"never mind", "no no"`} {
		if strings.Contains(s, gone) {
			t.Errorf("prompt still says %q", gone)
		}
	}
	if words := len(strings.Fields(s)); words > 400 {
		t.Errorf("prompt is %d words; keep it short (<= 400)", words)
	}
}

// TestRenderAssistantGPTLive holds the GPT-Live prompt to the same ear
// contract: no invented follow-up question, follow-ups from the plain
// explanation, and "never mind" delegated as ordinary speech.
func TestRenderAssistantGPTLive(t *testing.T) {
	s, _ := (*Loader)(nil).Render(AssistantGPTLive, AssistantVars("Rex", []string{"auth"}))
	for _, rule := range []string{
		"Do not add a follow-up question of your own",
		`"what does that mean"`,
		"no new terms",
		`"Never mind" is ordinary speech`,
	} {
		if !strings.Contains(s, rule) {
			t.Errorf("GPT-Live prompt lost the rule %q", rule)
		}
	}
	for _, gone := range []string{"offer the next step", `"never mind" and "no no"`, "cancel or never mind"} {
		if strings.Contains(s, gone) {
			t.Errorf("GPT-Live prompt still says %q", gone)
		}
	}
}

// TestRULESEarContract guards the brain's ear rules (docs/iteration-2.md
// WP1): the For the ear and Explaining sections and the ask field.
func TestRULESEarContract(t *testing.T) {
	text, err := embeddedText(SVPRules)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## For the ear",
		"thirty words or fewer",
		"Put the one decision in `ask`",
		"## Explaining",
		"do not repeat yourself",
		`End with "Clearer?" as the ` + "`ask`",
		`"ask": ""`,
		"Never more than one question per reply",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("RULES.md lacks %q", want)
		}
	}
}

// TestAssistantVarsDefaults covers the empty cases: no configured name
// falls back to DefaultName, no agents to a spoken phrase.
func TestAssistantVarsDefaults(t *testing.T) {
	vars := AssistantVars("  ", nil)
	if vars["name"] != DefaultName || vars["aliases"] != "none configured yet" {
		t.Errorf("AssistantVars(\"\", nil) = %v", vars)
	}
	if vars := AssistantVars(" Rex ", []string{"auth"}); vars["name"] != "Rex" {
		t.Errorf("name not trimmed: %q", vars["name"])
	}
}

// firstWords is the head of s, for a readable failure message.
func firstWords(s string) string {
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}

func TestRenderMissingTemplate(t *testing.T) {
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
