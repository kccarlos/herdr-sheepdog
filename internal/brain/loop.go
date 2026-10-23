package brain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/prose"
)

// logResultBytes is how much of a tool result a brain.tool_result keeps.
const logResultBytes = 1024

// turn is the bookkeeping of one Turn call.
type turn struct {
	n   int
	req Request
	// provider, model and impl are snapshotted when the turn started: a
	// SetModel during the turn never lands between its tool rounds.
	provider, model string
	impl            Model
	start           time.Time
	// queued is the wall-clock time Turn was called (trace.go).
	queued time.Time
	// soft is when the budget for tool rounds ends; the hard deadline is
	// FinalGrace later.
	soft time.Time
	// failedOver is set once a step was retried on another provider
	// (generateStep): provider, model and impl are that one's from then
	// on, and there is no second failover in the turn.
	failedOver bool
	// retried is set once a step was retried on the same provider after
	// a 499 CANCELLED (generateStep): once per turn.
	retried bool
	// tried are the providers this turn already left (failed over away
	// from, or routed around as down): never picked again in the turn.
	tried []string
	// alerts are the account problems this turn found first (markDown):
	// told once, after the turn (tellAccount).
	alerts   []AccountAlert
	steps    int
	calls    int // model calls
	tokIn    int
	tokOut   int
	acted    []string        // spoken echoes of actions sent
	actedOn  []string        // the panes those actions went to, each once
	prompted map[string]bool // pane + instruction already typed this turn (execTool)
	// notVerified: the tool calls that timed out (Reply.NotVerified);
	// consulted: the aliases the tools named (Reply.Agents).
	notVerified []string
	consulted   []string
	// shown: the titles of the cards show put on the screen (Reply.Shown).
	shown []string
	// prior is the answer an event turn wrote before it learned a line
	// about its event had gone out meanwhile (recheck): the turn asks
	// once more with that fact; should the second step fail, prior is
	// returned only for an owed event (fail).
	prior *Reply
}

// Turn runs one turn (an empty kind is an utterance). The reply is
// returned as soon as the model answers; pruning, compaction and the hand-
// over to the next queued turn happen afterwards without blocking the
// caller. ErrFallback (wrapping the cause) means the supervisor should
// speak its own code sentence instead.
func (b *Brain) Turn(ctx context.Context, req Request) (Reply, error) {
	if req.Kind == "" {
		req.Kind = KindUtterance
	}
	if req.Trigger == "" {
		req.Trigger = TriggerCEO
	}
	b.mu.Lock()
	host, closed := b.host, b.closed
	if !closed {
		b.inFlight.Add(1)
		b.activeTurns.Add(1)
	}
	b.mu.Unlock()
	if closed {
		return Reply{}, fmt.Errorf("%w: %w", ErrFallback, ErrClosed)
	}
	defer b.inFlight.Done()
	defer b.activeTurns.Add(-1)
	if host == nil {
		return Reply{}, fmt.Errorf("%w: %w", ErrFallback, ErrNoHost)
	}
	// An utterance may turn out to be an instruction, so it gets the
	// longer budget: the orchestrator decides what it is (docs/dev/roles.md "One
	// decision-maker").
	timeout := b.cfg.UtteranceTimeout
	switch {
	case req.Kind == KindInvestigation:
		// The answer the user is owed, composed from a result that took
		// minutes to get: it gets the longer budget, not the announce
		// turn's, which exists to beat the code sentence.
		timeout = b.cfg.UtteranceTimeout
		req.Trigger = TriggerEvent
	case req.Kind.IsEvent():
		timeout = b.cfg.AnnounceTurnTimeout
		if req.Owed || len(req.Promises) > 0 {
			// The user is owed this line (docs/dev/roles.md "Pushes"):
			// it gets the longer budget.
			timeout = b.cfg.AnnounceTimeout
		}
		req.Trigger = TriggerEvent
	}
	// The wait for the turn lock is bounded on its own; the budget for
	// the turn's steps starts when the lock is handed over. Time queued
	// behind other turns is not the model's time: with it counted, an
	// announce turn that waited 8 s of its 9 s behind a user turn started
	// a step with 1 s left and timed out while steps take ~2 s
	// (docs/dev/eval/analysis-clustered-events-2026-10-12.md). A user turn
	// waits at most its own budget (event turns yield to it, so it waits
	// for a step in flight or another user turn). An event turn waits
	// behind user turns, so it may wait as long as one user turn may hold
	// the lock, or its own budget when that is longer.
	wait := timeout
	if req.Trigger == TriggerEvent && b.cfg.UtteranceTimeout > wait {
		wait = b.cfg.UtteranceTimeout
	}
	qctx, qcancel := context.WithTimeout(ctx, wait+b.cfg.FinalGrace)
	defer qcancel()
	// Close ends a wait or a turn in flight, so neither waits out its bound.
	defer context.AfterFunc(b.ctx, qcancel)()
	queued := time.Now() // wall clock, for brain.turn_started's queue_ms (trace.go)
	key := ""
	if req.Trigger == TriggerEvent {
		// Event turns about the same agent coalesce — but a delivery
		// turn is not a worker event: an answer the user waited minutes
		// for must not be dropped because the same agent printed a line
		// meanwhile, so it coalesces only with a newer delivery.
		key = req.Agent
		if req.Kind == KindInvestigation {
			key = string(KindInvestigation) + ":" + req.Agent
		}
	}
	replaced, err := b.lock.acquire(qctx, req.Trigger == TriggerCEO, key)
	if replaced {
		b.log.Log(logSource, "brain.event_coalesced", "", map[string]any{"agent": req.Agent, "event": req.Event, "kind": string(req.Kind)})
	}
	if err != nil {
		reason := "queue_timeout"
		if errors.Is(err, ErrCoalesced) {
			reason = "coalesced"
		}
		payload := map[string]any{"reason": reason, "kind": string(req.Kind), "trigger": string(req.Trigger), "agent": req.Agent, "error": err.Error()}
		if req.UtteranceID != "" {
			payload["utterance_id"] = req.UtteranceID
		}
		b.log.Log(logSource, "brain.fallback", "", payload)
		return Reply{}, fmt.Errorf("%w: waiting for the turn lock: %w", ErrFallback, err)
	}
	tctx, cancel := context.WithTimeout(ctx, timeout+b.cfg.FinalGrace)
	defer cancel()
	defer context.AfterFunc(b.ctx, cancel)()
	start := b.cfg.Now()
	t := &turn{req: req, start: start, soft: start.Add(timeout), queued: queued}
	t.provider, t.model, t.impl = b.current()
	reply, err := b.run(tctx, t)
	b.tellAccount(t, err)
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
		// Whatever the user said before this tool call is the utterance
		// that caused it, final or not.
		b.flushInLocked()
	}
	b.mu.Unlock()
	b.traceTurn(t)
	b.routeAroundDown(t)
	if t.req.Facts != nil {
		// Now, not when the turn was asked for: it may have queued
		// behind another turn while the template line went out.
		t.req.facts = t.req.Facts()
	}
	if t.req.Kind == KindUtterance {
		// What the daemon knows about the agents the words name, and that
		// it knows nothing more (run C, H7: "did it push?" answered "no"
		// from a screen that says nothing about pushing).
		t.req.records = agentRecords(b.host.Agents(), t.req.Question, t.req.Focus, b.cfg.Now)
	}
	words := ""
	if t.req.Kind == KindUtterance && t.req.Trigger != TriggerEvent {
		words = t.req.Question
	}
	b.ingest(Message{Role: RoleUser, Kind: KindRequest, Content: requestText(t.req), Words: words, Agent: t.req.Agent, Link: t.req.Link}, t.n)
	if t.req.Kind == KindAnnounce && t.req.Agent != "" {
		// What the worker itself wrote for a human, before its screen:
		// the finding in its words, not its working notes
		// (docs/dev/iteration-2.md WP4).
		if a, ok := b.host.Resolve(t.req.Agent); ok {
			if text := workerWrote(a); text != "" {
				b.ingest(Message{Role: RoleUser, Kind: KindEventScreen, Content: wrapWorker(t.req.Agent, "transcript", text), Agent: t.req.Agent,
					ToolResult: &ToolResult{Name: toolWorkerLog, Summary: fmt.Sprintf("what %s wrote at %s", aliasOr(t.req.Agent), t.req.Event)}}, t.n)
			}
		}
	}
	if t.req.Screen != "" {
		// The supervisor's own read for this event: in the window like a
		// read_screen result (wrapped, pruned to a stub later).
		text := prose.TailBytes(t.req.Screen, maxResultBytes)
		b.ingest(Message{Role: RoleUser, Kind: KindEventScreen, Content: wrapWorker(t.req.Agent, "screen", text), Agent: t.req.Agent,
			ToolResult: &ToolResult{Name: ToolReadScreen, Summary: fmt.Sprintf("screen of %s at %s, %d lines", aliasOr(t.req.Agent), t.req.Event, strings.Count(text, "\n")+1)}}, t.n)
	}

	system, secs, state := b.systemPrompt()
	base := b.turnConfig(t.req.Kind, t.req.Trigger)
	corrected := false
	for step := 1; step <= b.cfg.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return b.fail(t, reasonFor(err), err)
		}
		if t.req.Trigger == TriggerEvent {
			// The user comes first: an event turn gives way before its
			// next model call when a user turn is waiting for the lock
			// (the step in flight is never cut short; a turn that
			// answered in the step before returns its answer).
			if n := b.lock.ceoWaiting(); n > 0 {
				b.log.Log(logSource, "brain.yielded", "", map[string]any{"turn": t.n, "kind": string(t.req.Kind), "agent": t.req.Agent, "event": t.req.Event, "step": step, "steps_done": t.steps, "ceo_waiting": n})
				return b.fail(t, "yielded", ErrYielded)
			}
		}
		t.steps = step
		if step > 1 && t.req.Facts != nil {
			// What happened to the event during the previous step (the
			// template line spoken at announce_wait, the prompt answered).
			now := t.req.Facts()
			if note := factsUpdate(t.req.facts, now); note != "" {
				b.ingest(Message{Role: RoleUser, Kind: KindSystem, Content: note, Agent: t.req.Agent}, t.n)
			}
			t.req.facts = now
		}
		// The last step, or one starting with too little budget left for
		// another tool round, is answer-only: the model must reply from
		// what it has.
		forced := step == b.cfg.MaxSteps || (step > 1 && t.soft.Sub(b.cfg.Now()) < b.cfg.StepReserve) || t.prior != nil
		config := base // one config per step: the fake model keeps the pointer
		if forced {
			config.ToolConfig = &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeNone}}
			if t.prior == nil { // a recheck's note says what to do
				b.ingest(Message{Role: RoleUser, Kind: KindRequest, Content: "Time is up: answer now with the JSON object of the answer contract from what you have, without further tool calls."}, t.n)
			}
		}
		asm := b.engine.Assemble(b.cfg.WindowTokens)
		text := systemWith(system, asm.SystemAddition)
		config.SystemInstruction = &genai.Content{Parts: []*genai.Part{{Text: text}}}
		if step > 1 {
			state = b.stateNow()
		}
		msgs := withState(asm.Messages, state, t.n)

		t0 := b.cfg.Now()
		resp, raw, err := b.generateStep(ctx, t, step, msgs, &config)
		t.calls++
		dur := b.cfg.Now().Sub(t0)
		b.captureStep(t, step, forced, secs, asm.SystemAddition, msgs, &config, t0, dur, resp, err)
		payload := map[string]any{"turn": t.n, "step": step, "provider": t.provider, "model": t.model, "template": string(t.req.Kind), "dur_ms": dur.Milliseconds(), "forced": forced}
		var calls []*genai.FunctionCall
		if err != nil {
			payload["error"] = err.Error()
			if reasonFor(err) == "timeout" {
				// A call that timed out reports no usage: log the
				// assembled prompt's estimated size, to tell a slow
				// provider from an oversized window.
				payload["prompt_tokens_est"] = promptTokens(text, msgs)
			}
		} else {
			if u := resp.UsageMetadata; u != nil {
				payload["prompt_tokens"], payload["completion_tokens"] = u.PromptTokenCount, u.CandidatesTokenCount
				if u.ThoughtsTokenCount > 0 {
					// Reasoning is billed as output: DeepSeek counts it in
					// completion_tokens, Gemini separately.
					payload["reasoning_tokens"] = u.ThoughtsTokenCount
				}
				if u.CachedContentTokenCount > 0 {
					// The provider's prefix cache (DeepSeek's
					// prompt_cache_hit_tokens, Gemini's cached content):
					// how much of the stable prefix was reused
					// (docs/dev/iteration-2.md WP5).
					payload["cache_hit_tokens"] = u.CachedContentTokenCount
				}
				t.tokIn += int(u.PromptTokenCount)
				t.tokOut += int(u.CandidatesTokenCount)
			}
			if resp.ModelVersion != "" {
				// The model that actually answered, as the provider
				// reports it: under Anthropic's server-side fallbacks it
				// can differ from model, which is what was asked for.
				payload["served_model"] = resp.ModelVersion
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
					b.ingest(t.reply(answer, raw), t.n)
					b.ingest(Message{Role: RoleUser, Kind: KindRequest, Content: "That was not the JSON object of the answer contract. Reply with the JSON object only."}, t.n)
					continue
				}
				return b.fail(t, "bad_reply", perr)
			}
			b.ingest(t.reply(answer, raw), t.n)
			reply.Turn, reply.Acted, reply.ActedOn = t.n, len(t.acted) > 0, t.actedOn
			reply.NotVerified, reply.Agents, reply.Shown = t.notVerified, t.consulted, t.shown
			if b.recheck(t, step, reply) {
				continue
			}
			b.answeredEarlier(reply.AnswersEarlier)
			b.logTurn(t, false)
			return reply, nil
		}
		b.execCalls(ctx, t, resp, raw, calls)
	}
	return b.fail(t, "step_budget", fmt.Errorf("brain: %d steps without an answer", b.cfg.MaxSteps))
}

// answeredEarlier hands a reply's answers_earlier to the Host, which
// clears those unanswered questions (UnansweredHost); nothing without
// one.
func (b *Brain) answeredEarlier(ids []string) {
	if len(ids) == 0 {
		return
	}
	b.mu.Lock()
	host := b.host
	b.mu.Unlock()
	if uh, ok := host.(UnansweredHost); ok {
		uh.AnsweredEarlier(ids)
	}
}

// generate is one model call of the turn: through GenerateMessages when
// the turn's Model builds its request from the window itself (it then
// also returns the provider-native message to store), else through
// Generate over Gemini contents, where the content to store is the
// candidate's (the function call parts with their thought signatures).
func (b *Brain) generate(ctx context.Context, t *turn, msgs []Message, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, json.RawMessage, error) {
	if mm, ok := t.impl.(MessageModel); ok {
		return mm.GenerateMessages(ctx, t.model, msgs, cfg)
	}
	resp, err := t.impl.Generate(ctx, t.model, toContents(msgs, t.provider, t.model), cfg)
	if err != nil {
		return nil, nil, err
	}
	var raw json.RawMessage
	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil && len(resp.FunctionCalls()) > 0 {
		raw, _ = json.Marshal(resp.Candidates[0].Content)
	}
	return resp, raw, nil
}

// reply is the transcript message of the model's answer text; raw (when
// the provider returned something to replay, such as DeepSeek's
// reasoning_content) travels with it under the turn's provider and
// model.
func (t *turn) reply(text string, raw json.RawMessage) Message {
	m := Message{Role: RoleAssistant, Kind: KindReply, Content: text}
	if len(raw) > 0 {
		m.Raw, m.Provider, m.Model = raw, t.provider, t.model
	}
	return m
}

// execCalls records the model's content (every call first: the step is
// one assistant message to the provider, whatever the number of calls),
// executes every call and records the results; a promise a call made
// is recorded after the last result, so nothing sits between a step's
// results.
func (b *Brain) execCalls(ctx context.Context, t *turn, resp *genai.GenerateContentResponse, raw json.RawMessage, calls []*genai.FunctionCall) {
	modelText := strings.TrimSpace(textOf(resp))
	ids := make([]string, len(calls))
	for i, fc := range calls {
		// A provider that issues call ids (OpenAI-compatible) needs the
		// same id on the result; Gemini issues none and gets the run's.
		id := fc.ID
		if id == "" {
			id = b.nextToolID(t.n)
		}
		ids[i] = id
		m := Message{Role: RoleAssistant, Kind: KindToolCall, Provider: t.provider, Model: t.model, ToolCall: &ToolCall{ID: id, Name: fc.Name, Args: fc.Args}}
		if i == 0 {
			m.Raw, m.Content = raw, modelText
		}
		b.ingest(m, t.n)
		args := fc.Args
		if fc.Name == ToolShow {
			args = redactShowArgs(args)
		}
		b.log.Log(logSource, "brain.tool_call", "", map[string]any{"id": id, "name": fc.Name, "args": args, "turn": t.n})
	}
	var promises []Message
	for i, fc := range calls {
		id := ids[i]
		t0 := b.cfg.Now()
		out, limit := b.runTool(ctx, t, fc)
		dur := b.cfg.Now().Sub(t0).Milliseconds()
		if limit > 0 {
			b.log.Log(logSource, "brain.tool_timeout", "", map[string]any{"id": id, "name": fc.Name, "timeout_ms": limit.Milliseconds(), "agent": out.agent, "turn": t.n})
			t.notVerified = append(t.notVerified, notVerifiedLine(fc.Name, out.agent, limit))
		}
		if out.agent != "" && !slices.Contains(t.consulted, out.agent) {
			t.consulted = append(t.consulted, out.agent)
		}
		if out.railBlocked != "" {
			b.log.Log(logSource, "brain.rail_blocked", "", map[string]any{"id": id, "agent": out.agent, "keys": keysArg(fc.Args["keys"]), "trigger": string(t.req.Trigger), "reason": out.railBlocked, "turn": t.n})
		}
		if out.shown != "" {
			t.shown = append(t.shown, out.shown)
		}
		if out.acted != "" {
			t.acted = append(t.acted, out.acted)
			if out.pane != "" && !slices.Contains(t.actedOn, out.pane) {
				t.actedOn = append(t.actedOn, out.pane)
			}
		}
		res := &ToolResult{ID: id, Name: fc.Name, DurMS: dur, Summary: out.summary}
		payload := map[string]any{"id": id, "name": fc.Name, "dur_ms": dur, "turn": t.n}
		if out.err != nil {
			res.Error = out.err.Error()
			payload["error"] = res.Error
		} else {
			payload["result"] = prose.TruncateRunes(out.result, logResultBytes)
		}
		b.ingest(Message{Role: RoleTool, Kind: KindToolResult, Content: out.result, Agent: out.agent, ToolResult: res}, t.n)
		b.log.Log(logSource, "brain.tool_result", "", payload)
		if p := out.promise; p != nil {
			promises = append(promises, Message{Role: RoleUser, Kind: KindPromise, Content: promiseLine(*p), Agent: p.Agent, Promise: p})
		}
	}
	for _, m := range promises {
		b.ingest(m, t.n)
	}
}

// recheck reports whether an event turn's answer must be asked for once
// more because the facts changed while its last step ran: a line about
// the event was handed to the voice (the block template at
// brain.announce_wait_s) after the request was rendered. factsUpdate
// runs only before a later step, so a turn that answers in one step —
// the common case on a slow provider (run C: 27 of 55 Vertex runs that
// started blocked heard the block twice) — wrote its line as the news
// the template had already carried. The update goes in the window and
// one more answer-only step runs; whether to skip, add or correct is the
// model's (docs/dev/timelines.md, "announce wait"). Once per turn, never past MaxSteps,
// never for a user turn, never for a line that would not be spoken.
func (b *Brain) recheck(t *turn, step int, reply Reply) bool {
	if t.prior != nil || t.req.Trigger != TriggerEvent || t.req.Facts == nil || step >= b.cfg.MaxSteps {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(reply.Urgency)) {
	case UrgencySkip, UrgencySilent:
		return false
	}
	if strings.TrimSpace(reply.Spoken) == "" {
		return false
	}
	now := t.req.Facts()
	if len(now.Spoken) <= len(t.req.facts.Spoken) {
		return false
	}
	note := factsUpdate(t.req.facts, now)
	t.req.facts = now
	t.prior = &reply
	b.log.Log(logSource, "brain.recheck", "", map[string]any{"turn": t.n, "kind": string(t.req.Kind), "agent": t.req.Agent, "event": t.req.Event, "step": step, "spoken_meanwhile": len(now.Spoken)})
	b.ingest(Message{Role: RoleUser, Kind: KindSystem, Content: note + " Your answer above was written before that and has not been spoken: answer again, now, with the JSON object of the answer contract and no tool calls.", Agent: t.req.Agent}, t.n)
	return true
}

// fail ends a turn without a model answer. When an action was already
// sent it returns a reply reporting it (nothing may be sent twice by the
// fallback path); otherwise ErrFallback wrapping the cause.
func (b *Brain) fail(t *turn, reason string, cause error) (Reply, error) {
	if t.prior != nil {
		// The recheck step did not answer. The first answer was written
		// as if nothing had been said, so it is no answer to the event as
		// it stands — except on an owed event (a kept promise, the user's
		// own request), where it is the finding they are waiting for and
		// stands as it would have without the recheck. Otherwise the turn
		// falls back like any other: the line already spoken carried the
		// event. On Vertex the recheck often runs out of the 6 + 3 s
		// announce budget (run C: first steps at 2–6 s, step p90 8 s).
		owed := t.req.Owed || len(t.req.Promises) > 0
		b.log.Log(logSource, "brain.recheck_failed", "", map[string]any{"turn": t.n, "reason": reason, "kept_first": owed && reason != "yielded"})
		if owed && reason != "yielded" {
			b.logTurn(t, false)
			return *t.prior, nil
		}
	}
	payload := map[string]any{"reason": reason, "kind": string(t.req.Kind), "trigger": string(t.req.Trigger), "turn": t.n, "acted": len(t.acted) > 0}
	if cause != nil {
		payload["error"] = cause.Error()
	}
	b.log.Log(logSource, "brain.fallback", "", payload)
	if len(t.acted) > 0 {
		reply := Reply{Spoken: strings.Join(t.acted, " "), Detail: "The supervisor acted but ran out of budget before reporting in full."}
		b.ingest(Message{Role: RoleAssistant, Kind: KindReply, Content: mustJSON(reply)}, t.n)
		b.logTurn(t, false)
		reply.Turn, reply.Acted, reply.ActedOn = t.n, true, t.actedOn
		reply.NotVerified, reply.Agents, reply.Shown = t.notVerified, t.consulted, t.shown
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
		"turn": t.n, "trigger": string(t.req.Trigger), "kind": string(t.req.Kind), "provider": t.provider, "model": t.model, "steps": t.steps, "model_calls": t.calls,
		"dur_ms": b.cfg.Now().Sub(t.start).Milliseconds(), "fallback": fallback, "tokens_in": t.tokIn, "tokens_out": t.tokOut,
		"window_tokens": b.engine.Tokens(), "acted": len(t.acted),
	})
}

// afterTurn prunes, hands the lock over, then compacts. The summary call
// runs without the turn lock (it took up to 14 s in the logs, and the
// user's next utterance queued behind it): the engine discards a summary
// whose messages changed meanwhile (ErrCompactionStale), and at most one
// compaction runs at a time — the next turn's after-work retries.
func (b *Brain) afterTurn(t *turn) {
	defer b.after.Done()
	b.engine.AfterTurn(t.n)
	if b.engine.Tokens() <= b.cfg.WindowTokens {
		b.lock.release()
		return
	}
	// The slot is claimed before the lock goes, so a Reset that takes
	// the lock next always finds this compaction to cancel.
	ctx, done, ok := b.startCompaction()
	b.lock.release()
	if !ok {
		return
	}
	defer done()
	t0 := b.cfg.Now()
	c, ok, err := b.engine.Compact(ctx, b.cfg.WindowTokens)
	payload := map[string]any{"turn": t.n, "dur_ms": b.cfg.Now().Sub(t0).Milliseconds()}
	switch {
	case errors.Is(err, ErrCompactionStale):
		payload["discarded"], payload["reason"] = true, "window_changed"
	case err != nil:
		payload["error"] = err.Error()
	case !ok:
		return
	default:
		payload["before_tokens"], payload["after_tokens"], payload["dropped"] = c.BeforeTokens, c.AfterTokens, c.Dropped
		b.mu.Lock()
		b.lastCompaction = CompactionInfo{BeforeTokens: c.BeforeTokens, AfterTokens: c.AfterTokens, Dropped: c.Dropped, SummarySeq: c.Summary.Seq, At: b.cfg.Now(), CoversTo: c.Summary.TS}
		b.mu.Unlock()
	}
	b.log.Log(logSource, "brain.compaction", "", payload)
}

// startCompaction claims the one compaction slot: ok is false while
// another compaction runs. ctx is bounded by CompactTimeout and ended by
// Close or Reset (stopCompaction); done frees the slot.
func (b *Brain) startCompaction() (ctx context.Context, done func(), ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.compactDone != nil {
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(b.ctx, b.cfg.CompactTimeout)
	ch := make(chan struct{})
	b.compactCancel, b.compactDone = cancel, ch
	return ctx, func() {
		cancel()
		b.mu.Lock()
		b.compactCancel, b.compactDone = nil, nil
		b.mu.Unlock()
		close(ch)
	}, true
}

// stopCompaction cancels a compaction in flight and waits for it (ctx
// bounds the wait): Reset must not let a summary of the old conversation
// land in the new segment.
func (b *Brain) stopCompaction(ctx context.Context) error {
	b.mu.Lock()
	cancel, ch := b.compactCancel, b.compactDone
	b.mu.Unlock()
	if ch == nil {
		return nil
	}
	cancel()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Quiesce waits for after-turn work (pruning, compaction) to finish.
func (b *Brain) Quiesce() { b.after.Wait() }

// systemPrompt assembles the sections and logs brain.system_prompt once
// per run. It returns the system text and the sections it was joined
// from — the stable ones (prompt files and USER.md) — and the state
// block rendered from the dynamic ones (roster, ledger, investigations,
// time), which travels after the window instead (withState): anything
// per-turn in the system message would end the provider's prefix cache
// before the conversation, which is most of the prompt
// (docs/dev/iteration-2.md WP5; the captures showed a flat 2.7 k cached
// tokens out of ~20 k).
func (b *Brain) systemPrompt() (string, []PromptSection, string) {
	b.mu.Lock()
	logged := b.promptLogged
	b.promptLogged = true
	b.mu.Unlock()
	all := b.sections()
	var secs, dynamic []PromptSection
	for _, s := range all {
		if s.Source == SourceDynamic {
			dynamic = append(dynamic, s)
			continue
		}
		secs = append(secs, s)
	}
	text := joinSections(secs)
	state := stateText(dynamic)
	if !logged {
		var report []map[string]any
		for _, s := range all {
			report = append(report, map[string]any{"name": s.Name, "chars": len(s.Text)})
		}
		provider, model, _ := b.current()
		b.log.Log(logSource, "brain.system_prompt", "", map[string]any{"sections": report, "total_chars": len(text), "state_chars": len(state), "provider": provider, "model": model, "tools": len(b.tools[0].FunctionDeclarations)})
	}
	return text, secs, state
}

// StateHeader opens the state block (braintest finds the block by it).
const StateHeader = "State as the daemon has it now (the roster, the record and the time, refreshed on every call; not said by anyone):"

// stateText is the state block: the header, then the dynamic sections.
func stateText(dynamic []PromptSection) string {
	if len(dynamic) == 0 {
		return ""
	}
	return StateHeader + "\n\n" + joinSections(dynamic)
}

// withState returns the window with the state block appended as the last
// message (KindState, never ingested), so everything before it — system
// prompt, tools and the transcript so far — is a byte-stable prefix from
// one call to the next.
func withState(msgs []Message, state string, turn int) []Message {
	if state == "" {
		return msgs
	}
	out := make([]Message, len(msgs), len(msgs)+1)
	copy(out, msgs)
	return append(out, Message{Role: RoleUser, Kind: KindState, Content: state, Turn: turn, Tokens: EstimateTokens(state)})
}

// sections assembles the system prompt's sections from the current
// prompt files, roster and ledger (no host yet: an empty roster).
func (b *Brain) sections() []PromptSection {
	b.mu.Lock()
	host := b.host
	userVars := b.userVars
	compaction := b.lastCompaction
	b.mu.Unlock()
	started := b.transcript.Started()
	var agents []AgentInfo
	var notes []string
	var promises []PromiseInfo
	var investigations []InvestigationInfo
	var language string
	var unanswered []UnansweredInfo
	var screen string
	if host != nil {
		agents, notes, promises, investigations = host.Agents(), host.Notes(), host.Promises(), host.Investigations()
		if lh, ok := host.(LanguageHost); ok {
			language = lh.LanguageLine()
		}
		if uh, ok := host.(UnansweredHost); ok {
			unanswered = uh.Unanswered()
		}
		if dh, ok := host.(DisplayHost); ok {
			screen = screenLine(dh.ScreenVisible())
		}
	}
	secs := append(append([]PromptSection(nil), stableSections(b.cfg.Prompts)...), userSection(b.cfg.Prompts, mergeUserVars(agents, userVars)))
	now := b.cfg.Now()
	record := recordLine(started, compaction, now)
	if screen != "" {
		record = strings.TrimSpace(record + "\n" + screen)
	}
	return append(secs, dynamicSections(agents, notes, promises, investigations, unanswered, language, now, record)...)
}

// stateNow renders the state block from the Host as it is now: the loop
// calls it before every model call, so a step sees what the step before
// it changed (an agent it prompted is working). At the end of the
// request it costs the prefix cache nothing.
func (b *Brain) stateNow() string {
	var dynamic []PromptSection
	for _, s := range b.sections() {
		if s.Source == SourceDynamic {
			dynamic = append(dynamic, s)
		}
	}
	return stateText(dynamic)
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
// code sentence was enough"); an ask with no spoken becomes the spoken.
// ask is optional (an old-shape reply has none).
func parseReply(text string, allowEmpty bool) (Reply, error) {
	raw, err := prose.ExtractJSON(text)
	if err != nil {
		return Reply{}, err
	}
	var r Reply
	if err := json.Unmarshal(raw, &r); err != nil {
		return Reply{}, fmt.Errorf("brain: decode reply: %w", err)
	}
	r.Spoken = strings.TrimSpace(r.Spoken)
	r.Ask = strings.TrimSpace(r.Ask)
	if r.Spoken == "" && r.Ask != "" {
		// A bare question is the whole line: every consumer reads
		// Spoken, so it goes there.
		r.Spoken, r.Ask = r.Ask, ""
	}
	if r.Spoken == "" && !allowEmpty {
		return Reply{}, errors.New("brain: reply without spoken text")
	}
	return r, nil
}

// reasonFor classifies a model or context error for brain.fallback.
// promptTokens estimates the size of one model call's prompt: the system
// text plus the window's messages.
func promptTokens(system string, msgs []Message) int {
	n := EstimateTokens(system)
	for _, m := range msgs {
		n += EstimateTokens(m.Content)
	}
	return n
}

func reasonFor(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, ErrAllKeysRateLimited):
		return "rate_limited"
	case isTransportError(err):
		return "transport"
	default:
		return "model_error"
	}
}

// turnConfig is the per-turn part of a model call's config: the tools
// (read-only on event turns) and the thinking setting. The system
// instruction is set per step (systemWith). The cache warm-up builds
// its request from the same function, so its prefix is the turn's.
func (b *Brain) turnConfig(kind Kind, trigger Trigger) genai.GenerateContentConfig {
	tools := b.tools
	if kind.ReadOnly() {
		tools = b.readTools
	}
	return genai.GenerateContentConfig{Tools: tools, ThinkingConfig: turnThinking(trigger, b.cfg.ThinkingLevel)}
}

// systemWith is the system text a step sends: the stable text plus the
// context engine's addition, when it has one.
func systemWith(system, addition string) string {
	if addition != "" {
		return system + "\n\n" + addition
	}
	return system
}

// turnThinking is a turn's thinking setting. An event turn (announce,
// investigation delivery) is a judgement call that needs no chain of
// thought: thinking off whatever is configured — Gemini's minimal level,
// DeepSeek's thinking disabled (buildRequest reads the minimal level so)
// — which the captures priced at ~470 reasoning tokens, ~2.3 s, per step
// (docs/dev/iteration-2.md WP5). A user turn keeps the configured level.
func turnThinking(trigger Trigger, level string) *genai.ThinkingConfig {
	if trigger == TriggerEvent {
		return thinkingOff()
	}
	if lvl := thinkingLevel(level); lvl != "" {
		return &genai.ThinkingConfig{ThinkingLevel: lvl}
	}
	return nil
}

// thinkingOff is the thinking setting of a call that should not think:
// Gemini's minimal level, which OpenAICompatModel sends as thinking
// disabled.
func thinkingOff() *genai.ThinkingConfig {
	return &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelMinimal}
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

// notVerifiedLine is one Reply.NotVerified entry: the tool, the agent it
// was about and the timeout that expired.
func notVerifiedLine(tool, agent string, limit time.Duration) string {
	line := tool + " timed out after " + seconds(limit)
	if agent != "" {
		line = tool + " about " + agent + " timed out after " + seconds(limit)
	}
	return line
}
