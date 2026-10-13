// Package orchestrator is the orchestrator, the orchestrator of docs/roles.md (plan
// D7-revised): a stateful agent that answers the user's relayed
// utterances and the supervisor's event requests by reasoning with tools
// over a function-calling model,
// instead of the stateless one-shot calls that lost the conversational
// referent at the tool boundary.
//
// # Turn
//
// Brain.Turn runs one turn: the request is appended to the
// transcript after the user's verbatim words, the context window is
// assembled into model contents, and the loop calls the Model; every
// function call it returns is executed (read_screen — the pane, or with
// source session the worker's own session record, docs/history/dive-deep.md
// WP8a — run_cli, list_agents, recall; prompt, send_keys, interrupt,
// promise, note, investigate)
// through the Host, the responses are
// appended, and the loop repeats until the model answers with the JSON
// reply of the answer contract. A turn has a step budget and a wall clock;
// exceeding either, or any model error, returns ErrFallback so the
// supervisor answers for itself (the voice never hangs on a loop),
// unless an action was already sent, in which case the reply reports it so
// nothing is sent twice.
//
// Turns are serialised by a single writer with two priorities: a turn
// triggered by a user utterance runs before one triggered by an event, and
// a turn in progress is never interleaved. An event turn also gives way:
// before each model call it checks for a waiting user turn and, when there
// is one, stops with ErrYielded (brain.yielded, a fallback turn) so the
// user turn runs next; the step in flight is never cut short. At most one
// event turn waits per agent: a newer event about the same agent replaces
// the queued one (brain.event_coalesced; the old turn fails with
// ErrCoalesced before it starts).
//
// # Event turns
//
// The supervisor runs one KindAnnounce turn on a worker event (docs/roles.md
// "Pushes"): the request carries the code sentence the daemon speaks when
// the orchestrator does not answer in time, the pane read as a KindEventScreen
// message instead of reading again, and the promises the event keeps. The reply
// adds Urgency — interrupt, idle, silent or skip — and KeepsPromiseIDs to
// the answer contract; an empty Spoken is a skip. Every reply may carry
// AnswersEarlier, the ids of the state block's unanswered questions it
// settles (UnansweredHost). The orchestrator decides
// whether and how urgently; the supervisor applies the chattiness
// ceiling and decides nothing about the moment (the push queue does).
// Announce turns are TriggerEvent, bounded by Config.AnnounceTurnTimeout
// (Config.AnnounceTimeout when the user is owed the line: it keeps a
// promise, or Request.Owed),
// declared only the read tools (the executor refuses prompt, send_keys,
// interrupt, promise and investigate in any event turn), sent with
// thinking off whatever is configured (docs/iteration-2.md WP5:
// turnThinking), and yield to a waiting user turn.
//
// A KindInvestigation turn is the other event turn: it delivers a
// finished background job (the investigate tool, docs/roles.md
// "Investigations and explanations"). Its request carries the job's
// question, the user's own words and the sub-agent's answer wrapped as
// worker output, and its reply is the explanation the supervisor pushes
// at the next pause. It takes the utterance budget rather than the
// announce one — the answer is what the user has been waiting for, not a
// line racing a code sentence — and it coalesces only with a newer
// delivery about the same agent, never with a worker event.
//
// # Promises
//
// "Tell me when it's done" is promised work: in a user turn the model
// calls promise(agent, text, event) and the Host records it (the
// supervisor keeps it in the state file and pushes it at the matching
// event whatever the chattiness level). The transcript gets a
// KindPromise message when it is made and, through Brain.PromiseEvent,
// when it is kept, expired or cancelled; the ledger section lists the
// open ones and recall searches them.
//
// # The one rail
//
// send_keys answers a worker's approval prompt, which is bypassing its
// gate. The executor permits it only when the turn was triggered by a user
// utterance, the supervisor has a pending answer_prompt for that pane, and
// every key belongs to that prompt's option set; the Host re-checks the
// pending action atomically at send time. Anything else is refused with an
// error the model sees and a brain.rail_blocked event. Everything read from
// a worker pane or produced by the CLI tool is wrapped in provenance
// delimiters and declared data, not instructions, in the system prompt.
//
// # Transcript and context engine
//
// One Transcript per daemon run is appended to <data_dir>/brain/<run-id>.jsonl,
// one JSON Message per line, complete: User utterances and assistant replies
// (from the voice transcript events), pushes, worker state changes, notes,
// requests, the orchestrator's own tool calls and results, replies and compaction
// summaries. The ContextEngine decides what of it the model sees: the
// default engine keeps an in-memory window, holds a bulky tool result's
// last 4 KB only (the transcript keeps it whole), prunes it to a
// one-line stub after Config.PruneAfterTurns turns, and compacts the
// oldest half into one summary message of at most 600 tokens when the
// window exceeds Config.WindowTokens (estimated; 12 k by default since
// docs/iteration-2.md WP5) — after the turn lock is released, and only
// over the messages it summarised if they are still there. The seam for the memory system (plan
// P6) is that interface: Ingest / Assemble / Compact / AfterTurn, borrowed
// from OpenClaw. A memory-backed engine plugs in through Config.Engine and
// may return a system-prompt addition from Assemble (recalled facts) and
// serve recall() from durable storage instead of the run's transcript;
// the disk transcript is its ingestion source. The transcript is in
// arrival order, so the assistant's speech can land between a step's
// tool call and its result; both request renderers visit the window in
// modelOrder, which keeps a step's calls and results adjacent and moves
// such a line to just after them (a step left without its results is
// dropped), and the compaction cut never falls inside a step.
//
// # Prompt
//
// The system prompt is sectioned and holds only stable content, so a
// provider's prefix cache applies to it and to the conversation after
// it: role (prompts/svp/SOUL.md), tool guidance with the provenance
// rule, the one rail and the answer contract (prompts/svp/RULES.md) and
// USER.md. The dynamic part — agent roster, ledger excerpt,
// investigations, time last — is the state block, the last message of
// every request (KindState, never ingested; docs/iteration-2.md WP5),
// every free text in it clipped so four agents stay near 2.5 k
// characters.
//
// Everything is observable through brain.* events on the logging.Logger:
// brain.turn, brain.step, brain.tool_call / brain.tool_result (paired by
// id), brain.rail_blocked (a refused send_keys, or an action tool named
// in an event turn), brain.compaction, brain.tool_timeout,
// brain.failover / brain.failover_notice, brain.fallback, brain.yielded,
// brain.event_coalesced, brain.system_prompt and brain.rate_limited
// (decision D10).
package brain
