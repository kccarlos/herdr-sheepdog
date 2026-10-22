// Package supervisor is the daemon's coordinator (plan §3.6): pure Go
// logic over the herdr client, the state table, the parsed approval
// prompt and the voice session's push channel. It is the only component
// that touches herdr (decision D1), and the only model it ever calls is
// the orchestrator (internal/brain).
//
// Supervisor.HandleTransition folds every debounced herdr transition into
// the state table and reacts: a `blocked` pane is read and its prompt
// parsed in code (herdr.ParsePrompt — the question, the command it is
// about, each option's label and keys), which becomes the single-slot
// answer_prompt PendingAction the fast path and the rail answer, and one
// line is spoken about it; a finish is read for the orchestrator and announced
// as "<alias> finished." unless the orchestrator phrases it; an agent that
// exits mid-work gets an interrupting push.
//
// The voice's two tools (docs/dev/roles.md "One decision-maker") are
// ToSupervisor and Stop; Handler adapts them to voice.ToolHandler.
// to_supervisor runs the code fast paths — a bare answer to the one open
// prompt, a bare stop word — and then one orchestrator turn over the user's
// verbatim words. The Supervisor is the orchestrator's Host: roster, ledger,
// screen and CLI reads, and the actions prompt / send_keys / interrupt /
// promise / note over the same pending-action paths. With no orchestrator the
// daemon answers from the roster; when the orchestrator errors it says it could
// not answer and to ask again; either way it sends nothing: deciding
// what words mean is the orchestrator's work. A voice input that is the
// assistant's own speech picked up by the microphone is dropped before
// any of this (the echo guard, echo.go).
//
// investigate.go owns the background jobs the orchestrator's investigate tool
// starts (docs/dev/roles.md "Investigations and explanations"): one live job
// per agent, the read-only CLI sub-agent in the worker's repository with
// its own ceiling, the ledger in the state file's session, the delivery
// event turn whose line is pushed at the next pause, and cancellation
// from the stop procedure.
//
// announce.go runs the orchestrator's announce turn on every block and finish
// and speaks the one line that event gets under the chattiness ceiling:
// a finish waits for the orchestrator's line and falls back to the code
// sentence only when the turn fails; a block speaks the parsed prompt at
// Config.AnnounceWait unless the orchestrator's line lands first, and a later
// line is added only when it says the user must act.
//
// Pending actions expire after a TTL (Run's ticker); an expired question
// that the user later confirms is revived and asked again before anything
// is sent (plan scenario 8). Everything is logged through logging.Logger
// (pending.*, supervisor.*, brain.push_*); herdr.read/send come from the
// adapters themselves.
package supervisor
