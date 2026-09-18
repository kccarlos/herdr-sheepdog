// Package braintest holds test doubles for internal/brain shared by the
// brain's own tests, the supervisor scenarios and the brain-test command:
// a scripted Model that returns canned responses in order and records what
// it was asked, and an in-memory Host.
package braintest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
)

// Call is one recorded Generate call.
type Call struct {
	Model    string
	Contents []*genai.Content
	Config   *genai.GenerateContentConfig
}

// Text returns every text part of the contents joined, for assertions on
// what the model saw.
func (c Call) Text() string {
	var b strings.Builder
	for _, content := range c.Contents {
		for _, p := range content.Parts {
			if p.Text != "" {
				b.WriteString(p.Text)
				b.WriteString("\n")
			}
			if p.FunctionResponse != nil {
				for k, v := range p.FunctionResponse.Response {
					fmt.Fprintf(&b, "%s: %v\n", k, v)
				}
			}
		}
	}
	return b.String()
}

// System returns the system instruction text.
func (c Call) System() string {
	if c.Config == nil || c.Config.SystemInstruction == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range c.Config.SystemInstruction.Parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// Step is one scripted response: canned text, function calls, an error,
// or a hook that computes the response from the call.
type Step struct {
	Text  string
	Calls []*genai.FunctionCall
	Err   error
	// Fn, when set, replaces the fields above.
	Fn func(call Call) (*genai.GenerateContentResponse, error)
	// Block, when set, is waited on before responding (serialisation
	// tests).
	Block <-chan struct{}
	// Usage is reported as prompt / candidate tokens.
	Usage [2]int32
}

// Model is a scripted brain.Model. Responses are consumed in order; when
// the script runs out, Default answers (a plain JSON reply when nil).
type Model struct {
	mu      sync.Mutex
	script  []Step
	calls   []Call
	Default *Step
}

// NewModel returns a Model with the given script.
func NewModel(steps ...Step) *Model {
	return &Model{script: steps}
}

// Add appends steps to the script.
func (m *Model) Add(steps ...Step) {
	m.mu.Lock()
	m.script = append(m.script, steps...)
	m.mu.Unlock()
}

// Calls returns every recorded call.
func (m *Model) Calls() []Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Call(nil), m.calls...)
}

// Generate implements brain.Model.
func (m *Model) Generate(ctx context.Context, model string, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	call := Call{Model: model, Contents: contents, Config: cfg}
	m.mu.Lock()
	m.calls = append(m.calls, call)
	var step Step
	if len(m.script) > 0 {
		step, m.script = m.script[0], m.script[1:]
	} else if m.Default != nil {
		step = *m.Default
	} else {
		step = Step{Text: Reply("I do not know.", "")}
	}
	m.mu.Unlock()
	if step.Block != nil {
		select {
		case <-step.Block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if step.Fn != nil {
		return step.Fn(call)
	}
	if step.Err != nil {
		return nil, step.Err
	}
	return Response(step), nil
}

// Response builds the GenerateContentResponse for a step.
func Response(step Step) *genai.GenerateContentResponse {
	var parts []*genai.Part
	if step.Text != "" {
		parts = append(parts, &genai.Part{Text: step.Text})
	}
	for i, fc := range step.Calls {
		p := &genai.Part{FunctionCall: fc}
		if i == 0 {
			p.ThoughtSignature = []byte("sig-" + fc.Name)
		}
		parts = append(parts, p)
	}
	return &genai.GenerateContentResponse{
		Candidates:    []*genai.Candidate{{Content: &genai.Content{Role: genai.RoleModel, Parts: parts}}},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: step.Usage[0], CandidatesTokenCount: step.Usage[1]},
	}
}

// Reply renders an answer-contract JSON object.
func Reply(spoken, detail string) string {
	b, _ := json.Marshal(brain.Reply{Spoken: spoken, Detail: detail})
	return string(b)
}

// FC builds a function call.
func FC(name string, args map[string]any) *genai.FunctionCall {
	if args == nil {
		args = map[string]any{}
	}
	return &genai.FunctionCall{Name: name, Args: args}
}

// Host is an in-memory brain.Host. Screens, CLI answers and the roster
// are set by tests; every action is recorded.
type Host struct {
	mu      sync.Mutex
	agents  []brain.AgentInfo
	notes   []string
	screens map[string]string
	CLI     func(paneID, question string) (string, error)
	// Prompts, Keys and Interrupts record the actions (pane -> payload).
	Prompts    [][2]string
	Keys       [][2]string
	Interrupts []string
	// PromptErr / KeysErr / InterruptErr fail the matching action.
	PromptErr, KeysErr, InterruptErr error
	// ReadErr fails ReadScreen.
	ReadErr error
	// ReadDelay hooks a slow screen read.
	ReadHook func(ctx context.Context) error
}

// NewHost returns an empty Host.
func NewHost() *Host {
	return &Host{screens: map[string]string{}}
}

// SetAgent adds or replaces a roster row.
func (h *Host) SetAgent(a brain.AgentInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.agents {
		if h.agents[i].PaneID == a.PaneID {
			h.agents[i] = a
			return
		}
	}
	h.agents = append(h.agents, a)
}

// SetScreen sets what ReadScreen returns for a pane.
func (h *Host) SetScreen(paneID, screen string) {
	h.mu.Lock()
	h.screens[paneID] = screen
	h.mu.Unlock()
}

// SetPending sets (or clears, with nil) the pending prompt of a pane.
func (h *Host) SetPending(paneID string, p *brain.PendingPrompt) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.agents {
		if h.agents[i].PaneID == paneID {
			h.agents[i].Pending = p
		}
	}
}

// AddNote appends a note.
func (h *Host) AddNote(text string) {
	h.mu.Lock()
	h.notes = append(h.notes, text)
	h.mu.Unlock()
}

// Agents implements brain.Host.
func (h *Host) Agents() []brain.AgentInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]brain.AgentInfo(nil), h.agents...)
}

// Resolve implements brain.Host (exact alias, then unambiguous prefix).
func (h *Host) Resolve(name string) (brain.AgentInfo, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, a := range h.agents {
		if strings.ToLower(a.Alias) == name {
			return a, true
		}
	}
	var hit brain.AgentInfo
	hits := 0
	for _, a := range h.agents {
		if name != "" && strings.HasPrefix(strings.ToLower(a.Alias), name) {
			hit, hits = a, hits+1
		}
	}
	if hits == 1 {
		return hit, true
	}
	return brain.AgentInfo{}, false
}

// Notes implements brain.Host.
func (h *Host) Notes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.notes...)
}

// ReadScreen implements brain.Host.
func (h *Host) ReadScreen(ctx context.Context, paneID string, lines int) (string, error) {
	h.mu.Lock()
	hook, err, screen, ok := h.ReadHook, h.ReadErr, h.screens[paneID], true
	h.mu.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return "", err
		}
	}
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no screen for %s", paneID)
	}
	return screen, nil
}

// RunCLI implements brain.Host.
func (h *Host) RunCLI(_ context.Context, paneID, question string) (string, error) {
	if h.CLI == nil {
		return "", brain.ErrNoCLI
	}
	return h.CLI(paneID, question)
}

// Prompt implements brain.Host.
func (h *Host) Prompt(_ context.Context, paneID, text string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.PromptErr != nil {
		return "", h.PromptErr
	}
	h.Prompts = append(h.Prompts, [2]string{paneID, text})
	return "Telling " + h.aliasLocked(paneID) + " to " + text + ". Sent.", nil
}

// AnswerPrompt implements brain.Host: it re-checks the pending prompt
// like the supervisor does.
func (h *Host) AnswerPrompt(_ context.Context, paneID string, keys []string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.KeysErr != nil {
		return "", h.KeysErr
	}
	var pending *brain.PendingPrompt
	for _, a := range h.agents {
		if a.PaneID == paneID {
			pending = a.Pending
		}
	}
	if pending == nil {
		return "", fmt.Errorf("%w: no pending prompt", brain.ErrNotPermitted)
	}
	allowed := pending.AllowedKeys()
	for _, k := range keys {
		ok := false
		for _, a := range allowed {
			if a == k {
				ok = true
			}
		}
		if !ok {
			return "", fmt.Errorf("%w: key %q not in the option set", brain.ErrNotPermitted, k)
		}
	}
	h.Keys = append(h.Keys, [2]string{paneID, strings.Join(keys, " ")})
	for i := range h.agents {
		if h.agents[i].PaneID == paneID {
			h.agents[i].Pending = nil
		}
	}
	return "Told " + h.aliasLocked(paneID) + " " + strings.Join(keys, " ") + ".", nil
}

// Interrupt implements brain.Host.
func (h *Host) Interrupt(_ context.Context, paneID string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.InterruptErr != nil {
		return "", h.InterruptErr
	}
	h.Interrupts = append(h.Interrupts, paneID)
	return "Interrupted " + h.aliasLocked(paneID) + ".", nil
}

func (h *Host) aliasLocked(paneID string) string {
	for _, a := range h.agents {
		if a.PaneID == paneID {
			return a.Alias
		}
	}
	return "an agent"
}

// ErrBoom is a generic failure for tests.
var ErrBoom = errors.New("boom")

// Request line shapes the reflex model parses (see assemble.go
// requestText and brain.summarize).
var (
	reRequest   = regexp.MustCompile(`Request from the assistant \((ask|propose)\): (?:question|instruction)="((?:[^"\\]|\\.)*)" agent=(?:"((?:[^"\\]|\\.)*)"|\(none)`)
	reSummarise = regexp.MustCompile(`^Summarise the following`)
)

// Reflex returns a Model that answers from what it sees instead of a
// script, so a whole daemon can run the brain loop without a network:
// `sheepdog serve --brain=fake`. An ask reads the named agent's screen
// and answers with its last line; when no name was given it lists the
// agents first and reads the one the question names (or the only one,
// or the first blocked one), else answers with the roster. A propose
// sends the instruction with prompt and reports the echo; the compaction
// summariser's prompt gets a one-line summary. Every answer is the JSON
// of the answer contract, so the loop runs exactly as with a real model:
// request → tool call → tool result → reply, with brain.* events and
// transcript messages.
func Reflex() *Model {
	m := &Model{}
	m.Default = &Step{Fn: reflex}
	return m
}

// reflex computes one response from the call.
func reflex(call Call) (*genai.GenerateContentResponse, error) {
	if len(call.Contents) == 0 {
		return Response(Step{Text: Reply("I have nothing to go on.", "")}), nil
	}
	last := call.Contents[len(call.Contents)-1]
	var text string
	for _, p := range last.Parts {
		if p.FunctionResponse != nil {
			usage := [2]int32{int32(len(call.Text()) / 4), 40}
			if p.FunctionResponse.Name == brain.ToolListAgents {
				if alias := pickAgent(p.FunctionResponse, lastQuestion(call)); alias != "" {
					return Response(Step{Calls: []*genai.FunctionCall{FC(brain.ToolReadScreen, map[string]any{"agent": alias, "lines": float64(100)})}, Usage: usage}), nil
				}
			}
			return Response(Step{Text: replyFromResult(p.FunctionResponse), Usage: usage}), nil
		}
		text += p.Text
	}
	if reSummarise.MatchString(strings.TrimSpace(text)) {
		lines := strings.Count(strings.TrimSpace(text), "\n")
		return Response(Step{Text: fmt.Sprintf("Summary by the fake model: %d transcript lines about the agents and what the CEO asked; nothing is still open.", lines)}), nil
	}
	// The newest request line decides.
	reqs := reRequest.FindAllStringSubmatch(text, -1)
	if len(reqs) == 0 {
		return Response(Step{Text: Reply("The fake model has no request to answer.", "This is the reflex model (--brain=fake); it only answers ask and propose requests."), Usage: [2]int32{int32(len(call.Text()) / 4), 20}}), nil
	}
	req := reqs[len(reqs)-1]
	kind, question, agent := req[1], unquote(req[2]), unquote(req[3])
	usage := [2]int32{int32(len(call.Text()) / 4), 30}
	if call.Config != nil && call.Config.ToolConfig != nil && call.Config.ToolConfig.FunctionCallingConfig != nil && call.Config.ToolConfig.FunctionCallingConfig.Mode == genai.FunctionCallingConfigModeNone {
		return Response(Step{Text: Reply("I ran out of steps before reading anything.", "The fake model was asked to answer without tools."), Usage: usage}), nil
	}
	switch {
	case kind == "propose" && agent != "":
		return Response(Step{Calls: []*genai.FunctionCall{FC(brain.ToolPrompt, map[string]any{"agent": agent, "text": question})}, Usage: usage}), nil
	case kind == "propose":
		return Response(Step{Text: Reply("Which agent should I tell?", "The CEO did not say a name and the fake model does not guess."), Usage: usage}), nil
	case agent == "":
		return Response(Step{Calls: []*genai.FunctionCall{FC(brain.ToolListAgents, nil)}, Usage: usage}), nil
	default:
		return Response(Step{Calls: []*genai.FunctionCall{FC(brain.ToolReadScreen, map[string]any{"agent": agent, "lines": float64(100)})}, Usage: usage}), nil
	}
}

// replyFromResult renders the answer after one tool result: the echo of
// an action, an error, or the first lines of a read.
func replyFromResult(fr *genai.FunctionResponse) string {
	if e, ok := fr.Response["error"].(string); ok && e != "" {
		return Reply(fmt.Sprintf("I could not do that: %s.", firstLine(e)), "The tool "+fr.Name+" failed: "+e)
	}
	out, _ := fr.Response["output"].(string)
	switch fr.Name {
	case brain.ToolPrompt, brain.ToolSendKeys, brain.ToolInterrupt:
		return Reply(strings.TrimSpace(out), "Sent through "+fr.Name+".")
	case brain.ToolListAgents:
		return Reply("Here is the roster.", llmTrim(out, 300))
	default:
		body := out
		if i := strings.Index(body, ">>>\n"); i >= 0 {
			body = body[i+4:]
		}
		body = strings.TrimSuffix(strings.TrimSpace(body), "<<<END_WORKER_OUTPUT>>>")
		var lines []string
		for _, l := range strings.Split(strings.TrimSpace(body), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
		spoken := "The screen is empty."
		if n := len(lines); n > 0 {
			spoken = fmt.Sprintf("The screen ends with: %s", llmTrim(lines[n-1], 120))
		}
		return Reply(spoken, fmt.Sprintf("Read %d non-empty lines through %s; the fake model quotes the last one and reasons no further.", len(lines), fr.Name))
	}
}

// lastQuestion returns the newest request's question text in the call.
func lastQuestion(call Call) string {
	var q string
	for _, c := range call.Contents {
		for _, p := range c.Parts {
			for _, m := range reRequest.FindAllStringSubmatch(p.Text, -1) {
				q = unquote(m[2])
			}
		}
	}
	return q
}

// pickAgent chooses the agent to read after a list_agents result: the
// one the question names, else the only one, else the first blocked
// one; "" when none applies.
func pickAgent(fr *genai.FunctionResponse, question string) string {
	out, _ := fr.Response["output"].(string)
	var rows []struct {
		Alias string `json:"alias"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) == 0 {
		return ""
	}
	q := strings.ToLower(question)
	for _, r := range rows {
		if r.Alias != "" && strings.Contains(q, strings.ToLower(r.Alias)) {
			return r.Alias
		}
	}
	if len(rows) == 1 {
		return rows[0].Alias
	}
	for _, r := range rows {
		if r.State == "blocked" {
			return r.Alias
		}
	}
	return ""
}

func unquote(s string) string {
	if u, err := strconv.Unquote(`"` + s + `"`); err == nil {
		return u
	}
	return s
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func llmTrim(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
