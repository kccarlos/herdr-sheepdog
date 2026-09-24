package workerlog

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// accumulator folds transcript lines into a Digest.
type accumulator struct {
	format Format

	// lastAssistant is the last message for a human; final marks it as a
	// Codex final_answer, which interim commentary does not replace.
	lastAssistant string
	final         bool

	instructions []string
	tools        map[string]int
	files        []string
}

func (a *accumulator) line(b []byte) {
	if len(b) == 0 || b[0] != '{' {
		return
	}
	switch a.format {
	case FormatClaude:
		a.claude(b)
	case FormatCodex:
		a.codex(b)
	}
}

// fill copies the accumulated summary into d.
func (a *accumulator) fill(d *Digest) {
	d.LastAssistant = a.lastAssistant
	d.Instructions = a.instructions
	if len(a.tools) > 0 {
		d.Tools = a.tools
	}
	d.Files = a.files
}

// instruction records a user message: the start of a new piece of work,
// so the tools and files counted so far belonged to the one before.
func (a *accumulator) instruction(text string) {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "<") || strings.HasPrefix(text, "[Request interrupted") {
		// Harness-injected context (<system-reminder>, <command-name>,
		// <environment_context>), not something a person asked.
		return
	}
	if n := len(a.instructions); n > 0 && a.instructions[n-1] == text {
		return
	}
	a.instructions = append(a.instructions, clipRunes(text, maxText))
	if over := len(a.instructions) - MaxInstructions; over > 0 {
		a.instructions = a.instructions[over:]
	}
	a.tools, a.files = nil, nil
	a.final = false
}

// assistant records a message the worker wrote; final is Codex's
// final_answer phase.
func (a *accumulator) assistant(text string, final bool) {
	text = strings.TrimSpace(text)
	if text == "" || (a.final && !final) {
		return
	}
	a.lastAssistant, a.final = clipRunes(text, maxText), final
}

func (a *accumulator) tool(name string) {
	if name == "" {
		return
	}
	if a.tools == nil {
		a.tools = map[string]int{}
	}
	a.tools[name]++
}

// file records an edited file by its base name (never the path: D9).
func (a *accumulator) file(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	base := filepath.Base(strings.TrimPrefix(path, "file://"))
	for _, f := range a.files {
		if f == base {
			return
		}
	}
	if len(a.files) < MaxFiles {
		a.files = append(a.files, base)
	}
}

// claudeLine is the part of a Claude Code transcript line read here.
type claudeLine struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	IsMeta      bool   `json:"isMeta"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Name  string `json:"name"`
	Input struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	} `json:"input"`
}

// claudeEdits are the tools whose file_path is a file the worker changed.
var claudeEdits = map[string]bool{"Edit": true, "MultiEdit": true, "Write": true, "NotebookEdit": true}

// claude folds one Claude Code line: a "user" line with text content is
// an instruction (tool results are list content of tool_result blocks);
// an "assistant" line carries text and tool_use blocks. Sub-agent
// (sidechain) and meta lines are not the worker speaking.
func (a *accumulator) claude(b []byte) {
	var l claudeLine
	if json.Unmarshal(b, &l) != nil || l.IsSidechain || l.IsMeta {
		return
	}
	switch l.Type {
	case "user":
		var s string
		if json.Unmarshal(l.Message.Content, &s) == nil {
			a.instruction(s)
			return
		}
		var blocks []claudeBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return
		}
		var text []string
		for _, bl := range blocks {
			switch bl.Type {
			case "tool_result":
				return
			case "text":
				text = append(text, bl.Text)
			}
		}
		a.instruction(strings.Join(text, "\n"))
	case "assistant":
		var blocks []claudeBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return
		}
		for _, bl := range blocks {
			switch bl.Type {
			case "text":
				a.assistant(bl.Text, false)
			case "tool_use":
				a.tool(bl.Name)
				if claudeEdits[bl.Name] {
					a.file(bl.Input.FilePath)
					a.file(bl.Input.NotebookPath)
				}
			}
		}
	}
}

// codexLine is the part of a Codex rollout line read here.
type codexLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type             string          `json:"type"`
		Message          string          `json:"message"`
		LastAgentMessage *string         `json:"last_agent_message"`
		Item             json.RawMessage `json:"item"`
	} `json:"payload"`
}

type codexItem struct {
	Type    string `json:"type"`
	Phase   string `json:"phase"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Changes map[string]json.RawMessage `json:"changes"`
}

// codex folds one Codex rollout line. The event_msg items are the
// readable record (verified against ~/.codex/sessions, codex-tui
// 0.154.0): item_completed UserMessage (what it was asked, without the
// environment context the response_item copy carries), AgentMessage
// (phase commentary or final_answer), CommandExecution and FileChange
// (keys are the changed files); task_complete's last_agent_message is the
// turn's final answer when set. user_message / agent_message events are
// an older shape of the same.
func (a *accumulator) codex(b []byte) {
	var l codexLine
	if json.Unmarshal(b, &l) != nil || l.Type != "event_msg" {
		return
	}
	p := l.Payload
	switch p.Type {
	case "user_message":
		a.instruction(p.Message)
	case "agent_message":
		a.assistant(p.Message, false)
	case "task_complete":
		if p.LastAgentMessage != nil {
			a.assistant(*p.LastAgentMessage, true)
		}
	case "item_completed":
		var it codexItem
		if json.Unmarshal(p.Item, &it) != nil {
			return
		}
		text := func() string {
			var parts []string
			for _, c := range it.Content {
				parts = append(parts, c.Text)
			}
			return strings.Join(parts, "\n")
		}
		switch it.Type {
		case "UserMessage":
			a.instruction(text())
		case "AgentMessage":
			a.assistant(text(), it.Phase == "final_answer")
		case "Reasoning", "":
		case "CommandExecution":
			a.tool("command")
		case "FileChange":
			a.tool("file_change")
			paths := make([]string, 0, len(it.Changes))
			for path := range it.Changes {
				paths = append(paths, path)
			}
			sort.Strings(paths)
			for _, path := range paths {
				a.file(path)
			}
		default:
			a.tool(strings.ToLower(it.Type))
		}
	}
}

// clipRunes cuts s to n runes.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Clip shortens s to at most max runes for a record that must stay
// small, keeping both ends: a worker's message usually opens with the
// outcome and closes with its sign-off lines (what changed, what needs a
// decision, what is next), so the middle is what goes.
func Clip(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if max <= 0 || len(r) <= max {
		return string(r)
	}
	if max < 20 {
		return string(r[:max-1]) + "…"
	}
	head := max / 3
	tail := max - head - 3
	return strings.TrimSpace(string(r[:head])) + " … " + strings.TrimSpace(string(r[len(r)-tail:]))
}
