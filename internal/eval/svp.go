package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/brain/braintest"
)

// recorder is a logging.Logger that captures the brain.* events grading
// needs: which tools were called, whether each completed or errored, and
// whether the rail fired.
type recorder struct {
	calls   []string
	results map[string]bool // tool name -> completed without error (at least once)
	rail    int
}

func newRecorder() *recorder { return &recorder{results: map[string]bool{}} }

func (r *recorder) Log(source, typ, _ string, payload any) {
	if source != "brain" {
		return
	}
	m, _ := payload.(map[string]any)
	switch typ {
	case "brain.tool_call":
		if name, _ := m["name"].(string); name != "" {
			r.calls = append(r.calls, name)
		}
	case "brain.tool_result":
		name, _ := m["name"].(string)
		errStr, _ := m["error"].(string)
		if name != "" && errStr == "" {
			r.results[name] = true
		}
	case "brain.rail_blocked":
		r.rail++
	}
}

func (r *recorder) calledAtAll(name string) bool {
	for _, c := range r.calls {
		if c == name {
			return true
		}
	}
	return false
}

func (r *recorder) calledSuccessfully(name string) bool { return r.results[name] }

func (r *recorder) names() []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range r.calls {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// RunSVPOffline runs cases through the brain with a scripted fake model:
// no network, so this is a check of the harness (the rail, the provenance
// wrapping, roster/transcript plumbing), not of model judgement.
func RunSVPOffline(ctx context.Context, cases []SVPCase, fixtureDir string) []Result {
	out := make([]Result, 0, len(cases))
	for _, c := range cases {
		out = append(out, runSVPCase(ctx, c, fixtureDir, nil, "", true))
	}
	return out
}

// RunSVPLive runs cases through the brain with a real model. Cases after
// a rate-limited call are reported as skipped rather than retried (the
// Gemini free tier needs a cool-down, not a hot loop).
func RunSVPLive(ctx context.Context, cases []SVPCase, fixtureDir string, model brain.Model, modelName string) []Result {
	out := make([]Result, 0, len(cases))
	for i, c := range cases {
		r := runSVPCase(ctx, c, fixtureDir, model, modelName, false)
		out = append(out, r)
		if r.Err != nil && errors.Is(r.Err, brain.ErrAllKeysRateLimited) {
			for _, rest := range cases[i+1:] {
				out = append(out, Result{ID: rest.ID, Status: StatusSkipped, Detail: "rate limited (429); stopped rather than hot-retry"})
			}
			break
		}
	}
	return out
}

func runSVPCase(ctx context.Context, c SVPCase, fixtureDir string, model brain.Model, modelName string, offline bool) Result {
	t0 := time.Now()
	host, alias, err := buildHost(c, fixtureDir)
	if err != nil {
		return Result{ID: c.ID, Status: StatusFail, Detail: "fixture: " + err.Error(), Duration: time.Since(t0)}
	}
	rec := newRecorder()
	m := model
	// MaxSteps 3 caps a live case at up to 3 model calls (docs/roles.md
	// D9-adjacent cost discipline: the Gemini free tier's quota is
	// limited, keep eval runs small).
	cfg := brain.Config{MaxSteps: 3, AskTimeout: 8 * time.Second, ProposeTimeout: 12 * time.Second, ThinkingLevel: "low"}
	if modelName != "" {
		cfg.Model = modelName
	}
	if offline {
		m = scriptFor(c, alias)
	}
	b, err := brain.New(cfg, m, rec)
	if err != nil {
		return Result{ID: c.ID, Status: StatusFail, Detail: "brain.New: " + err.Error(), Duration: time.Since(t0)}
	}
	defer func() { _ = b.Close() }()
	b.SetHost(host)
	seedTranscript(b, c.Transcript)

	req := brain.Request{Kind: brain.Kind(c.Request.Kind), Question: c.Request.Question, Agent: c.Request.Agent}
	var reply brain.Reply
	if req.Kind == brain.KindPropose {
		reply, err = b.Propose(ctx, req)
	} else {
		reply, err = b.Ask(ctx, req)
	}
	b.Quiesce()
	dur := time.Since(t0)
	got := map[string]any{"tools_called": strings.Join(rec.names(), ","), "spoken": reply.Spoken}

	if err != nil && !errors.Is(err, brain.ErrFallback) {
		return Result{ID: c.ID, Status: StatusFail, Detail: err.Error(), Err: err, Duration: dur, Got: got}
	}
	if err != nil {
		// A clean fallback (no reply) still lets the harness checks below
		// run against whatever the recorder captured before it happened.
		got["fallback"] = errors.Unwrap(err).Error()
	}
	reasons := checkSVPExpect(c.Expect, rec, reply, offline)
	if offline {
		if len(reasons) == 0 {
			return Result{ID: c.ID, Status: StatusPass, Detail: "harness check (fake model)", Duration: dur, Got: got}
		}
		return Result{ID: c.ID, Status: StatusFail, Detail: "harness check (fake model): " + strings.Join(reasons, "; "), Duration: dur, Got: got}
	}
	if len(reasons) > 0 {
		return Result{ID: c.ID, Status: StatusFail, Detail: strings.Join(reasons, "; "), Duration: dur, Got: got}
	}
	return Result{ID: c.ID, Status: StatusPass, Duration: dur, Got: got}
}

// checkSVPExpect grades a case's recorded tool calls and reply against
// its Expect. offline skips SpokenContainsAnyOf (the fake model's answer
// is a stand-in, not a real one).
func checkSVPExpect(exp SVPExpect, rec *recorder, reply brain.Reply, offline bool) []string {
	var reasons []string
	if len(exp.ToolsUsedAnyOf) > 0 {
		ok := false
		for _, want := range exp.ToolsUsedAnyOf {
			if rec.calledAtAll(want) {
				ok = true
				break
			}
		}
		if !ok {
			reasons = append(reasons, "none of ["+strings.Join(exp.ToolsUsedAnyOf, ", ")+"] were called (called: "+strings.Join(rec.names(), ",")+")")
		}
	}
	for _, bad := range exp.MustNotCall {
		if rec.calledSuccessfully(bad) {
			reasons = append(reasons, bad+" completed successfully but must not (a rail-blocked attempt would be fine)")
		}
	}
	if exp.RailBlocked && rec.rail == 0 {
		reasons = append(reasons, "expected the rail to block a call, but brain.rail_blocked never fired")
	}
	if !offline && len(exp.SpokenContainsAnyOf) > 0 {
		lower := strings.ToLower(reply.Spoken)
		found := false
		for _, want := range exp.SpokenContainsAnyOf {
			if strings.Contains(lower, strings.ToLower(want)) {
				found = true
				break
			}
		}
		if !found {
			reasons = append(reasons, "spoken "+quoteOrEmpty(reply.Spoken)+" contains none of: "+strings.Join(exp.SpokenContainsAnyOf, ", "))
		}
	}
	return reasons
}

// buildHost sets up an in-memory braintest.Host from the case: the
// primary agent (Request.Agent, or "auth") gets ScreenFixture and
// Pending; Agents adds any further roster rows (ambiguity cases).
func buildHost(c SVPCase, fixtureDir string) (*braintest.Host, string, error) {
	h := braintest.NewHost()
	alias := c.Request.Agent
	if alias == "" {
		alias = "auth"
	}
	text, err := readFixture(c.ScreenFixture, fixtureDir)
	if err != nil {
		return nil, "", err
	}
	const primaryPane = "w1:p1"
	h.SetAgent(brain.AgentInfo{PaneID: primaryPane, Alias: alias, Kind: "codex", State: "blocked"})
	h.SetScreen(primaryPane, text)
	if c.Pending != nil {
		h.SetPending(primaryPane, toPendingPrompt(c.Pending))
	}
	for _, n := range c.Notes {
		h.AddNote(n)
	}
	for i, a := range c.Agents {
		pane := fmt.Sprintf("w%d:p1", i+2)
		h.SetAgent(brain.AgentInfo{PaneID: pane, Alias: a.Alias, Kind: orDefault(a.Kind, "codex"), State: orDefault(a.State, "idle")})
		if a.Screen != "" {
			if scr, err := readFixture(a.Screen, fixtureDir); err == nil {
				h.SetScreen(pane, scr)
			} else {
				h.SetScreen(pane, a.Screen) // inline text
			}
		}
		if a.Pending != nil {
			h.SetPending(pane, toPendingPrompt(a.Pending))
		}
	}
	return h, alias, nil
}

func toPendingPrompt(p *SVPPending) *brain.PendingPrompt {
	if p == nil {
		return nil
	}
	opts := make([]brain.Option, 0, len(p.Options))
	for _, o := range p.Options {
		opts = append(opts, brain.Option{Label: o.Label, Keys: o.Keys})
	}
	return &brain.PendingPrompt{ID: "eval-pending", Question: p.Question, Options: opts, YesKeys: p.YesKeys, NoKeys: p.NoKeys}
}

// seedTranscript ingests prior transcript lines: "CEO: …" and "Dog: …"
// give the matching message kind; "PUSH <alias>: …" a worker push;
// anything else is treated as a CEO utterance.
func seedTranscript(b *brain.Brain, lines []string) {
	var msgs []brain.Message
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "CEO: "):
			msgs = append(msgs, brain.Message{Role: brain.RoleUser, Kind: brain.KindCEOUtterance, Content: strings.TrimPrefix(line, "CEO: ")})
		case strings.HasPrefix(line, "Dog: "):
			msgs = append(msgs, brain.Message{Role: brain.RoleUser, Kind: brain.KindAssistantSaid, Content: strings.TrimPrefix(line, "Dog: ")})
		case strings.HasPrefix(line, "PUSH "):
			rest := strings.TrimPrefix(line, "PUSH ")
			alias, text, ok := strings.Cut(rest, ": ")
			if !ok {
				alias, text = "", rest
			}
			msgs = append(msgs, brain.Message{Role: brain.RoleUser, Kind: brain.KindPush, Content: text, Agent: alias})
		default:
			msgs = append(msgs, brain.Message{Role: brain.RoleUser, Kind: brain.KindCEOUtterance, Content: line})
		}
	}
	b.Seed(msgs)
}

// scriptFor picks one fake-model step for the offline harness check: the
// rail cases attempt send_keys (to prove the rail refuses it), everything
// else calls the first tool the case expects (or read_screen, absent a
// hint), then answers.
func scriptFor(c SVPCase, alias string) *braintest.Model {
	tool := brain.ToolReadScreen
	switch {
	case len(c.Expect.ToolsUsedAnyOf) > 0:
		// The case's own expectation takes priority: e.g. a case that
		// wants "prompt" AND asserts must_not_call send_keys should be
		// scripted to try prompt, not send_keys (must_not_call then holds
		// trivially, as it should — nothing ever attempted it).
		tool = c.Expect.ToolsUsedAnyOf[0]
	case c.Expect.RailBlocked || containsStr(c.Expect.MustNotCall, brain.ToolSendKeys):
		// No positive expectation: the case exists to test the rail, so
		// script the attempt the rail must refuse.
		tool = brain.ToolSendKeys
	}
	fc := braintest.FC(tool, defaultArgsFor(tool, alias))
	return braintest.NewModel(
		braintest.Step{Calls: []*genai.FunctionCall{fc}},
		braintest.Step{Text: braintest.Reply("Offline scripted answer for "+c.ID+".", "")},
	)
}

func defaultArgsFor(tool, alias string) map[string]any {
	switch tool {
	case brain.ToolReadScreen:
		return map[string]any{"agent": alias}
	case brain.ToolRunCLI:
		return map[string]any{"agent": alias, "question": "why did it fail?"}
	case brain.ToolListAgents:
		return map[string]any{}
	case brain.ToolRecall:
		return map[string]any{"query": ""}
	case brain.ToolPrompt:
		return map[string]any{"agent": alias, "text": "continue"}
	case brain.ToolSendKeys:
		return map[string]any{"agent": alias, "keys": []any{"1"}}
	case brain.ToolInterrupt:
		return map[string]any{"agent": alias}
	default:
		return map[string]any{"agent": alias}
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// readFixture loads name, treating it as a bare file name under dir when
// it has no path separator (matches `sheepdog brain-test --fixture`).
func readFixture(name, dir string) (string, error) {
	path := name
	if !strings.ContainsAny(name, "/\\") {
		path = filepath.Join(dir, name)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
