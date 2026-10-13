package brain

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// PromptSection is one part of the system prompt; the stable ones come
// first so a provider's prefix cache applies (docs/roles.md, borrowed
// from OpenClaw).
type PromptSection struct {
	Name string `json:"name"`
	// Source is where the text came from: prompts.SourceEmbedded or
	// prompts.SourceOverride for a prompt file, SourceDynamic for the
	// roster, ledger and time rendered per call.
	Source string `json:"source"`
	Text   string `json:"text"`
}

// SourceDynamic is the Source of a section rendered per call (roster,
// ledger, time), as opposed to a prompt file's.
const SourceDynamic = "dynamic"

// stableSections loads SOUL.md as the role section and splits RULES.md
// into one section per "## " heading (tools, the one rail, the answer
// contract). pl resolves each file against its override, if any
// (docs/roles.md "Prompt files"); a nil pl uses the embedded defaults.
// Recomputed on every turn (systemPrompt) so a reloaded override takes
// effect without a restart.
func stableSections(pl *prompts.Loader) []PromptSection {
	soul, soulSrc := pl.Load(prompts.SVPSoul)
	rules, rulesSrc := pl.Load(prompts.SVPRules)
	out := []PromptSection{{Name: "role", Source: soulSrc, Text: strings.TrimSpace(soul)}}
	for _, chunk := range strings.Split("\n"+rules, "\n## ") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		name, body, _ := strings.Cut(chunk, "\n")
		out = append(out, PromptSection{Name: slug(name), Source: rulesSrc, Text: "## " + strings.TrimSpace(name) + "\n\n" + strings.TrimSpace(body)})
	}
	return out
}

// userSection renders USER.md (the user's profile: aliases, chattiness,
// timezone, and anything the user added by hand) with vars, which
// SetUserVars sets and aliases (always freshly derived from the roster)
// extends.
func userSection(pl *prompts.Loader, vars map[string]string) PromptSection {
	text, src := pl.Render(prompts.User, vars)
	return PromptSection{Name: "user", Source: src, Text: text}
}

// mergeUserVars derives {aliases} from the current roster and overlays
// extra (SetUserVars: {chattiness}, {tz}), so aliases are always current
// even when SetUserVars has not been called yet.
func mergeUserVars(agents []AgentInfo, extra map[string]string) map[string]string {
	seen := map[string]bool{}
	var names []string
	for _, a := range agents {
		if a.Alias != "" && !seen[a.Alias] {
			seen[a.Alias] = true
			names = append(names, a.Alias)
		}
	}
	aliases := "none configured yet"
	if len(names) > 0 {
		aliases = strings.Join(names, ", ")
	}
	vars := map[string]string{"aliases": aliases, "chattiness": "normal", "tz": "local"}
	for k, v := range extra {
		vars[k] = v
	}
	return vars
}

// slug lowercases a heading into a section name ("The one rail" -> "rail",
// "For the ear" -> "ear").
func slug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimPrefix(name, "for ")
	name = strings.TrimPrefix(name, "the ")
	name = strings.TrimPrefix(name, "one ")
	if i := strings.IndexAny(name, " :"); i > 0 {
		last := name[strings.LastIndexByte(name, ' ')+1:]
		if last == "rail" || last == "contract" {
			return last
		}
		name = name[:i]
	}
	return name
}

// The state block's budget (docs/iteration-2.md WP5): every free text
// in the roster and ledger is clipped to a few words — the captures had
// a 6 k-character ledger of verbatim instructions — and when the block
// is still over stateBudgetChars the journal gives way, down to one line
// per agent, so four agents with everything filled in stay near 2.5 k
// characters. The full text is one recall away.
const (
	stateBudgetChars   = 2500
	rosterTaskRunes    = 36
	rosterPromptRunes  = 72
	rosterOptionRunes  = 20
	ledgerTaskRunes    = 36
	ledgerResultRunes  = 50
	ledgerBlockRunes   = 36
	ledgerNotes        = 5
	ledgerNoteRunes    = 40
	ledgerPromiseRunes = 40
	// ledgerInvestigationsDone is how many finished investigations the
	// section lists (every running one is listed).
	ledgerInvestigationsDone = 2
	invQuestionRunes         = 40
	invSummaryRunes          = 60
	unansweredRunes          = 80
)

// dynamicSections renders the roster, the ledger excerpt (with the open
// promises), the investigations, the user's language setting (when
// set: language is lang.Line's sentence) and the time with where the
// record begins (record, recordLine; may be empty) — the state block,
// which the loop sends after the window (withState), time last.
func dynamicSections(agents []AgentInfo, notes []string, promises []PromiseInfo, investigations []InvestigationInfo, unanswered []UnansweredInfo, language string, now time.Time, record string) []PromptSection {
	roster := renderRoster(agents, now)
	invs := renderInvestigations(investigations, now)
	clock := "## Now\n\n" + now.Format("Mon 2006-01-02 15:04:05 MST")
	if record != "" {
		clock += "\n" + record
	}
	var ledger string
	for n := journalLedgerEntries; n >= 1; n-- {
		ledger = renderLedgerN(agents, notes, promises, now, n)
		if len(roster)+len(ledger)+len(invs)+len(clock) <= stateBudgetChars {
			break
		}
	}
	secs := []PromptSection{
		{Name: "roster", Source: SourceDynamic, Text: roster},
		{Name: "ledger", Source: SourceDynamic, Text: ledger},
		{Name: "investigations", Source: SourceDynamic, Text: invs},
	}
	if len(unanswered) > 0 {
		secs = append(secs, PromptSection{Name: "unanswered", Source: SourceDynamic, Text: renderUnanswered(unanswered, now)})
	}
	if language != "" {
		// The user's own setting (iOS Settings ▸ Languages): `spoken`
		// is retold by the voice, which has the same line.
		secs = append(secs, PromptSection{Name: "language", Source: SourceDynamic, Text: "## Language\n\n" + language})
	}
	return append(secs, PromptSection{Name: "time", Source: SourceDynamic, Text: clock})
}

// recordLine tells the model where its record begins: when this
// conversation started, and whether the transcript it is shown is
// complete since then or, after a compaction, holds only the summary up
// to the time the summary covers.
func recordLine(started time.Time, c CompactionInfo, now time.Time) string {
	if started.IsZero() {
		return ""
	}
	at := func(t time.Time) string {
		t = t.In(now.Location())
		if y, m, d := t.Date(); y == now.Year() && m == now.Month() && d == now.Day() {
			return t.Format("15:04:05")
		}
		return t.Format("Mon 2006-01-02 15:04:05")
	}
	if c.At.IsZero() || c.CoversTo.IsZero() {
		return fmt.Sprintf("Record: this conversation started at %s and the transcript above is complete since then: nothing of it is missing.", at(started))
	}
	return fmt.Sprintf("Record: this conversation started at %s. Up to %s the transcript above holds only the compaction summary (its first message); every line since then is complete; recall finds the earlier lines.", at(started), at(c.CoversTo))
}

// renderUnanswered is the Unanswered section (only when there is one):
// the user's questions that got no real answer, with how long ago and
// what happened to the answer. Facts only: whether and when to come back
// to one is the model's.
func renderUnanswered(qs []UnansweredInfo, now time.Time) string {
	var b strings.Builder
	b.WriteString("## Unanswered\n\nThe user asked these and got no real answer (the daemon's record of what happened, not a judgment):\n")
	for _, q := range qs {
		fmt.Fprintf(&b, "- %s, %s ago: %q (%s)\n", q.ID, ago(now.Sub(q.Asked)), clipText(oneLine(q.Text), unansweredRunes), q.Why)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderInvestigations is the Investigations section: the jobs running
// now with how long they have been going (so "anything yet?" is answered
// from the prompt, without a second job), then what the finished ones
// found — the same shape as the open promises in the ledger.
func renderInvestigations(investigations []InvestigationInfo, now time.Time) string {
	var b strings.Builder
	b.WriteString("## Investigations\n\n")
	var running, done []InvestigationInfo
	for _, inv := range investigations {
		if inv.Status == InvestigationRunning {
			running = append(running, inv)
			continue
		}
		done = append(done, inv)
	}
	if len(running) == 0 && len(done) == 0 {
		b.WriteString("Nothing has been looked into yet.")
		return b.String()
	}
	if len(running) > 0 {
		b.WriteString("Running now (the answer comes on its own; say how it is going, do not start it again):\n")
		for _, inv := range running {
			fmt.Fprintf(&b, "- %s (%s): %q, %s so far\n", inv.ID, aliasOr(inv.Agent), clipText(oneLine(inv.Question), invQuestionRunes), ago(now.Sub(inv.Started)))
		}
	}
	if len(done) > ledgerInvestigationsDone {
		// The last few only; recall has the rest.
		sort.SliceStable(done, func(i, j int) bool { return done[i].Started.Before(done[j].Started) })
		done = done[len(done)-ledgerInvestigationsDone:]
	}
	if len(done) > 0 {
		b.WriteString("Already looked into (follow_up_of digs deeper):\n")
		for _, inv := range done {
			inv.Question, inv.Summary = clipText(oneLine(inv.Question), invQuestionRunes), clipText(oneLine(inv.Summary), invSummaryRunes)
			fmt.Fprintf(&b, "- %s\n", investigationLine(inv, now))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderRoster is one line per agent: alias, kind, state and how long,
// the current task, and the pending prompt with its option keys.
func renderRoster(agents []AgentInfo, now time.Time) string {
	var b strings.Builder
	b.WriteString("## Agents\n\n")
	if len(agents) == 0 {
		b.WriteString("No agents are running.")
		return b.String()
	}
	for _, a := range agents {
		fmt.Fprintf(&b, "- %s (%s)", aliasOr(a.Alias), kindOr(a.Kind))
		if a.Spoken != "" && a.Spoken != a.Alias {
			fmt.Fprintf(&b, ", say %q", a.Spoken)
		}
		if a.SpokenWas != "" {
			fmt.Fprintf(&b, " (just renamed from %q)", a.SpokenWas)
		}
		if a.Title != "" && !strings.Contains(a.Spoken, a.Title) {
			fmt.Fprintf(&b, ", session %q", a.Title)
		}
		fmt.Fprintf(&b, ": %s", stateOr(a.State))
		if !a.StateSince.IsZero() {
			fmt.Fprintf(&b, " for %s", ago(now.Sub(a.StateSince)))
		}
		if a.Task != "" {
			fmt.Fprintf(&b, "; task: %q", clipText(oneLine(a.Task), rosterTaskRunes))
		}
		if !a.LastInteraction.IsZero() {
			fmt.Fprintf(&b, "; user last dealt with it %s ago", agoCoarse(now.Sub(a.LastInteraction)))
		}
		if p := a.Pending; p != nil {
			b.WriteString("; WAITING FOR AN ANSWER")
			if !p.AskedAt.IsZero() {
				fmt.Fprintf(&b, " (asked %s ago)", ago(now.Sub(p.AskedAt)))
			}
			fmt.Fprintf(&b, ": %s", clipText(oneLine(p.Question), rosterPromptRunes))
			if len(p.Options) > 0 {
				var opts []string
				for i, o := range p.Options {
					opt := fmt.Sprintf("%d %q -> keys %v", i+1, clipText(o.Label, rosterOptionRunes), o.Keys)
					if o.Meta {
						opt += " (a free-text entry, not an answer: never offer it)"
					}
					opts = append(opts, opt)
				}
				fmt.Fprintf(&b, "; options: %s", strings.Join(opts, ", "))
			}
			if len(p.YesKeys) > 0 || len(p.NoKeys) > 0 {
				fmt.Fprintf(&b, "; yes -> %v, no -> %v", p.YesKeys, p.NoKeys)
			}
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderLedger lists what each agent last did and asked, the notes and
// the open promises (kept, expired and cancelled ones are in the
// transcript and recall), every text clipped (the budget above).
func renderLedger(agents []AgentInfo, notes []string, promises []PromiseInfo, now time.Time) string {
	return renderLedgerN(agents, notes, promises, now, journalLedgerEntries)
}

// renderLedgerN is renderLedger with at most journalN journal lines per
// agent.
func renderLedgerN(agents []AgentInfo, notes []string, promises []PromiseInfo, now time.Time, journalN int) string {
	var b strings.Builder
	b.WriteString("## Ledger\n\n")
	n := 0
	for _, a := range agents {
		var parts []string
		if a.LastTask != "" && a.LastTask != a.Task {
			// The roster has the current task.
			parts = append(parts, "last task "+fmt.Sprintf("%q", clipText(oneLine(a.LastTask), ledgerTaskRunes)))
		}
		if a.LastResult != "" {
			r := a.LastResult
			if a.LastDetail != "" {
				r += " (" + a.LastDetail + ")"
			}
			parts = append(parts, "last result: "+clipText(oneLine(r), ledgerResultRunes))
		}
		if a.LastBlock != "" && a.Pending == nil {
			// A pending prompt is in the roster in full.
			parts = append(parts, "last question: "+clipText(oneLine(a.LastBlock), ledgerBlockRunes))
		}
		journal := journalLedgerLinesN(a.Journal, now, journalN)
		if len(parts) == 0 && journal == "" {
			continue
		}
		if len(parts) == 0 {
			parts = append(parts, "recent work")
		}
		fmt.Fprintf(&b, "- %s: %s\n", aliasOr(a.Alias), strings.Join(parts, "; "))
		b.WriteString(journal)
		n++
	}
	if len(notes) > 0 {
		start := max(0, len(notes)-ledgerNotes)
		b.WriteString("Notes from the user: ")
		for i, note := range notes[start:] {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(clipText(oneLine(note), ledgerNoteRunes))
		}
		b.WriteString("\n")
		n++
	}
	open := 0
	for _, p := range promises {
		if statusOr(p.Status) != PromiseOpen {
			continue
		}
		if open == 0 {
			b.WriteString("Open promises (kept by the supervisor; do not repeat unasked):\n")
		}
		open++
		fmt.Fprintf(&b, "- %q: %s", clipText(oneLine(p.Text), ledgerPromiseRunes), promiseCondition(p))
		if !p.CreatedAt.IsZero() {
			fmt.Fprintf(&b, ", made %s ago", ago(now.Sub(p.CreatedAt)))
		}
		if p.CarriedOver {
			b.WriteString(" (from an earlier run)")
		}
		b.WriteString("\n")
	}
	if open > 0 {
		n++
	}
	if n == 0 {
		b.WriteString("Nothing recorded yet.")
	}
	return strings.TrimRight(b.String(), "\n")
}

// joinSections concatenates sections with blank lines between them.
func joinSections(secs []PromptSection) string {
	parts := make([]string, 0, len(secs))
	for _, s := range secs {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, "\n\n")
}

// ago renders a duration for the ear: "40 s", "3 min", "2 h".
func ago(d time.Duration) string {
	switch {
	case d < 0:
		return "0 s"
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	}
}

// agoCoarse is ago in whole hours past the first hour, for the roster's
// "last dealt with" where minutes are noise.
func agoCoarse(d time.Duration) string {
	if d < time.Hour {
		return ago(d)
	}
	return fmt.Sprintf("%d h", int(d.Hours()))
}

func aliasOr(a string) string {
	if a == "" {
		return "an unnamed agent"
	}
	return a
}

func kindOr(k string) string {
	if k == "" {
		return "unknown kind"
	}
	return k
}

func stateOr(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
