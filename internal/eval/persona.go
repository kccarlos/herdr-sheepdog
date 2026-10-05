package eval

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
)

// Simulated-user personas: how the CEO talks, selectable per run
// (`--personas` on `eval sim` and `bench run`), independent of the
// scenario. A persona changes the words, never the content: the agenda's
// beats (or the goal) stay what the CEO wants. The zh persona speaks only
// Mandarin and ignores a scenario's language switch.

// Persona is one speaking style.
type Persona struct {
	Name string
	// Instructions are added to the simulated user's prompt.
	Instructions string
	// Language, when set, overrides the scenario's language.
	Language string
}

// Personas are the known personas by name.
var Personas = map[string]Persona{
	"terse":     {Name: "terse", Instructions: "You are terse: two to six words per line, no greetings, no thanks, no filler (\"status?\", \"approve it\", \"why?\"). Keep every worker name and decision your item contains."},
	"rambling":  {Name: "rambling", Instructions: "You ramble: long, loose sentences (20 to 40 words) with asides, self-corrections and context nobody asked for (\"so, okay, I was just thinking, and actually before that…\"), but the request is always in there somewhere. Keep every worker name and decision your item contains."},
	"zh":        {Name: "zh", Language: "zh", Instructions: "You speak ONLY Mandarin Chinese (simplified), every line, the whole call, even where your item is written in English or tells you to switch language: say it in Chinese. Worker names stay as they are (bench-codex). Natural spoken Chinese, short."},
	"impatient": {Name: "impatient", Instructions: "You are impatient and in a hurry: short, clipped lines, you push for the bottom line (\"just tell me\", \"skip the details\" — unless your item asks for detail), and if an answer runs long or is vague you say so. Keep every worker name and decision your item contains."},
	"vague":     {Name: "vague", Instructions: "You are vague: you refer to things loosely (\"that one\", \"the tests thing\", \"the other guy\") rather than by exact name, unless your item says to name it; when sheepdog asks which one you mean, you answer clearly. Never vague about yes/no decisions."},
}

// PersonaNames lists the personas in a fixed order (rotation order).
var PersonaNames = []string{"terse", "rambling", "zh", "impatient", "vague"}

// ParsePersonas checks a --personas value: empty (the scenario's own
// persona), "rotate" (every persona in turn), or a comma list of names.
func ParsePersonas(spec string) ([]string, error) {
	spec = strings.TrimSpace(spec)
	switch spec {
	case "":
		return nil, nil
	case "rotate", "all":
		return append([]string(nil), PersonaNames...), nil
	}
	var out []string
	for _, n := range strings.Split(spec, ",") {
		n = strings.TrimSpace(n)
		if _, ok := Personas[n]; !ok {
			known := append([]string(nil), PersonaNames...)
			sort.Strings(known)
			return nil, fmt.Errorf("persona %q: want rotate or one of %s", n, strings.Join(known, ", "))
		}
		out = append(out, n)
	}
	return out, nil
}

// PersonaFor picks the persona of one run: the list rotated by the
// repeat index, offset by the scenario id so that every scenario does
// not start on the same persona. Empty list: no persona.
func PersonaFor(list []string, id string, repeat int) string {
	if len(list) == 0 {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	r := repeat - 1
	if r < 0 {
		r = 0
	}
	return list[(int(h.Sum32()%uint32(len(list)))+r)%len(list)]
}

// WithPersona returns sc spoken in persona name (unchanged for "" or a
// fixed script, whose exact words are the point). The scenario's own
// persona text stays (it says what this CEO does); the style is added.
// A persona with its own language replaces the scenario's, and the
// opening (said verbatim) is replaced by OpeningZH when the scenario has
// one, else dropped so the simulated user says the first line.
func WithPersona(sc SimScenario, name string) SimScenario {
	p, ok := Personas[name]
	if !ok || sc.Fixed {
		return sc
	}
	sc.Persona = strings.TrimSpace(sc.Persona + "\nSpeaking style (persona " + p.Name + "): " + p.Instructions)
	if p.Language != "" && p.Language != sc.Language {
		sc.Language = p.Language
		if sc.Opening != "" && !hasHan(sc.Opening) {
			sc.Opening = sc.OpeningZH
		}
	}
	return sc
}

func hasHan(s string) bool {
	for _, r := range s {
		if r >= 0x4e00 && r <= 0x9fff {
			return true
		}
	}
	return false
}
