// Package lang is the shepherd's language preference: the catalog of
// languages the settings offer and the one line of information both
// brains get about the choice (docs/design/language-preference.md).
//
// It is information, not judgment: the daemon never decides which
// language an utterance is in or which one to answer in. It tells the
// models what the shepherd set, and the models choose.
package lang

import (
	"fmt"
	"strings"
)

// Provider ids, as voice.provider names them.
const (
	ProviderGemini = "gemini"
	ProviderOpenAI = "openai"
)

// Language is one entry of the catalog.
type Language struct {
	// Code is the BCP-47 code, as Gemini Live's language table writes it
	// (zh-Hans and zh-Hant are two entries).
	Code string `json:"code"`
	// Name is the English name; Native the name in the language itself.
	Name   string `json:"name"`
	Native string `json:"native"`
	// Providers lists the voice providers that support it.
	Providers []string `json:"providers"`
}

// both is the Providers of every catalog entry today.
var both = []string{ProviderGemini, ProviderOpenAI}

// Catalog is the languages the settings offer, English and the two
// Chinese scripts first, then alphabetical by English name.
//
// It is the intersection of the two voice providers' lists, checked on
// 2026-10-05:
//   - Gemini Live: the "Supported languages" table of
//     https://ai.google.dev/gemini-api/docs/live-guide (99 languages,
//     Chinese as zh-Hans and zh-Hant).
//   - GPT-Live: OpenAI publishes no language list for gpt-live-1 (the
//     model page, the Live guide and the Live prompting guide name
//     none). The list used is OpenAI's published speech-recognition
//     list (Whisper's languages under 50 % word error rate, the same
//     list OpenAI gives for its realtime models); Chinese there covers
//     both scripts.
//
// Every one of those 57 OpenAI languages is in Gemini's table, so every
// entry is supported by both. Providers is kept per entry so a future
// one-sided entry can say so.
var Catalog = []Language{
	{"en", "English", "English", both},
	{"zh-Hans", "Chinese (Simplified)", "简体中文", both},
	{"zh-Hant", "Chinese (Traditional)", "繁體中文", both},
	{"af", "Afrikaans", "Afrikaans", both},
	{"ar", "Arabic", "العربية", both},
	{"hy", "Armenian", "Հայերեն", both},
	{"az", "Azerbaijani", "Azərbaycanca", both},
	{"be", "Belarusian", "Беларуская", both},
	{"bs", "Bosnian", "Bosanski", both},
	{"bg", "Bulgarian", "Български", both},
	{"ca", "Catalan", "Català", both},
	{"hr", "Croatian", "Hrvatski", both},
	{"cs", "Czech", "Čeština", both},
	{"da", "Danish", "Dansk", both},
	{"nl", "Dutch", "Nederlands", both},
	{"et", "Estonian", "Eesti", both},
	{"fil", "Filipino", "Filipino", both},
	{"fi", "Finnish", "Suomi", both},
	{"fr", "French", "Français", both},
	{"gl", "Galician", "Galego", both},
	{"de", "German", "Deutsch", both},
	{"el", "Greek", "Ελληνικά", both},
	{"he", "Hebrew", "עברית", both},
	{"hi", "Hindi", "हिन्दी", both},
	{"hu", "Hungarian", "Magyar", both},
	{"is", "Icelandic", "Íslenska", both},
	{"id", "Indonesian", "Bahasa Indonesia", both},
	{"it", "Italian", "Italiano", both},
	{"ja", "Japanese", "日本語", both},
	{"kn", "Kannada", "ಕನ್ನಡ", both},
	{"kk", "Kazakh", "Қазақ тілі", both},
	{"ko", "Korean", "한국어", both},
	{"lv", "Latvian", "Latviešu", both},
	{"lt", "Lithuanian", "Lietuvių", both},
	{"mk", "Macedonian", "Македонски", both},
	{"ms", "Malay", "Bahasa Melayu", both},
	{"mi", "Maori", "Māori", both},
	{"mr", "Marathi", "मराठी", both},
	{"ne", "Nepali", "नेपाली", both},
	{"nb", "Norwegian", "Norsk", both},
	{"fa", "Persian", "فارسی", both},
	{"pl", "Polish", "Polski", both},
	{"pt-BR", "Portuguese (Brazil)", "Português (Brasil)", both},
	{"pt-PT", "Portuguese (Portugal)", "Português (Portugal)", both},
	{"ro", "Romanian", "Română", both},
	{"ru", "Russian", "Русский", both},
	{"sr", "Serbian", "Српски", both},
	{"sk", "Slovak", "Slovenčina", both},
	{"sl", "Slovenian", "Slovenščina", both},
	{"es", "Spanish", "Español", both},
	{"sw", "Swahili", "Kiswahili", both},
	{"sv", "Swedish", "Svenska", both},
	{"ta", "Tamil", "தமிழ்", both},
	{"th", "Thai", "ไทย", both},
	{"tr", "Turkish", "Türkçe", both},
	{"uk", "Ukrainian", "Українська", both},
	{"ur", "Urdu", "اردو", both},
	{"vi", "Vietnamese", "Tiếng Việt", both},
	{"cy", "Welsh", "Cymraeg", both},
}

// Lookup returns the catalog entry for code, matched case-insensitively.
func Lookup(code string) (Language, bool) {
	code = strings.TrimSpace(code)
	for _, l := range Catalog {
		if strings.EqualFold(l.Code, code) {
			return l, true
		}
	}
	return Language{}, false
}

// Normalize validates codes against the catalog and returns them in
// catalog spelling, in the given order, duplicates dropped. An unknown
// code is an error naming it.
func Normalize(codes []string) ([]string, error) {
	out := make([]string, 0, len(codes))
	seen := map[string]bool{}
	for _, c := range codes {
		l, ok := Lookup(c)
		if !ok {
			return nil, fmt.Errorf("language %q is not in the catalog", c)
		}
		if seen[l.Code] {
			continue
		}
		seen[l.Code] = true
		out = append(out, l.Code)
	}
	return out, nil
}

// Line is the information both brains get about the preference, one
// sentence pair; "" when nothing is set (the prompts then say what they
// said before the setting existed). Unknown codes are skipped.
//
// Example: "The shepherd's language setting, in order of preference:
// Chinese (Simplified, 简体中文), English. Answer in the language they
// use; write Chinese in Simplified characters."
func Line(codes []string) string {
	var names []string
	var chinese []string
	for _, c := range codes {
		l, ok := Lookup(c)
		if !ok {
			continue
		}
		names = append(names, label(l))
		switch l.Code {
		case "zh-Hans":
			chinese = append(chinese, "Simplified")
		case "zh-Hant":
			chinese = append(chinese, "Traditional")
		}
	}
	if len(names) == 0 {
		return ""
	}
	line := "The shepherd's language setting, in order of preference: " + strings.Join(names, ", ") + ". Answer in the language they use"
	switch len(chinese) {
	case 1:
		line += "; write Chinese in " + chinese[0] + " characters"
	case 2:
		line += "; write Chinese in " + chinese[0] + " characters unless they ask for " + chinese[1]
	}
	return line + "."
}

// label is how Line names a language: the English name with the native
// one beside it when it differs ("Chinese (Simplified, 简体中文)",
// "Japanese (日本語)", "English").
func label(l Language) string {
	if l.Native == l.Name || l.Native == "" {
		return l.Name
	}
	if i := strings.Index(l.Name, " ("); i > 0 && strings.HasSuffix(l.Name, ")") {
		return l.Name[:len(l.Name)-1] + ", " + l.Native + ")"
	}
	return l.Name + " (" + l.Native + ")"
}
