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
