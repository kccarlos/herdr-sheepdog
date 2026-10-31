// Package herdrsheepdog ships the herdr-app panel with the sheepdog binary.
package herdrsheepdog

import "embed"

// Panel is the herdr-app mod that ships with this sheepdog build. It holds
// only runtime files: README and Node tests stay out.
//
//go:embed mods/sheepdog/main.js mods/sheepdog/mod.json mods/sheepdog/logo.svg mods/sheepdog/model/*.js mods/sheepdog/net/*.js mods/sheepdog/ui/*.js
var Panel embed.FS
