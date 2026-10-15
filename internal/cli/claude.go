package cli

import (
	"encoding/json"
	"fmt"
	"strings"
)

// claudeResult is the subset of `claude --output-format json`'s result
// object Runner needs (docs/dev/cli-query-tools.md §2). NumTurns, DurationMS
// and TotalCostUSD are pointers so a field claude's JSON omitted stays
// distinguishable from a reported zero.
type claudeResult struct {
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	// SessionID is what `claude -p --resume <id>` takes for a follow-up
	// (the same id comes back from a resumed run).
	SessionID    string   `json:"session_id"`
	NumTurns     *int     `json:"num_turns"`
	DurationMS   *int64   `json:"duration_ms"`
	TotalCostUSD *float64 `json:"total_cost_usd"`
	// Subtype is "success" or why the run stopped: error_max_budget_usd,
	// error_max_turns, error_during_execution. A run that stops on an
	// error_* subtype exits 1 with stderr empty and no result; Errors
	// carries the reason ("Reached maximum budget ($2.00)"), verified
	// with claude 2.1.293 on 2026-10-08.
	Subtype        string          `json:"subtype"`
	Errors         []string        `json:"errors"`
	APIErrorStatus json.RawMessage `json:"api_error_status"`
}

// reported is what claude's JSON says went wrong, for an error message:
// the subtype, its errors, the API status and any result text, "" when
// it says nothing.
func (r claudeResult) reported() string {
	var parts []string
	if r.Subtype != "" && r.Subtype != "success" {
		parts = append(parts, r.Subtype)
	}
	for _, e := range r.Errors {
		if e = strings.TrimSpace(e); e != "" {
			parts = append(parts, e)
		}
	}
	if s := strings.TrimSpace(string(r.APIErrorStatus)); s != "" && s != "null" {
		parts = append(parts, fmt.Sprintf("API status %s", s))
	}
	if res := strings.TrimSpace(r.Result); res != "" {
		parts = append(parts, res)
	}
	return strings.Join(parts, ": ")
}

// parseClaudeResult extracts the answer from claude's JSON stdout. ok is
// false when stdout is not that JSON shape, in which case the caller
// falls back to the raw text (docs/dev/cli-query-tools.md: "extract result,
// fall back to raw text if not JSON").
func parseClaudeResult(stdout string) (out claudeResult, ok bool) {
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		return claudeResult{}, false
	}
	return out, true
}
