package herdrsheepdog

import (
	"embed"
	"strings"
)

// Factory is the factory engine that ships with this sheepdog build. It holds
// only runtime files (the Python package and CLI wrappers, agent templates,
// macOS reviewer sandbox profile, MIT notice and .python-version pin): tests,
// case fixtures and design contracts stay out.
//
//go:embed factory/.python-version factory/NOTICE
//go:embed factory/bin factory/templates
//go:embed factory/mods
//go:embed factory/mods/tasks/bin/herdr_task/*.py
var Factory embed.FS

// FactoryPythonVersionRaw holds the raw contents of factory/.python-version.
//
//go:embed factory/.python-version
var FactoryPythonVersionRaw string

// FactoryPythonVersion returns the pinned CPython version in factory/.python-version.
func FactoryPythonVersion() string {
	return strings.TrimSpace(FactoryPythonVersionRaw)
}
