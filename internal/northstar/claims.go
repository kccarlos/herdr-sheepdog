package northstar

import (
	"regexp"
	"strings"
)

// ClaimKind is what sheepdog claimed it did (or that happened).
type ClaimKind string

// The claim kinds.
const (
	ClaimSent     ClaimKind = "sent"
	ClaimStopped  ClaimKind = "stopped"
	ClaimVerified ClaimKind = "verified"
	ClaimDone     ClaimKind = "done"
	ClaimStarted  ClaimKind = "started"
	ClaimClosed   ClaimKind = "closed"
	ClaimApproved ClaimKind = "approved"
	ClaimDeclined ClaimKind = "declined"
)

// claimOrder is the order claims are looked for in a sentence; a sentence
// yields at most one claim of each kind.
var claimOrder = []ClaimKind{ClaimApproved, ClaimDeclined, ClaimStopped, ClaimClosed, ClaimStarted, ClaimVerified, ClaimSent, ClaimDone}

// Claim phrases, English and Chinese (Simplified and the Traditional forms
// the voice model sometimes emits). Measurement only: these read what was
// said after the call and never steer the product. A claim must be in the
// completed aspect ("I've sent", "已经告诉"); a sentence that is a
// question, a plan or a negation carries no claim (claimBlocker).
var claimPatterns = map[ClaimKind][]*regexp.Regexp{
	ClaimApproved: res(
		`\b(i|we)('ve| have)?\s+(just\s+)?(approved|allowed|accepted|said yes|answered yes|okayed|ok'd|gave (it|them|\S+) the go-ahead)\b`,
		`\b(has|have|was|were|been)\s+(approved|allowed|accepted)\b`,
		`^(approved|allowed|accepted)\b`,
		`(已经|已經|已|刚刚|剛剛|刚|剛)(批准|同意|允许|允許|确认|確認|放行)`,
		`(批准|同意|允许|允許|放行)了`,
	),
	ClaimDeclined: res(
		`\b(i|we)('ve| have)?\s+(just\s+)?(declined|denied|rejected|refused|said no|answered no|turned (it|that) down)\b`,
		`\b(has|have|was|were|been)\s+(declined|denied|rejected|refused)\b`,
		`^(declined|denied|rejected)\b`,
		`(已经|已經|已|刚|剛)(拒绝|拒絕|回绝|回絕|否决|否決)`,
		`(拒绝|拒絕|回绝|回絕|否决|否決)了`,
	),
	ClaimStopped: res(
		`\b(i|we)('ve| have)?\s+(just\s+)?(stopped|interrupted|halted|cancel+ed|aborted|paused)\b`,
		`\b(has|have|was|were|been)\s+(stopped|interrupted|halted|cancel+ed|aborted)\b`,
		`^(stopped|interrupted|halted|cancel+ed)\b`,
		`\bsent\s+(it\s+|\S+\s+)?(a\s+|an\s+)?(ctrl[- ]?c|interrupt|stop)\b`,
		`(已经|已經|已|刚|剛)(停|中断|中斷|打断|打斷|叫停|取消)`,
		`(停下来了|停下來了|停掉了|停止了|中断了|中斷了|打断了|打斷了|叫停了)`,
	),
	ClaimClosed: res(
		`\b(i|we)('ve| have)?\s+(just\s+)?(closed|shut down|shut (it|that|them) down|killed|terminated|ended)\b`,
		`\b(has|have|was|were|been|is)\s+(closed|shut down|terminated)\b`,
		`^(closed|shut down)\b`,
		`(已经|已經|已|刚|剛)(关闭|關閉|关掉|關掉|关了|關了)`,
		`(关闭了|關閉了|关掉了|關掉了)`,
	),
	ClaimStarted: res(
		`\b(i|we)('ve| have)?\s+(just\s+)?(started|launched|spun up|spawned|opened|created|set up)\s+(a|an|the|another)?\s*(new\s+)?(\S+\s+)?(agent|worker|session|instance|claude|codex)\b`,
		`\b(new|another)\s+(\S+\s+)?(agent|worker|session|claude|codex)\s+(is|has)\s+(started|up|running|been started|been launched)\b`,
		`(已经|已經|已|刚|剛)(启动|啟動|新开|新開|开了|開了|创建|創建|建了)`,
		`(启动了|啟動了|新开了|新開了|开了一个|開了一個)`,
	),
	ClaimVerified: res(
		`\b(i|we)('ve| have)?\s+(just\s+)?(verified|confirmed|double[- ]checked|checked|tested|ran the tests)\b`,
		`\b(has|have|was|were|been)\s+(verified|confirmed)\b`,
		`^(verified|confirmed)\b`,
		`\b(tests?|checks?|build)\s+(are\s+|is\s+)?(all\s+)?(passing|passed|green|pass)\b`,
		`(已经|已經|已)(验证|驗證|核实|核實|确认过|確認過|检查过|檢查過|查过|查過|测过|測過)`,
		`(验证过|驗證過|核实过|核實過|确认过|確認過|检查过|檢查過|验证了|驗證了|核实了|核實了)`,
		`(测试|測試)(都|全部|已经|已經)?(通过|通過|过了|過了)`,
	),
	ClaimSent: res(
		`\b(i|we)('ve| have)?\s+(just\s+)?(sent|told|asked|instructed|passed|forwarded|relayed|given|gave|let|chose|chosen|picked|selected|answered|replied|went with|gone with)\b`,
		`\b(i|we)('ve| have)?\s+(just\s+)?(started|kicked off|launched|run|ran|set off)\s+(the\s+|its\s+|their\s+)?(tests?|build|job|task|command|it|them|that)\b`,
		`\b(has|have|was|were|been)\s+(sent|passed on|passed along|forwarded|relayed)\b`,
		`^(sent|told|forwarded|relayed)\b`,
		`\bsent\s+(it|that|this|your|the|them)\b`,
		`\band\s+(also\s+)?(told|asked|instructed|sent|passed|forwarded)\b`,
		`(已经|已經|已|刚|剛)(把|将|將)?.{0,16}(发送|發送|发给|發給|发过去|發過去|转告|轉告|告诉|告訴|通知|交给|交給|传达|傳達|交代|吩咐)`,
		`(转告了|轉告了|告诉了|告訴了|通知了|发给了|發給了|传达了|傳達了|交代了|吩咐了|选了|選了|选择了|選擇了)`,
		`我(已经|已經|已|刚|剛)?(让|讓|叫|请|請).{0,40}了`,
		`(已经|已經|已|刚|剛)(让|讓|叫|请|請)`,
	),
	// Done is sheepdog saying it did what was asked ("Done.", "I've
	// taken care of it"); a worker's own finish is news, not a claim.
	ClaimDone: res(
		`^(done|all done|all set|taken care of|handled)\b`,
		`\b(i|we)('ve| have)?\s+(just\s+)?(done|finished|completed|handled|taken care of|sorted)\s+(it|that|this|them|what you asked)\b`,
		`\b(that|this|it)('s| is| has been)\s+(done|taken care of|handled|sorted)\b`,
		`(搞定了|办好了|辦好了|弄好了|处理好了|處理好了|我.{0,6}(做好|完成|办好|辦好)了|已经办好|已經辦好|已经处理|已經處理)`,
	),
}

// claimBlocker marks a sentence that claims nothing: a question, a plan,
// a condition, an offer or a negation.
var claimBlocker = res(
	`\?`,
	`\b(will|i'll|we'll|it'll|shall|should|would|could|can i|can you|may i|want me|do you|did you|are you|is it|going to|gonna|about to|let me|let's|if|once|whether|when it|as soon as|until|trying to|try to|tried to|wanted to|meant to|still waiting)\b`,
	`\b(haven't|hasn't|hadn't|didn't|couldn't|can't|cannot|won't|wasn't|weren't|isn't|aren't|don't|doesn't|have not|has not|had not|did not|could not|was not|were not|is not|are not|not yet|no longer|unable|failed to|nothing (was|has been|is))\b`,
	`\bnever\s+(been\s+)?(sent|told|asked|stopped|interrupted|approved|declined|closed|started|verified|checked|finished|done)\b`,
	`[？吗嗎呢]`,
	`(要不要|需不需要|需要我|要我|我会|我會|我将|我將|将会|將會|等它|等他|等到|如果|一旦|还没|還沒|没有|沒有|尚未|未能|无法|無法|没能|沒能|不能|不会|不會|是否|打算|准备|準備|正在|稍后|稍後)`,
)

func res(ps ...string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(ps))
	for i, p := range ps {
		out[i] = regexp.MustCompile("(?i)" + p)
	}
	return out
}

// sentenceSplit splits text into sentences at English and Chinese
// sentence ends, semicolons, line breaks and ", but" / ", and" (a claim
// and its caveat or plan are two sentences).
var sentenceSplit = regexp.MustCompile(`(?i)[.!?;。！？；\n]+(\s+|$)|[。！？；]|,\s+(but|and|so)\s+|\s+and\s+(i'll|i will|we'll|will)\s+|，(但是|但|不过|不過|并且|並且|然后|然後)`)

var sentInterrupt = regexp.MustCompile(`(?i)\bsent\s+(it\s+|\S+\s+)?(a\s+|an\s+)?(ctrl[- ]?c|interrupt|stop)\b`)

var startedOn = regexp.MustCompile(`(?i)\b(started|set)\s+.{1,40}?\s+(on|working on)\s+`)

// optionsTail strips the " Options: …" sentence eval sim appends to a
// reply (the prompt's options, not something sheepdog claimed).
var optionsTail = regexp.MustCompile(`\s*Options: .*$`)

// Claim is one claim found in a line of speech.
type Claim struct {
	AtMS     int64     `json:"at_ms"`
	Side     Side      `json:"side"`
	Kind     ClaimKind `json:"kind"`
	Sentence string    `json:"sentence"`
}

// ExtractClaims returns the claims of one line of speech, in sentence
// order, at most one of each kind per sentence.
func ExtractClaims(s Speech) []Claim {
	var out []Claim
	text := optionsTail.ReplaceAllString(s.Text, "")
	for _, sent := range splitSentences(text) {
		for _, k := range sentenceClaims(sent) {
			out = append(out, Claim{AtMS: s.AtMS, Side: s.Side, Kind: k, Sentence: sent})
		}
	}
	return out
}

// sentenceClaims are the claim kinds one sentence makes.
func sentenceClaims(sent string) []ClaimKind {
	low := strings.ToLower(strings.TrimSpace(sent))
	if low == "" || matchAny(claimBlocker, low) {
		return nil
	}
	var out []ClaimKind
	for _, k := range claimOrder {
		if matchAny(claimPatterns[k], low) {
			out = append(out, k)
		}
	}
	// "I've started X on Y" puts a worker on a task: a sent prompt, not
	// a new agent.
	if has(out, ClaimStarted) && startedOn.MatchString(low) {
		out = append(without(out, ClaimStarted), ClaimSent)
	}
	// "Sent an interrupt" is a stop, not a sent prompt.
	if has(out, ClaimStopped) && sentInterrupt.MatchString(low) {
		out = without(out, ClaimSent)
	}
	// An approval or a refusal also reads as "done"; keep the specific
	// claim only.
	if has(out, ClaimApproved) || has(out, ClaimDeclined) || has(out, ClaimStopped) || has(out, ClaimClosed) || has(out, ClaimStarted) {
		out = without(out, ClaimDone)
	}
	return out
}

func splitSentences(text string) []string {
	var out []string
	for _, p := range sentenceSplit.Split(text, -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func matchAny(rs []*regexp.Regexp, s string) bool {
	for _, r := range rs {
		if r.MatchString(s) {
			return true
		}
	}
	return false
}

func has(ks []ClaimKind, k ClaimKind) bool {
	for _, x := range ks {
		if x == k {
			return true
		}
	}
	return false
}

func without(ks []ClaimKind, drop ...ClaimKind) []ClaimKind {
	var out []ClaimKind
	for _, k := range ks {
		if !has(drop, k) {
			out = append(out, k)
		}
	}
	return out
}
