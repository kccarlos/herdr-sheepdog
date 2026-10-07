package brain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
)

// The Context tab's capture of every model call (docs/architecture.md
// "Context capture"): the provider-native request, its raw text as the
// model receives it, and a section map over that text, recorded in the
// BrainLog after the call returns.

// geminiRequest is the Gemini body as the daemon hands it to the SDK:
// the SDK owns the wire bytes, this is everything they are made from.
type geminiRequest struct {
	Model    string                       `json:"model"`
	Contents []*genai.Content             `json:"contents"`
	Config   *genai.GenerateContentConfig `json:"config"`
}

// requestRenderer is a Model that builds its own provider request from
// the window (OpenAICompatModel): the capture asks it for the exact body.
type requestRenderer interface {
	requestFor(model string, msgs []Message, cfg *genai.GenerateContentConfig) (chatRequest, []origin)
}

// The body formats.
const (
	FormatGemini = "gemini"
	FormatOpenAI = "openai"
)

// captureStep records one model call of turn t in the request log.
func (b *Brain) captureStep(t *turn, step int, forced bool, secs []PromptSection, addition string, msgs []Message, cfg *genai.GenerateContentConfig, at time.Time, dur time.Duration, resp *genai.GenerateContentResponse, callErr error) {
	if b.requests == nil {
		return
	}
	snap, format := renderRequest(t.impl, t.provider, t.model, secs, addition, msgs, cfg, t.n)
	b.mu.Lock()
	segment := b.curRunID
	b.mu.Unlock()
	st := ctxcap.BrainStep{
		Run: b.cfg.RunID, Segment: segment, Turn: t.n, TurnStep: step, Kind: string(t.req.Kind), Trigger: string(t.req.Trigger), Agent: t.req.Agent,
		Provider: t.provider, Model: t.model, TS: at, Format: format, Forced: forced, WindowTokens: b.cfg.WindowTokens,
		Tokens: snap.Tokens(), DurMS: dur.Milliseconds(), Snapshot: snap,
	}
	if callErr != nil {
		st.Error = callErr.Error()
	} else if resp != nil && resp.UsageMetadata != nil {
		u := resp.UsageMetadata
		st.Usage = &ctxcap.Usage{PromptTokens: int(u.PromptTokenCount), CompletionTokens: int(u.CandidatesTokenCount), ReasoningTokens: int(u.ThoughtsTokenCount), CachedTokens: int(u.CachedContentTokenCount)}
	}
	b.requests.Record(st)
}

// renderRequest builds the body impl sends for this window and maps it:
// the exact chat completions request for an OpenAI-compatible model,
// else the Gemini contents and config. turn is the turn the call belongs
// to (its first request is the current one).
func renderRequest(impl Model, provider, model string, secs []PromptSection, addition string, msgs []Message, cfg *genai.GenerateContentConfig, turn int) (ctxcap.Snapshot, string) {
	info := annotate(msgs, turn)
	if rr, ok := impl.(requestRenderer); ok {
		req, tr := rr.requestFor(model, msgs, cfg)
		return renderOpenAI(req, tr, secs, addition, info), FormatOpenAI
	}
	var tr []origin
	contents := toContentsTraced(msgs, provider, model, &tr)
	return renderGemini(geminiRequest{Model: model, Contents: contents, Config: cfg}, tr, secs, addition, info), FormatGemini
}

// annotate is each window message's section: its key, label, colour,
// links, and whether it is a pruned stub or the current request.
//
// Links: a request carries the one it was made with (Message.Link: the
// voice's tool call, or the worker event an announce turn is about);
// the turn's tool calls, results, screens, promises and reply take their
// turn's; a user utterance ingested outside a turn takes the call
// link of the next request, the tool call that relayed it; pushes and
// worker events carry their own.
func annotate(msgs []Message, turn int) []ctxcap.Section {
	turnLink := map[int]string{}
	firstReq := map[int]int{}
	for i, m := range msgs {
		if m.Kind != KindRequest || m.Turn == 0 {
			continue
		}
		if _, ok := firstReq[m.Turn]; !ok {
			firstReq[m.Turn] = i
			turnLink[m.Turn] = m.Link
		}
	}
	out := make([]ctxcap.Section, len(msgs))
	var pending []int // utterances waiting for the request that relayed them
	for i, m := range msgs {
		sec := ctxcap.Section{Key: fmt.Sprintf("seq:%d", m.Seq), Seq: m.Seq, Group: "messages"}
		if m.Seq == 0 {
			sec.Key = fmt.Sprintf("msg:%d", i)
		}
		sec.Label, sec.Class = kindLabel(m.Kind)
		if m.Kind == KindState {
			// One key whatever its position, so a step's diff shows the
			// state as changed, not removed and re-added.
			sec.Key = stateKey
		}
		link := m.Link
		switch m.Kind {
		case KindRequest:
			if m.Turn > 0 && firstReq[m.Turn] == i {
				if m.Turn == turn {
					sec.Label, sec.Current = "current request", true
				}
				if strings.HasPrefix(link, "call:") {
					for _, p := range pending {
						out[p].Links = []string{link}
					}
				}
				pending = nil
			} else if m.Turn > 0 {
				sec.Label, link = "daemon nudge", ""
			}
		case KindCEOUtterance:
			if m.Turn == 0 {
				pending = append(pending, i)
			}
		case KindReply, KindToolCall, KindToolResult, KindEventScreen, KindPromise:
			if link == "" && m.Turn > 0 {
				link = turnLink[m.Turn]
			}
		}
		if link != "" {
			sec.Links = []string{link}
		}
		if prunable(m) && m.Content == stubFor(m) {
			sec.Pruned, sec.Class, sec.Label = true, ctxcap.ClassPruned, "pruned stub ("+strings.TrimPrefix(sec.Label, "orchestrator ")+")"
		}
		out[i] = sec
	}
	return out
}

// kindLabel is a message kind's label and colour on the Context tab.
func kindLabel(k MessageKind) (string, string) {
	switch k {
	case KindCEOUtterance:
		return "user utterance", ctxcap.ClassShepherd
	case KindAssistantSaid:
		return "voice said", ctxcap.ClassVoice
	case KindPush:
		return "push", ctxcap.ClassPush
	case KindWorkerEvent:
		return "worker event", ctxcap.ClassWorker
	case KindNote:
		return "note", ctxcap.ClassShepherd
	case KindRequest:
		return "request", ctxcap.ClassRequest
	case KindToolCall:
		return "orchestrator tool call", ctxcap.ClassToolCall
	case KindToolResult:
		return "orchestrator tool result", ctxcap.ClassToolResult
	case KindReply:
		return "orchestrator reply", ctxcap.ClassBrain
	case KindCompaction:
		return "compaction summary", ctxcap.ClassCompaction
	case KindPromise:
		return "promise", ctxcap.ClassBrain
	case KindEventScreen:
		return "event screen", ctxcap.ClassWorker
	case KindSystem:
		return "daemon note", ctxcap.ClassSystem
	case KindState:
		return "state (roster, ledger, time)", ctxcap.ClassSystem
	default:
		return string(k), ctxcap.ClassMeta
	}
}

// promptLabel names a system prompt section for the gutter.
func promptLabel(name string) string {
	switch name {
	case "role":
		return "role (SOUL.md)"
	case "user":
		return "USER.md"
	default:
		return name
	}
}

// addSystem renders the system prompt, one section per prompt section,
// then the engine's addition.
func addSystem(bd *ctxcap.Builder, header string, secs []PromptSection, addition, path string) {
	first := true
	add := func(sec ctxcap.Section, text string) {
		if first {
			text, first = header+text, false
		}
		bd.Add(sec, text)
	}
	for _, s := range secs {
		add(ctxcap.Section{Key: "prompt:" + s.Name, Label: promptLabel(s.Name), Class: ctxcap.ClassSystem, Group: "system", Path: path, Role: "system"}, s.Text+"\n\n")
	}
	if addition != "" {
		add(ctxcap.Section{Key: "prompt:addition", Label: "engine addition", Class: ctxcap.ClassSystem, Group: "system", Path: path, Role: "system"}, addition+"\n\n")
	}
}

// toolLine is one declaration as a line: name(params) — description.
func toolLine(name, desc string, props []string, required map[string]bool) string {
	params := make([]string, 0, len(props))
	for _, p := range props {
		if !required[p] {
			p += "?"
		}
		params = append(params, p)
	}
	line := name + "(" + strings.Join(params, ", ") + ")"
	if desc != "" {
		line += " — " + desc
	}
	return line + "\n"
}

// part is one rendered piece of a content or message.
type part struct {
	text string // with its trailing newline
	path string
	// raw: the text is the piece's own text verbatim (a merged user
	// text), so the origins' byte ranges apply to it.
	raw bool
}

// addItem renders one content / message: header, then its parts, each
// cut into the sections of the messages that made it (origins), each of
// those cut again around worker output. The blank line after the item
// goes to its last section.
func addItem(bd *ctxcap.Builder, header, itemPath, role string, parts []part, os []origin, info []ctxcap.Section) {
	pending := header
	emit := func(base ctxcap.Section, text string) {
		text, pending = pending+text, ""
		if base.Key == stateKey {
			for _, piece := range splitState(base, text) {
				bd.Add(piece.sec, piece.text)
			}
			return
		}
		for _, piece := range splitWorker(base, text) {
			bd.Add(piece.sec, piece.text)
		}
	}
	whole := -1
	for _, o := range os {
		if o.part == -1 {
			whole = o.msg
			break
		}
	}
	if whole >= 0 {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.text)
		}
		base := info[whole]
		base.Path, base.Role = itemPath, role
		emit(base, b.String())
	} else {
		for j, p := range parts {
			var mine []origin
			for _, o := range os {
				if o.part == j {
					mine = append(mine, o)
				}
			}
			sort.Slice(mine, func(a, b int) bool { return mine[a].start < mine[b].start })
			switch {
			case len(mine) == 0:
				emit(ctxcap.Section{Key: p.path, Label: "unmapped", Class: ctxcap.ClassMeta, Group: "messages", Path: p.path, Role: role}, p.text)
			case len(mine) == 1 || !p.raw:
				base := info[mine[0].msg]
				base.Path, base.Role = p.path, role
				emit(base, p.text)
			default:
				for k, o := range mine {
					start, end := o.start, len(p.text)
					if k == 0 {
						start = 0
					}
					if k+1 < len(mine) {
						end = mine[k+1].start
					}
					if start < 0 || start > end || end > len(p.text) {
						continue
					}
					base := info[o.msg]
					base.Path, base.Role = p.path, role
					emit(base, p.text[start:end])
				}
			}
		}
	}
	if pending != "" {
		// An item with nothing rendered (a signature-only part): the
		// header alone.
		bd.Add(ctxcap.Section{Key: itemPath, Label: "empty " + role + " content", Class: ctxcap.ClassMeta, Group: "messages", Path: itemPath, Role: role}, pending)
	}
	bd.Extend("\n")
}

type piece struct {
	sec  ctxcap.Section
	text string
}

// The provenance delimiters' fixed prefixes (tools.go's wrapWorker).
var (
	beginWorkerPrefix = beginWorkerOutput[:strings.Index(beginWorkerOutput, " ")]
)

// splitWorker cuts text around the provenance delimiters: worker output
// inside them is its own piece, coloured untrusted.
func splitWorker(base ctxcap.Section, text string) []piece {
	var out []piece
	n := 0
	add := func(untrusted bool, s string) {
		if s == "" {
			return
		}
		if !untrusted && len(out) > 0 && strings.TrimSpace(s) == "" {
			// Only the line break after the delimiters: it stays with them.
			out[len(out)-1].text += s
			return
		}
		sec := base
		if n > 0 {
			sec.Key = fmt.Sprintf("%s#%d", base.Key, n)
		}
		if untrusted && !base.Pruned {
			sec.Label, sec.Class = "worker output (untrusted)", ctxcap.ClassUntrusted
		}
		n++
		out = append(out, piece{sec, s})
	}
	rest := text
	for {
		i := strings.Index(rest, beginWorkerPrefix)
		if i < 0 {
			add(false, rest)
			return out
		}
		add(false, rest[:i])
		rest = rest[i:]
		j := strings.Index(rest, endWorkerPrefix)
		if j >= 0 {
			// The end marker runs to its closing ">>>" (it carries a nonce).
			if k := strings.Index(rest[j:], ">>>"); k >= 0 {
				j += k + len(">>>")
			} else {
				j = -1
			}
		}
		if j < 0 {
			add(true, rest)
			return out
		}
		add(true, rest[:j])
		rest = rest[j:]
	}
}

// stateKey is the section key of the state block (KindState).
const stateKey = "state"

// stateLabels names the state block's sections by heading, with the
// labels they had as system prompt sections.
var stateLabels = map[string]string{"## Agents": "roster", "## Ledger": "ledger", "## Investigations": "investigations", "## Now": "time"}

// splitState cuts the state block at its "## " headings, one section
// each (state:roster, state:ledger, …); the text before the first
// heading stays with the first.
func splitState(base ctxcap.Section, text string) []piece {
	var cuts []int
	for i := 0; i < len(text); {
		j := strings.Index(text[i:], "\n## ")
		if j < 0 {
			break
		}
		cuts = append(cuts, i+j+1)
		i += j + 1
	}
	if len(cuts) == 0 {
		return []piece{{base, text}}
	}
	var out []piece
	for k, at := range cuts {
		start, end := at, len(text)
		if k == 0 {
			start = 0
		}
		if k+1 < len(cuts) {
			end = cuts[k+1]
		}
		heading, _, _ := strings.Cut(text[at:], "\n")
		sec := base
		name := strings.ToLower(strings.TrimPrefix(heading, "## "))
		sec.Label = name
		if l, ok := stateLabels[heading]; ok {
			sec.Label, name = l, l
		}
		sec.Key = stateKey + ":" + name
		out = append(out, piece{sec, text[start:end]})
	}
	return out
}

// originsByItem groups origins by item.
func originsByItem(tr []origin) map[int][]origin {
	m := map[int][]origin{}
	for _, o := range tr {
		m[o.item] = append(m[o.item], o)
	}
	return m
}

// compactJSON renders call arguments on one line ({} for none).
func compactJSON(v any) string {
	if m, ok := v.(map[string]any); ok && len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// renderGemini maps a Gemini body.
func renderGemini(req geminiRequest, tr []origin, secs []PromptSection, addition string, info []ctxcap.Section) ctxcap.Snapshot {
	var bd ctxcap.Builder
	meta := "### request · gemini · model=" + req.Model
	if c := req.Config; c != nil {
		if c.ThinkingConfig != nil && c.ThinkingConfig.ThinkingLevel != "" {
			meta += " · thinking=" + strings.ToLower(string(c.ThinkingConfig.ThinkingLevel))
		}
		if c.ToolConfig != nil && c.ToolConfig.FunctionCallingConfig != nil && c.ToolConfig.FunctionCallingConfig.Mode != "" {
			meta += " · function_calling=" + strings.ToLower(string(c.ToolConfig.FunctionCallingConfig.Mode))
		}
	}
	bd.Add(ctxcap.Section{Key: "meta", Label: "request settings", Class: ctxcap.ClassMeta, Group: "system", Path: "config"}, meta+"\n\n")
	addSystem(&bd, "### system instruction\n", secs, addition, "config.systemInstruction")
	if req.Config != nil {
		first := true
		for ti, t := range req.Config.Tools {
			if t == nil {
				continue
			}
			for di, d := range t.FunctionDeclarations {
				if d == nil {
					continue
				}
				var props []string
				required := map[string]bool{}
				if d.Parameters != nil {
					for name := range d.Parameters.Properties {
						props = append(props, name)
					}
					for _, r := range d.Parameters.Required {
						required[r] = true
					}
				}
				sort.Strings(props)
				text := toolLine(d.Name, d.Description, props, required)
				if first {
					text, first = "### tools\n"+text, false
				}
				bd.Add(ctxcap.Section{Key: "tool:" + d.Name, Label: "tool " + d.Name, Class: ctxcap.ClassTools, Group: "tools", Path: fmt.Sprintf("config.tools[%d].functionDeclarations[%d]", ti, di)}, text)
			}
		}
		if !first {
			bd.Extend("\n")
		}
	}
	by := originsByItem(tr)
	for i, c := range req.Contents {
		if c == nil {
			continue
		}
		role := c.Role
		if role == "" {
			role = genai.RoleUser
		}
		path := fmt.Sprintf("contents[%d]", i)
		var parts []part
		for j, p := range c.Parts {
			if p == nil {
				continue
			}
			pp := part{path: fmt.Sprintf("%s.parts[%d]", path, j)}
			switch {
			case p.FunctionCall != nil:
				pp.text = "→ " + p.FunctionCall.Name + "(" + compactJSON(p.FunctionCall.Args) + ")\n"
			case p.FunctionResponse != nil:
				pp.text = "← " + p.FunctionResponse.Name + ": " + responseText(p.FunctionResponse.Response) + "\n"
			case p.Text != "" && p.Thought:
				pp.text = "(thought) " + p.Text + "\n"
			case p.Text != "":
				pp.text, pp.raw = p.Text+"\n", true
			}
			parts = append(parts, pp)
		}
		addItem(&bd, "### "+role+"\n", path, role, parts, by[i], info)
	}
	body, _ := json.Marshal(req)
	return bd.Snapshot(body)
}

// responseText is a function response as the model reads it: the output
// text, or the error.
func responseText(resp map[string]any) string {
	if s, ok := resp["output"].(string); ok && len(resp) == 1 {
		return s
	}
	if s, ok := resp["error"].(string); ok && len(resp) == 1 {
		return "error: " + s
	}
	return compactJSON(resp)
}

// renderOpenAI maps an OpenAI-compatible chat completions body.
func renderOpenAI(req chatRequest, tr []origin, secs []PromptSection, addition string, info []ctxcap.Section) ctxcap.Snapshot {
	var bd ctxcap.Builder
	meta := "### request · openai-compatible · model=" + req.Model
	if req.ToolChoice != "" {
		meta += " · tool_choice=" + req.ToolChoice
	}
	if req.Thinking != nil {
		meta += " · thinking=" + req.Thinking.Type
	}
	if req.ReasoningEffort != "" {
		meta += " · reasoning_effort=" + req.ReasoningEffort
	}
	bd.Add(ctxcap.Section{Key: "meta", Label: "request settings", Class: ctxcap.ClassMeta, Group: "system", Path: "model"}, meta+"\n\n")
	start := 0
	if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
		addSystem(&bd, "### system\n", secs, addition, "messages[0]")
		start = 1
	}
	for ti, t := range req.Tools {
		var props []string
		required := map[string]bool{}
		if p, ok := t.Function.Parameters["properties"].(map[string]any); ok {
			for name := range p {
				props = append(props, name)
			}
		}
		if r, ok := t.Function.Parameters["required"].([]string); ok {
			for _, name := range r {
				required[name] = true
			}
		}
		sort.Strings(props)
		text := toolLine(t.Function.Name, t.Function.Description, props, required)
		if ti == 0 {
			text = "### tools\n" + text
		}
		bd.Add(ctxcap.Section{Key: "tool:" + t.Function.Name, Label: "tool " + t.Function.Name, Class: ctxcap.ClassTools, Group: "tools", Path: fmt.Sprintf("tools[%d]", ti)}, text)
	}
	if len(req.Tools) > 0 {
		bd.Extend("\n")
	}
	by := originsByItem(tr)
	for i := start; i < len(req.Messages); i++ {
		m := req.Messages[i]
		path := fmt.Sprintf("messages[%d]", i)
		var parts []part
		switch {
		case m.Role == "tool":
			parts = []part{{text: "← " + m.ToolCallID + ": " + m.Content + "\n", path: path}}
		case len(m.ToolCalls) > 0:
			for j, c := range m.ToolCalls {
				text := "→ " + c.Function.Name + "(" + c.Function.Arguments + ")\n"
				if j == 0 {
					if m.ReasoningContent != "" {
						text = "(reasoning_content) " + m.ReasoningContent + "\n" + text
					}
					if m.Content != "" {
						text = m.Content + "\n" + text
					}
				}
				parts = append(parts, part{text: text, path: fmt.Sprintf("%s.tool_calls[%d]", path, j)})
			}
		default:
			text := m.Content + "\n"
			if m.ReasoningContent != "" {
				text = "(reasoning_content) " + m.ReasoningContent + "\n" + text
			}
			parts = []part{{text: text, path: path, raw: m.ReasoningContent == ""}}
		}
		addItem(&bd, "### "+m.Role+"\n", path, m.Role, parts, by[i], info)
	}
	body, _ := json.Marshal(req)
	return bd.Snapshot(body)
}
