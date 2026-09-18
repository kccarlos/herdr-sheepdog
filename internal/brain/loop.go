package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/llm"
)

// logResultBytes is how much of a tool result a brain.tool_result keeps.
const logResultBytes = 1024

// turn is the bookkeeping of one Turn call.
type turn struct {
	n     int
	req   Request
	start time.Time
	// soft is when the budget for tool rounds ends; the hard deadline is
	// FinalGrace later.
	soft   time.Time
	steps  int
	calls  int // model calls
	tokIn  int
	tokOut int
	acted  []string // spoken echoes of actions sent
}

// Turn runs one turn (Ask and Propose set the kind). The reply is
// returned as soon as the model answers; pruning, compaction and the hand-
// over to the next queued turn happen afterwards without blocking the
// caller. ErrFallback (wrapping the cause) means the supervisor should
// answer from its template path.
func (b *Brain) Turn(ctx context.Context, req Request) (Reply, error) {
	if req.Kind == "" {
		req.Kind = KindAsk
	}
	if req.Trigger == "" {
		req.Trigger = TriggerCEO
	}
	b.mu.Lock()
	host := b.host
	b.mu.Unlock()
	if host == nil {
		return Reply{}, fmt.Errorf("%w: %w", ErrFallback, ErrNoHost)
	}
	timeout := b.cfg.AskTimeout
	switch {
	case req.Kind == KindPropose:
		timeout = b.cfg.ProposeTimeout
	case req.Kind.IsEvent():
		timeout = b.cfg.EnrichTimeout
		req.Trigger = TriggerEvent
	}
	tctx, cancel := context.WithTimeout(ctx, timeout+b.cfg.FinalGrace)
	defer cancel()
	start := b.cfg.Now()
	if err := b.lock.acquire(tctx, req.Trigger == TriggerCEO); err != nil {
		b.log.Log(logSource, "brain.fallback", "", map[string]any{"reason": "queue_timeout", "kind": string(req.Kind), "trigger": string(req.Trigger), "error": err.Error()})
		return Reply{}, fmt.Errorf("%w: waiting for the turn lock: %w", ErrFallback, err)
	}
	t := &turn{req: req, start: start, soft: start.Add(timeout)}
	reply, err := b.run(tctx, t)
	b.after.Add(1)
	go b.afterTurn(t)
	return reply, err
}

// run is the loop: assemble, generate, execute calls, repeat; the turn
// lock is held.
func (b *Brain) run(ctx context.Context, t *turn) (Reply, error) {
	b.mu.Lock()
	b.turns++
	t.n = b.turns
	if t.req.Trigger == TriggerCEO {
		// Whatever the CEO said before this tool call is the utterance
		// that caused it, final or not.
		b.flushInLocked()
	}
	b.mu.Unlock()
	b.ingest(Message{Role: RoleUser, Kind: KindRequest, Content: requestText(t.req), Agent: t.req.Agent}, t.n)
	if t.req.Screen != "" {
		// The supervisor's own read for this event: in the window like a
		// read_screen result (wrapped, pruned to a stub later).
		text := llm.TailBytes(t.req.Screen, maxResultBytes)
		b.ingest(Message{Role: RoleUser, Kind: KindEventScreen, Content: wrapWorker(t.req.Agent, "screen", text), Agent: t.req.Agent,
			ToolResult: &ToolResult{Name: ToolReadScreen, Summary: fmt.Sprintf("screen of %s at %s, %d lines", aliasOr(t.req.Agent), t.req.Event, strings.Count(text, "\n")+1)}}, t.n)
	}

	system := b.systemPrompt()
	tools := b.tools
	if t.req.Kind.IsEvent() {
		tools = b.readTools
	}
	base := genai.GenerateContentConfig{Tools: tools}
	if lvl := thinkingLevel(b.cfg.ThinkingLevel); lvl != "" {
		base.ThinkingConfig = &genai.ThinkingConfig{ThinkingLevel: lvl}
	}
	corrected := false
	for step := 1; step <= b.cfg.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return b.fail(t, reasonFor(err), err)
		}
		t.steps = step
		// The last step, or one starting with too little budget left for
		// another tool round, is answer-only: the model must reply from
		// what it has.
		forced := step == b.cfg.MaxSteps || (step > 1 && t.soft.Sub(b.cfg.Now()) < b.cfg.StepReserve)
		config := base // one config per step: the fake model keeps the pointer
		if forced {
			config.ToolConfig = &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeNone}}
			b.ingest(Message{Role: RoleUser, Kind: KindRequest, Content: "Time is up: answer now with the JSON object of the answer contract from what you have, without further tool calls."}, t.n)
		}
		asm := b.engine.Assemble(b.cfg.WindowTokens)
		text := system
		if asm.SystemAddition != "" {
			text += "\n\n" + asm.SystemAddition
		}
		config.SystemInstruction = &genai.Content{Parts: []*genai.Part{{Text: text}}}

		t0 := b.cfg.Now()
		resp, err := b.model.Generate(ctx, b.cfg.Model, toContents(asm.Messages), &config)
		t.calls++
		payload := map[string]any{"turn": t.n, "step": step, "model": b.cfg.Model, "template": string(t.req.Kind), "dur_ms": b.cfg.Now().Sub(t0).Milliseconds(), "forced": forced}
		var calls []*genai.FunctionCall
		if err != nil {
			payload["error"] = err.Error()
		} else {
			if u := resp.UsageMetadata; u != nil {
				payload["prompt_tokens"], payload["completion_tokens"] = u.PromptTokenCount, u.CandidatesTokenCount
				t.tokIn += int(u.PromptTokenCount)
				t.tokOut += int(u.CandidatesTokenCount)
			}
			calls = resp.FunctionCalls()
			names := make([]string, 0, len(calls))
			for _, c := range calls {
				names = append(names, c.Name)
			}
			payload["function_calls"], payload["tool_calls"] = names, len(calls)
		}
		b.log.Log(logSource, "brain.step", "", payload)
		if err != nil {
			return b.fail(t, reasonFor(err), err)
		}
		if err := ctx.Err(); err != nil {
			// Answered past the budget: the caller has moved on.
			return b.fail(t, reasonFor(err), err)
		}

		if forced && len(calls) > 0 {
			return b.fail(t, "step_budget", errors.New("brain: tool calls in an answer-only step"))
		}
		if len(calls) == 0 {
			answer := strings.TrimSpace(textOf(resp))
			reply, perr := parseReply(answer, t.req.Kind.IsEvent())
			if perr != nil {
				if !corrected && answer != "" {
					corrected = true
					b.ingest(Message{Role: RoleAssistant, Kind: KindReply, Content: answer}, t.n)
					b.ingest(Message{Role: RoleUser, Kind: KindRequest, Content: "That was not the JSON object of the answer contract. Reply with the JSON object only."}, t.n)
					continue
				}
				return b.fail(t, "bad_reply", perr)
			}
			b.ingest(Message{Role: RoleAssistant, Kind: KindReply, Content: answer}, t.n)
			b.logTurn(t, false)
			return reply, nil
		}
		b.execCalls(ctx, t, resp, calls)
	}
	return b.fail(t, "step_budget", fmt.Errorf("brain: %d steps without an answer", b.cfg.MaxSteps))
}

// execCalls records the model's content, executes every call and records
// the results.
func (b *Brain) execCalls(ctx context.Context, t *turn, resp *genai.GenerateContentResponse, calls []*genai.FunctionCall) {
	var raw json.RawMessage
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		raw, _ = json.Marshal(resp.Candidates[0].Content)
	}
	modelText := strings.TrimSpace(textOf(resp))
	for i, fc := range calls {
		id := b.nextToolID(t.n)
		m := Message{Role: RoleAssistant, Kind: KindToolCall, ToolCall: &ToolCall{ID: id, Name: fc.Name, Args: fc.Args}}
		if i == 0 {
			m.Raw, m.Content = raw, modelText
		}
		b.ingest(m, t.n)
		b.log.Log(logSource, "brain.tool_call", "", map[string]any{"id": id, "name": fc.Name, "args": fc.Args, "turn": t.n})

		t0 := b.cfg.Now()
		out := b.execTool(ctx, t.req.Trigger, t.n, fc)
		dur := b.cfg.Now().Sub(t0).Milliseconds()
		if out.railBlocked != "" {
			b.log.Log(logSource, "brain.rail_blocked", "", map[string]any{"id": id, "agent": out.agent, "keys": keysArg(fc.Args["keys"]), "trigger": string(t.req.Trigger), "reason": out.railBlocked, "turn": t.n})
		}
		if out.acted != "" {
			t.acted = append(t.acted, out.acted)
		}
		res := &ToolResult{ID: id, Name: fc.Name, DurMS: dur, Summary: out.summary}
		payload := map[string]any{"id": id, "name": fc.Name, "dur_ms": dur, "turn": t.n}
		if out.err != nil {
			res.Error = out.err.Error()
			payload["error"] = res.Error
		} else {
			payload["result"] = llm.TruncateRunes(out.result, logResultBytes)
		}
		b.ingest(Message{Role: RoleTool, Kind: KindToolResult, Content: out.result, Agent: out.agent, ToolResult: res}, t.n)
		b.log.Log(logSource, "brain.tool_result", "", payload)
		if p := out.promise; p != nil {
			b.ingest(Message{Role: RoleUser, Kind: KindPromise, Content: promiseLine(*p), Agent: p.Agent, Promise: p}, t.n)
		}
	}
}

// fail ends a turn without a model answer. When an action was already
// sent it returns a reply reporting it (nothing may be sent twice by the
// fallback path); otherwise ErrFallback wrapping the cause.
func (b *Brain) fail(t *turn, reason string, cause error) (Reply, error) {
	payload := map[string]any{"reason": reason, "kind": string(t.req.Kind), "trigger": string(t.req.Trigger), "turn": t.n, "acted": len(t.acted) > 0}
	if cause != nil {
		payload["error"] = cause.Error()
	}
	b.log.Log(logSource, "brain.fallback", "", payload)
	if len(t.acted) > 0 {
		reply := Reply{Spoken: strings.Join(t.acted, " "), Detail: "The supervisor acted but ran out of budget before reporting in full."}
		b.ingest(Message{Role: RoleAssistant, Kind: KindReply, Content: mustJSON(reply)}, t.n)
		b.logTurn(t, false)
		return reply, nil
	}
	b.logTurn(t, true)
	if cause == nil {
		cause = errors.New(reason)
	}
	return Reply{}, fmt.Errorf("%w (%s): %w", ErrFallback, reason, cause)
}

// logTurn emits brain.turn.
func (b *Brain) logTurn(t *turn, fallback bool) {
	b.log.Log(logSource, "brain.turn", "", map[string]any{
		"turn": t.n, "trigger": string(t.req.Trigger), "kind": string(t.req.Kind), "steps": t.steps, "model_calls": t.calls,
		"dur_ms": b.cfg.Now().Sub(t.start).Milliseconds(), "fallback": fallback, "tokens_in": t.tokIn, "tokens_out": t.tokOut,
		"window_tokens": b.engine.Tokens(), "acted": len(t.acted),
	})
}

// afterTurn prunes, compacts and hands the lock over.
func (b *Brain) afterTurn(t *turn) {
	defer b.after.Done()
	defer b.lock.release()
	b.engine.AfterTurn(t.n)
	if b.engine.Tokens() <= b.cfg.WindowTokens {
		return
	}
	ctx, cancel := context.WithTimeout(b.ctx, b.cfg.CompactTimeout)
	defer cancel()
	t0 := b.cfg.Now()
	c, ok, err := b.engine.Compact(ctx, b.cfg.WindowTokens)
	payload := map[string]any{"turn": t.n, "dur_ms": b.cfg.Now().Sub(t0).Milliseconds()}
	switch {
	case err != nil:
		payload["error"] = err.Error()
	case !ok:
		return
	default:
		payload["before_tokens"], payload["after_tokens"], payload["dropped"] = c.BeforeTokens, c.AfterTokens, c.Dropped
		b.mu.Lock()
		b.lastCompaction = CompactionInfo{BeforeTokens: c.BeforeTokens, AfterTokens: c.AfterTokens, Dropped: c.Dropped, SummarySeq: c.Summary.Seq, At: b.cfg.Now()}
		b.mu.Unlock()
	}
	b.log.Log(logSource, "brain.compaction", "", payload)
}

// Quiesce waits for after-turn work (pruning, compaction) to finish.
func (b *Brain) Quiesce() { b.after.Wait() }

// systemPrompt assembles the sections and logs brain.system_prompt once
// per run.
func (b *Brain) systemPrompt() string {
	b.mu.Lock()
	logged := b.promptLogged
	b.promptLogged = true
	b.mu.Unlock()
	secs := b.sections()
	text := joinSections(secs)
	if !logged {
		var report []map[string]any
		for _, s := range secs {
			report = append(report, map[string]any{"name": s.Name, "chars": len(s.Text)})
		}
		b.log.Log(logSource, "brain.system_prompt", "", map[string]any{"sections": report, "total_chars": len(text), "model": b.cfg.Model, "tools": len(b.tools[0].FunctionDeclarations)})
	}
	return text
}

// sections assembles the system prompt's sections from the current
// prompt files, roster and ledger (no host yet: an empty roster).
func (b *Brain) sections() []PromptSection {
	b.mu.Lock()
	host := b.host
	userVars := b.userVars
	b.mu.Unlock()
	var agents []AgentInfo
	var notes []string
	var promises []PromiseInfo
	if host != nil {
		agents, notes, promises = host.Agents(), host.Notes(), host.Promises()
	}
	secs := append(append([]PromptSection(nil), stableSections(b.cfg.Prompts)...), userSection(b.cfg.Prompts, mergeUserVars(agents, userVars)))
	return append(secs, dynamicSections(agents, notes, promises, b.cfg.Now())...)
}

// PromptSections returns the system prompt as it would be assembled for
// the next model call: every section with its text and where it came
// from (the Supervisor tab's prompt-budget meter). Computed live, so a
// prompt reload shows at once.
func (b *Brain) PromptSections() []PromptSection { return b.sections() }

// nextToolID returns a call id unique within the run.
func (b *Brain) nextToolID(turn int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.toolSeq++
	return fmt.Sprintf("bt-%d-%d", turn, b.toolSeq)
}

// textOf concatenates the response's text parts (thoughts excluded)
// without the SDK's stdout warning about non-text parts.
func textOf(resp *genai.GenerateContentResponse) string {
	if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range resp.Candidates[0].Content.Parts {
		if p != nil && p.Text != "" && !p.Thought {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// parseReply decodes the answer contract from the model's text. An
// empty spoken is an error except when allowEmpty (an event turn: "the
// template was sufficient").
func parseReply(text string, allowEmpty bool) (Reply, error) {
	raw, err := llm.ExtractJSON(text)
	if err != nil {
		return Reply{}, err
	}
	var r Reply
	if err := json.Unmarshal(raw, &r); err != nil {
		return Reply{}, fmt.Errorf("brain: decode reply: %w", err)
	}
	r.Spoken = strings.TrimSpace(r.Spoken)
	if r.Spoken == "" && !allowEmpty {
		return Reply{}, errors.New("brain: reply without spoken text")
	}
	return r, nil
}

// reasonFor classifies a model or context error for brain.fallback.
func reasonFor(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, ErrAllKeysRateLimited):
		return "rate_limited"
	default:
		return "model_error"
	}
}

// thinkingLevel maps the config word onto the SDK constant.
func thinkingLevel(s string) genai.ThinkingLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "minimal":
		return genai.ThinkingLevelMinimal
	case "low":
		return genai.ThinkingLevelLow
	case "medium":
		return genai.ThinkingLevelMedium
	case "high":
		return genai.ThinkingLevelHigh
	default:
		return ""
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
