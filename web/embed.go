// Package web holds the static pages served by the daemon (plan §3.8,
// decision D11: vanilla JS, no build step). They are embedded into the
// binary and served by internal/web; the files under voice/ are the
// browser audio pipe (WP6) and the files under debug/ the event viewer.
package web

import "embed"

// FS contains voice/, debug/ and static/ (shared assets such as the logo).
//
//go:embed voice debug static
var FS embed.FS
