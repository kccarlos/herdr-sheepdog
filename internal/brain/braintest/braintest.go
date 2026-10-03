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
	"time"

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

// IsEvent reports whether the call belongs to an event turn (announce):
// the newest request line in its contents is an event's.
func (c Call) IsEvent() bool {
	for i := len(c.Contents) - 1; i >= 0; i-- {
		var t string
		for _, p := range c.Contents[i].Parts {
			t += p.Text
		}
		reqs := reRequest.FindAllStringIndex(t, -1)
		evs := reEvent.FindAllStringIndex(t, -1)
		switch {
		case len(evs) > 0 && (len(reqs) == 0 || evs[len(evs)-1][0] > reqs[len(reqs)-1][0]):
			return true
		case len(reqs) > 0:
			return false
		}
	}
	return false
}

// System returns the prompt as the brain assembles it: the system
// instruction text, then the state block (roster, ledger,
// investigations, time), which travels as the last user content since
// docs/iteration-2.md WP5 (State returns it alone).
func (c Call) System() string {
	var b strings.Builder
	if c.Config != nil && c.Config.SystemInstruction != nil {
		for _, p := range c.Config.SystemInstruction.Parts {
			b.WriteString(p.Text)
		}
	}
	if st := c.State(); st != "" {
		b.WriteString("\n\n")
		b.WriteString(st)
	}
	return b.String()
}

// State returns the call's state block — from its header to the end of
// the last content that holds one — or "".
func (c Call) State() string {
	for i := len(c.Contents) - 1; i >= 0; i-- {
		var t string
		for _, p := range c.Contents[i].Parts {
			t += p.Text
		}
		if at := strings.LastIndex(t, brain.StateHeader); at >= 0 {
			return t[at:]
		}
	}
	return ""
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
// EventDefault, when set, answers every event turn (announce) without
// consuming the script, so a scripted CEO turn is not thrown off by the
// announce turn a block or finish triggers. Delay, when set, is waited
// before every response (a dev knob: `sheepdog serve --brain=fake` with
// SHEEPDOG_FAKE_BRAIN_DELAY_MS, to watch turns overlap).
type Model struct {
	mu           sync.Mutex
	script       []Step
	calls        []Call
	Default      *Step
	EventDefault *Step
	Delay        time.Duration
	// Models is what ListModels returns (brain.ModelCatalog); ListErr,
	// when set, fails it instead (the /debug picker's error path).
	Models  []brain.ModelInfo
	ListErr error
}

// FakeModel is the model name of the fake catalog's own entry.
const FakeModel = "fake-reflex"

// Catalog is a static model list for --brain=fake: the fake's own model
// plus a few Gemini-shaped names, recommended first like the real one.
func Catalog() []brain.ModelInfo {
	return []brain.ModelInfo{
		{Name: "gemini-3.8-flash", DisplayName: "Gemini 3.8 Flash", InputTokenLimit: 1048576, OutputTokenLimit: 65536, Methods: []string{"generateContent"}, Recommended: true},
		{Name: "gemini-3.5-flash", DisplayName: "Gemini 3.5 Flash", InputTokenLimit: 1048576, OutputTokenLimit: 65536, Methods: []string{"generateContent"}, Recommended: true},
		{Name: "gemini-3.5-flash-lite", DisplayName: "Gemini 3.5 Flash-Lite", InputTokenLimit: 1048576, OutputTokenLimit: 65536, Methods: []string{"generateContent"}, Recommended: true},
		{Name: "gemini-3.5-flash-tts", DisplayName: "Gemini 3.5 Flash TTS", InputTokenLimit: 8192, OutputTokenLimit: 16384, Methods: []string{"generateContent"}},
		{Name: FakeModel, DisplayName: "Scripted reflex (no network)", Methods: []string{"generateContent"}},
	}
}

// Catalog2 is the static model list of the second fake provider
// (`--brain=fake` registers fake and fake2, so the /debug picker's
// provider switch can be exercised without a network): the fake's own
// model plus DeepSeek-shaped names.
func Catalog2() []brain.ModelInfo {
	return []brain.ModelInfo{
		{Name: "deepseek-flash", DisplayName: "deepseek-flash", Methods: []string{"chat/completions"}, Recommended: true},
		{Name: "deepseek-v4-pro", DisplayName: "deepseek-v4-pro", Methods: []string{"chat/completions"}, Recommended: true},
		{Name: FakeModel, DisplayName: "Scripted reflex (no network)", Methods: []string{"chat/completions"}},
	}
}

// NothingToAdd is the announce reply "say nothing": an empty spoken.
func NothingToAdd() *Step {
	return &Step{Text: `{"spoken": "", "detail": "Nothing to add.", "needs_user": false, "urgency": "skip"}`}
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

// ListModels implements brain.ModelCatalog: Models, or ListErr.
func (m *Model) ListModels(context.Context) ([]brain.ModelInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	return append([]brain.ModelInfo(nil), m.Models...), nil
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
	if m.EventDefault != nil && call.IsEvent() {
		step = *m.EventDefault
	} else if len(m.script) > 0 {
		step, m.script = m.script[0], m.script[1:]
	} else if m.Default != nil {
		step = *m.Default
	} else {
		step = Step{Text: Reply("I do not know.", "")}
	}
	delay := m.Delay
	m.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
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
	// sessions are what ReadSession returns per pane (SetSession).
	sessions map[string]brain.SessionRead
	CLI      func(paneID, question string) (string, error)
	// Prompts, Keys and Interrupts record the actions (pane -> payload).
	Prompts    [][2]string
	Keys       [][2]string
	Interrupts []string
	// PromptErr / KeysErr / InterruptErr fail the matching action.
	PromptErr, KeysErr, InterruptErr error
	// ReadErr fails ReadScreen.
	ReadErr error
	// SessionErr fails ReadSession; a pane with no SetSession fails with
	// a not_reported error.
	SessionErr error
	// ReadDelay hooks a slow screen read.
	ReadHook func(ctx context.Context) error
	// PromiseErr fails Promise; Promised records every promise made.
	PromiseErr error
	Promised   []brain.PromiseInfo
	// NoteErr fails Note; Noted records every note the note tool made.
	NoteErr error
	Noted   []string
	// InvestigateErr fails Investigate; Investigated is the job ledger
	// the investigate tool filled (running until SetInvestigation).
	InvestigateErr error
	Investigated   []brain.InvestigationInfo
	// Now is the clock for promises (time.Now when nil).
	Now func() time.Time
}

// NewHost returns an empty Host.
func NewHost() *Host {
	return &Host{screens: map[string]string{}, sessions: map[string]brain.SessionRead{}}
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

// SetSession sets what ReadSession returns for a pane.
func (h *Host) SetSession(paneID string, r brain.SessionRead) {
	h.mu.Lock()
	h.sessions[paneID] = r
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

// Note implements brain.Host: the note tool's write path. NoteErr fails
// it; Noted records every note the tool made.
func (h *Host) Note(text string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.NoteErr != nil {
		return "", h.NoteErr
	}
	h.notes = append(h.notes, text)
	h.Noted = append(h.Noted, text)
	return "Noted.", nil
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

// ReadSession implements brain.Host.
func (h *Host) ReadSession(_ context.Context, paneID string) (brain.SessionRead, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.SessionErr != nil {
		return brain.SessionRead{}, h.SessionErr
	}
	r, ok := h.sessions[paneID]
	if !ok {
		return brain.SessionRead{}, fmt.Errorf("no session record for %s: not_reported (herdr has not reported a session for this pane)", h.aliasLocked(paneID))
	}
	return r, nil
}

// RunCLI implements brain.Host.
func (h *Host) RunCLI(_ context.Context, paneID, question string) (string, error) {
	if h.CLI == nil {
		return "", brain.ErrNoCLI
	}
	return h.CLI(paneID, question)
}

// Investigate implements brain.Host: the job is recorded in
// Investigated (running) and answered by hand with SetInvestigation.
// InvestigateErr fails it; ErrNoCLI when the test set neither.
func (h *Host) Investigate(_ context.Context, paneID string, req brain.InvestigationRequest) (brain.InvestigationStart, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.InvestigateErr != nil {
		return brain.InvestigationStart{}, h.InvestigateErr
	}
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	inv := brain.InvestigationInfo{
		ID: fmt.Sprintf("inv-%d", len(h.Investigated)+1), Agent: h.aliasLocked(paneID), Question: req.Question,
		Asked: req.Asked, FollowUpOf: req.FollowUpOf, Source: brain.InvestigationSourceCLI, Backend: "codex",
		Status: brain.InvestigationRunning, Started: now,
	}
	if req.Source != "" {
		inv.Source = req.Source
	}
	h.Investigated = append(h.Investigated, inv)
	return brain.InvestigationStart{Job: inv.ID, ETASeconds: 30}, nil
}

// Investigations implements brain.Host.
func (h *Host) Investigations() []brain.InvestigationInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]brain.InvestigationInfo(nil), h.Investigated...)
}

// SetInvestigation replaces a recorded job (a test finishing one).
func (h *Host) SetInvestigation(inv brain.InvestigationInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.Investigated {
		if h.Investigated[i].ID == inv.ID {
			h.Investigated[i] = inv
			return
		}
	}
	h.Investigated = append(h.Investigated, inv)
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

// Promise implements brain.Host: the record is kept in Promised (open).
func (h *Host) Promise(_ context.Context, paneID string, req brain.PromiseRequest) (brain.PromiseInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.PromiseErr != nil {
		return brain.PromiseInfo{}, h.PromiseErr
	}
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	p := brain.PromiseInfo{ID: fmt.Sprintf("pr-%d", len(h.Promised)+1), Text: req.Text, Agent: h.aliasLocked(paneID), Event: req.Event, Status: brain.PromiseOpen, CreatedTurn: req.Turn, CreatedAt: now, Due: req.Due}
	h.Promised = append(h.Promised, p)
	return p, nil
}

// Promises implements brain.Host.
func (h *Host) Promises() []brain.PromiseInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]brain.PromiseInfo(nil), h.Promised...)
}

// SetPromiseStatus changes a recorded promise's status (kept, expired…).
func (h *Host) SetPromiseStatus(id, status string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.Promised {
		if h.Promised[i].ID == id {
			h.Promised[i].Status = status
		}
	}
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
	// reRequest also reads the ask / propose request lines of
	// transcripts written before those kinds were removed.
	reRequest   = regexp.MustCompile(`Request from the assistant \((ask|propose|utterance)\): (?:question|instruction|utterance)="((?:[^"\\]|\\.)*)" agent=(?:"((?:[^"\\]|\\.)*)"|\(none)`)
	reEvent     = regexp.MustCompile(`Event from the supervisor \((announce|investigation)\): agent="((?:[^"\\]|\\.)*)" event="((?:[^"\\]|\\.)*)" template="((?:[^"\\]|\\.)*)"`)
	reSummarise = regexp.MustCompile(`^Summarise the following`)
	// rePromiseLine is one promise of an announce request ("- pr-1: "text" (when auth finishes)").
	rePromiseLine = regexp.MustCompile(`(?m)^- ([^:\s]+): "((?:[^"\\]|\\.)*)"`)
	// rePushed is a push already in the window ("Pushed to the assistant: "(idle) text"").
	rePushed = regexp.MustCompile(`Pushed to the assistant: "\((\w+)\) ((?:[^"\\]|\\.)*)"`)
	// rePromise splits a combined utterance ("run the tests and let me
	// know when it's done") into the instruction and the promise.
	rePromise  = regexp.MustCompile(`(?i)^(.*?)\s*(?:,|;|\band\b|\bthen\b)?\s*\b((?:please\s+)?(?:let me know|tell me|remind me|ping me|notify me)\b.*)$`)
	reLastTask = regexp.MustCompile(`(?m)^- ([^:\n]+): last task "((?:[^"\\]|\\.)*)"`)
	reCEOSaid  = regexp.MustCompile(`CEO said: "((?:[^"\\]|\\.)*)"`)
	// The shapes an utterance takes for the reflex's own intent split
	// (the real brain decides this with a model; this is a stand-in).
	reNoteWords = regexp.MustCompile(`(?i)^(?:please\s+)?(?:remember|note|keep in mind|don'?t forget)\b`)
	// reExplainWords is a question the screen is unlikely to answer: the
	// reflex starts a background investigation for it.
	reExplainWords = regexp.MustCompile(`(?i)\b(?:explain|why did|why does|why is|what went wrong|walk me through|dig into)\b`)
	// reFollowUpWords is a question that continues the last one, so the
	// reflex resumes the last investigation's session.
	// reSessionWords asks what a worker said, found or changed in
	// detail: the reflex reads its session record, not its screen.
	reSessionWords  = regexp.MustCompile(`(?i)\b(?:what exactly|exactly what|tell me more|in detail|the details?|read it to me)\b`)
	reFollowUpWords = regexp.MustCompile(`(?i)^(?:and|but|so|ok(?:ay)?,?)\b`)
	// reLedgerInvestigation is a finished job in the prompt's
	// Investigations section ("- investigation inv-1 (auth, 2 min ago): …").
	reLedgerInvestigation = regexp.MustCompile(`investigation (\S+) \(`)
	// reWorkerOutput is the provenance-wrapped body of a request (the
	// investigation result the delivery turn is given).
	reWorkerOutput = regexp.MustCompile(`(?s)<<<BEGIN_WORKER_OUTPUT[^>]*>>>\n(.*?)\n<<<END_WORKER_OUTPUT[^>]*>>>`)
	reStatusWords  = regexp.MustCompile(`(?i)^(?:what'?s going on|what is going on|how are things|how'?s it going|status|any(?:thing)? (?:news|update)|what'?s up|are (?:we|they) (?:ok|done)|is (?:it|everything) done)\b`)
	reTellWords    = regexp.MustCompile(`(?i)^(?:tell|ask|have|instruct|get)\s+\S+\s`)
	// reBriefingLine is the roster sentence the request carries.
	reBriefingLine = regexp.MustCompile(`(?m)^Status as the daemon has it: (.*)$`)
)

// utteranceKind is the reflex's stand-in for the brain's intent
// decision on a verbatim utterance: a note, a status question, an
// explanation to investigate, an instruction, or something to look up.
func utteranceKind(question string) string {
	q := strings.TrimSpace(question)
	switch {
	case reNoteWords.MatchString(q):
		return "note"
	case reStatusWords.MatchString(q):
		return "status"
	case reExplainWords.MatchString(q):
		// The screen rarely holds the why: start a background look.
		return "investigate"
	case reTellWords.MatchString(q) || rePromise.MatchString(q):
		return "propose"
	default:
		return "ask"
	}
}

// Reflex returns a Model that answers from what it sees instead of a
// script, so a whole daemon can run the brain loop without a network:
// `sheepdog serve --brain=fake`. It sorts an utterance itself
// (utteranceKind: a note, a status question, an explanation to
// investigate, an instruction — "propose" — or a question — "ask"). An
// ask reads the named agent's screen
// and answers with its last line; a propose sends the instruction with
// prompt and reports the echo. When no name was given it lists the
// agents first and picks the one the words name (or the only one, or
// the first blocked one): an ask then reads its screen, a propose
// prompts it with "tell <alias> to" stripped; with no pick an ask
// answers with the roster and a propose asks which agent. A propose
// with "let me know when …" / "tell me when …" / "remind me …" in it
// also records a promise (after the instruction, when there is one);
// a bare one records only the promise. An announce turn about a finish
// first recalls the agent's record (one read tool round), then answers:
// with one "As you asked: <template>" line when the event keeps
// promises; with skip when the window shows the sentence was already
// pushed; else with the template plus "That was the "<last task>" you
// asked for." when the ledger shows a last task for the agent, at idle
// urgency. An announce turn whose template was already spoken (a block)
// answers at once: the promise line when promises are kept, else skip.
// The compaction summariser's prompt gets a one-line summary. Every
// answer is the JSON of the answer contract, so the loop runs exactly as
// with a real model: request → tool call → tool result → reply, with
// brain.* events and transcript messages.
func Reflex() *Model {
	m := &Model{Models: Catalog()}
	m.Default = &Step{Fn: reflex}
	return m
}

// reflex computes one response from the call.
func reflex(call Call) (*genai.GenerateContentResponse, error) {
	if len(call.Contents) == 0 {
		return Response(Step{Text: Reply("I have nothing to go on.", "")}), nil
	}
	// The newest content that is a function response or holds a request
	// / event line decides; bare context lines (a promise record after a
	// tool result) are skipped.
	var text string
	for i := len(call.Contents) - 1; i >= 0 && text == ""; i-- {
		var fr *genai.FunctionResponse
		var t string
		for _, p := range call.Contents[i].Parts {
			if p.FunctionResponse != nil {
				fr = p.FunctionResponse
			}
			t += p.Text
		}
		if fr != nil {
			return respondToResult(call, fr), nil
		}
		if reRequest.MatchString(t) || reEvent.MatchString(t) || reSummarise.MatchString(strings.TrimSpace(t)) {
			text = t
		}
	}
	if reSummarise.MatchString(strings.TrimSpace(text)) {
		lines := strings.Count(strings.TrimSpace(text), "\n")
		return Response(Step{Text: fmt.Sprintf("Summary by the fake model: %d transcript lines about the agents and what the CEO asked; nothing is still open.", lines)}), nil
	}
	usage := [2]int32{int32(len(call.Text()) / 4), 30}
	forced := call.Config != nil && call.Config.ToolConfig != nil && call.Config.ToolConfig.FunctionCallingConfig != nil && call.Config.ToolConfig.FunctionCallingConfig.Mode == genai.FunctionCallingConfigModeNone
	// The newest request or event line decides.
	reqs := reRequest.FindAllStringSubmatchIndex(text, -1)
	evs := reEvent.FindAllStringSubmatchIndex(text, -1)
	if len(evs) > 0 && (len(reqs) == 0 || evs[len(evs)-1][0] > reqs[len(reqs)-1][0]) {
		m := evs[len(evs)-1]
		ev := reEvent.FindStringSubmatch(text[m[0]:])
		if ev[1] == "investigation" {
			// The delivery turn: the answer is in the request, wrapped
			// as worker output; compose the paragraph from it.
			return Response(Step{Text: investigationReply(text[m[0]:], unquote(ev[2])), Usage: usage}), nil
		}
		if !forced && !calledSinceEvent(call, brain.ToolRecall) {
			// A finish: look at the record for the agent first (what the
			// CEO asked of it), then announce.
			return Response(Step{Calls: []*genai.FunctionCall{FC(brain.ToolRecall, map[string]any{"query": unquote(ev[2])})}, Usage: usage}), nil
		}
		return Response(Step{Text: eventReply(call, text, m), Usage: usage}), nil
	}
	if len(reqs) == 0 {
		return Response(Step{Text: Reply("The fake model has no request to answer.", "This is the reflex model (--brain=fake); it only answers the shepherd's utterances and the supervisor's events."), Usage: [2]int32{int32(len(call.Text()) / 4), 20}}), nil
	}
	req := reRequest.FindStringSubmatch(text[reqs[len(reqs)-1][0]:])
	kind, question, agent := req[1], unquote(req[2]), unquote(req[3])
	if question == "" {
		// The assistant passed no text: the CEO's own words are the
		// request (a bare "let me know when it's done").
		question = lastCEOUtterance(call)
	}
	if forced {
		return Response(Step{Text: Reply("I ran out of steps before reading anything.", "The fake model was asked to answer without tools."), Usage: usage}), nil
	}
	if kind == "utterance" {
		switch k := utteranceKind(question); k {
		case "note":
			return Response(Step{Calls: []*genai.FunctionCall{FC(brain.ToolNote, map[string]any{"text": question})}, Usage: usage}), nil
		case "status":
			// The roster line travels with the request, so a status
			// question costs no tool call at all (D1-revised).
			return Response(Step{Text: Reply(briefingLine(text), "The fake model answered from the roster line in the request."), Usage: usage}), nil
		case "investigate":
			if running := runningInvestigation(call.System()); running != "" {
				// A look is already going: say how it is going rather
				// than start a second one.
				return Response(Step{Text: Reply("Still looking; I will tell you the moment it comes back.", "The fake model saw an investigation running in its prompt."), Usage: usage}), nil
			}
			if agent != "" {
				return Response(Step{Calls: []*genai.FunctionCall{investigateCall(agent, question, call.System())}, Usage: usage}), nil
			}
			kind = k
		default:
			kind = k
		}
	}
	switch {
	case kind == "propose" && agent != "":
		return Response(Step{Calls: proposeCalls(agent, question), Usage: usage}), nil
	case agent == "":
		// No name given: the roster decides (the question may name one).
		return Response(Step{Calls: []*genai.FunctionCall{FC(brain.ToolListAgents, nil)}, Usage: usage}), nil
	default:
		return Response(Step{Calls: []*genai.FunctionCall{readCall(agent, question)}, Usage: usage}), nil
	}
}

// readCall is the reflex's read of an agent: its session record when the
// question asks what it said, found or changed in detail (docs/dive-deep.md
// WP8a), else its screen.
func readCall(alias, question string) *genai.FunctionCall {
	if reSessionWords.MatchString(question) {
		return FC(brain.ToolReadScreen, map[string]any{"agent": alias, "source": brain.SourceSession})
	}
	return FC(brain.ToolReadScreen, map[string]any{"agent": alias, "lines": float64(100)})
}

// respondToResult is the reflex's next step after a tool result.
func respondToResult(call Call, fr *genai.FunctionResponse) *genai.GenerateContentResponse {
	usage := [2]int32{int32(len(call.Text()) / 4), 40}
	if text, m := lastEvent(call); m != nil {
		// An announce turn after its recall round: answer.
		return Response(Step{Text: eventReply(call, text, m), Usage: usage})
	}
	kind, question := lastRequest(call)
	switch fr.Name {
	case brain.ToolListAgents:
		if alias := pickAgent(fr, question); alias != "" {
			switch kind {
			case "propose":
				return Response(Step{Calls: proposeCalls(alias, question), Usage: usage})
			case "investigate":
				return Response(Step{Calls: []*genai.FunctionCall{investigateCall(alias, question, call.System())}, Usage: usage})
			}
			return Response(Step{Calls: []*genai.FunctionCall{readCall(alias, question)}, Usage: usage})
		}
		if kind == "propose" {
			return Response(Step{Text: Reply("Which agent should I tell?", "The CEO did not say a name and the fake model does not guess."), Usage: usage})
		}
	case brain.ToolPrompt:
		// The instruction went; a promise in the same words is still
		// owed unless one was already made this turn.
		if _, promise := splitPromise(question); promise != "" && !calledSinceRequest(call, brain.ToolPromise) {
			if alias := agentOfCall(call, brain.ToolPrompt); alias != "" {
				return Response(Step{Calls: []*genai.FunctionCall{promiseCall(alias, promise)}, Usage: usage})
			}
		}
	case brain.ToolInvestigate:
		if e, ok := fr.Response["error"].(string); ok && e != "" {
			return Response(Step{Text: Reply("I could not start that: "+firstLine(e)+".", "The investigate tool failed: "+e), Usage: usage})
		}
		out, _ := fr.Response["output"].(string)
		var start brain.InvestigationStart
		_ = json.Unmarshal([]byte(out), &start)
		eta := "about half a minute"
		if start.ETASeconds >= 60 {
			eta = fmt.Sprintf("about %d minutes", (start.ETASeconds+59)/60)
		}
		// The acknowledgement: pending true, and what it is doing.
		return Response(Step{Text: mustJSON(brain.Reply{
			Spoken: "Looking into it now, " + eta + ".", Pending: true,
			Detail: "The fake model started investigation " + start.Job + "; its answer comes back on its own.",
		}), Usage: usage})
	case brain.ToolPromise:
		if e, ok := fr.Response["error"].(string); ok && e != "" {
			return Response(Step{Text: Reply("I could not record that: "+firstLine(e)+".", "The promise tool failed: "+e), Usage: usage})
		}
		out, _ := fr.Response["output"].(string)
		spoken := strings.TrimSpace(out)
		if echo := promptEcho(call); echo != "" {
			// Two sentences at most, no semicolons (the ear
			// contract): "Telling auth to run the tests. I will tell
			// you when auth finishes."
			spoken = strings.TrimSuffix(echo, " Sent.") + " " + spoken
		}
		return Response(Step{Text: Reply(spoken, "Recorded a promise through the promise tool."), Usage: usage})
	}
	return Response(Step{Text: replyFromResult(fr), Usage: usage})
}

// eventReply answers an announce turn. m is the submatch index of the
// newest event line in text.
func eventReply(call Call, text string, m []int) string {
	ev := reEvent.FindStringSubmatch(text[m[0]:])
	agent, template := unquote(ev[2]), unquote(ev[4])
	sentence := template
	if sentence != "" && !strings.HasSuffix(sentence, ".") {
		sentence += "."
	}
	var ids []string
	for _, p := range rePromiseLine.FindAllStringSubmatch(text[m[0]:], -1) {
		ids = append(ids, p[1])
	}
	if len(ids) > 0 {
		// One line keeps every promise; nothing else is said about it.
		return mustJSON(brain.Reply{Spoken: "As you asked: " + sentence, Detail: "The fake model kept the promise with the template sentence.", Urgency: brain.UrgencyIdle, KeepsPromiseIDs: ids})
	}
	for _, p := range rePushed.FindAllStringSubmatch(call.Text(), -1) {
		if strings.Contains(unquote(p[2]), template) {
			return mustJSON(brain.Reply{Spoken: "", Detail: "The fake model saw the sentence was already pushed to the assistant.", Urgency: brain.UrgencySkip})
		}
	}
	// Relate the finish to the last task the ledger shows for the agent.
	for _, lt := range reLastTask.FindAllStringSubmatch(call.System(), -1) {
		if strings.EqualFold(lt[1], agent) {
			task := unquote(lt[2])
			return mustJSON(brain.Reply{Spoken: fmt.Sprintf("%s That was the %q you asked for.", sentence, task), Detail: "The fake model related the event to the agent's last task from the ledger.", RelatesTo: task, Urgency: brain.UrgencyIdle})
		}
	}
	return mustJSON(brain.Reply{Spoken: sentence, Detail: "The fake model found nothing to add to the template sentence.", Urgency: brain.UrgencyIdle})
}

// investigateCall is what an explanation asks for: a background look at
// the agent, resuming the last finished one when the words continue it
// ("and why does that guard exist?").
func investigateCall(alias, question, system string) *genai.FunctionCall {
	args := map[string]any{"agent": alias, "question": question}
	if reFollowUpWords.MatchString(strings.TrimSpace(question)) {
		if id := lastInvestigation(system); id != "" {
			args["follow_up_of"] = id
		}
	}
	return FC(brain.ToolInvestigate, args)
}

// lastInvestigation is the newest finished job in the prompt's
// Investigations section, or "".
func lastInvestigation(system string) string {
	m := reLedgerInvestigation.FindAllStringSubmatch(investigationsSection(system), -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// runningInvestigation is the id of a job the prompt says is running, or
// "".
func runningInvestigation(system string) string {
	sec := investigationsSection(system)
	_, after, found := strings.Cut(sec, "Running now")
	if !found {
		return ""
	}
	if stop := strings.Index(after, "Already looked into"); stop >= 0 {
		after = after[:stop]
	}
	for _, line := range strings.Split(after, "\n") {
		if id, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "- "), " "); ok && strings.HasPrefix(id, "inv-") {
			return id
		}
	}
	return ""
}

// investigationsSection is the "## Investigations" section of the system
// prompt.
func investigationsSection(system string) string {
	_, after, found := strings.Cut(system, "## Investigations")
	if !found {
		return ""
	}
	if stop := strings.Index(after, "\n## "); stop >= 0 {
		after = after[:stop]
	}
	return after
}

// investigationReply answers a delivery turn from the result the request
// carries: the gist and an offer, pushed at the next pause.
func investigationReply(text, agent string) string {
	found := "it came back with nothing"
	if m := reWorkerOutput.FindStringSubmatch(text); m != nil {
		found = strings.TrimSpace(m[1])
	}
	return mustJSON(brain.Reply{
		Spoken:  fmt.Sprintf("Here is what the look into %s found. %s", agent, found),
		Detail:  "The fake model relayed the investigation's answer.",
		Urgency: brain.UrgencyIdle,
	})
}

// lastEvent returns the text part holding the newest event line and the
// line's submatch index, or nil when a request line is newer (or there
// is none).
func lastEvent(call Call) (string, []int) {
	var text string
	var m []int
	for _, c := range call.Contents {
		for _, p := range c.Parts {
			if p.Text == "" {
				continue
			}
			reqs := reRequest.FindAllStringIndex(p.Text, -1)
			evs := reEvent.FindAllStringSubmatchIndex(p.Text, -1)
			switch {
			case len(evs) > 0 && (len(reqs) == 0 || evs[len(evs)-1][0] > reqs[len(reqs)-1][0]):
				text, m = p.Text, evs[len(evs)-1]
			case len(reqs) > 0:
				text, m = "", nil
			}
		}
	}
	return text, m
}

// calledSinceEvent reports whether a function call named tool was made
// after the newest event line in the call's contents.
func calledSinceEvent(call Call, tool string) bool {
	called := false
	for _, c := range call.Contents {
		for _, p := range c.Parts {
			if p.Text != "" && reEvent.MatchString(p.Text) {
				called = false
			}
			if p.FunctionCall != nil && p.FunctionCall.Name == tool {
				called = true
			}
		}
	}
	return called
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// proposeCalls is what a propose sends first: the instruction when the
// words hold one (the promise, if any, follows after its result), else
// the promise alone.
func proposeCalls(alias, question string) []*genai.FunctionCall {
	instruction, promise := splitPromise(question)
	if instruction == "" && promise != "" {
		return []*genai.FunctionCall{promiseCall(alias, promise)}
	}
	return []*genai.FunctionCall{FC(brain.ToolPrompt, map[string]any{"agent": alias, "text": instructionFor(alias, instruction)})}
}

// promiseCall builds the promise call for the promise clause.
func promiseCall(alias, promise string) *genai.FunctionCall {
	return FC(brain.ToolPromise, map[string]any{"agent": alias, "text": promise, "event": promiseEventFor(promise)})
}

// splitPromise separates "run the tests and let me know when it's done"
// into the instruction ("run the tests") and the promise clause; the
// instruction is the whole text when there is no promise clause.
func splitPromise(text string) (instruction, promise string) {
	text = strings.TrimSpace(text)
	m := rePromise.FindStringSubmatch(text)
	if m == nil {
		return text, ""
	}
	return strings.TrimRight(strings.TrimSpace(m[1]), ",;"), strings.TrimSpace(m[2])
}

// promiseEventFor maps the promise clause's words onto a promise event.
func promiseEventFor(promise string) string {
	p := strings.ToLower(promise)
	switch {
	case strings.Contains(p, "ask") || strings.Contains(p, "block") || strings.Contains(p, "question") || strings.Contains(p, "need") || strings.Contains(p, "respond") || strings.Contains(p, "stuck"):
		return brain.PromiseEventBlocked
	case strings.Contains(p, "idle"):
		return brain.PromiseEventIdle
	case strings.Contains(p, "anything") || strings.Contains(p, "change"):
		return brain.PromiseEventAnyChange
	default:
		return brain.PromiseEventDone
	}
}

// calledSinceRequest reports whether a function call named tool was
// made after the newest request line in the call's contents.
func calledSinceRequest(call Call, tool string) bool {
	called := false
	for _, c := range call.Contents {
		for _, p := range c.Parts {
			if p.Text != "" && reRequest.MatchString(p.Text) {
				called = false
			}
			if p.FunctionCall != nil && p.FunctionCall.Name == tool {
				called = true
			}
		}
	}
	return called
}

// agentOfCall returns the agent argument of the newest call named tool.
func agentOfCall(call Call, tool string) string {
	alias := ""
	for _, c := range call.Contents {
		for _, p := range c.Parts {
			if p.FunctionCall != nil && p.FunctionCall.Name == tool {
				if a, ok := p.FunctionCall.Args["agent"].(string); ok {
					alias = a
				}
			}
		}
	}
	return alias
}

// promptEcho returns the newest prompt result's echo ("Telling auth to
// run the tests. Sent.") after the newest request, or "".
func promptEcho(call Call) string {
	echo := ""
	for _, c := range call.Contents {
		for _, p := range c.Parts {
			if p.Text != "" && reRequest.MatchString(p.Text) {
				echo = ""
			}
			if p.FunctionResponse != nil && p.FunctionResponse.Name == brain.ToolPrompt {
				if out, ok := p.FunctionResponse.Response["output"].(string); ok {
					echo = strings.TrimSpace(out)
				}
			}
		}
	}
	return echo
}

// replyFromResult renders the answer after one tool result: the echo of
// an action, an error, or the first lines of a read.
func replyFromResult(fr *genai.FunctionResponse) string {
	if e, ok := fr.Response["error"].(string); ok && e != "" {
		return Reply(fmt.Sprintf("I could not do that: %s.", firstLine(e)), "The tool "+fr.Name+" failed: "+e)
	}
	out, _ := fr.Response["output"].(string)
	switch fr.Name {
	case brain.ToolPrompt, brain.ToolSendKeys, brain.ToolInterrupt, brain.ToolNote:
		return Reply(strings.TrimSpace(out), "Sent through "+fr.Name+".")
	case brain.ToolListAgents:
		return Reply("Here is the roster.", llmTrim(out, 300))
	default:
		body := out
		if i := strings.Index(body, ">>>\n"); i >= 0 {
			body = body[i+4:]
		}
		body = strings.TrimSpace(body)
		if i := strings.LastIndex(body, "<<<END_WORKER_OUTPUT"); i >= 0 {
			body = body[:i]
		}
		var lines []string
		for _, l := range strings.Split(strings.TrimSpace(body), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
		spoken := "The screen is empty."
		if strings.Contains(out, "source="+brain.SourceSession) {
			// A session read: quote the first line of its last message.
			msg := lines
			for i, l := range lines {
				if strings.HasPrefix(l, "Its last message") {
					msg = lines[i+1:]
					break
				}
			}
			spoken = "It has written nothing for a human yet."
			if len(msg) > 0 {
				spoken = "It wrote: " + llmTrim(strings.TrimPrefix(msg[0], "…"), 160)
			}
			return Reply(spoken, fmt.Sprintf("Read its session record through %s; the fake model quotes the first line of its last message.", fr.Name))
		}
		if n := len(lines); n > 0 {
			spoken = fmt.Sprintf("The screen ends with: %s", llmTrim(lines[n-1], 120))
		}
		return Reply(spoken, fmt.Sprintf("Read %d non-empty lines through %s; the fake model quotes the last one and reasons no further.", len(lines), fr.Name))
	}
}

// lastRequest returns the newest request's kind and question in the
// call (the CEO's own words when the request carried no text).
func lastRequest(call Call) (kind, question string) {
	for _, c := range call.Contents {
		for _, p := range c.Parts {
			for _, m := range reRequest.FindAllStringSubmatch(p.Text, -1) {
				kind, question = m[1], unquote(m[2])
			}
		}
	}
	if question == "" {
		question = lastCEOUtterance(call)
	}
	if kind == "utterance" {
		kind = utteranceKind(question)
	}
	return kind, question
}

// briefingLine is the roster sentence the request carries, or a stand-in
// when it carried none.
func briefingLine(text string) string {
	if m := reBriefingLine.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	return "Nothing is running."
}

// lastCEOUtterance returns the newest "CEO said" line in the call.
func lastCEOUtterance(call Call) string {
	out := ""
	for _, c := range call.Contents {
		for _, p := range c.Parts {
			for _, m := range reCEOSaid.FindAllStringSubmatch(p.Text, -1) {
				out = unquote(m[1])
			}
		}
	}
	return out
}

// instructionFor strips a leading "tell <alias> to" from an instruction
// the text router passed through verbatim.
func instructionFor(alias, instruction string) string {
	re := regexp.MustCompile(`(?i)^(?:tell|ask|have|instruct)\s+` + regexp.QuoteMeta(alias) + `\s+(?:to\s+)?`)
	if out := strings.TrimSpace(re.ReplaceAllString(instruction, "")); out != "" {
		return out
	}
	return instruction
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
