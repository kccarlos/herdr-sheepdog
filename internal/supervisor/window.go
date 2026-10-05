package supervisor

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
	"github.com/kccarlos/herdr-sheepdog/internal/state"
)

// The agent window: the CEO looking at an agent's terminal and operating
// it himself from the app (GET /api/agents/{pane}/screen, POST
// /api/agents/{pane}/input; docs/ws-contract.md "Agent window"). The
// authority for an input is his own tap, so it does not pass the one
// rail; the brain is told what he did so its picture stays right.

// Window screen limits: the default and the cap of lines one read asks
// for, and the most text or keys one input carries.
const (
	WindowDefaultLines = 120
	WindowMaxLines     = 500
	WindowMaxText      = 4000
	WindowMaxKeys      = 8
)

// ErrNoSuchAgent is a pane the daemon does not track: the window never
// reaches the user's other herdr panes.
var ErrNoSuchAgent = errors.New("no such agent")

// ErrWindowInput is an input the window refuses before anything is sent
// (a key off the allow-list, empty text, both or neither).
var ErrWindowInput = errors.New("bad input")

// ErrWindowRefused is keys that matched the open question's option but
// that the tap path refused.
var ErrWindowRefused = errors.New("refused")

// windowKeys is the key allow-list of the window's key bar.
var windowKeys = map[string]bool{"enter": true, "esc": true, "ctrl+c": true, "up": true, "down": true, "tab": true,
	"1": true, "2": true, "3": true, "4": true, "5": true, "6": true, "7": true, "8": true, "9": true}

// reANSI matches the escape sequences herdr's strip_ansi should already
// have removed (CSI, OSC, two-byte escapes): belt and braces for a client
// that renders the text verbatim.
var reANSI = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

// Screen is one read of an agent's window.
type Screen struct {
	PaneID     string      `json:"pane_id"`
	Alias      string      `json:"alias"`
	Spoken     string      `json:"spoken,omitempty"`
	Kind       string      `json:"kind,omitempty"`
	State      herdr.State `json:"state"`
	StateSince time.Time   `json:"state_since,omitzero"`
	Text       string      `json:"text"`
	Lines      int         `json:"lines"`
	Truncated  bool        `json:"truncated"`
	// Hash fingerprints State and Text, so a polling client can skip a
	// frame that did not change.
	Hash string `json:"hash"`
}

// Screen reads the tracked agent in paneID's window: the herdr read the
// brain's read_screen makes, without making the agent the subject
// (looking is not talking). lines <= 0 is WindowDefaultLines; it is
// capped at WindowMaxLines.
func (s *Supervisor) Screen(ctx context.Context, paneID string, lines int) (Screen, error) {
	row, ok := s.store.Get(paneID)
	if !ok {
		return Screen{}, ErrNoSuchAgent
	}
	if lines <= 0 {
		lines = WindowDefaultLines
	}
	lines = min(lines, WindowMaxLines)
	read, err := s.herdr.Read(ctx, paneID, herdr.SourceRecentUnwrapped, lines)
	if err != nil {
		return Screen{}, err
	}
	text := reANSI.ReplaceAllString(read.Text, "")
	h := fnv.New64a()
	_, _ = h.Write([]byte(row.State))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(text))
	return Screen{
		PaneID: paneID, Alias: row.Alias, Spoken: state.Say(row), Kind: row.Kind,
		State: row.State, StateSince: row.StateSince,
		Text: text, Lines: lines, Truncated: read.Truncated,
		Hash: strconv.FormatUint(h.Sum64(), 16),
	}, nil
}

// WindowResult is what an input did: Kind is "prompt", "keys", or
// "answer" (the keys answered the agent's open question by the tap path).
type WindowResult struct {
	Kind   string
	Spoken string
}

// WindowPrompt submits text to the tracked agent in paneID as the CEO's
// own prompt (agent.prompt): its current task becomes the text, and the
// brain is told. herdr.ErrAgentBlocked comes back as is (the agent is at
// a prompt; answer that first).
func (s *Supervisor) WindowPrompt(ctx context.Context, paneID, text string) (WindowResult, error) {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > WindowMaxText {
		return WindowResult{}, fmt.Errorf("%w: the text must be 1 to %d bytes", ErrWindowInput, WindowMaxText)
	}
	row, ok := s.store.Get(paneID)
	if !ok {
		return WindowResult{}, ErrNoSuchAgent
	}
	name := aliasOr(state.Say(row))
	if _, err := s.herdr.Prompt(ctx, paneID, text); err != nil {
		if errors.Is(err, herdr.ErrAgentBlocked) {
			return WindowResult{}, fmt.Errorf("%s is waiting at a prompt; answer it first (%w)", name, err)
		}
		return WindowResult{}, err
	}
	s.mu.Lock()
	now := s.cfg.Now()
	if _, err := s.store.Update(paneID, func(a *state.Agent) { a.CurrentTask, a.LastInteraction = text, &now }); err != nil {
		s.log.Log(logSource, "supervisor.state_error", paneID, map[string]any{"error": err.Error()})
	}
	s.invalidateBriefingLocked()
	s.mu.Unlock()
	if s.brain != nil {
		s.brain.Daemon(fmt.Sprintf("the user typed a prompt in %s's window: %q", name, text))
	}
	return WindowResult{Kind: "prompt", Spoken: "Sent to " + name + "."}, nil
}

// WindowKeys sends allow-listed keys to the tracked agent in paneID.
// When they are exactly one option of the agent's open approval
// question, they answer it through the tap path (AnswerTap), so the
// pending action is confirmed and cleared the way a card tap does;
// otherwise they go to the pane as they are and the brain is told.
func (s *Supervisor) WindowKeys(ctx context.Context, paneID string, keys []string) (WindowResult, error) {
	if len(keys) == 0 || len(keys) > WindowMaxKeys {
		return WindowResult{}, fmt.Errorf("%w: send 1 to %d keys", ErrWindowInput, WindowMaxKeys)
	}
	for _, k := range keys {
		if !windowKeys[k] {
			return WindowResult{}, fmt.Errorf("%w: key %q is not allowed", ErrWindowInput, k)
		}
	}
	row, ok := s.store.Get(paneID)
	if !ok {
		return WindowResult{}, ErrNoSuchAgent
	}
	if id, option, ok := s.windowAnswer(paneID, keys); ok {
		res, err := s.AnswerTap(ctx, id, option)
		if err == nil {
			return WindowResult{Kind: "answer", Spoken: res.Spoken}, nil
		}
		switch {
		case errors.Is(err, ErrNotSent):
			return WindowResult{Kind: "answer", Spoken: res.Spoken}, err
		case !errors.Is(err, ErrPendingGone):
			return WindowResult{Kind: "answer"}, fmt.Errorf("%w: %v", ErrWindowRefused, err)
		}
		// Answered or replaced between the look and the tap: the keys
		// still go to the window as plain keys.
	}
	if err := s.herdr.SendKeys(ctx, paneID, keys...); err != nil {
		return WindowResult{}, err
	}
	name := aliasOr(state.Say(row))
	if s.brain != nil {
		s.brain.Daemon(fmt.Sprintf("the user pressed %s in %s's window", strings.Join(keys, " "), name))
	}
	return WindowResult{Kind: "keys", Spoken: "Pressed " + strings.Join(keys, " ") + " in " + name + "'s window."}, nil
}

// windowAnswer finds the option of the open approval question for paneID
// that keys select: its pending id and 1-based index. Keys select an
// option when they are its own keys or the digit it is listed under
// (both TUIs take the digit even where the option's key is "y"). Enter
// selects nothing here: the CEO may have moved the TUI's cursor with
// up/down since the question was recorded, so enter goes to the pane as
// a plain key, and the transition off blocked closes the question
// (transitions.go).
func (s *Supervisor) windowAnswer(paneID string, keys []string) (string, int, bool) {
	p, has := s.store.Pending()
	if !has || p.Kind != state.PendingAnswerPrompt || p.PaneID != paneID {
		return "", 0, false
	}
	for i, o := range p.Options {
		if o.Meta {
			continue
		}
		if slices.Equal(o.Keys, keys) || (len(keys) == 1 && keys[0] == strconv.Itoa(i+1)) {
			return p.ID, i + 1, true
		}
	}
	return "", 0, false
}
