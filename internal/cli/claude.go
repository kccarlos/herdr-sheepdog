package cli

import "encoding/json"

// claudeResult is the subset of `claude --output-format json`'s result
// object Runner needs (docs/cli-query-tools.md §2). NumTurns, DurationMS
// and TotalCostUSD are pointers so a field claude's JSON omitted stays
// distinguishable from a reported zero.
type claudeResult struct {
	Result       string   `json:"result"`
	IsError      bool     `json:"is_error"`
	NumTurns     *int     `json:"num_turns"`
	DurationMS   *int64   `json:"duration_ms"`
	TotalCostUSD *float64 `json:"total_cost_usd"`
}

// parseClaudeResult extracts the answer from claude's JSON stdout. ok is
// false when stdout is not that JSON shape, in which case the caller
// falls back to the raw text (docs/cli-query-tools.md: "extract result,
// fall back to raw text if not JSON").
func parseClaudeResult(stdout string) (out claudeResult, ok bool) {
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		return claudeResult{}, false
	}
	return out, true
}
