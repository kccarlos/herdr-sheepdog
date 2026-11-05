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

	// lastAssistant is the last message for a human in the current turn;
	// prevAssistant preserves an earlier turn's message in the same scan
	// when the latest turn is still running cleanly; final marks
	// lastAssistant as a Codex final_answer, which interim commentary
	// does not replace.
	lastAssistant string
	prevAssistant string
	final         bool
	hasTurnText   bool

	stopErr   *TurnError
	streamErr bool

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
	d.Fresh = a.lastAssistant != ""
	d.LastAssistant = a.lastAssistant
	if d.LastAssistant == "" && a.stopErr == nil {
		d.LastAssistant = a.prevAssistant
	}
	d.Instructions = a.instructions
	if len(a.tools) > 0 {
		d.Tools = a.tools
	}
	d.Files = a.files
	d.Error = a.stopErr
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
	text = clipRunes(text, maxText)
	if n := len(a.instructions); n > 0 && a.instructions[n-1] == text &&
		a.stopErr == nil && a.lastAssistant == "" && len(a.tools) == 0 && len(a.files) == 0 {
		return
	}
	if a.lastAssistant != "" {
		a.prevAssistant = a.lastAssistant
	}
	a.lastAssistant = ""
	a.hasTurnText = false
	a.stopErr = nil
	a.streamErr = false
	a.tools, a.files = nil, nil
	a.final = false
	if n := len(a.instructions); n == 0 || a.instructions[n-1] != text {
		a.instructions = append(a.instructions, text)
		if over := len(a.instructions) - MaxInstructions; over > 0 {
			a.instructions = a.instructions[over:]
		}
	}
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
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype"`
	IsSidechain       bool            `json:"isSidechain"`
	IsMeta            bool            `json:"isMeta"`
	IsAPIErrorMessage bool            `json:"isApiErrorMessage"`
	Error             json.RawMessage `json:"error"`
	APIErrorStatus    int             `json:"apiErrorStatus"`
	ThinkingDisplay   string          `json:"thinkingDisplay"`
	Message           struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
	Name      string `json:"name"`
	Input     struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
	} `json:"input"`
}

// claudeEdits are the tools whose file_path is a file the worker changed.
var claudeEdits = map[string]bool{"Edit": true, "MultiEdit": true, "Write": true, "NotebookEdit": true}

// claude folds one Claude Code line: a "user" line with text content is
// an instruction (tool results are list content of tool_result blocks,
// and "[Request interrupted…" marks an aborted turn); an "assistant"
// line carries text, tool_use and — when no text block exists in the
// turn and narration markers are set — narration thinking blocks; API
// error lines populate stopErr without touching lastAssistant. Sub-agent
// (sidechain) and meta lines are not the worker speaking.
func (a *accumulator) claude(b []byte) {
	var l claudeLine
	if json.Unmarshal(b, &l) != nil || l.IsSidechain {
		return
	}
	if l.Type == "system" && l.Subtype == "api_error" {
		if te := parseClaudeErrorField(l.APIErrorStatus, l.Error, ""); te != nil {
			a.stopErr = te
		}
		return
	}
	if l.IsMeta {
		return
	}
	switch l.Type {
	case "user":
		text, isToolResult, ok := claudeContentText(l.Message.Content)
		if !ok || isToolResult {
			return
		}
		if u := strings.TrimSpace(text); strings.HasPrefix(u, "[Request interrupted") {
			a.stopErr = &TurnError{Kind: StopAborted, Message: u}
			return
		}
		a.instruction(text)
	case "assistant":
		if l.IsAPIErrorMessage {
			errText, _, _ := claudeContentText(l.Message.Content)
			a.stopErr = parseClaudeErrorField(l.APIErrorStatus, l.Error, errText)
			return
		}
		var s string
		if json.Unmarshal(l.Message.Content, &s) == nil {
			if strings.TrimSpace(s) != "" {
				a.hasTurnText = true
				a.stopErr = nil
				a.assistant(s, false)
			}
			return
		}
		var blocks []claudeBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return
		}
		lineHasText := false
		for _, bl := range blocks {
			if bl.Type == "text" && strings.TrimSpace(bl.Text) != "" {
				lineHasText = true
				break
			}
		}
		if lineHasText {
			a.hasTurnText = true
			a.stopErr = nil
		}
		for _, bl := range blocks {
			switch bl.Type {
			case "text":
				a.assistant(bl.Text, false)
			case "tool_use":
				a.stopErr = nil
				a.tool(bl.Name)
				if claudeEdits[bl.Name] {
					a.file(bl.Input.FilePath)
					a.file(bl.Input.NotebookPath)
				}
			case "thinking":
				if !lineHasText && !a.hasTurnText && strings.TrimSpace(bl.Thinking) != "" &&
					(l.ThinkingDisplay == "updates" || narrationSig(bl.Signature)) {
					a.stopErr = nil
					a.assistant(bl.Thinking, false)
				}
			}
		}
	}
}

// claudeContentText extracts joined text from a Claude content field
// (either a JSON string or a slice of content blocks).
func claudeContentText(raw json.RawMessage) (text string, isToolResult bool, ok bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false, true
	}
	var blocks []claudeBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false, false
	}
	var parts []string
	for _, bl := range blocks {
		switch bl.Type {
		case "tool_result":
			return "", true, true
		case "text":
			parts = append(parts, bl.Text)
		}
	}
	return strings.Join(parts, "\n"), false, true
}

// narrationSig reports whether a Claude thinking block's base64-encoded
// protobuf signature carries the literal "narration" tag at any of the
// three base64 byte alignments within its header.
func narrationSig(sig string) bool {
	if len(sig) > 80 {
		sig = sig[:80]
	}
	return strings.Contains(sig, "bmFycmF0aW9u") ||
		strings.Contains(sig, "5hcnJhdGlvb") ||
		strings.Contains(sig, "uYXJyYXRpb2")
}

// parseClaudeErrorField classifies a Claude "error" field (string code on
// assistant lines, nested object on system api_error lines) combined with
// apiErrorStatus and any content message text.
func parseClaudeErrorField(status int, raw json.RawMessage, contentText string) *TurnError {
	code := ""
	msg := strings.TrimSpace(contentText)
	cur := raw
	for depth := 0; depth < 4 && hasJSONValue(cur); depth++ {
		var s string
		if json.Unmarshal(cur, &s) == nil {
			if code == "" {
				code = s
			}
			break
		}
		var obj struct {
			Status  int             `json:"status"`
			Type    string          `json:"type"`
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Error   json.RawMessage `json:"error"`
		}
		if json.Unmarshal(cur, &obj) != nil {
			break
		}
		if status == 0 && obj.Status != 0 {
			status = obj.Status
		}
		if obj.Code != "" {
			code = obj.Code
		} else if obj.Type != "" && obj.Type != "error" {
			code = obj.Type
		}
		if msg == "" && obj.Message != "" {
			msg = obj.Message
		}
		cur = obj.Error
	}
	return ClassifyError(status, code, msg, 0)
}

// codexLine is the part of a Codex rollout line read here.
type codexLine struct {
	Type    string `json:"type"`
	Payload struct {
		Type             string          `json:"type"`
		Message          string          `json:"message"`
		Reason           string          `json:"reason"`
		Error            json.RawMessage `json:"error"`
		CodexErrorInfo   json.RawMessage `json:"codex_error_info"`
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
// turn's final answer when set; error, stream_error and turn_aborted
// record non-clean stops. user_message / agent_message events are an
// older shape of the same.
func (a *accumulator) codex(b []byte) {
	var l codexLine
	if json.Unmarshal(b, &l) != nil || l.Type != "event_msg" {
		return
	}
	p := l.Payload
	switch p.Type {
	case "task_started":
		a.stopErr = nil
		a.streamErr = false
		a.final = false
	case "user_message":
		a.instruction(p.Message)
	case "agent_message":
		a.stopErr = nil
		a.streamErr = false
		a.assistant(p.Message, false)
	case "error":
		a.stopErr = codexTurnError(p.Message, p.Error, p.CodexErrorInfo)
		a.streamErr = false
	case "stream_error":
		a.stopErr = codexTurnError(p.Message, p.Error, p.CodexErrorInfo)
		a.streamErr = true
	case "turn_aborted":
		if hasJSONValue(p.Error) || hasJSONValue(p.CodexErrorInfo) {
			a.stopErr = codexTurnError(p.Message, p.Error, p.CodexErrorInfo)
			a.streamErr = false
		} else if a.stopErr == nil || a.streamErr {
			msg := strings.TrimSpace(p.Message)
			if msg == "" && p.Reason != "" {
				msg = "turn aborted (" + p.Reason + ")"
			}
			a.stopErr = &TurnError{Kind: StopAborted, Code: p.Reason, Message: msg}
			a.streamErr = false
		}
	case "task_complete":
		if hasJSONValue(p.Error) || hasJSONValue(p.CodexErrorInfo) {
			a.stopErr = codexTurnError(p.Message, p.Error, p.CodexErrorInfo)
			a.streamErr = false
			return
		}
		if a.stopErr != nil && !a.streamErr {
			return
		}
		if p.LastAgentMessage != nil && strings.TrimSpace(*p.LastAgentMessage) != "" {
			if a.streamErr {
				a.stopErr = nil
				a.streamErr = false
			}
			a.assistant(*p.LastAgentMessage, true)
		} else if a.streamErr {
			a.stopErr = nil
			a.streamErr = false
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
			a.stopErr = nil
			a.streamErr = false
			a.assistant(text(), it.Phase == "final_answer")
		case "Reasoning", "":
		case "CommandExecution":
			a.stopErr = nil
			a.streamErr = false
			a.tool("command")
		case "FileChange":
			a.stopErr = nil
			a.streamErr = false
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
			a.stopErr = nil
			a.streamErr = false
			a.tool(strings.ToLower(it.Type))
		}
	}
}

func hasJSONValue(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != `""`
}

func codexTurnError(msg string, errRaw, infoRaw json.RawMessage) *TurnError {
	code, status, resetSec := parseCodexErrorInfo(infoRaw)
	if hasJSONValue(errRaw) {
		var s string
		if json.Unmarshal(errRaw, &s) == nil {
			if msg == "" {
				msg = s
			}
		} else {
			var obj struct {
				Message        string          `json:"message"`
				Code           string          `json:"code"`
				Type           string          `json:"type"`
				Status         int             `json:"status"`
				HTTPStatusCode int             `json:"http_status_code"`
				CodexErrorInfo json.RawMessage `json:"codex_error_info"`
			}
			if json.Unmarshal(errRaw, &obj) == nil {
				if msg == "" && obj.Message != "" {
					msg = obj.Message
				}
				if c, st, rs := parseCodexErrorInfo(obj.CodexErrorInfo); c != "" || st != 0 || rs != 0 {
					if code == "" {
						code = c
					}
					if status == 0 {
						status = st
					}
					if resetSec == 0 {
						resetSec = rs
					}
				}
				if code == "" {
					if obj.Code != "" {
						code = obj.Code
					} else if obj.Type != "" {
						code = obj.Type
					}
				}
				if status == 0 {
					if obj.HTTPStatusCode != 0 {
						status = obj.HTTPStatusCode
					} else {
						status = obj.Status
					}
				}
			}
		}
	}
	return ClassifyError(status, code, msg, resetSec)
}

func parseCodexErrorInfo(raw json.RawMessage) (code string, status int, resetSec int) {
	if !hasJSONValue(raw) {
		return "", 0, 0
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, 0, 0
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return "", 0, 0
	}
	for k, v := range m {
		code = k
		var detail struct {
			HTTPStatusCode    int `json:"http_status_code"`
			Status            int `json:"status"`
			ResetAfterSeconds int `json:"reset_after_seconds"`
		}
		if json.Unmarshal(v, &detail) == nil {
			if detail.HTTPStatusCode != 0 {
				status = detail.HTTPStatusCode
			} else {
				status = detail.Status
			}
			resetSec = detail.ResetAfterSeconds
		}
		break
	}
	return code, status, resetSec
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
