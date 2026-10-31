// Package herdrsheepdog ships the herdr-app panel with the sheepdog binary.
package herdrsheepdog

import "embed"

// Panel is the herdr-app mod that ships with this sheepdog build. It holds
// only runtime files: README and Node tests stay out.
//
//go:embed mods/sheepdog/main.js mods/sheepdog/mod.json mods/sheepdog/logo.svg mods/sheepdog/model/*.js mods/sheepdog/net/*.js mods/sheepdog/ui/*.js
var Panel embed.FS

// Layout is the herdr-app layout that starts from Herdr's main layout with
// the Sheepdog panel docked on the right. setup installs it alongside Panel.
//
//go:embed sheepdog-layout/layouts/sheepdog.json
var Layout []byte
