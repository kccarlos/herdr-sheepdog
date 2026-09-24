package gen

import (
	"hash/fnv"
	"math/rand/v2"
	"strconv"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// The noise operations: what the voice models' speech recognition did to
// the shepherd's words in the logged sessions.
const (
	// OpDrop drops one word ("tell the sheepdog claude that…" heard
	// without "the").
	OpDrop = "drop"
	// OpHomophone swaps a word for one that sounds alike ("two" → "to",
	// "sixteen" → "16"; 2026-09-24 00:46:34 heard "What does it say?" as
	// "16").
	OpHomophone = "homophone"
	// OpTruncate keeps only the last k words: GPT-Live's partial relay
	// (2026-09-23 23:53:26 relayed "sometimes" of a whole sentence).
	OpTruncate = "truncate"
	// OpStray appends a stray token the recogniser picked up.
	OpStray = "stray"
)

// Ops lists the noise operations in the order the generator draws them.
var Ops = []string{OpDrop, OpHomophone, OpTruncate, OpStray}

// homophones is the swap table, lower case; a word with no entry is left
// alone and the op falls back to a drop.
var homophones = map[string][]string{
	"yes":       {"yeah", "yep", "yes."},
	"no":        {"know", "nope"},
	"two":       {"to", "too", "2"},
	"to":        {"two", "too"},
	"for":       {"four"},
	"sixteen":   {"16"},
	"one":       {"won", "1"},
	"right":     {"write"},
	"tests":     {"test", "texts"},
	"test":      {"text"},
	"mean":      {"mean?"},
	"say":       {"see"},
	"what's":    {"what"},
	"stop":      {"stuff", "stop."},
	"mind":      {"mine"},
	"never":     {"ever"},
	"sheepdog":  {"sheep dog"},
	"claude":    {"cloud", "clod"},
	"codex":     {"code x"},
	"status":    {"state us"},
	"find":      {"fine"},
	"there":     {"their"},
	"the":       {"a"},
	"it":        {"that"},
	"approve":   {"a proof"},
	"merge":     {"merch"},
	"finished":  {"finish"},
	"lags":      {"legs"},
	"agents":    {"ages"},
	"going":     {"gonna"},
	"happening": {"happen"},
}

// strays are tokens the recogniser appended: fillers, a cut-off word, a
// breath read as a word.
var strays = []string{"um", "uh", "so", "okay", "the", "and", "yeah", "hmm", "right"}

// Noise applies one noise operation, drawn from seed, to utterance and
// returns the result and the operation. Deterministic: the same inputs
// give the same output.
func Noise(utterance string, seed int64) (string, string) {
	r := rand.New(rand.NewPCG(uint64(seed), 0x5eedcafe))
	words := strings.Fields(utterance)
	if len(words) == 0 {
		return utterance, ""
	}
	op := Ops[r.IntN(len(Ops))]
	switch op {
	case OpHomophone:
		var idx []int
		for i, w := range words {
			if _, ok := homophones[bare(w)]; ok {
				idx = append(idx, i)
			}
		}
		if len(idx) > 0 {
			i := idx[r.IntN(len(idx))]
			alts := homophones[bare(words[i])]
			words[i] = alts[r.IntN(len(alts))]
			return strings.Join(words, " "), op
		}
		// No word in the table: drop one instead.
		fallthrough
	case OpDrop:
		if len(words) == 1 {
			// Dropping the only word leaves nothing to relay; a single
			// word is truncated to itself, so stray is the only noise.
			return utterance + " " + strays[r.IntN(len(strays))], OpStray
		}
		i := r.IntN(len(words))
		words = append(words[:i:i], words[i+1:]...)
		return strings.Join(words, " "), OpDrop
	case OpTruncate:
		if len(words) <= 2 {
			return utterance + " " + strays[r.IntN(len(strays))], OpStray
		}
		k := 1 + r.IntN(len(words)/2)
		return strings.Join(words[len(words)-k:], " "), op
	default: // OpStray
		return utterance + " " + strays[r.IntN(len(strays))], OpStray
	}
}

// bare is w lower-cased without surrounding punctuation, for the table.
func bare(w string) string {
	return strings.ToLower(strings.Trim(w, ".,?!;:\"'"))
}

// SeedFor derives a case's noise seed from the run seed, its id and the
// variant number, so adding a case never changes another's noise.
func SeedFor(seed int64, id string, k int) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strconv.FormatInt(seed, 10) + "/" + id + "/" + strconv.Itoa(k)))
	return int64(h.Sum64() >> 1)
}

// NoiseCase returns c with noise variant k applied to its utterance. The
// positive expectations are dropped: noise can remove the very words
// they depend on (a truncated "tell auth to run the tests" should not be
// carried out blindly), so a noisy case keeps only what must never happen
// — must_not_call, rail_blocked, prompt_must_not_contain — plus the
// phrasing limits that hold whatever was heard.
func NoiseCase(c eval.SVPCase, seed int64, k int) (eval.SVPCase, bool) {
	if !isUtterance(c) {
		return c, false
	}
	s := SeedFor(seed, c.ID, k)
	text, op := Noise(c.Request.Question, s)
	if op == "" || text == c.Request.Question {
		return c, false
	}
	v := derive(c, "~n"+strconv.Itoa(k)+"-"+op, "noise:"+op)
	v.Seed = s
	v.Request.Question = text
	v.Expect.ToolsUsedAnyOf = nil
	v.Expect.SpokenContainsAnyOf = nil
	v.Expect.FirstSentenceContainsAnyOf = nil
	v.Expect.NoTool = false
	v.Expect.NeedsUser = nil
	return v, true
}

// NoiseAssistantCase is NoiseCase for an assistant case. Its
// expectations are kept: the voice relays what it heard verbatim, and a
// tool choice the case rules out (a stop on "never mind") stays ruled out
// however the words were garbled.
func NoiseAssistantCase(c eval.AssistantCase, seed int64, k int) (eval.AssistantCase, bool) {
	s := SeedFor(seed, c.ID, k)
	text, op := Noise(c.Utterance, s)
	if op == "" || text == c.Utterance {
		return c, false
	}
	v := c
	v.ID = c.ID + "~n" + strconv.Itoa(k) + "-" + op
	if v.Parent == "" {
		v.Parent = c.ID
	}
	if v.Generator == "" {
		v.Generator = "noise:" + op
	} else {
		v.Generator += "+noise:" + op
	}
	v.Seed = s
	v.Observed, v.BaselineFails, v.PendingCheck = nil, nil, ""
	v.Utterance = text
	return v, true
}

// Options configures Expand.
type Options struct {
	// Paraphrase adds the job's paraphrases of each utterance case
	// (up to ParaphraseMax each; 0 = the whole table).
	Paraphrase    bool
	ParaphraseMax int
	// Noise is how many noisy variants to make of each case and of each
	// paraphrase.
	Noise int
	// Seed makes the noise reproducible.
	Seed int64
}

// Expand returns the generated cases for seeds (not the seeds
// themselves): paraphrases, then Noise noisy variants of each seed and
// each paraphrase. Cases that are not utterances (announce) are skipped.
func Expand(seeds []eval.SVPCase, opt Options) []eval.SVPCase {
	var out []eval.SVPCase
	for _, c := range seeds {
		if !isUtterance(c) {
			continue
		}
		variants := []eval.SVPCase{c}
		if opt.Paraphrase {
			ps := Paraphrase(c, opt.ParaphraseMax)
			for i := range ps {
				ps[i].Seed = opt.Seed
			}
			out = append(out, ps...)
			variants = append(variants, ps...)
		}
		for _, v := range variants {
			for k := 0; k < opt.Noise; k++ {
				if nv, ok := NoiseCase(v, opt.Seed, k); ok {
					out = append(out, nv)
				}
			}
		}
	}
	return out
}

// ExpandAssistant is Expand for assistant cases.
func ExpandAssistant(seeds []eval.AssistantCase, opt Options) []eval.AssistantCase {
	var out []eval.AssistantCase
	for _, c := range seeds {
		variants := []eval.AssistantCase{c}
		if opt.Paraphrase {
			ps := ParaphraseAssistant(c, opt.ParaphraseMax)
			for i := range ps {
				ps[i].Seed = opt.Seed
			}
			out = append(out, ps...)
			variants = append(variants, ps...)
		}
		for _, v := range variants {
			for k := 0; k < opt.Noise; k++ {
				if nv, ok := NoiseAssistantCase(v, opt.Seed, k); ok {
					out = append(out, nv)
				}
			}
		}
	}
	return out
}

// PickSeeds picks up to perJob utterance cases per job from cases, in
// order (perJob ≤ 0: every case with a job): a handful of extracted
// cases to expand alongside the regressions.
func PickSeeds(cases []eval.SVPCase, perJob int) []eval.SVPCase {
	count := map[string]int{}
	var out []eval.SVPCase
	for _, c := range cases {
		if c.Job == "" || !isUtterance(c) {
			continue
		}
		if perJob > 0 && count[c.Job] >= perJob {
			continue
		}
		count[c.Job]++
		out = append(out, c)
	}
	return out
}
