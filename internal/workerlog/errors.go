package workerlog

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/redact"
)

// StopKind classifies why a worker's turn stopped short of a normal finish.
type StopKind string

// The stop kinds parsed from Claude Code and Codex session transcripts.
const (
	StopQuota       StopKind = "quota"
	StopRateLimit   StopKind = "rate_limit"
	StopOverloaded  StopKind = "overloaded"
	StopServerError StopKind = "server_error"
	StopConnection  StopKind = "connection"
	StopAuth        StopKind = "auth"
	StopContextFull StopKind = "context_full"
	StopAPIError    StopKind = "api_error"
	StopAborted     StopKind = "aborted"
)

// TurnError describes an API, quota, rate-limit, auth, connection or abort
// stop recorded at the end of a worker's session transcript.
type TurnError struct {
	Kind     StopKind
	Status   int
	Code     string
	ResetsAt string
	Message  string
}

// Summary returns a short, ear-safe verb phrase ("stopped because …") with
// no trailing period, suitable after an agent alias or "it".
func (e *TurnError) Summary() string {
	if e == nil {
		return ""
	}
	switch e.Kind {
	case StopQuota:
		if e.Code == "billing_error" || e.Status == 402 || strings.Contains(strings.ToLower(e.Message), "credit") {
			return "stopped because its credit balance is too low"
		}
		if e.ResetsAt != "" {
			return "stopped because it hit its usage limit, resets " + e.ResetsAt
		}
		return "stopped because it hit its usage limit"
	case StopOverloaded:
		base := "stopped because the model provider is overloaded"
		if e.Status == 529 {
			base = "stopped because the model provider returned 529 overloaded"
		}
		if e.ResetsAt != "" {
			return base + ", resets " + e.ResetsAt
		}
		return base
	case StopRateLimit:
		base := "stopped because its provider rate-limited it"
		if e.Status == 429 {
			base = "stopped because its provider returned a 429 rate limit"
		}
		if e.ResetsAt != "" {
			return base + ", resets " + e.ResetsAt
		}
		return base
	case StopServerError:
		if e.Status >= 500 {
			return fmt.Sprintf("stopped because the model provider returned a %d server error", e.Status)
		}
		return "stopped because the model provider returned a server error"
	case StopConnection:
		return "stopped because its connection to the model provider dropped"
	case StopAuth:
		return "stopped because its provider login or API token failed"
	case StopContextFull:
		return "stopped because its conversation reached the context limit"
	case StopAborted:
		return "stopped because its turn was aborted"
	default:
		if e.Status > 0 {
			return fmt.Sprintf("stopped with a %d API error", e.Status)
		}
		return "stopped with an API error"
	}
}

// Sentence returns a complete spoken sentence ("<alias> stopped because ….").
func (e *TurnError) Sentence(alias string) string {
	if e == nil {
		return ""
	}
	return alias + " " + e.Summary() + "."
}

var (
	reAPIErrorStatus = regexp.MustCompile(`(?i)\bapi error:\s*(\d{3})\b`)
	rePipeEpoch      = regexp.MustCompile(`\|\d{9,}\b`)
	reResetsAt       = regexp.MustCompile(`(?i)\bresets(?:\s+at)?\s+([^\n·|]+)`)
	reTryAgain       = regexp.MustCompile(`(?i)\btry again\s+((?:in|at)\s+[^\n.]+)`)
	reTrailingParens = regexp.MustCompile(`\s*\([^)]*\)\s*$`)
)

// ClassifyError classifies a provider or harness error from HTTP status,
// error code, raw message text and optional reset delay in seconds.
func ClassifyError(status int, code, rawMsg string, resetAfterSec int) *TurnError {
	msg := strings.TrimSpace(rawMsg)
	msg = rePipeEpoch.ReplaceAllString(msg, "")

	if m := reAPIErrorStatus.FindStringSubmatch(msg); len(m) == 2 && status == 0 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			status = n
		}
	}
	if i := strings.IndexByte(msg, '{'); i >= 0 {
		if j := strings.LastIndexByte(msg, '}'); j > i {
			innerType, innerMsg := parseEmbeddedErrorJSON(msg[i : j+1])
			if innerType != "" && isGenericErrorCode(code) {
				code = innerType
			}
			if innerMsg != "" {
				prefix := strings.TrimSpace(msg[:i])
				if prefix != "" {
					msg = prefix + " " + innerMsg
				} else {
					msg = innerMsg
				}
			}
		}
	}
	msg = clipRunes(strings.Join(strings.Fields(new(redact.Redactor).String(msg)), " "), 300)

	resetsAt := extractResetsAt(msg)
	if resetsAt == "" && resetAfterSec > 0 {
		resetsAt = formatResetSeconds(resetAfterSec)
	}

	kind := classifyStopKind(status, code, msg)
	return &TurnError{
		Kind:     kind,
		Status:   status,
		Code:     code,
		ResetsAt: resetsAt,
		Message:  msg,
	}
}

func isGenericErrorCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "", "unknown", "error", "api_error", "server_error":
		return true
	}
	return false
}

func parseEmbeddedErrorJSON(blob string) (errType, errMsg string) {
	var env struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Error   struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(blob), &env) != nil {
		return "", ""
	}
	errType = env.Error.Type
	if errType == "" {
		errType = env.Error.Code
	}
	if errType == "" && env.Type != "error" {
		errType = env.Type
	}
	if errType == "" {
		errType = env.Code
	}
	errMsg = env.Error.Message
	if errMsg == "" {
		errMsg = env.Message
	}
	return strings.TrimSpace(errType), strings.TrimSpace(errMsg)
}

func extractResetsAt(msg string) string {
	for _, re := range []*regexp.Regexp{reResetsAt, reTryAgain} {
		m := re.FindStringSubmatch(msg)
		if len(m) != 2 {
			continue
		}
		v := reTrailingParens.ReplaceAllString(m[1], "")
		v = strings.TrimSpace(strings.TrimRight(v, ".,;:"))
		if v != "" {
			return v
		}
	}
	return ""
}

func formatResetSeconds(sec int) string {
	if sec >= 3600 {
		h := (sec + 1800) / 3600
		if h <= 1 {
			return "in 1 hour"
		}
		return fmt.Sprintf("in %d hours", h)
	}
	if sec >= 60 {
		m := (sec + 30) / 60
		if m <= 1 {
			return "in 1 minute"
		}
		return fmt.Sprintf("in %d minutes", m)
	}
	if sec == 1 {
		return "in 1 second"
	}
	return fmt.Sprintf("in %d seconds", sec)
}

func classifyStopKind(status int, code, msg string) StopKind {
	lc := strings.ToLower(strings.TrimSpace(code))
	lm := strings.ToLower(msg)

	switch {
	case lc == "context_window_exceeded" || lc == "context_length_exceeded" ||
		strings.Contains(lm, "prompt is too long") ||
		strings.Contains(lm, "context window") ||
		strings.Contains(lm, "maximum context length") ||
		strings.Contains(lm, "input exceeds the context"):
		return StopContextFull

	case status == 401 || status == 403 ||
		lc == "authentication_failed" || lc == "authentication_error" || lc == "unauthorized" || lc == "permission_error" ||
		strings.Contains(lm, "oauth token") ||
		strings.Contains(lm, "invalid api key") ||
		strings.Contains(lm, "not logged in") ||
		strings.Contains(lm, "please run /login") ||
		strings.Contains(lm, "authentication failed"):
		return StopAuth

	case status == 529 ||
		strings.Contains(lc, "overloaded") || lc == "model_cap" ||
		strings.Contains(lm, "overloaded") || strings.Contains(lm, "model capacity"):
		return StopOverloaded

	case status == 402 ||
		lc == "usage_limit_exceeded" || lc == "billing_error" || lc == "insufficient_quota" ||
		strings.Contains(lm, "hit your limit") ||
		strings.Contains(lm, "hit your usage limit") ||
		strings.Contains(lm, "usage limit") ||
		strings.Contains(lm, "plan limit") ||
		strings.Contains(lm, "5-hour limit") ||
		strings.Contains(lm, "weekly limit") ||
		strings.Contains(lm, "monthly limit") ||
		strings.Contains(lm, "credit balance") ||
		strings.Contains(lm, "insufficient credits") ||
		strings.Contains(lm, "insufficient quota") ||
		strings.Contains(lm, "out of credits") ||
		strings.Contains(lm, "upgrade to pro"):
		return StopQuota

	case status == 429 ||
		strings.Contains(lc, "rate_limit") ||
		strings.Contains(lm, "rate limit") ||
		strings.Contains(lm, "rate-limit") ||
		strings.Contains(lm, "too many requests"):
		return StopRateLimit

	case (status >= 500 && status <= 599) ||
		lc == "server_error" || lc == "internal_server_error" || lc == "bad_gateway" || lc == "gateway_timeout":
		return StopServerError

	case lc == "http_connection_failed" || lc == "response_stream_disconnected" || lc == "response_too_many_failed_attempts" ||
		strings.Contains(lm, "stream disconnected") ||
		strings.Contains(lm, "connection failed") ||
		strings.Contains(lm, "connection reset") ||
		strings.Contains(lm, "connection refused") ||
		strings.Contains(lm, "network error") ||
		strings.Contains(lm, "reconnecting") ||
		strings.Contains(lm, "timed out"):
		return StopConnection

	default:
		return StopAPIError
	}
}
