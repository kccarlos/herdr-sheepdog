package herdrcore

import (
	"context"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/herdr"
)

// Read returns a terminal's screen as plain text. The visible source is the
// screen alone (lines 0); any other source reads that many lines of
// history too. hc has no ANSI read.
func (b *Backend) Read(ctx context.Context, paneID string, source herdr.ReadSource, lines int) (herdr.ReadResult, error) {
	if source == herdr.SourceVisible || lines < 0 {
		lines = 0
	}
	start := time.Now()
	res, err := b.client.Read(ctx, paneID, lines)
	b.logEvent("herdr.read", paneID, map[string]any{
		"source":    string(source),
		"lines":     lines,
		"bytes":     len(res.Text),
		"truncated": !res.Complete,
		"dur_ms":    time.Since(start).Milliseconds(),
		"error":     errString(err),
	})
	if err != nil {
		return herdr.ReadResult{}, translate(err)
	}
	return herdr.ReadResult{PaneID: paneID, Source: source, Text: res.Text, Truncated: !res.Complete}, nil
}

// Prompt types text and Enter into the agent, never forcing past a blocked
// question: that is an error matching herdr.ErrAgentBlocked.
func (b *Backend) Prompt(ctx context.Context, paneID, text string) (herdr.AgentInfo, error) {
	_, err := b.client.Send(ctx, paneID, text, false)
	b.logEvent("herdr.prompt", paneID, map[string]any{"text": text, "error": errString(err)})
	if err != nil {
		return herdr.AgentInfo{}, translate(err)
	}
	// The text went in: a failed read-back must not look like a failed
	// send, or a caller could send it again.
	info, err := b.GetAgent(ctx, paneID)
	if err != nil {
		return herdr.AgentInfo{PaneID: paneID, TerminalID: paneID}, nil
	}
	return info, nil
}

// SendKeys presses named keys in the terminal.
func (b *Backend) SendKeys(ctx context.Context, paneID string, keys ...string) error {
	_, err := b.client.Keys(ctx, paneID, keys...)
	b.logEvent("herdr.send_keys", paneID, map[string]any{"keys": keys, "error": errString(err)})
	return translate(err)
}

// interruptWatch is how long hc watches an interrupt for its effect:
// below the client's call timeout, so a slow stop is an answer
// (still_working), not a failed call after the key went in.
const interruptWatch = 2 * time.Second

// Interrupt presses the terminal's stop key (esc for a status reporter,
// else ctrl+c).
func (b *Backend) Interrupt(ctx context.Context, paneID string) error {
	res, err := b.client.Interrupt(ctx, paneID, interruptWatch)
	b.logEvent("herdr.interrupt", paneID, map[string]any{"key": res.Key, "outcome": res.Outcome, "error": errString(err)})
	return translate(err)
}

// ClosePane ends the terminal's program (terminal.kill).
func (b *Backend) ClosePane(ctx context.Context, paneID string) error {
	_, err := b.client.Kill(ctx, paneID)
	b.logEvent("herdr.close_pane", paneID, map[string]any{"error": errString(err)})
	return translate(err)
}
