// Package web implements the daemon's HTTP surface: the /voice and /debug
// static pages served from the binary (embedded), one WebSocket endpoint
// for audio and control messages, one SSE endpoint that tails the event
// log, and a small HTTP API for the debug page (list agents, inject a fake
// event, cancel the pending action).
package web
