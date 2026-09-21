package brain

import (
	"fmt"
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

// userSection renders USER.md (the CEO's profile: aliases, chattiness,
// timezone, and anything the CEO added by hand) with vars, which
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

// slug lowercases a heading into a section name ("The one rail" -> "rail").
func slug(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
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

// dynamicSections renders the roster, the ledger excerpt (with the open
// promises), the investigations and the time.
func dynamicSections(agents []AgentInfo, notes []string, promises []PromiseInfo, investigations []InvestigationInfo, now time.Time) []PromptSection {
	return []PromptSection{
		{Name: "roster", Source: SourceDynamic, Text: renderRoster(agents, now)},
		{Name: "ledger", Source: SourceDynamic, Text: renderLedger(agents, notes, promises, now)},
		{Name: "investigations", Source: SourceDynamic, Text: renderInvestigations(investigations, now)},
		{Name: "time", Source: SourceDynamic, Text: "## Now\n\n" + now.Format("Mon 2006-01-02 15:04:05 MST")},
	}
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
		b.WriteString("Running now (the answer reaches the CEO on its own when it lands; say how it is going, do not start it again):\n")
		for _, inv := range running {
			fmt.Fprintf(&b, "- %s (%s): %q, %s so far\n", inv.ID, aliasOr(inv.Agent), inv.Question, ago(now.Sub(inv.Started)))
		}
	}
	if len(done) > 0 {
		b.WriteString("Already looked into (follow_up_of resumes the same session for a deeper question):\n")
		for _, inv := range done {
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
		fmt.Fprintf(&b, "- %s (%s): %s", aliasOr(a.Alias), kindOr(a.Kind), stateOr(a.State))
		if !a.StateSince.IsZero() {
			fmt.Fprintf(&b, " for %s", ago(now.Sub(a.StateSince)))
		}
		if a.Task != "" {
			fmt.Fprintf(&b, "; task: %q", a.Task)
		}
		if !a.LastInteraction.IsZero() {
			fmt.Fprintf(&b, "; the CEO last dealt with it %s ago", ago(now.Sub(a.LastInteraction)))
		}
		if p := a.Pending; p != nil {
			fmt.Fprintf(&b, "; WAITING FOR AN ANSWER: %s", p.Question)
			if len(p.Options) > 0 {
				var opts []string
				for i, o := range p.Options {
					opts = append(opts, fmt.Sprintf("%d %q -> keys %v", i+1, o.Label, o.Keys))
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
// the open promises.
func renderLedger(agents []AgentInfo, notes []string, promises []PromiseInfo, now time.Time) string {
	var b strings.Builder
	b.WriteString("## Ledger\n\n")
	n := 0
	for _, a := range agents {
		var parts []string
		if a.LastTask != "" {
			parts = append(parts, "last task "+fmt.Sprintf("%q", a.LastTask))
		}
		if a.LastResult != "" {
			r := "last result: " + a.LastResult
			if a.LastDetail != "" {
				r += " (" + a.LastDetail + ")"
			}
			parts = append(parts, r)
		}
		if a.LastBlock != "" {
			parts = append(parts, "last question: "+a.LastBlock)
		}
		if len(parts) == 0 {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", aliasOr(a.Alias), strings.Join(parts, "; "))
		n++
	}
	if len(notes) > 0 {
		start := 0
		if len(notes) > 5 {
			start = len(notes) - 5
		}
		b.WriteString("Notes from the CEO: ")
		for i, note := range notes[start:] {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(note)
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
			b.WriteString("Open promises (the supervisor keeps them at the matching event; do not repeat them unasked):\n")
		}
		open++
		fmt.Fprintf(&b, "- %q: %s", p.Text, promiseCondition(p))
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
