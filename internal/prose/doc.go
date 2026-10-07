// Package prose holds the plain-text helpers the daemon's model-facing
// code shares: what goes into a model's window (TailBytes,
// TruncateRunes), what comes back out of it (ExtractJSON), and what may
// be said aloud (Spoken, which enforces decision D9 — at most n
// sentences, no paths, hashes, URLs or pane ids — whatever the model
// produced).
//
// Nothing here calls a model: the orchestrator (internal/brain) is the only
// component that does.
package prose
