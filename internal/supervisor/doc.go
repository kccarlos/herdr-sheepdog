// Package supervisor is the daemon's coordinator (plan §3.6): pure Go
// logic over the herdr client, the state table, the parsed approval
// prompt and the voice session's push channel. It is the only component
// that touches herdr (decision D1), and the only model it ever calls is
// the brain (internal/brain).
//
// Supervisor.HandleTransition folds every debounced herdr transition into
// the state table and reacts: a `blocked` pane is read and its prompt
// parsed in code (herdr.ParsePrompt — the question, the command it is
// about, each option's label and keys), which becomes the single-slot
// answer_prompt PendingAction the fast path and the rail answer, and one
// line is spoken about it; a finish is read for the brain and announced
// as "<alias> finished." unless the brain phrases it; an agent that
// exits mid-work gets an interrupting push.
//
// The voice's two tools (docs/roles.md "One decision-maker") are
// ToSupervisor and Stop; Handler adapts them to voice.ToolHandler.
// to_supervisor runs the code fast paths — a bare answer to the one open
// prompt, a bare stop word — and then one brain turn over the shepherd's
// verbatim words. The Supervisor is the brain's Host: roster, ledger,
// screen and CLI reads, and the actions prompt / send_keys / interrupt /
// promise / note over the same pending-action paths. With no brain (or
// when it errors) the daemon answers from the roster and sends nothing:
// deciding what words mean is the brain's work.
//
// investigate.go owns the background jobs the brain's investigate tool
// starts (docs/roles.md "Investigations and explanations"): one live job
// per agent, the read-only CLI sub-agent in the worker's repository with
// its own ceiling, the ledger in the state file's session, the delivery
// event turn whose line is pushed at the next pause, and cancellation
// from the stop procedure.
//
// announce.go runs the brain's announce turn on every block and finish
// and speaks the one line that event gets — the brain's when it lands
// within Config.AnnounceWait, else the code sentence, never both — under
// the chattiness ceiling.
//
// Pending actions expire after a TTL (Run's ticker); an expired question
// that the user later confirms is revived and asked again before anything
// is sent (plan scenario 8). Everything is logged through logging.Logger
// (pending.*, supervisor.*, brain.push_*); herdr.read/send come from the
// adapters themselves.
package supervisor
