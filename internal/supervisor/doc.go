// Package supervisor is the daemon's brain (plan §3.6): pure Go logic over
// the herdr client, the state table, the cheap LLM templates and the voice
// session's push channel. It is the only component that touches herdr
// (decision D1).
//
// Supervisor.HandleTransition folds every debounced herdr transition into
// the state table and reacts: a `blocked` pane is read and summarised, the
// spoken sentence is pushed to the voice session, and — when the agent is
// really asking something — a single-slot answer_prompt PendingAction is
// created (replacing and announcing any earlier one). `done` gets a
// low-priority push; an agent that exits mid-work gets an interrupting one.
//
// The five voice tools (briefing, ask, propose, confirm, cancel, note) are
// methods returning voice.ToolResult with the scheduling the Supervisor
// chose; Handler adapts them to voice.ToolHandler. briefing never calls a
// model: it is a cached sentence built from the state table. ask and
// propose go through the brain (internal/brain, docs/roles.md) when one
// is attached through Config.Brain — the Supervisor is its Host: roster,
// ledger, screen and CLI reads, and the actions prompt / send_keys /
// interrupt over the same pending-action paths — and fall back to the
// template path here when the brain errors or runs out of budget.
// propose echoes and sends immediately (D4) unless the target is
// ambiguous (D6); cancel sends Ctrl-C to the pane of the in-flight
// instruction. The brain's transcript is fed from here too: voice
// transcripts, pushes, transitions and notes.
//
// Pending actions expire after a TTL (Run's ticker); an expired question
// that the user later confirms is revived and asked again before anything
// is sent (plan scenario 8). Everything is logged through logging.Logger
// (pending.*, supervisor.*); llm.call and herdr.read/send come from the
// adapters themselves.
package supervisor
