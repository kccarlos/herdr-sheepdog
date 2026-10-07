package state

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Agent names have three layers (docs/roles.md "Agent names"): the pane
// id, which code, the app and the API use and nobody ever hears; the
// alias, unique and stable, which the orchestrator's tool calls use; and the
// spoken name, computed here, which is what the user hears and says.
// The spoken name is the shortest form that is unique among the running
// agents:
//
//  1. a name the user gave (herdr agent rename, a voice rename, a config
//     or API alias) always wins, verbatim;
//  2. the kind alone ("Claude") when it is the only one of its kind;
//  3. project and kind ("sheepdog Claude") when it is the only one of
//     its kind in its project;
//  4. the worker's own session title and kind ("README Claude") when one
//     project has several of a kind;
//  5. project, kind and a number ("sheepdog Claude 2").
//
// A computed name the user has heard or said is pinned (PinSpoken) and
// kept like a user name from then on: rules 2-5 no longer move it.
//
// Exited agents (state unknown) take no part: they keep the name they
// last had.

// maxTitleWords caps the session title inside a spoken name; the roster
// carries the whole (clipped) title, and the orchestrator picks a word or two
// of a long one when it speaks.
const maxTitleWords = 3

// MaxTitleRunes caps a session title stored on a row.
const MaxTitleRunes = 60

// running reports whether the agent takes part in the uniqueness check.
func running(a Agent) bool {
	switch a.State {
	case herdr.StateWorking, herdr.StateBlocked, herdr.StateIdle, herdr.StateDone:
		return true
	}
	return false
}

// CEOName is the name the user gave the agent, or "": the herdr agent
// name (herdr agent rename, or a voice rename through the orchestrator), else
// an alias pinned in the config or through the API.
func CEOName(a Agent) string {
	if n := strings.TrimSpace(a.Name); n != "" {
		return n
	}
	if !a.AliasAuto && a.Alias != "" {
		return a.Alias
	}
	return ""
}

// kindWords are the spoken forms of the herdr kind labels; any other
// kind is capitalised.
var kindWords = map[string]string{
	"claude":   "Claude",
	"codex":    "Codex",
	"agy":      "Antigravity",
	"gemini":   "Gemini",
	"opencode": "OpenCode",
	"amp":      "Amp",
	"cursor":   "Cursor",
	"droid":    "Droid",
}

// KindWord is how a kind label is said ("claude" -> "Claude").
func KindWord(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	if w, ok := kindWords[k]; ok {
		return w
	}
	if k == "" {
		return "Agent"
	}
	r, n := utf8.DecodeRuneInString(k)
	return string(unicode.ToUpper(r)) + k[n:]
}

// projectWords is how a workspace label is said: separators become
// spaces ("herdr-sheepdog" -> "herdr sheepdog").
func projectWords(label string) string {
	return strings.Join(strings.FieldsFunc(label, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '/' || unicode.IsSpace(r)
	}), " ")
}

// CleanTitle makes a worker's session title fit a row: one line, no
// surrounding quotes or trailing punctuation, at most MaxTitleRunes.
func CleanTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	title = strings.TrimLeft(title, "\"'`“‘ ")
	title = strings.TrimRight(title, "\"'`”’ .!?:;,")
	if utf8.RuneCountInString(title) > MaxTitleRunes {
		r := []rune(title)
		title = strings.TrimSpace(string(r[:MaxTitleRunes]))
	}
	return title
}

// maxTitleNameRunes bounds the title inside a spoken name when words do
// not (a Chinese title has no spaces).
const maxTitleNameRunes = 32

// titleWords is the title as it goes into a spoken name: its first
// maxTitleWords plain words (speakableWord), without a dangling "and" or
// "the" at the end, at most maxTitleNameRunes. Tier-3 bench 2026-10-03,
// finding 5: Claude Code titles name files ("shop/cart.go Cart type",
// "TODO.md tasks"), and the names built from them verbatim were ones
// the orchestrator would not say. "" when no plain word is left: the agent is
// then numbered (project, kind, number).
func titleWords(title string) string {
	var f []string
	for _, w := range strings.Fields(CleanTitle(title)) {
		if len(f) == maxTitleWords {
			break
		}
		if w = strings.Trim(w, "\"'`“”‘’()[]{},;:!?"); speakableWord(w) {
			f = append(f, w)
		}
	}
	for len(f) > 1 && danglingWords[strings.ToLower(f[len(f)-1])] {
		f = f[:len(f)-1]
	}
	t := strings.Join(f, " ")
	if r := []rune(t); len(r) > maxTitleNameRunes {
		t = strings.TrimSpace(string(r[:maxTitleNameRunes]))
	}
	return t
}

// danglingWords do not end a name.
var danglingWords = map[string]bool{"and": true, "or": true, "the": true, "a": true, "an": true, "of": true, "to": true, "for": true, "with": true, "in": true, "on": true}

// speakableWord reports whether a title word reads aloud as a word: only
// letters, with inner hyphens or apostrophes, and not a code identifier
// (an upper-case letter after a lower-case one: "CartItem", "addItem").
// A path, a file name, a flag, a number or anything with code
// punctuation is not.
func speakableWord(w string) bool {
	if w == "" {
		return false
	}
	prevLower := false
	for i, r := range w {
		switch {
		case r == '-' || r == '\'' || r == '’':
			if i == 0 || i == len(w)-utf8.RuneLen(r) {
				return false
			}
			prevLower = false
		case unicode.IsLetter(r):
			if prevLower && unicode.IsUpper(r) {
				return false
			}
			prevLower = unicode.IsLower(r)
		default:
			return false
		}
	}
	return true
}

// SpokenNames computes the spoken name of every running agent, keyed by
// pane id (see the rules above). Agents that are not running are not in
// the map.
func SpokenNames(agents []Agent) map[string]string {
	run := make([]Agent, 0, len(agents))
	for _, a := range agents {
		if running(a) {
			run = append(run, a)
		}
	}
	sort.Slice(run, func(i, j int) bool { return run[i].PaneID < run[j].PaneID })

	out := make(map[string]string, len(run))
	byKind := map[string][]Agent{}
	var kinds []string
	for _, a := range run {
		if n := CEOName(a); n != "" {
			out[a.PaneID] = n
			continue
		}
		if a.SpokenPinned && a.Spoken != "" {
			out[a.PaneID] = a.Spoken
			continue
		}
		k := strings.ToLower(a.Kind)
		if _, ok := byKind[k]; !ok {
			kinds = append(kinds, k)
		}
		byKind[k] = append(byKind[k], a)
	}
	// Names already given (user names, pinned names) are not handed out
	// again: a newcomer skips to the next, longer form.
	taken := map[string]bool{}
	for _, n := range out {
		taken[strings.ToLower(n)] = true
	}
	free := func(n string) bool { return !taken[strings.ToLower(n)] }
	for _, k := range kinds {
		group := byKind[k]
		kw := KindWord(k)
		if len(group) == 1 && free(kw) {
			out[group[0].PaneID] = kw
			continue
		}
		byProject := map[string][]Agent{}
		var projects []string
		for _, a := range group {
			p := projectWords(a.WorkspaceLabel)
			if _, ok := byProject[p]; !ok {
				projects = append(projects, p)
			}
			byProject[p] = append(byProject[p], a)
		}
		for _, p := range projects {
			pg := byProject[p]
			base := strings.TrimSpace(p + " " + kw)
			if len(pg) == 1 && free(base) {
				out[pg[0].PaneID] = base
				continue
			}
			titles := map[string]int{}
			for _, a := range pg {
				if t := titleWords(a.Title); t != "" {
					titles[strings.ToLower(t)]++
				}
			}
			var rest []Agent
			for _, a := range pg {
				if t := titleWords(a.Title); t != "" && titles[strings.ToLower(t)] == 1 && free(t+" "+kw) {
					out[a.PaneID] = t + " " + kw
					continue
				}
				rest = append(rest, a)
			}
			if len(rest) == 1 && free(base) {
				out[rest[0].PaneID] = base
				continue
			}
			for i, a := range rest {
				out[a.PaneID] = base + " " + strconv.Itoa(i+1)
			}
		}
	}

	// Whatever still collides (two user names alike, a user name equal to
	// a computed one) gets a number, the first in pane order keeping it;
	// CEO-given and pinned names go first, so they keep theirs.
	kept := func(a Agent) bool { return CEOName(a) != "" || (a.SpokenPinned && a.Spoken != "") }
	order := make([]Agent, 0, len(run))
	for _, a := range run {
		if kept(a) {
			order = append(order, a)
		}
	}
	for _, a := range run {
		if !kept(a) {
			order = append(order, a)
		}
	}
	seen := map[string]bool{}
	for _, a := range order {
		name := out[a.PaneID]
		key := strings.ToLower(name)
		if !seen[key] {
			seen[key] = true
			continue
		}
		for i := 2; ; i++ {
			c := name + " " + strconv.Itoa(i)
			if !seen[strings.ToLower(c)] {
				out[a.PaneID] = c
				seen[strings.ToLower(c)] = true
				break
			}
		}
	}
	return out
}

// refreshSpokenLocked recomputes every row's spoken name. A running
// agent whose name changed keeps the old one in SpokenWas, stamped with
// SpokenChangedAt, so the roster can tell the orchestrator to mention it once.
// An agent that is not running keeps the name it last had (none when it
// never ran: Say falls back to its user name or alias). Caller holds s.mu.
func (s *Store) refreshSpokenLocked() {
	list := make([]Agent, 0, len(s.table.Agents))
	for _, a := range s.table.Agents {
		list = append(list, *a)
	}
	names := SpokenNames(list)
	for id, a := range s.table.Agents {
		name, ok := names[id]
		if !ok || name == a.Spoken {
			continue
		}
		if a.Spoken != "" {
			a.SpokenWas, a.SpokenChangedAt = a.Spoken, s.now()
		}
		a.Spoken = name
	}
}

// Say is how the agent is named aloud: its spoken name, else the name
// the user gave it, else its alias ("" when it has none).
func Say(a Agent) string {
	if a.Spoken != "" {
		return a.Spoken
	}
	if n := CEOName(a); n != "" {
		return n
	}
	return a.Alias
}

// PinSpoken pins the current spoken name of every running agent whose
// name (case-insensitive) is in names: the user heard or said it, so it
// stays (SpokenNames). It returns the panes newly pinned.
func (s *Store) PinSpoken(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[strings.ToLower(strings.TrimSpace(n))] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var pinned []string
	for id, a := range s.table.Agents {
		if a.SpokenPinned || a.Spoken == "" || CEOName(*a) != "" || !running(*a) || !want[strings.ToLower(a.Spoken)] {
			continue
		}
		a.SpokenPinned = true
		pinned = append(pinned, id)
	}
	if len(pinned) == 0 {
		return nil, nil
	}
	sort.Strings(pinned)
	return pinned, s.persist()
}
