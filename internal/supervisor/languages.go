package supervisor

import (
	"github.com/kccarlos/herdr-sheepdog/internal/lang"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Languages returns the shepherd's language preference (internal/lang
// codes, most preferred first), nil when none is set. The voice reads it
// at every (re)connect for its system instruction; the brain's host
// reads it for the state block.
func (s *Supervisor) Languages() []string {
	langs := s.store.Snapshot().Session.Languages
	if len(langs) == 0 {
		return nil
	}
	return langs
}

// LanguageLine is the preference as the one line both brains get ("" when
// none is set): information, never a rule about which language to use.
func (s *Supervisor) LanguageLine() string { return lang.Line(s.Languages()) }

// pushLanguages tells a running voice session about a changed preference
// as silent context. Gemini Live reads its system instruction only at
// setup, so the session running now learns it here; the next session
// has it in its system instruction (gemini.Config.Languages).
func (s *Supervisor) pushLanguages(langs []string) {
	line := lang.Line(langs)
	if line == "" {
		line = "The shepherd cleared their language setting."
	} else {
		line = "The shepherd changed their language setting just now. " + line
	}
	s.log.Log(logSource, "supervisor.languages", "", map[string]any{"languages": langs, "text": line})
	s.push("", line, voice.UrgencySilent)
}
