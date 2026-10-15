package supervisor

import (
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/prose"
)

// The language of the daemon's own code sentences
// (docs/dev/eval/eval-campaign-2026-10-03.md H4). Wherever an orchestrator turn follows,
// the orchestrator gets the facts and writes the line in the user's
// language itself. The few sentences the daemon speaks without it — the
// bare-answer fast path, a tapped answer, the block template when the
// orchestrator is slower than the announce wait — follow the language of the
// user's last words: Chinese or English, nothing else.

// ceoChineseLocked reports whether the user's last words (the newest
// voice or typed input with text; a tap's text is the screen's option
// label, not their language) were Chinese. Caller holds s.mu.
func (s *Supervisor) ceoChineseLocked() bool {
	for i := len(s.inputs) - 1; i >= 0; i-- {
		in := s.inputs[i]
		if in.kind == InputTap || strings.TrimSpace(in.text) == "" {
			continue
		}
		return prose.Chinese(in.text)
	}
	return false
}

// ceoLanguage is the language of the user's last words for the
// orchestrator's request facts (brain.EventFacts.Language): "" before they have
// said anything.
func (s *Supervisor) ceoLanguage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.inputs) - 1; i >= 0; i-- {
		in := s.inputs[i]
		if in.kind == InputTap || strings.TrimSpace(in.text) == "" {
			continue
		}
		if prose.Chinese(in.text) {
			return brain.LanguageChinese
		}
		return brain.LanguageEnglish
	}
	return ""
}

// toldLine is the spoken confirmation of an answer the daemon sent
// without the orchestrator: "Told auth yes." or "已回复 auth：同意。". An
// option's label is the screen's own text and is said as it is.
func toldLine(alias, label string, zh bool) string {
	if !zh {
		return "Told " + alias + " " + label + "."
	}
	switch label {
	case "yes":
		label = "同意"
	case "no":
		label = "不同意"
	}
	return "已回复 " + alias + "：" + strings.TrimRight(label, ".。 ") + "。"
}

// noticeText is an orchestrator notice in the user's language: Chinese once their
// last words were Chinese, English otherwise.
func (s *Supervisor) noticeText(n brain.Notice) string {
	if s.ceoLanguage() == brain.LanguageChinese && n.Chinese != "" {
		return n.Chinese
	}
	return n.English
}
