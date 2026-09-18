// Package echotools provides a stub ToolHandler for the five voice tools
// (plan §5) that just echoes the call back as spoken text, so the tool
// round-trip can be verified without the Supervisor (WP3).
package echotools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// Names are the five tools every handler must accept.
var Names = []string{"briefing", "ask", "propose", "confirm", "cancel", "note"}

// Handler returns a ToolHandler whose result for every known tool is
// "Echo test: the <name> tool was called with <args>." scheduled at
// UrgencyInterrupt (so the test hears it immediately). Unknown tools
// return an error.
//
// The sentence form matters: in WP2's live runs the model refused to read
// the bare "briefing called with no arguments" aloud (it answered "this
// function is not available"), while a natural sentence is read verbatim.
func Handler() voice.ToolHandler {
	known := map[string]bool{}
	for _, n := range Names {
		known[n] = true
	}
	return func(_ context.Context, call voice.ToolCall) (voice.ToolResult, error) {
		if !known[call.Name] {
			return voice.ToolResult{}, fmt.Errorf("echotools: unknown tool %q", call.Name)
		}
		return voice.ToolResult{
			Spoken:     fmt.Sprintf("Echo test: the %s tool was called with %s.", call.Name, FormatArgs(call.Args)),
			Scheduling: voice.UrgencyInterrupt,
		}, nil
	}
}

// FormatArgs renders args as "key value, key value" in key order, or
// "no arguments" when empty. Values are spoken, so no JSON punctuation.
func FormatArgs(args map[string]any) string {
	if len(args) == 0 {
		return "no arguments"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %v", k, args[k]))
	}
	return strings.Join(parts, ", ")
}
