// Package app assembles the sheepdog daemon (`sheepdog serve`): config →
// event log → state store → herdr client + Watcher (or the mock herdr of
// internal/inject) → orchestrator → supervisor → voice session (Gemini
// Live, or a no-op pusher with --voice=off) → web server.
//
// Build wires everything without starting anything; Listen binds the web
// port so the caller can print the URLs; Run starts the goroutines
// (watcher → HandleTransition serialised per pane, voice events →
// supervisor + web, supervisor expiry, audio pump, HTTP) and blocks until
// the context ends, then drains the HTTP server for a bounded time, closes
// the voice session, and closes the event log last so daemon.stop is the
// final line.
package app
