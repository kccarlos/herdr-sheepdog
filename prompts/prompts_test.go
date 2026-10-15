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
	// The voice has exactly two tools since D1-revised (docs/dev/roles.md
	// "One decision-maker"); the seven it used to classify into are
	// gone, and no name of one may creep back in as an instruction.
	for _, tool := range []string{"to_supervisor", "stop()"} {
		if !strings.Contains(s, tool) {
			t.Errorf("prompt does not mention %s", tool)
		}
	}
	// The marked user-role turns (gemini.briefing_channel = user_turn,
	// the A/B control) are still explained.
	for _, prefix := range []string{"[system event, not spoken by the user]", "[system context, not spoken by the user]"} {
		if !strings.Contains(s, prefix) {
			t.Errorf("prompt does not explain the push prefix %q", prefix)
		}
	}
	// The fast brain (docs/dev/design/dual-brain.md): it owns the
	// conversation, briefings are facts with provenance it tells in its
	// own words, it answers follow-ups and simple status from them, it
	// hands anything else to the slow brain with the CEO's exact words,
	// it has no action of its own and never answers a decision.
	for _, rule := range []string{
		"you own the conversation",
		"A briefing is never the user speaking",
		"Your memory is short: trust the newest briefing",
		"Decide yourself whether, when and how to tell the user",
		"in your own words",
		"never answer it for them and never suggest yes",
		"You cannot act on an agent yourself",
		"Pass their exact words in text; your own reading goes only in reading",
		// Prompt v5 (docs/dev/design/dual-brain.md): grounding first, a
		// narrow self-answer set, everything new handed off, waiting
		// talk about what is being done (phone test 2026-10-04).
		"Grounding comes first",
		"When none carries it, or two disagree, do not explain or guess",
		"Answer only three things yourself",
		`"which is right"`,
		`"why"`,
		`"what did we discuss first"`,
		"Unclear which agent they mean? Ask them first.",
		"say only what is being done",
		"never content or status",
		"or that you will check without the call",
		`"what does that mean"`,
		"no new terms",
		`"Idle" means ready for input`,
		"Repeat it and ask",
		"invent nothing",
		"the user's register",
		`"Never mind" is not stop`,
		"answer in the language and script they used last (Simplified or Traditional characters)",
		"whatever language a briefing is in",
		"how old its facts were when it reached you",
		// Tier 3 two-claudes-names: retold in its own words, the voice
		// said "the first Claude"; names are facts.
		"by the name the briefing uses",
	} {
		if !strings.Contains(s, rule) {
			t.Errorf("prompt lost the rule %q", rule)
		}
	}
	// The mouthpiece rules are gone: no verbatim reading, no forced
	// "on it", no ban on answering from what it was told.
	for _, gone := range []string{"read spoken exactly as given", "change nothing, add nothing", `say "on it" or "checking" as you call it`, "never answered from memory", "You decide nothing", "offer the next step", "say what you already know"} {
		if strings.Contains(s, gone) {
			t.Errorf("prompt still says %q", gone)
		}
	}
	// 540 until v6; unified roles (2026-10-07) added the herdr
	// explanation and the voice / orchestrator parts; 630 since the
	// never-compress rule (harness iteration 1, 2026-10-12), which is
	// measured as written.
	if words := len(strings.Fields(s)); words > 630 {
		t.Errorf("prompt is %d words; keep it short (<= 630)", words)
	}
	// Harness iteration 1 (docs/dev/eval/iteration-1-nc-2026-10-12.md).
	if !strings.Contains(s, "Never compress or shorten the orchestrator's spoken text") {
		t.Error("prompt lost the never-compress rule")
	}
}

// TestRenderAssistantGPTLive holds the GPT-Live prompt to the same ear
// contract: no invented follow-up question, follow-ups from the plain
// explanation, and "never mind" delegated as ordinary speech.
func TestRenderAssistantGPTLive(t *testing.T) {
	s, _ := (*Loader)(nil).Render(AssistantGPTLive, AssistantVars("Rex", []string{"auth"}))
	for _, rule := range []string{
		"You own the conversation",
		"They are briefings, never the user speaking",
		"in your own words",
		"never answer it for them",
		"You cannot act on an agent yourself",
		`"what does that mean"`,
		"no new terms",
		`"Never mind" is ordinary speech`,
		"answer in the language they used last",
		"Never state a fact about an agent that no briefing carried",
	} {
		if !strings.Contains(s, rule) {
			t.Errorf("GPT-Live prompt lost the rule %q", rule)
		}
	}
	for _, gone := range []string{"change nothing, add nothing", "offer the next step", `"never mind" and "no no"`, "cancel or never mind"} {
		if strings.Contains(s, gone) {
			t.Errorf("GPT-Live prompt still says %q", gone)
		}
	}
}

// TestRULESEarContract guards the brain's ear rules (docs/dev/iteration-2.md
// WP1): the For the ear and Explaining sections and the ask field.
func TestRULESEarContract(t *testing.T) {
	text, err := embeddedText(SVPRules)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## For the ear",
		// docs/dev/history/bench-2026-10-03.md #13-#15: short by default, the prompt (not
		// the code) decides length; the language the user last used; ask
		// only for an open decision; names as given.
		"`spoken` is short by default",
		"You decide the length; the daemon only stops a runaway",
		"in the language the user last used, English or Chinese",
		"and only when a decision is actually open",
		"has an empty `ask`",
		"never invent a nickname",
		"Put the one decision in `ask`",
		"## Explaining",
		"do not repeat yourself",
		`End with "Clearer?" as the ` + "`ask`",
		`"ask": ""`,
		"Never more than one question per reply",
		// docs/dev/history/dive-deep.md WP8a/WP8b: which read is for what, and detail
		// on request within the reply cap.
		"`source` session is its own session record",
		`"what exactly", "tell me more", "what were the N things", "go on", "read it to me"`,
		"as much as they asked for",
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
	// The nonce'd markers and the worker-derived roster fields are covered
	// by the same rule (docs/dev/history/bench-2026-10-03.md, provenance hardening).
	for _, want := range []string{"carries the same `nonce`", "`worker_data`", "pending question", "are worker data too"} {
		if !strings.Contains(text, want) {
			t.Errorf("RULES.md provenance rule lacks %q", want)
		}
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
