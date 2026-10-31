package herdrcore

import (
	"path"
	"slices"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/hc"
	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// knownKinds are the agent programs sheepdog recognises without a status
// report, and the kinds it can start.
var knownKinds = []string{"claude", "codex", "agy", "gemini", "opencode", "pi"}

const (
	stateLive  = "live"
	claudeApp  = "claude-code"
	spawnLabel = "sheepdog"
	spawnValue = "spawn"
)

func knownKind(kind string) bool { return slices.Contains(knownKinds, kind) }

// isLocal reports whether address is a terminal of this machine; an address
// on an added machine (rohan/t7) holds a slash.
func isLocal(address string) bool { return !strings.Contains(address, "/") }

// isPane reports whether t is a live local terminal.
func isPane(t hc.Terminal) bool { return t.State == stateLive && isLocal(t.Address) }

// kindOf names the program in t: the reported app, else the foreground
// program, else the start program.
func kindOf(t hc.Terminal) string {
	var app string
	if t.Status != nil {
		app = t.Status.App
	}
	if kind := appKind(app); kind != "" {
		return kind
	}
	if t.Foreground != nil {
		if base := argvBase(t.Foreground.Argv); base != "" {
			return base
		}
	}
	return argvBase(t.Argv)
}

// appKind maps a status app name to a kind.
func appKind(app string) string {
	if app == claudeApp {
		return "claude"
	}
	return app
}

// argvBase is the program name of argv[0], without directory or the
// leading dash of a login shell.
func argvBase(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return strings.TrimPrefix(path.Base(argv[0]), "-")
}

// isAgent reports whether t is an agent: it reported status or runs a
// known agent program.
func isAgent(t hc.Terminal) bool { return t.Reported || knownKind(kindOf(t)) }

// agentKind is the kind of t when it is an agent, else empty.
func agentKind(t hc.Terminal) string {
	if isAgent(t) {
		return kindOf(t)
	}
	return ""
}

// needsFallback reports whether t is an agent that has not reported, so its
// state has to come from its screen.
func needsFallback(t hc.Terminal) bool {
	return wantsFallback(t.Status, t.Reported, kindOf(t))
}

func wantsFallback(status *hc.Status, reported bool, kind string) bool {
	return status == nil && !reported && knownKind(kind)
}

// mapState translates a reported status into a herdr state. It does not
// apply the screen fallback.
func mapState(status *hc.Status) herdr.State {
	if status == nil {
		return herdr.StateUnknown
	}
	switch s := herdr.State(status.State); s {
	case herdr.StateIdle, herdr.StateWorking, herdr.StateDone, herdr.StateBlocked:
		return s
	}
	if status.State == "error" {
		return herdr.StateDone
	}
	return herdr.StateUnknown
}

// cwdOf is the foreground program's directory when known, else the
// terminal's.
func cwdOf(t hc.Terminal) string {
	if t.Foreground != nil && t.Foreground.Cwd != "" {
		return t.Foreground.Cwd
	}
	return t.Cwd
}

func agentInfo(t hc.Terminal, state herdr.State) herdr.AgentInfo {
	return herdr.AgentInfo{
		PaneID:         t.Address,
		WorkspaceID:    spaceOf(t),
		TabID:          t.Labels[labelTab],
		TerminalID:     t.Address,
		Status:         state,
		Agent:          kindOf(t),
		Name:           t.Name,
		Cwd:            cwdOf(t),
		TerminalTitle:  t.Title,
		StateChangeSeq: t.Turn,
	}
}

func paneInfo(t hc.Terminal, state herdr.State) herdr.PaneInfo {
	return herdr.PaneInfo{
		PaneID:        t.Address,
		WorkspaceID:   spaceOf(t),
		TabID:         t.Labels[labelTab],
		Status:        state,
		Agent:         agentKind(t),
		Cwd:           cwdOf(t),
		TerminalTitle: t.Title,
	}
}
