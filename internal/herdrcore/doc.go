// Package herdrcore is the herdr-core (hc) backend: it implements
// herdr.Backend, and the supervisor's Spawner and Closer, on the typed
// JSON-RPC client in internal/hc, translating hc terminals into the shared
// vocabulary of internal/herdr (docs/dev/design/herdr-core-backend.md).
//
// hc has terminals only, so a pane id is a terminal address and there are
// no workspaces. Workspaces, tabs, renames and worktrees and coloured reads
// are deliberately not implemented: the callers find that out by type
// assertion and refuse cleanly. The live terminal is Bridge (bridge.go).
package herdrcore
