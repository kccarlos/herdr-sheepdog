package brain

import (
	"fmt"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/prompts"
)

// section is one part of the system prompt; the stable ones come first
// so a provider's prefix cache applies (docs/roles.md, borrowed from
// OpenClaw).
type section struct {
	Name string
	Text string
}

// stableSections loads SOUL.md as the role section and splits RULES.md
// into one section per "## " heading (tools, the one rail, the answer
// contract).
func stableSections() ([]section, error) {
	soul, err := prompts.SVP.ReadFile(prompts.SVPSoul)
	if err != nil {
		return nil, fmt.Errorf("brain: %w", err)
	}
	rules, err := prompts.SVP.ReadFile(prompts.SVPRules)
	if err != nil {
		return nil, fmt.Errorf("brain: %w", err)
	}
	out := []section{{Name: "role", Text: strings.TrimSpace(string(soul))}}
	for _, chunk := range strings.Split("\n"+string(rules), "\n## ") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		name, body, _ := strings.Cut(chunk, "\n")
		out = append(out, section{Name: slug(name), Text: "## " + strings.TrimSpace(name) + "\n\n" + strings.TrimSpace(body)})
	}
	return out, nil
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

// dynamicSections renders the roster, the ledger excerpt and the time.
func dynamicSections(agents []AgentInfo, notes []string, now time.Time) []section {
	return []section{
		{Name: "roster", Text: renderRoster(agents, now)},
		{Name: "ledger", Text: renderLedger(agents, notes)},
		{Name: "time", Text: "## Now\n\n" + now.Format("Mon 2006-01-02 15:04:05 MST")},
	}
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

// renderLedger lists what each agent last did and asked, and the notes.
func renderLedger(agents []AgentInfo, notes []string) string {
	var b strings.Builder
	b.WriteString("## Ledger\n\n")
	n := 0
	for _, a := range agents {
		var parts []string
		if a.LastTask != "" {
			parts = append(parts, "last task "+fmt.Sprintf("%q", a.LastTask))
		}
		if a.LastResult != "" {
			parts = append(parts, "last result: "+a.LastResult)
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
	if n == 0 {
		b.WriteString("Nothing recorded yet.")
	}
	return strings.TrimRight(b.String(), "\n")
}

// joinSections concatenates sections with blank lines between them.
func joinSections(secs []section) string {
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
