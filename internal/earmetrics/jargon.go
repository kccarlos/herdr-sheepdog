package earmetrics

// Jargon is the checked-in list of worker/developer terms that a user
// with no engineering background should not have to parse out of a spoken
// line (docs/dev/iteration-2.md "Scorecard metrics": "unfamiliar terms per
// spoken line"). It is a plain slice so a real transcript can grow it
// without touching the matching logic. Matching is case-insensitive and
// whole-word (WholeWordMatches), so "CI" does not fire on "specifically"
// and "PR" does not fire on "spring".
var Jargon = []string{
	"jitter",
	"buffer",
	"refactor",
	"worktree",
	"rebase",
	"merge",
	"merged",
	"CI",
	"lint",
	"race",
	"SessionCore",
	"tool surface",
	"dead code",
	"policy",
	"target",
	"delegation",
	"transcript",
	"daemon",
	"endpoint",
	"schema",
	"migration",
	"regression",
	"flaky",
	"PR",
	"branch",
	"commit",
	"diff",
	"stack trace",
	"latency",
	"underrun",
	"DSP",
	"WebRTC",
	"JSONL",
	"socket",
}
