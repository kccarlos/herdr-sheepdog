// Package state implements the supervisor's persisted state table (plan
// §3.2): per-pane agent rows (alias, kind, workspace, state, state_since,
// current task, last block), the single-slot pending action, and session
// bookkeeping.
//
// Store folds herdr.Transitions into the table, assigns default aliases
// (config alias if present, else workspace label + agent name/kind,
// slugged and made unique), and persists every change atomically as JSON
// (temp file + fsync + rename) so the table survives a daemon restart.
package state
