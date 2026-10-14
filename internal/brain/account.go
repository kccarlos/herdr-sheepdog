package brain

import "strings"

// AccountProblem is an API failure the user must fix on the provider's
// side (a key, a quota, a balance), as opposed to a transient error a
// retry or failover covers.
type AccountProblem string

const (
	// ProblemNone is not an account problem.
	ProblemNone AccountProblem = ""
	// ProblemInvalidKey: the provider rejected the key (401, 403, or
	// Google's 400 API_KEY_INVALID).
	ProblemInvalidKey AccountProblem = "invalid_key"
	// ProblemQuota: the quota is used up (a 429 whose message says so,
	// not a plain rate limit).
	ProblemQuota AccountProblem = "quota"
	// ProblemBalance: the prepaid balance or credit is used up (402).
	ProblemBalance AccountProblem = "balance"
)

// AccountProblemOf classifies an HTTP status and the API's message.
func AccountProblemOf(status int, message string) AccountProblem {
	msg := strings.ToLower(message)
	switch {
	case status == 402 || strings.Contains(msg, "insufficient balance") || strings.Contains(msg, "insufficient credits"):
		return ProblemBalance
	case status == 401 || status == 403:
		return ProblemInvalidKey
	case status == 400 && (strings.Contains(msg, "api_key_invalid") || strings.Contains(msg, "api key not valid") || strings.Contains(msg, "api key expired")):
		return ProblemInvalidKey
	case status == 429 && (strings.Contains(msg, "quota") || strings.Contains(msg, "exhausted") || strings.Contains(msg, "billing")):
		return ProblemQuota
	}
	return ProblemNone
}
