package brain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"google.golang.org/genai"
)

// AccountProblem is an API failure the user must fix on the provider's
// side (a key, a quota, a balance), as opposed to a transient error a
// retry or failover covers.
type AccountProblem string

const (
	// ProblemNone is not an account problem.
	ProblemNone AccountProblem = ""
	// ProblemInvalidKey: the provider rejected the key (401, 403, or
	// Google's 400 API_KEY_INVALID; Gemini Live closes with 1007 "API key
	// not valid").
	ProblemInvalidKey AccountProblem = "invalid_key"
	// ProblemQuota: the quota is used up (a 429 whose message says so,
	// not a plain or per-minute rate limit).
	ProblemQuota AccountProblem = "quota"
	// ProblemBalance: the prepaid balance or credit is used up (402).
	ProblemBalance AccountProblem = "balance"
)

// AccountProblemOf classifies an HTTP status and the API's message.
// status may be 0 when only the message is known (a WebSocket close
// frame, a realtime error event): the message's own words decide then.
func AccountProblemOf(status int, message string) AccountProblem {
	msg := strings.ToLower(message)
	switch {
	case status == 402 || hasAny(msg, "insufficient balance", "insufficient_balance", "insufficient credits", "insufficient_credits"):
		return ProblemBalance
	case status == 401 || status == 403 || invalidKeyText(msg):
		return ProblemInvalidKey
	case (status == 429 || status == 0) && quotaText(msg):
		return ProblemQuota
	}
	return ProblemNone
}

// invalidKeyText: the message itself says the key is bad (Google's
// API_KEY_INVALID and Live's close 1007, OpenAI's invalid_api_key).
func invalidKeyText(msg string) bool {
	return hasAny(msg, "api_key_invalid", "api key not valid", "api key expired", "invalid_api_key", "incorrect api key", "invalid api key")
}

// quotaText: the message says a quota is used up, and it is not a
// per-minute limit. Google words its per-minute free-tier 429 like the
// daily one ("You exceeded your current quota … quotaId:
// GenerateRequestsPerMinutePerProjectPerModel-FreeTier"); only the
// quota id tells them apart, and a per-minute limit clears by itself.
// A bare "Resource exhausted" (Vertex's shared capacity) is transient.
func quotaText(msg string) bool {
	if hasAny(msg, "perminute", "per minute", "per_minute") {
		return false
	}
	if hasAny(msg, "insufficient_quota", "perday", "per day", "per_day") {
		return true
	}
	return strings.Contains(msg, "quota") && hasAny(msg, "exceeded", "exhausted", "used up")
}

func hasAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// AccountProblemOfErr classifies a model-call or connect error: the
// HTTP status and message of a genai.APIError or an APIError, else the
// error's text alone.
func AccountProblemOfErr(err error) AccountProblem {
	if err == nil {
		return ProblemNone
	}
	var api genai.APIError
	if errors.As(err, &api) {
		return AccountProblemOf(api.Code, err.Error())
	}
	var compat *APIError
	if errors.As(err, &compat) {
		return AccountProblemOf(compat.Status, compat.Message)
	}
	return AccountProblemOf(0, err.Error())
}

// Service is a provider as the user knows it: its name, the variable
// that holds its key ("" when it signs in another way) and where the
// account is managed.
type Service struct {
	Name    string
	KeyVar  string
	Console string
}

// ServiceOf describes a provider id (an orchestrator provider or a voice
// provider: gemini, vertex, deepseek, openai); another id (an
// OpenAI-compatible provider's name) is named as it is.
func ServiceOf(id string) Service {
	switch id {
	case ProviderGemini:
		return Service{Name: "Gemini (AI Studio)", KeyVar: "GEMINI_API_KEY", Console: "https://aistudio.google.com/apikey"}
	case ProviderVertex:
		return Service{Name: "Vertex AI", Console: "https://console.cloud.google.com"}
	case ProviderDeepSeek:
		return Service{Name: "DeepSeek", KeyVar: "DEEPSEEK_API_KEY", Console: "https://platform.deepseek.com"}
	case "openai":
		return Service{Name: "OpenAI", KeyVar: "OPENAI_API_KEY", Console: "https://platform.openai.com/api-keys"}
	}
	return Service{Name: id}
}

// Fix is the one-line fix for p, for a screen (doctor, /voice, the app):
// it may carry the key variable and the console URL.
func (s Service) Fix(p AccountProblem) string {
	where := s.Console
	if where == "" {
		where = s.Name + "'s console"
	}
	switch p {
	case ProblemInvalidKey:
		if s.KeyVar == "" {
			return fmt.Sprintf("check the credentials and project (%s), then restart sheepdog", where)
		}
		return fmt.Sprintf("put a valid key in %s (%s), then restart sheepdog", s.KeyVar, where)
	case ProblemQuota:
		return fmt.Sprintf("wait for the quota to reset or raise it (%s)", where)
	case ProblemBalance:
		return fmt.Sprintf("top up the account (%s)", where)
	}
	return ""
}

// Roles an account problem can block.
const (
	RoleOrchestrator = "orchestrator"
	RoleVoice        = "voice"
)

// AccountAlert is one account problem the user is told about: what it
// blocks (Role), on which provider, in words for a screen (Message,
// Fix) and for the ear (Spoken: no URL, no variable name).
type AccountAlert struct {
	Role     string
	Provider string
	Problem  AccountProblem
	Message  string
	Fix      string
	Spoken   Notice
}

// NewAccountAlert words problem p of provider for role. backup says
// another orchestrator provider answers meanwhile, so the spoken line
// does not leave the user thinking nothing works. The zero AccountAlert
// for ProblemNone.
//
// Unlike the failover notice (failoverNotice), this one names the
// vendor: a failover is the daemon's business, a key, a quota or a
// balance is the user's to fix, and they cannot fix what is not named.
func NewAccountAlert(role, provider string, p AccountProblem, backup bool) AccountAlert {
	s := ServiceOf(provider)
	ear, _, _ := strings.Cut(s.Name, " (") // "Gemini (AI Studio)" is said "Gemini"
	what, whatZh := "the orchestrator", "调度模型"
	if role == RoleVoice {
		what, whatZh = "the voice", "语音"
	}
	backup = backup && role == RoleOrchestrator
	// The spoken line is two sentences at most (the ear's cap,
	// prose.EarLine): what broke and what it blocks, then the fix.
	cant, cantZh := fmt.Sprintf("so %s can't use it", what), fmt.Sprintf("%s用不了它", whatZh)
	if backup {
		cant += " and a backup model answers for now"
		cantZh += "，现在先由备用模型回答"
	}
	var msg, en, zh string
	switch p {
	case ProblemInvalidKey:
		if s.KeyVar == "" {
			msg = fmt.Sprintf("%s refused sheepdog's credentials, so %s cannot use it.", s.Name, what)
			en = fmt.Sprintf("%s refused my credentials, %s. Check the account, then restart sheepdog.", ear, cant)
			zh = fmt.Sprintf("%s 拒绝了 sheepdog 的凭据，%s。请检查账户，然后重启 sheepdog。", ear, cantZh)
			break
		}
		msg = fmt.Sprintf("%s rejected the API key, so %s cannot use it.", s.Name, what)
		en = fmt.Sprintf("%s rejected my API key, %s. Put a valid key in the sheepdog config and restart sheepdog.", ear, cant)
		zh = fmt.Sprintf("%s 拒绝了 API 密钥，%s。请在 sheepdog 配置里换一个有效的密钥，然后重启 sheepdog。", ear, cantZh)
	case ProblemQuota:
		msg = fmt.Sprintf("%s says the quota is used up, so %s cannot use it.", s.Name, what)
		en = fmt.Sprintf("%s says my quota is used up, %s. It works again when the quota resets or you raise it.", ear, cant)
		zh = fmt.Sprintf("%s 的额度用完了，%s。额度重置或提高之后就能再用。", ear, cantZh)
	case ProblemBalance:
		msg = fmt.Sprintf("%s says the balance is used up, so %s cannot use it.", s.Name, what)
		en = fmt.Sprintf("%s says the account balance is used up, %s. Top up the %s account.", ear, cant, ear)
		zh = fmt.Sprintf("%s 账户余额用完了，%s。请给 %s 账户充值。", ear, cantZh, ear)
	default:
		return AccountAlert{}
	}
	return AccountAlert{Role: role, Provider: provider, Problem: p, Message: msg, Fix: s.Fix(p), Spoken: Notice{English: en, Chinese: zh}}
}

// AccountAlerts is the daemon's set of account problems in force, one
// per role and provider, shared by the orchestrator (which has the voice
// speak them) and the web layer (which shows them on /voice and in the
// app, also to a client that connects later). Information only: it
// changes no routing. Nil-safe.
type AccountAlerts struct {
	mu     sync.Mutex
	active map[string]AccountAlert
	subs   []func(a AccountAlert, active bool)
}

// NewAccountAlerts returns an empty set.
func NewAccountAlerts() *AccountAlerts {
	return &AccountAlerts{active: map[string]AccountAlert{}}
}

func alertKey(role, provider string) string { return role + "/" + provider }

// Report records a. It reports true when a is news (no problem of this
// kind was in force for its role and provider), and only then tells the
// subscribers: a provider that keeps failing the same way is told once.
func (s *AccountAlerts) Report(a AccountAlert) bool {
	if s == nil || a.Problem == ProblemNone {
		return false
	}
	s.mu.Lock()
	k := alertKey(a.Role, a.Provider)
	if old, ok := s.active[k]; ok && old.Problem == a.Problem {
		s.mu.Unlock()
		return false
	}
	s.active[k] = a
	subs := append([]func(AccountAlert, bool){}, s.subs...)
	s.mu.Unlock()
	for _, f := range subs {
		f(a, true)
	}
	return true
}

// Clear ends the problem in force for role and provider (it answered
// again, or the user selected it again), so a later failure is told
// anew. It reports whether there was one.
func (s *AccountAlerts) Clear(role, provider string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	k := alertKey(role, provider)
	a, ok := s.active[k]
	delete(s.active, k)
	subs := append([]func(AccountAlert, bool){}, s.subs...)
	s.mu.Unlock()
	if !ok {
		return false
	}
	for _, f := range subs {
		f(a, false)
	}
	return true
}

// Get is the problem in force for role and provider, if any.
func (s *AccountAlerts) Get(role, provider string) (AccountAlert, bool) {
	if s == nil {
		return AccountAlert{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.active[alertKey(role, provider)]
	return a, ok
}

// Active lists the problems in force, by role and provider.
func (s *AccountAlerts) Active() []AccountAlert {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AccountAlert, 0, len(s.active))
	for _, a := range s.active {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		return alertKey(out[i].Role, out[i].Provider) < alertKey(out[j].Role, out[j].Provider)
	})
	return out
}

// Subscribe calls f on every new problem (active true) and every cleared
// one (active false), outside the set's lock.
func (s *AccountAlerts) Subscribe(f func(a AccountAlert, active bool)) {
	if s == nil || f == nil {
		return
	}
	s.mu.Lock()
	s.subs = append(s.subs, f)
	s.mu.Unlock()
}
