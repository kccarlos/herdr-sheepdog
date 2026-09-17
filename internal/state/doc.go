// Package state implements the supervisor's persisted state table: per-pane
// agent records (alias, kind, workspace, state, current task, last block),
// the single-slot pending action, and session bookkeeping. It will persist
// to disk as an atomically-written JSON file (SQLite acceptable) so the
// table survives a daemon restart.
package state
