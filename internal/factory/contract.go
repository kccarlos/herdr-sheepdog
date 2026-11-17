package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// ProtocolVersion is the owner contract version implemented by this package.
const ProtocolVersion = 1

// AskKind is the closed v1 set of factory ask kinds (docs/dev/factory/owner-contract.md §4.1).
type AskKind string

const (
	AskKindDecision AskKind = "decision"
	AskKindGate     AskKind = "gate"
	AskKindRelease  AskKind = "release"
	AskKindPaidKey  AskKind = "paid-key"
	AskKindDelete   AskKind = "delete"
	AskKindInstall  AskKind = "install"
	AskKindReset    AskKind = "reset"
	AskKindRule     AskKind = "rule"
)

var allAskKinds = []AskKind{
	AskKindDecision,
	AskKindGate,
	AskKindRelease,
	AskKindPaidKey,
	AskKindDelete,
	AskKindInstall,
	AskKindReset,
	AskKindRule,
}

// AskKinds returns a copy of the closed v1 ask-kind list.
func AskKinds() []AskKind { return slices.Clone(allAskKinds) }

// ValidAskKind reports whether k is one of the eight v1 ask kinds.
func ValidAskKind(k AskKind) bool { return slices.Contains(allAskKinds, k) }

// Gated reports whether asks of kind k require a verified AnswerProof (or an
// active delegation rule for gate/release).
func (k AskKind) Gated() bool { return ValidAskKind(k) && k != AskKindDecision }

// Delegable reports whether asks of kind k may be answered by a typed
// delegation rule.
func (k AskKind) Delegable() bool { return k == AskKindGate || k == AskKindRelease }

// RuleType is the closed v1 set of typed standing-rule types (§2.4).
type RuleType string

const (
	RuleTypeMergePolicy     RuleType = "merge_policy"
	RuleTypeReviewRounds    RuleType = "review_rounds"
	RuleTypeAutoAccept      RuleType = "auto_accept"
	RuleTypeDesignGate      RuleType = "design_gate"
	RuleTypeDelegation      RuleType = "delegation"
	RuleTypeNotifyLevel     RuleType = "notify_level"
	RuleTypeProjectPriority RuleType = "project_priority"
)

var allRuleTypes = []RuleType{
	RuleTypeMergePolicy,
	RuleTypeReviewRounds,
	RuleTypeAutoAccept,
	RuleTypeDesignGate,
	RuleTypeDelegation,
	RuleTypeNotifyLevel,
	RuleTypeProjectPriority,
}

// RuleTypes returns a copy of the closed v1 typed rule types.
func RuleTypes() []RuleType { return slices.Clone(allRuleTypes) }

// ValidRuleType reports whether t is one of the seven v1 typed rule types.
func ValidRuleType(t RuleType) bool { return slices.Contains(allRuleTypes, t) }

// AlertKind is the closed v1 set of factory-stopping alert kinds (§6.2).
type AlertKind string

const (
	AlertKindNoPool        AlertKind = "no_pool"
	AlertKindDisk          AlertKind = "disk"
	AlertKindConsentPrompt AlertKind = "consent_prompt"
	AlertKindMainRed       AlertKind = "main_red"
	AlertKindCIStalled     AlertKind = "ci_stalled"
)

var allAlertKinds = []AlertKind{
	AlertKindNoPool,
	AlertKindDisk,
	AlertKindConsentPrompt,
	AlertKindMainRed,
	AlertKindCIStalled,
}

// AlertKinds returns a copy of the closed v1 alert-kind list.
func AlertKinds() []AlertKind { return slices.Clone(allAlertKinds) }

// ValidAlertKind reports whether k is one of the five v1 alert kinds.
func ValidAlertKind(k AlertKind) bool { return slices.Contains(allAlertKinds, k) }

// Standard owner contract v1 error codes (§1.4).
const (
	CodeNotFound      = "not_found"
	CodeAlreadyClosed = "already_closed"
	CodeInvalidOption = "invalid_option"
	CodeInvalidAsk    = "invalid_ask"
	CodeUnknownKind   = "unknown_kind"
	CodeProofRequired = "proof_required"
	CodeProofRejected = "proof_rejected"
	CodeNotDelegable  = "not_delegable"
	CodeInvalidRule   = "invalid_rule"
	CodeUnexpectedKey = "unexpected_key"
	CodeNotReady      = "not_ready"
)

// Sentinel errors corresponding to owner contract v1 failure classes.
var (
	ErrUnknownAskKind   = errors.New("factory: unknown ask kind")
	ErrUnknownAlertKind = errors.New("factory: unknown alert kind")
	ErrInvalidAsk       = errors.New("factory: invalid ask")
	ErrProofRequired    = errors.New("factory: answer proof required")
	ErrProofRejected    = errors.New("factory: answer proof rejected")
	ErrNotDelegable     = errors.New("factory: ask kind is not delegable")
	ErrInvalidRule      = errors.New("factory: invalid rule")
	ErrUnexpectedKey    = errors.New("factory: unexpected JSON key")
	ErrContractNotFound = errors.New("factory: not found")
	ErrAlreadyClosed    = errors.New("factory: ask already closed")
	ErrInvalidOption    = errors.New("factory: invalid ask option")
	ErrNotReady         = errors.New("factory: not ready")
)

// ContractError is a structured validation or engine error carrying a v1 error code.
type ContractError struct {
	Code    string
	Message string
	Err     error
}

func (e *ContractError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return fmt.Sprintf("factory (%s): %s", e.Code, e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("factory (%s): %v", e.Code, e.Err)
	}
	return "factory: " + e.Code
}

func (e *ContractError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func contractErr(code string, sentinel error, format string, args ...any) error {
	return &ContractError{
		Code:    code,
		Message: fmt.Sprintf(format, args...),
		Err:     sentinel,
	}
}

// ErrorCode extracts the v1 contract error code from err, or "" if err is not a
// ContractError.
func ErrorCode(err error) string {
	var ce *ContractError
	if errors.As(err, &ce) && ce != nil {
		return ce.Code
	}
	return ""
}

func sentinelForCode(code string) error {
	switch code {
	case CodeUnknownKind:
		return ErrUnknownAskKind
	case CodeInvalidAsk:
		return ErrInvalidAsk
	case CodeProofRequired:
		return ErrProofRequired
	case CodeProofRejected:
		return ErrProofRejected
	case CodeNotDelegable:
		return ErrNotDelegable
	case CodeInvalidRule:
		return ErrInvalidRule
	case CodeUnexpectedKey:
		return ErrUnexpectedKey
	case CodeNotFound:
		return ErrContractNotFound
	case CodeAlreadyClosed:
		return ErrAlreadyClosed
	case CodeInvalidOption:
		return ErrInvalidOption
	case CodeNotReady:
		return ErrNotReady
	default:
		return nil
	}
}

// ErrorEnvelope is the JSON error object emitted by `engine owner ... --json`
// on non-zero exit (§1.4).
type ErrorEnvelope struct {
	Protocol int    `json:"protocol"`
	OK       bool   `json:"ok"`
	Code     string `json:"code"`
	Error    string `json:"error"`
}

var (
	askIDRe           = regexp.MustCompile(`^[a-z0-9]{6,24}$`)
	ruleIDRe          = regexp.MustCompile(`^rul-[a-z0-9]{6,16}$`)
	alertIDRe         = regexp.MustCompile(`^alt-[a-z0-9]{6,16}$`)
	proofIDShapeRe    = regexp.MustCompile(`^prf-[a-z0-9]{6,32}$`)
	baseHashRe        = regexp.MustCompile(`^[0-9a-f]{12}$`)
	optionKeySuffixRe = regexp.MustCompile(`\([a-z0-9]{1,6}\)$`)
)

// ProjectsResponse is `owner projects --json` (§2.1).
type ProjectsResponse struct {
	Protocol int       `json:"protocol"`
	Projects []Project `json:"projects"`
}

// Project is one registered factory project in ProjectsResponse (§2.1).
type Project struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Folder          string            `json:"folder"`
	Prefix          string            `json:"prefix"`
	Machine         string            `json:"machine"`
	Status          string            `json:"status"`
	OpenAsks        int               `json:"open_asks"`
	Goal            *string           `json:"goal"`
	ActiveMilestone *string           `json:"active_milestone"`
	Coordinator     *CoordinatorLease `json:"coordinator"`
	Spent           Spent             `json:"spent"`
	Pools           []Pool            `json:"pools"`
}

// CoordinatorLease describes the single coordinator lease on a project (§2.1).
type CoordinatorLease struct {
	Terminal   string  `json:"terminal"`
	Session    *string `json:"session"`
	State      string  `json:"state"`
	LeaseValid bool    `json:"lease_valid"`
}

// Spent tracks cumulative work counters on a project (§2.1, §2.3).
type Spent struct {
	TasksMerged  int `json:"tasks_merged"`
	ReviewRounds int `json:"review_rounds"`
	CIRuns       int `json:"ci_runs"`
}

// Pool is one account quota pool reported by usage.py (§2.1).
type Pool struct {
	Pool          string   `json:"pool"`
	State         string   `json:"state"`
	Rolling       bool     `json:"rolling"`
	FiveHourLeft  *float64 `json:"five_hour_left"`
	WeekLeft      *float64 `json:"week_left"`
	FiveHourReset *string  `json:"five_hour_reset"`
	WeekReset     *string  `json:"week_reset"`
}

// AsksResponse is `owner asks --json` (§2.2).
type AsksResponse struct {
	Protocol int   `json:"protocol"`
	Asks     []Ask `json:"asks"`
}

// Ask is one structured owner/coordinator question in `<store_dir>/asks/<id>.json` (§2.2).
type Ask struct {
	ID         string         `json:"id"`
	Project    string         `json:"project"`
	Name       string         `json:"name"`
	Folder     *string        `json:"folder"`
	Kind       AskKind        `json:"kind"`
	Gated      bool           `json:"gated"`
	Question   string         `json:"question"`
	Options    []string       `json:"options"`
	Recommend  string         `json:"recommend"`
	Task       *string        `json:"task"`
	Milestone  *string        `json:"milestone"`
	Subject    map[string]any `json:"subject"`
	At         int64          `json:"at"`
	Status     string         `json:"status"`
	Answer     *string        `json:"answer"`
	AnsweredAt *int64         `json:"answered_at"`
	AnsweredBy *string        `json:"answered_by"`
	ProofID    *string        `json:"proof_id"`
}

// DigestResponse is `owner digest --json` (§2.3).
type DigestResponse struct {
	Protocol      int             `json:"protocol"`
	GeneratedAt   int64           `json:"generated_at"`
	Since         int64           `json:"since"`
	SpokenSummary string          `json:"spoken_summary"`
	OpenAsks      int             `json:"open_asks"`
	AlertsActive  int             `json:"alerts_active"`
	Pools         []Pool          `json:"pools"`
	Projects      []ProjectDigest `json:"projects"`
}

// ProjectDigest is the per-project slice of DigestResponse (§2.3).
type ProjectDigest struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Status    string              `json:"status"`
	Milestone *MilestoneDigest    `json:"milestone"`
	Merged    []MergedTaskDigest  `json:"merged"`
	Waiting   []WaitingTaskDigest `json:"waiting"`
	Replans   int                 `json:"replans"`
	OpenAsks  int                 `json:"open_asks"`
	Spent     Spent               `json:"spent"`
}

// MilestoneDigest summarizes a project's active or completed milestone in a digest.
type MilestoneDigest struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Merged int    `json:"merged"`
	Total  int    `json:"total"`
}

// MergedTaskDigest summarizes one merged task in a digest window.
type MergedTaskDigest struct {
	Ref      string `json:"ref"`
	Title    string `json:"title"`
	PR       *int   `json:"pr"`
	MergedAt int64  `json:"merged_at"`
}

// WaitingTaskDigest summarizes one waiting task in a digest.
type WaitingTaskDigest struct {
	Ref   string `json:"ref"`
	Title string `json:"title"`
	Stage string `json:"stage"`
	Why   string `json:"why"`
}

// RulesResponse is `owner rules --json` (§2.4).
type RulesResponse struct {
	Protocol int    `json:"protocol"`
	Rules    []Rule `json:"rules"`
}

// Rule is one layered standing rule (`typed` or `text`) (§2.4).
type Rule struct {
	ID           string    `json:"id"`
	Scope        string    `json:"scope"`
	Project      *string   `json:"project"`
	Class        string    `json:"class"`
	Type         *RuleType `json:"type"`
	Value        any       `json:"value"`
	Text         *string   `json:"text"`
	Section      *string   `json:"section"`
	BaseHash     *string   `json:"base_hash"`
	ReadBack     string    `json:"read_back"`
	CreatedAt    int64     `json:"created_at"`
	ExpiresAt    *int64    `json:"expires_at"`
	SavedByAsk   string    `json:"saved_by_ask"`
	RevokedAt    *int64    `json:"revoked_at"`
	RevokedByAsk *string   `json:"revoked_by_ask"`
}

// ReviewRoundsValue is the typed value of a `review_rounds` standing rule.
type ReviewRoundsValue struct {
	Code   int `json:"code"`
	Design int `json:"design"`
	Doc    int `json:"doc"`
}

// DelegationValue is the typed value of a `delegation` standing rule.
type DelegationValue struct {
	Kinds        []string  `json:"kinds"`
	Milestones   *[]string `json:"milestones"`
	ReleaseLevel *string   `json:"release_level"`
	Until        *int64    `json:"until"`
}

// AlertsResponse is `owner alerts --json` (§6.3).
type AlertsResponse struct {
	Protocol int     `json:"protocol"`
	Alerts   []Alert `json:"alerts"`
}

// Alert is one factory-stopping alert episode (§6.3).
type Alert struct {
	ID         string    `json:"id"`
	EpisodeKey string    `json:"episode_key"`
	Kind       AlertKind `json:"kind"`
	Project    *string   `json:"project"`
	Name       *string   `json:"name"`
	Task       *string   `json:"task"`
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Fix        string    `json:"fix"`
	At         int64     `json:"at"`
	ClearedAt  *int64    `json:"cleared_at"`
}

// AnswerProof is the loopback verification payload minted and served by the
// daemon at `GET /api/factory/answer-proof/{ask_id}` (§5.2).
type AnswerProof struct {
	OK          bool    `json:"ok"`
	Verified    bool    `json:"verified"`
	ProofID     string  `json:"proof_id"`
	AskID       string  `json:"ask_id"`
	Project     string  `json:"project"`
	Kind        AskKind `json:"kind"`
	OptionIndex int     `json:"option_index"`
	OptionLabel string  `json:"option_label"`
	InputKind   string  `json:"input_kind"`
	InputSeq    uint64  `json:"input_seq"`
	UtteranceID string  `json:"utterance_id"`
	DeviceID    string  `json:"device_id"`
	AnsweredAt  int64   `json:"answered_at"`
}

// AnswerResponse is `owner answer --json` (§3.1).
type AnswerResponse struct {
	Protocol   int     `json:"protocol"`
	OK         bool    `json:"ok"`
	Idempotent bool    `json:"idempotent"`
	Unlocked   *string `json:"unlocked"`
	Ask        Ask     `json:"ask"`
}

// SayResponse is `owner say --json` (§3.2).
type SayResponse struct {
	Protocol    int    `json:"protocol"`
	OK          bool   `json:"ok"`
	Project     string `json:"project"`
	Name        string `json:"name"`
	Coordinator string `json:"coordinator"`
	Delivery    string `json:"delivery"`
}

// ProposeRuleResponse is `owner propose-rule --json` (§3.3).
type ProposeRuleResponse struct {
	Protocol int    `json:"protocol"`
	OK       bool   `json:"ok"`
	ReadBack string `json:"read_back"`
	Ask      Ask    `json:"ask"`
}

// RevokeRuleResponse is `owner revoke-rule --json` (§3.4).
type RevokeRuleResponse struct {
	Protocol int  `json:"protocol"`
	OK       bool `json:"ok"`
	Revoked  bool `json:"revoked"`
	Rule     Rule `json:"rule"`
	Ask      *Ask `json:"ask"`
}

// GraphResponse is the provisional Phase 4 shape for `owner graph --json` (§2.5).
// Extra keys are tolerated until SDF-32 finalizes the stage and mandate fields.
type GraphResponse struct {
	Protocol      int              `json:"protocol"`
	Project       string           `json:"project"`
	Name          string           `json:"name"`
	Folder        string           `json:"folder"`
	UpdatedAt     int64            `json:"updated_at"`
	Replans       int              `json:"replans"`
	SpokenSummary string           `json:"spoken_summary"`
	CriticalPath  *CriticalPath    `json:"critical_path"`
	Milestones    []map[string]any `json:"milestones"`
	Tasks         []map[string]any `json:"tasks"`
}

// CriticalPath is the provisional critical-path summary inside GraphResponse (§2.5).
type CriticalPath struct {
	Milestone     string   `json:"milestone"`
	Tasks         []string `json:"tasks"`
	RemainingMS   int64    `json:"remaining_ms"`
	RemainingText string   `json:"remaining_text"`
}

func decodeStrictJSON(data []byte, dst any, invalidCode string, invalidSentinel error) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			return contractErr(CodeUnexpectedKey, ErrUnexpectedKey, "%s", err.Error())
		}
		return contractErr(invalidCode, invalidSentinel, "decode JSON: %v", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return contractErr(invalidCode, invalidSentinel, "unexpected trailing JSON data")
	}
	return nil
}

// DecodeProjectsResponse strictly decodes and validates `owner projects --json`.
func DecodeProjectsResponse(data []byte) (ProjectsResponse, error) {
	var out ProjectsResponse
	if err := decodeStrictJSON(data, &out, CodeInvalidAsk, ErrInvalidAsk); err != nil {
		return ProjectsResponse{}, err
	}
	if err := out.Validate(); err != nil {
		return ProjectsResponse{}, err
	}
	return out, nil
}

// Validate checks v1 invariants on ProjectsResponse.
func (r ProjectsResponse) Validate() error {
	if r.Protocol != ProtocolVersion {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "unsupported protocol %d", r.Protocol)
	}
	if r.Projects == nil {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "projects must be an array")
	}
	for i, p := range r.Projects {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("projects[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate checks v1 invariants on Project.
func (p Project) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "project id is empty")
	}
	if strings.TrimSpace(p.Name) == "" {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "project name is empty")
	}
	if !strings.HasPrefix(p.Folder, "/") {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "project folder %q must be absolute", p.Folder)
	}
	if strings.TrimSpace(p.Prefix) == "" || strings.ToUpper(p.Prefix) != p.Prefix {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "project prefix %q must be non-empty uppercase", p.Prefix)
	}
	if strings.TrimSpace(p.Machine) == "" {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "project machine is empty")
	}
	switch p.Status {
	case "working", "waiting", "idle", "stopped":
	default:
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid project status %q", p.Status)
	}
	if p.OpenAsks < 0 {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "open_asks must be >= 0")
	}
	if p.Coordinator != nil {
		if strings.TrimSpace(p.Coordinator.Terminal) == "" {
			return contractErr(CodeInvalidAsk, ErrInvalidAsk, "coordinator terminal is empty")
		}
		switch p.Coordinator.State {
		case "working", "idle", "dead":
		default:
			return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid coordinator state %q", p.Coordinator.State)
		}
	}
	if err := p.Spent.Validate(); err != nil {
		return err
	}
	if p.Pools == nil {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "pools must be an array")
	}
	for i, pool := range p.Pools {
		if err := pool.Validate(); err != nil {
			return fmt.Errorf("pools[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate checks v1 invariants on Spent.
func (s Spent) Validate() error {
	if s.TasksMerged < 0 || s.ReviewRounds < 0 || s.CIRuns < 0 {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "spent counters must be >= 0")
	}
	return nil
}

// Validate checks v1 invariants on Pool.
func (p Pool) Validate() error {
	switch p.Pool {
	case "claude", "codex", "antigravity-gemini", "antigravity-claude-gpt":
	default:
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "unknown quota pool %q", p.Pool)
	}
	switch p.State {
	case "ok", "floor", "unknown":
	default:
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid pool state %q", p.State)
	}
	wantRolling := strings.HasPrefix(p.Pool, "antigravity-")
	if p.Rolling != wantRolling {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "pool %q rolling=%v, want %v", p.Pool, p.Rolling, wantRolling)
	}
	return nil
}

// DecodeAsk strictly decodes and validates one Ask JSON object.
func DecodeAsk(data []byte) (Ask, error) {
	var out Ask
	if err := decodeStrictJSON(data, &out, CodeInvalidAsk, ErrInvalidAsk); err != nil {
		return Ask{}, err
	}
	if err := out.Validate(); err != nil {
		return Ask{}, err
	}
	return out, nil
}

// DecodeAsksResponse strictly decodes and validates `owner asks --json`.
func DecodeAsksResponse(data []byte) (AsksResponse, error) {
	var out AsksResponse
	if err := decodeStrictJSON(data, &out, CodeInvalidAsk, ErrInvalidAsk); err != nil {
		return AsksResponse{}, err
	}
	if err := out.Validate(); err != nil {
		return AsksResponse{}, err
	}
	return out, nil
}

// Validate checks v1 invariants on AsksResponse.
func (r AsksResponse) Validate() error {
	if r.Protocol != ProtocolVersion {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "unsupported protocol %d", r.Protocol)
	}
	if r.Asks == nil {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "asks must be an array")
	}
	for i, a := range r.Asks {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("asks[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate checks v1 invariants on Ask (§2.2, §4).
func (a Ask) Validate() error {
	if !ValidAskKind(a.Kind) {
		return contractErr(CodeUnknownKind, ErrUnknownAskKind, "unknown ask kind %q", a.Kind)
	}
	if !askIDRe.MatchString(a.ID) {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid ask id %q", a.ID)
	}
	if strings.TrimSpace(a.Project) == "" {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "ask project is empty")
	}
	if strings.TrimSpace(a.Name) == "" {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "ask name is empty")
	}
	if a.Project == "everywhere" {
		if a.Folder != nil {
			return contractErr(CodeInvalidAsk, ErrInvalidAsk, "everywhere ask must have null folder")
		}
	} else if a.Folder != nil && !strings.HasPrefix(*a.Folder, "/") {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "ask folder %q must be absolute", *a.Folder)
	}
	wantGated := a.Kind != AskKindDecision
	if a.Gated != wantGated {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "ask kind %q requires gated=%v, got %v", a.Kind, wantGated, a.Gated)
	}
	qLen := utf8.RuneCountInString(a.Question)
	if strings.TrimSpace(a.Question) == "" || qLen > 300 || strings.ContainsAny(a.Question, "\r\n") {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "ask question must be 1..300 single-line chars")
	}
	if len(a.Options) < 2 || len(a.Options) > 9 {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "ask options must have 2..9 items, got %d", len(a.Options))
	}
	seenOpts := make(map[string]bool, len(a.Options))
	for _, opt := range a.Options {
		trimmed := strings.TrimSpace(opt)
		if trimmed == "" || seenOpts[opt] || optionKeySuffixRe.MatchString(trimmed) {
			return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid ask option %q", opt)
		}
		seenOpts[opt] = true
	}
	if !seenOpts[a.Recommend] {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "recommend %q is not in options", a.Recommend)
	}
	if a.Kind == AskKindDecision && a.Subject != nil {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "decision ask must have null subject")
	}
	if a.At <= 0 {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "ask timestamp must be > 0")
	}
	switch a.Status {
	case "open", "cancelled":
		if a.Answer != nil || a.AnsweredAt != nil || a.AnsweredBy != nil || a.ProofID != nil {
			return contractErr(CodeInvalidAsk, ErrInvalidAsk, "%s ask must not carry answer metadata", a.Status)
		}
	case "answered":
		if a.Answer == nil || !seenOpts[*a.Answer] {
			return contractErr(CodeInvalidAsk, ErrInvalidAsk, "answered ask missing valid answer option")
		}
		if a.AnsweredAt == nil || *a.AnsweredAt < a.At {
			return contractErr(CodeInvalidAsk, ErrInvalidAsk, "answered ask missing valid answered_at")
		}
		if a.AnsweredBy == nil {
			return contractErr(CodeInvalidAsk, ErrInvalidAsk, "answered ask missing answered_by")
		}
		switch *a.AnsweredBy {
		case "owner":
			if a.Gated && (a.ProofID == nil || !proofIDShapeRe.MatchString(*a.ProofID)) {
				return contractErr(CodeInvalidAsk, ErrInvalidAsk, "gated ask answered by owner requires prf-* proof_id")
			}
			if !a.Gated && a.ProofID != nil && !proofIDShapeRe.MatchString(*a.ProofID) {
				return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid proof_id %q", *a.ProofID)
			}
		case "rule":
			if !a.Kind.Delegable() {
				return contractErr(CodeInvalidAsk, ErrInvalidAsk, "ask kind %q cannot be answered by rule", a.Kind)
			}
			if a.ProofID == nil || !ruleIDRe.MatchString(*a.ProofID) {
				return contractErr(CodeInvalidAsk, ErrInvalidAsk, "delegated ask answered by rule requires rul-* proof_id")
			}
		case "coordinator":
			if a.Gated {
				return contractErr(CodeInvalidAsk, ErrInvalidAsk, "gated ask kind %q cannot be answered by coordinator", a.Kind)
			}
			if a.ProofID != nil {
				return contractErr(CodeInvalidAsk, ErrInvalidAsk, "coordinator answer must have null proof_id")
			}
		default:
			return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid answered_by %q", *a.AnsweredBy)
		}
	default:
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid ask status %q", a.Status)
	}
	return nil
}

// DecodeDigestResponse strictly decodes and validates `owner digest --json`.
func DecodeDigestResponse(data []byte) (DigestResponse, error) {
	var out DigestResponse
	if err := decodeStrictJSON(data, &out, CodeInvalidAsk, ErrInvalidAsk); err != nil {
		return DigestResponse{}, err
	}
	if err := out.Validate(); err != nil {
		return DigestResponse{}, err
	}
	return out, nil
}

// Validate checks v1 invariants on DigestResponse (§2.3).
func (d DigestResponse) Validate() error {
	if d.Protocol != ProtocolVersion {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "unsupported protocol %d", d.Protocol)
	}
	if d.GeneratedAt <= 0 || d.Since <= 0 || d.Since > d.GeneratedAt {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid digest window [%d, %d]", d.Since, d.GeneratedAt)
	}
	if strings.TrimSpace(d.SpokenSummary) == "" {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "digest spoken_summary is empty")
	}
	if d.OpenAsks < 0 || d.AlertsActive < 0 {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "digest counts must be >= 0")
	}
	if d.Pools == nil || d.Projects == nil {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "digest pools and projects must be arrays")
	}
	for i, p := range d.Pools {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("pools[%d]: %w", i, err)
		}
	}
	for i, pr := range d.Projects {
		if strings.TrimSpace(pr.ID) == "" || strings.TrimSpace(pr.Name) == "" {
			return contractErr(CodeInvalidAsk, ErrInvalidAsk, "projects[%d] missing id or name", i)
		}
		switch pr.Status {
		case "working", "waiting", "idle", "stopped":
		default:
			return contractErr(CodeInvalidAsk, ErrInvalidAsk, "projects[%d] invalid status %q", i, pr.Status)
		}
		if pr.Merged == nil || pr.Waiting == nil {
			return contractErr(CodeInvalidAsk, ErrInvalidAsk, "projects[%d] merged and waiting must be arrays", i)
		}
		if pr.Replans < 0 || pr.OpenAsks < 0 {
			return contractErr(CodeInvalidAsk, ErrInvalidAsk, "projects[%d] counts must be >= 0", i)
		}
		if err := pr.Spent.Validate(); err != nil {
			return fmt.Errorf("projects[%d]: %w", i, err)
		}
	}
	return nil
}

// DecodeRule strictly decodes and validates one Rule JSON object.
func DecodeRule(data []byte) (Rule, error) {
	var out Rule
	if err := decodeStrictJSON(data, &out, CodeInvalidRule, ErrInvalidRule); err != nil {
		return Rule{}, err
	}
	if err := out.Validate(); err != nil {
		return Rule{}, err
	}
	return out, nil
}

// DecodeRulesResponse strictly decodes and validates `owner rules --json`.
func DecodeRulesResponse(data []byte) (RulesResponse, error) {
	var out RulesResponse
	if err := decodeStrictJSON(data, &out, CodeInvalidRule, ErrInvalidRule); err != nil {
		return RulesResponse{}, err
	}
	if err := out.Validate(); err != nil {
		return RulesResponse{}, err
	}
	return out, nil
}

// Validate checks v1 invariants on RulesResponse.
func (r RulesResponse) Validate() error {
	if r.Protocol != ProtocolVersion {
		return contractErr(CodeInvalidRule, ErrInvalidRule, "unsupported protocol %d", r.Protocol)
	}
	if r.Rules == nil {
		return contractErr(CodeInvalidRule, ErrInvalidRule, "rules must be an array")
	}
	for i, rule := range r.Rules {
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("rules[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate checks v1 invariants on Rule (§2.4, §3.3, §3.4).
func (r Rule) Validate() error {
	if !ruleIDRe.MatchString(r.ID) {
		return contractErr(CodeInvalidRule, ErrInvalidRule, "invalid rule id %q", r.ID)
	}
	switch r.Scope {
	case "project":
		if r.Project == nil || strings.TrimSpace(*r.Project) == "" {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "project-scoped rule requires project id")
		}
	case "machine", "everywhere":
		if r.Project != nil {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "%s-scoped rule must have null project", r.Scope)
		}
	default:
		return contractErr(CodeInvalidRule, ErrInvalidRule, "invalid rule scope %q", r.Scope)
	}
	if strings.TrimSpace(r.ReadBack) == "" {
		return contractErr(CodeInvalidRule, ErrInvalidRule, "rule read_back is empty")
	}
	if r.CreatedAt <= 0 {
		return contractErr(CodeInvalidRule, ErrInvalidRule, "rule created_at must be > 0")
	}
	if !askIDRe.MatchString(r.SavedByAsk) {
		return contractErr(CodeInvalidRule, ErrInvalidRule, "invalid saved_by_ask %q", r.SavedByAsk)
	}

	switch r.Class {
	case "typed":
		if r.Type == nil || !ValidRuleType(*r.Type) {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "unknown typed rule type")
		}
		if r.Text != nil || r.Section != nil || r.BaseHash != nil {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "typed rule must have null text, section, and base_hash")
		}
		if err := r.validateTypedValue(*r.Type); err != nil {
			return err
		}
	case "text":
		if r.Type != nil || r.Value != nil {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "text rule must have null type and value")
		}
		if r.Text == nil || strings.TrimSpace(*r.Text) == "" {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "text rule requires non-empty text")
		}
		if r.Section == nil || strings.TrimSpace(*r.Section) == "" {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "text rule requires non-empty section")
		}
		if r.BaseHash == nil || !baseHashRe.MatchString(*r.BaseHash) {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "text rule requires 12-hex base_hash")
		}
	default:
		return contractErr(CodeInvalidRule, ErrInvalidRule, "invalid rule class %q", r.Class)
	}

	if r.RevokedAt == nil {
		if r.RevokedByAsk != nil {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "active rule must have null revoked_by_ask")
		}
	} else {
		if *r.RevokedAt < r.CreatedAt {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "revoked_at precedes created_at")
		}
		isDelegation := r.Class == "typed" && r.Type != nil && *r.Type == RuleTypeDelegation
		if !isDelegation {
			if r.RevokedByAsk == nil || !askIDRe.MatchString(*r.RevokedByAsk) {
				return contractErr(CodeProofRequired, ErrProofRequired, "revoking non-delegation rule %s requires a verified rule ask", r.ID)
			}
		} else if r.RevokedByAsk != nil && !askIDRe.MatchString(*r.RevokedByAsk) {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "invalid revoked_by_ask %q", *r.RevokedByAsk)
		}
	}
	return nil
}

func (r Rule) validateTypedValue(rt RuleType) error {
	if r.Value == nil {
		return contractErr(CodeInvalidRule, ErrInvalidRule, "typed rule %s requires value", rt)
	}
	raw, err := json.Marshal(r.Value)
	if err != nil {
		return contractErr(CodeInvalidRule, ErrInvalidRule, "marshal rule value: %v", err)
	}

	switch rt {
	case RuleTypeMergePolicy:
		var v string
		if err := decodeStrictJSON(raw, &v, CodeInvalidRule, ErrInvalidRule); err != nil {
			return err
		}
		if v != "checks" && v != "auto" && v != "ask" {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "invalid merge_policy %q", v)
		}
	case RuleTypeReviewRounds:
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "review_rounds value must be an object")
		}
		for _, k := range []string{"code", "design", "doc"} {
			if _, ok := m[k]; !ok {
				return contractErr(CodeInvalidRule, ErrInvalidRule, "review_rounds missing %q", k)
			}
		}
		var v ReviewRoundsValue
		if err := decodeStrictJSON(raw, &v, CodeInvalidRule, ErrInvalidRule); err != nil {
			return err
		}
		if v.Code < 0 || v.Design < 0 || v.Doc < 0 {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "review_rounds counts must be >= 0")
		}
	case RuleTypeAutoAccept:
		var v bool
		if err := decodeStrictJSON(raw, &v, CodeInvalidRule, ErrInvalidRule); err != nil {
			return err
		}
	case RuleTypeDesignGate:
		var v string
		if err := decodeStrictJSON(raw, &v, CodeInvalidRule, ErrInvalidRule); err != nil {
			return err
		}
		if v != "fable" && v != "owner" {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "invalid design_gate %q", v)
		}
	case RuleTypeDelegation:
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "delegation value must be an object")
		}
		for _, k := range []string{"kinds", "milestones", "release_level", "until"} {
			if _, ok := m[k]; !ok {
				return contractErr(CodeInvalidRule, ErrInvalidRule, "delegation value missing %q", k)
			}
		}
		var v DelegationValue
		if err := decodeStrictJSON(raw, &v, CodeInvalidRule, ErrInvalidRule); err != nil {
			return err
		}
		if len(v.Kinds) == 0 {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "delegation kinds must be non-empty")
		}
		hasRelease := false
		seen := make(map[string]bool, len(v.Kinds))
		for _, k := range v.Kinds {
			ak := AskKind(k)
			if !ak.Delegable() {
				if ValidAskKind(ak) {
					return contractErr(CodeNotDelegable, ErrNotDelegable, "ask kind %q cannot be delegated", k)
				}
				return contractErr(CodeInvalidRule, ErrInvalidRule, "unknown delegation kind %q", k)
			}
			if seen[k] {
				return contractErr(CodeInvalidRule, ErrInvalidRule, "duplicate delegation kind %q", k)
			}
			seen[k] = true
			if ak == AskKindRelease {
				hasRelease = true
			}
		}
		if v.Milestones != nil {
			if len(*v.Milestones) == 0 {
				return contractErr(CodeInvalidRule, ErrInvalidRule, "delegation milestones must be non-empty or null")
			}
			for _, ms := range *v.Milestones {
				if strings.TrimSpace(ms) == "" {
					return contractErr(CodeInvalidRule, ErrInvalidRule, "delegation milestone id is empty")
				}
			}
		}
		if hasRelease {
			if v.ReleaseLevel == nil {
				return contractErr(CodeInvalidRule, ErrInvalidRule, "release delegation requires release_level")
			}
			switch *v.ReleaseLevel {
			case "patch", "minor", "any":
			default:
				return contractErr(CodeInvalidRule, ErrInvalidRule, "invalid release_level %q", *v.ReleaseLevel)
			}
		} else if v.ReleaseLevel != nil {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "non-release delegation must have null release_level")
		}
		if v.Until == nil || *v.Until <= r.CreatedAt {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "delegation rule requires until > created_at")
		}
		if r.ExpiresAt == nil || *r.ExpiresAt != *v.Until {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "delegation rule requires expires_at == value.until")
		}
	case RuleTypeNotifyLevel:
		var v string
		if err := decodeStrictJSON(raw, &v, CodeInvalidRule, ErrInvalidRule); err != nil {
			return err
		}
		if v != "asks_and_alerts" && v != "alerts_only" && v != "quiet" {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "invalid notify_level %q", v)
		}
	case RuleTypeProjectPriority:
		if r.Scope == "project" {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "project_priority is only valid at machine or everywhere scope")
		}
		var v []string
		if err := decodeStrictJSON(raw, &v, CodeInvalidRule, ErrInvalidRule); err != nil {
			return err
		}
		if len(v) == 0 {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "project_priority must be non-empty")
		}
		for _, p := range v {
			if strings.TrimSpace(p) == "" {
				return contractErr(CodeInvalidRule, ErrInvalidRule, "project_priority contains empty id")
			}
		}
	default:
		return contractErr(CodeInvalidRule, ErrInvalidRule, "unknown rule type %q", rt)
	}
	return nil
}

// DecodeAlert strictly decodes and validates one Alert JSON object.
func DecodeAlert(data []byte) (Alert, error) {
	var out Alert
	if err := decodeStrictJSON(data, &out, CodeUnknownKind, ErrUnknownAlertKind); err != nil {
		return Alert{}, err
	}
	if err := out.Validate(); err != nil {
		return Alert{}, err
	}
	return out, nil
}

// DecodeAlertsResponse strictly decodes and validates `owner alerts --json`.
func DecodeAlertsResponse(data []byte) (AlertsResponse, error) {
	var out AlertsResponse
	if err := decodeStrictJSON(data, &out, CodeUnknownKind, ErrUnknownAlertKind); err != nil {
		return AlertsResponse{}, err
	}
	if err := out.Validate(); err != nil {
		return AlertsResponse{}, err
	}
	return out, nil
}

// Validate checks v1 invariants on AlertsResponse.
func (r AlertsResponse) Validate() error {
	if r.Protocol != ProtocolVersion {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "unsupported protocol %d", r.Protocol)
	}
	if r.Alerts == nil {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "alerts must be an array")
	}
	for i, a := range r.Alerts {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("alerts[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate checks v1 invariants on Alert (§6.2, §6.3).
func (a Alert) Validate() error {
	if !ValidAlertKind(a.Kind) {
		return contractErr(CodeUnknownKind, ErrUnknownAlertKind, "unknown alert kind %q", a.Kind)
	}
	if !alertIDRe.MatchString(a.ID) {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid alert id %q", a.ID)
	}
	if strings.TrimSpace(a.EpisodeKey) == "" {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "alert episode_key is empty")
	}
	tLen := utf8.RuneCountInString(a.Title)
	bLen := utf8.RuneCountInString(a.Body)
	fLen := utf8.RuneCountInString(a.Fix)
	if strings.TrimSpace(a.Title) == "" || tLen > 120 {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "alert title must be 1..120 chars")
	}
	if strings.TrimSpace(a.Body) == "" || bLen > 300 {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "alert body must be 1..300 chars")
	}
	if strings.TrimSpace(a.Fix) == "" || fLen > 200 {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "alert fix must be 1..200 chars")
	}
	if a.At <= 0 {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "alert timestamp must be > 0")
	}
	if a.ClearedAt != nil && *a.ClearedAt < a.At {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "alert cleared_at precedes at")
	}
	return nil
}

// DecodeAnswerProof strictly decodes and validates an AnswerProof JSON object.
func DecodeAnswerProof(data []byte) (AnswerProof, error) {
	var out AnswerProof
	if err := decodeStrictJSON(data, &out, CodeProofRejected, ErrProofRejected); err != nil {
		return AnswerProof{}, err
	}
	if err := out.Validate(); err != nil {
		return AnswerProof{}, err
	}
	return out, nil
}

// Validate checks v1 safety invariants on AnswerProof (§5.2).
func (p AnswerProof) Validate() error {
	if !p.OK || !p.Verified {
		return contractErr(CodeProofRejected, ErrProofRejected, "answer proof is not verified")
	}
	if !proofIDShapeRe.MatchString(p.ProofID) {
		return contractErr(CodeProofRejected, ErrProofRejected, "invalid proof_id %q", p.ProofID)
	}
	if !askIDRe.MatchString(p.AskID) {
		return contractErr(CodeProofRejected, ErrProofRejected, "invalid ask_id %q", p.AskID)
	}
	if strings.TrimSpace(p.Project) == "" {
		return contractErr(CodeProofRejected, ErrProofRejected, "answer proof project is empty")
	}
	if !ValidAskKind(p.Kind) {
		return contractErr(CodeUnknownKind, ErrUnknownAskKind, "unknown ask kind %q in proof", p.Kind)
	}
	if p.OptionIndex < 1 || p.OptionIndex > 9 {
		return contractErr(CodeProofRejected, ErrProofRejected, "invalid option_index %d", p.OptionIndex)
	}
	if strings.TrimSpace(p.OptionLabel) == "" {
		return contractErr(CodeProofRejected, ErrProofRejected, "empty option_label")
	}
	if p.InputKind != "tap" && p.InputKind != "voice" {
		return contractErr(CodeProofRejected, ErrProofRejected, "input_kind %q is not tap or voice", p.InputKind)
	}
	if p.InputSeq == 0 {
		return contractErr(CodeProofRejected, ErrProofRejected, "input_seq must be > 0")
	}
	dev := strings.TrimSpace(p.DeviceID)
	if dev == "" || dev == "local" {
		return contractErr(CodeProofRejected, ErrProofRejected, "device_id %q cannot authorize factory answer proof", p.DeviceID)
	}
	if p.AnsweredAt <= 0 {
		return contractErr(CodeProofRejected, ErrProofRejected, "answered_at must be > 0")
	}
	return nil
}

// DecodeAnswerResponse strictly decodes and validates `owner answer --json`.
func DecodeAnswerResponse(data []byte) (AnswerResponse, error) {
	var out AnswerResponse
	if err := decodeStrictJSON(data, &out, CodeInvalidAsk, ErrInvalidAsk); err != nil {
		return AnswerResponse{}, err
	}
	if err := out.Validate(); err != nil {
		return AnswerResponse{}, err
	}
	return out, nil
}

// Validate checks v1 invariants on AnswerResponse (§3.1).
func (r AnswerResponse) Validate() error {
	if r.Protocol != ProtocolVersion || !r.OK {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid answer response envelope")
	}
	if err := r.Ask.Validate(); err != nil {
		return err
	}
	if r.Ask.Status != "answered" {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "answer response ask status is %q, want answered", r.Ask.Status)
	}
	return nil
}

// DecodeSayResponse strictly decodes and validates `owner say --json`.
func DecodeSayResponse(data []byte) (SayResponse, error) {
	var out SayResponse
	if err := decodeStrictJSON(data, &out, CodeInvalidAsk, ErrInvalidAsk); err != nil {
		return SayResponse{}, err
	}
	if err := out.Validate(); err != nil {
		return SayResponse{}, err
	}
	return out, nil
}

// Validate checks v1 invariants on SayResponse (§3.2).
func (r SayResponse) Validate() error {
	if r.Protocol != ProtocolVersion || !r.OK {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid say response envelope")
	}
	if strings.TrimSpace(r.Project) == "" || strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.Coordinator) == "" {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "say response missing project, name, or coordinator")
	}
	if r.Delivery != "sent" && r.Delivery != "held" {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid say delivery %q", r.Delivery)
	}
	return nil
}

// DecodeProposeRuleResponse strictly decodes and validates `owner propose-rule --json`.
func DecodeProposeRuleResponse(data []byte) (ProposeRuleResponse, error) {
	var out ProposeRuleResponse
	if err := decodeStrictJSON(data, &out, CodeInvalidRule, ErrInvalidRule); err != nil {
		return ProposeRuleResponse{}, err
	}
	if err := out.Validate(); err != nil {
		return ProposeRuleResponse{}, err
	}
	return out, nil
}

// Validate checks v1 invariants on ProposeRuleResponse (§3.3).
func (r ProposeRuleResponse) Validate() error {
	if r.Protocol != ProtocolVersion || !r.OK {
		return contractErr(CodeInvalidRule, ErrInvalidRule, "invalid propose-rule response envelope")
	}
	if strings.TrimSpace(r.ReadBack) == "" {
		return contractErr(CodeInvalidRule, ErrInvalidRule, "propose-rule read_back is empty")
	}
	if err := r.Ask.Validate(); err != nil {
		return err
	}
	if r.Ask.Kind != AskKindRule || !r.Ask.Gated || r.Ask.Status != "open" {
		return contractErr(CodeInvalidRule, ErrInvalidRule, "propose-rule must return an open gated rule ask")
	}
	return nil
}

// DecodeRevokeRuleResponse strictly decodes and validates `owner revoke-rule --json`.
func DecodeRevokeRuleResponse(data []byte) (RevokeRuleResponse, error) {
	var out RevokeRuleResponse
	if err := decodeStrictJSON(data, &out, CodeInvalidRule, ErrInvalidRule); err != nil {
		return RevokeRuleResponse{}, err
	}
	if err := out.Validate(); err != nil {
		return RevokeRuleResponse{}, err
	}
	return out, nil
}

// Validate checks v1 invariants on RevokeRuleResponse (§3.4).
func (r RevokeRuleResponse) Validate() error {
	if r.Protocol != ProtocolVersion || !r.OK {
		return contractErr(CodeInvalidRule, ErrInvalidRule, "invalid revoke-rule response envelope")
	}
	if err := r.Rule.Validate(); err != nil {
		return err
	}
	if r.Revoked {
		if r.Rule.RevokedAt == nil || r.Ask != nil {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "immediate revoke must stamp revoked_at and return null ask")
		}
	} else {
		if r.Rule.RevokedAt != nil || r.Ask == nil {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "gated revoke must leave rule active and return open rule ask")
		}
		if err := r.Ask.Validate(); err != nil {
			return err
		}
		if r.Ask.Kind != AskKindRule || !r.Ask.Gated || r.Ask.Status != "open" {
			return contractErr(CodeInvalidRule, ErrInvalidRule, "gated revoke must return an open gated rule ask")
		}
	}
	return nil
}

// DecodeGraphResponse decodes and checks the provisional Phase 4 `owner graph --json`
// envelope without strict extra-key rejection (§2.5, §7.1).
func DecodeGraphResponse(data []byte) (GraphResponse, error) {
	var out GraphResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return GraphResponse{}, contractErr(CodeInvalidAsk, ErrInvalidAsk, "decode graph JSON: %v", err)
	}
	if err := out.Validate(); err != nil {
		return GraphResponse{}, err
	}
	return out, nil
}

// Validate checks top-level provisional invariants on GraphResponse (§2.5).
func (g GraphResponse) Validate() error {
	if g.Protocol != ProtocolVersion {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "unsupported protocol %d", g.Protocol)
	}
	if strings.TrimSpace(g.Project) == "" || strings.TrimSpace(g.Name) == "" || !strings.HasPrefix(g.Folder, "/") {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "graph missing project, name, or absolute folder")
	}
	if g.UpdatedAt <= 0 || g.Replans < 0 {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "invalid graph updated_at or replans")
	}
	if g.Milestones == nil || g.Tasks == nil {
		return contractErr(CodeInvalidAsk, ErrInvalidAsk, "graph milestones and tasks must be arrays")
	}
	return nil
}
