package brain

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/genai"
)

// ToolShow puts something on the user's phone (or /voice page) screen:
// display only, no side effect on any worker, and no way to answer an
// approval (the one rail stays in send_keys). Declared in user turns only.
const ToolShow = "show"

// show's kinds.
const (
	// ShowCard is a card the orchestrator writes: a title and markdown.
	ShowCard = "card"
	// ShowAgent opens an agent's window (its live terminal).
	ShowAgent = "agent"
	// ShowExcerpt is the last lines of an agent's screen, read by the
	// daemon itself (exact, and costs the model nothing).
	ShowExcerpt = "excerpt"
	// ShowApproval points at an agent's open approval prompt, built by
	// the daemon from the pending action, never from the model's words.
	ShowApproval = "approval"
)

// ShowKinds are show's kinds, for the declaration's enum.
var ShowKinds = []string{ShowCard, ShowAgent, ShowExcerpt, ShowApproval}

// Limits on a card (constants, not config keys).
const (
	// ShowTitleRunes caps a card's title.
	ShowTitleRunes = 80
	// ShowMarkdownBytes caps a card's markdown: a longer one is cut at a
	// line boundary and marked, rather than refused (a refusal costs a
	// step).
	ShowMarkdownBytes = 6000
	// ShowExcerptLines is an excerpt's default line count; ShowExcerptMax
	// its cap.
	ShowExcerptLines = 40
	ShowExcerptMax   = 120
	// toolTimeoutShow bounds a show call (an excerpt reads the pane).
	toolTimeoutShow = 4 * time.Second
)

// ErrNotVisible is a DisplayHost's answer when no screen is visible.
var ErrNotVisible = errors.New("the user's screen is not visible")

// CardRequest is one show call as the Host gets it: the kind, the
// target agent's pane (agent, excerpt, approval), the title and the
// markdown (card), the line count (excerpt) and the turn.
type CardRequest struct {
	Kind     string
	PaneID   string
	Alias    string
	Title    string
	Markdown string
	Lines    int
	Turn     int
}

// CardResult is what the Host showed: the card's id and title.
type CardResult struct {
	ID    string
	Title string
}

// DisplayHost is a Host that can put a card on the user's screen. Show
// returns ErrNotVisible (wrapped or not) when no screen is visible, and
// another error when the card cannot be built (no open approval on
// that agent). ScreenVisible is what the state block tells the model.
// Optional, so test hosts need not implement it.
type DisplayHost interface {
	ScreenVisible() bool
	Show(ctx context.Context, req CardRequest) (CardResult, error)
}

// showDeclaration is show's function declaration.
func showDeclaration() *genai.FunctionDeclaration {
	str := func(desc string) *genai.Schema { return &genai.Schema{Type: genai.TypeString, Description: desc} }
	return &genai.FunctionDeclaration{
		Name: ToolShow,
		Description: "Put something on the user's phone screen instead of saying it, when the screen is visible (see Now). " +
			"Use it only when reading aloud would be long or hard to follow: a list of more than three items, a table, steps, a diff, exact text, or long detail the user asked for. Never for a short answer that fits one or two sentences. " +
			"kind card: your own title and markdown (tables, lists, code fences; a diff from run_cli as a ```diff fence). kind agent: opens that agent's live window. kind excerpt: the last lines of that agent's screen, exactly as they are. kind approval: shows that agent's open approval prompt (it does not answer it). " +
			"Display only: it changes nothing. If the screen is not visible the result says so: then say it instead. When it is shown, say one short sentence that it is on the screen plus the gist, and do not read the card aloud.",
		Parameters: &genai.Schema{Type: genai.TypeObject, Required: []string{"kind"}, Properties: map[string]*genai.Schema{
			"kind":     {Type: genai.TypeString, Enum: ShowKinds, Description: "card, agent, excerpt or approval"},
			"title":    str("card: a short title (a few words)"),
			"markdown": str(fmt.Sprintf("card: the content in markdown, at most about %d characters", ShowMarkdownBytes)),
			"agent":    str("agent, excerpt, approval: the agent's alias"),
			"lines":    {Type: genai.TypeInteger, Description: fmt.Sprintf("excerpt only: how many lines from the bottom (default %d)", ShowExcerptLines)},
		}},
	}
}

// screenLine is the state block's line about the screen ("## Now").
func screenLine(visible bool) string {
	if visible {
		return "Screen: the user's screen is visible; show can put a list, table or long detail on it."
	}
	return "Screen: not visible (app in the background, locked or not open); show is refused, so speak."
}

// show runs one show call against the DisplayHost.
func (b *Brain) show(ctx context.Context, t *turn, arg func(string) string, args map[string]any) toolOutcome {
	if t.req.Trigger != TriggerCEO || t.req.Kind.ReadOnly() {
		// Declared in user turns only; refused here whatever the model
		// names, so an event can never pop a card up unasked.
		return toolOutcome{err: errors.New("show is only permitted in a turn the user's own words triggered")}
	}
	dh, ok := b.host.(DisplayHost)
	if !ok {
		return toolOutcome{result: notShown("there is no screen to show it on"), summary: "show: no screen"}
	}
	kind := strings.ToLower(arg("kind"))
	req := CardRequest{Kind: kind, Turn: t.n}
	var out toolOutcome
	switch kind {
	case ShowCard:
		req.Title = clipRunes(strings.Join(strings.Fields(arg("title")), " "), ShowTitleRunes)
		md, _ := args["markdown"].(string)
		req.Markdown = clipMarkdown(strings.TrimSpace(md), ShowMarkdownBytes)
		if req.Markdown == "" {
			return toolOutcome{err: errors.New("show card needs markdown")}
		}
		if req.Title == "" {
			req.Title = clipRunes(firstLineOf(req.Markdown), ShowTitleRunes)
		}
	case ShowAgent, ShowExcerpt, ShowApproval:
		row, ok := b.host.Resolve(arg("agent"))
		if !ok {
			return toolOutcome{err: fmt.Errorf("no agent called %q; the agents are %s", arg("agent"), b.aliases())}
		}
		out.agent, out.pane = row.Alias, row.PaneID
		req.PaneID, req.Alias = row.PaneID, row.Alias
		if kind == ShowExcerpt {
			req.Lines = ShowExcerptLines
			if n, ok := args["lines"].(float64); ok && n > 0 {
				req.Lines = min(int(n), ShowExcerptMax)
			}
		}
		if kind == ShowApproval && row.Pending == nil {
			out.err = fmt.Errorf("%s has no open approval prompt to show", aliasOr(row.Alias))
			return out
		}
	default:
		return toolOutcome{err: fmt.Errorf("show kind %q: want one of %s", kind, strings.Join(ShowKinds, ", "))}
	}
	if req.Kind == ShowCard && slices.Contains(t.shown, req.Title) {
		// A runaway guard (smoke run 2026-10-20: the same card shown
		// twice in one turn): the screen already has it.
		out.result = fmt.Sprintf("A card titled %q is already on the screen from this turn; it was not shown again. Answer now.", req.Title)
		return out
	}
	res, err := dh.Show(ctx, req)
	switch {
	case errors.Is(err, ErrNotVisible):
		out.result = notShown("the user's screen is not visible (the app is in the background, locked, or not open)")
		out.summary = "show: not visible"
		return out
	case err != nil:
		out.err = err
		return out
	}
	out.shown = res.Title
	out.result = fmt.Sprintf("Shown on the user's screen: %q (%s). Now say one short sentence that it is on the screen, plus the gist in a few words. Do not read the card aloud, and leave detail empty.", res.Title, kind)
	out.summary = fmt.Sprintf("show %s %q", kind, res.Title)
	return out
}

// notShown is show's result when nothing was shown.
func notShown(why string) string {
	return "Not shown: " + why + ". Nothing is on the screen, so say it instead, as briefly as the content allows."
}

// clipMarkdown cuts md to max bytes at a line boundary (else a rune
// boundary) and marks the cut.
func clipMarkdown(md string, max int) string {
	if len(md) <= max {
		return md
	}
	const mark = "\n\n…(cut)"
	cut := md[:max-len(mark)]
	if i := strings.LastIndexByte(cut, '\n'); i > len(cut)/2 {
		cut = cut[:i]
	}
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimRight(cut, " \n") + mark
}

// firstLineOf is md's first non-empty line without markdown markers.
func firstLineOf(md string) string {
	for _, l := range strings.Split(md, "\n") {
		l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#*->|` "))
		if l != "" {
			return l
		}
	}
	return "Card"
}

// redactShowArgs is a show call's args for the event log: the markdown
// as its length only (the log carries metadata; the card itself is in
// the Host's card store).
func redactShowArgs(args map[string]any) map[string]any {
	md, ok := args["markdown"].(string)
	if !ok {
		return args
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	out["markdown"] = fmt.Sprintf("(%d bytes)", len(md))
	return out
}
