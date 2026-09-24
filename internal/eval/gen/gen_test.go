package gen

import (
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

func TestParaphraseTable(t *testing.T) {
	sizes := map[string]int{}
	for job, list := range Paraphrases {
		prefix, _, _ := strings.Cut(job, "-")
		sizes[prefix] += len(list)
		seen := map[string]bool{}
		for _, p := range list {
			if seen[p] {
				t.Errorf("%s: duplicate paraphrase %q", job, p)
			}
			seen[p] = true
		}
	}
	for _, job := range []string{"J1", "J2", "J3", "J4", "J5", "J6"} {
		if sizes[job] < 15 {
			t.Errorf("%s has %d paraphrases, want about 15", job, sizes[job])
		}
	}
	// "never mind" is never a stop, and a stop is never a never mind
	// (docs/iteration-2.md J6): the two lists must not overlap in intent.
	for _, p := range Paraphrases["J6-nevermind"] {
		if strings.Contains(strings.ToLower(p), "stop") {
			t.Errorf("J6-nevermind paraphrase %q says stop", p)
		}
	}
}

func TestParaphraseSubstitutesWithinJob(t *testing.T) {
	c := eval.SVPCase{
		ID: "seed", RosterOnly: true,
		Agents:  []eval.SVPAgent{{Alias: "docs"}, {Alias: "auth", Pending: &eval.SVPPending{Question: "ok?"}}},
		Request: eval.SVPRequest{Kind: "utterance", Question: "Tell auth to deploy it."},
		Expect:  eval.SVPExpect{ToolsUsedAnyOf: []string{"prompt"}},
	}
	c.Job = "J3"
	c.Observed = &eval.SVPObserved{Spoken: "logged"}
	yes := true
	c.BaselineFails = &yes
	ps := Paraphrase(c, 0)
	if len(ps) != len(Paraphrases["J3"]) {
		t.Fatalf("got %d paraphrases, want %d", len(ps), len(Paraphrases["J3"]))
	}
	for _, p := range ps {
		if strings.Contains(p.Request.Question, "{alias}") {
			t.Errorf("%s: unsubstituted %q", p.ID, p.Request.Question)
		}
		if p.Parent != "seed" || p.Generator != "paraphrase" || p.Observed != nil || p.BaselineFails != nil {
			t.Errorf("%s: meta = %+v", p.ID, p.Meta)
		}
		if len(p.Expect.ToolsUsedAnyOf) != 1 {
			t.Errorf("%s: a paraphrase keeps the expectations", p.ID)
		}
	}
	if !strings.Contains(ps[0].Request.Question, "auth") {
		t.Errorf("{alias} should name the agent with the open prompt: %q", ps[0].Request.Question)
	}
	c.Job = ""
	if len(Paraphrase(c, 0)) != 0 {
		t.Error("a case without a job has no paraphrases")
	}
}

func TestNoiseDeterministicAndCoversEveryOp(t *testing.T) {
	const utt = "Tell the sheepdog claude that the iOS app voice still has lags, yes, two of them"
	ops := map[string]bool{}
	for s := int64(0); s < 200; s++ {
		a, opA := Noise(utt, s)
		b, opB := Noise(utt, s)
		if a != b || opA != opB {
			t.Fatalf("seed %d not deterministic: %q/%s vs %q/%s", s, a, opA, b, opB)
		}
		if a == utt {
			t.Errorf("seed %d: %s left the utterance unchanged", s, opA)
		}
		ops[opA] = true
		if opA == OpTruncate && !strings.HasSuffix(utt, a) {
			t.Errorf("truncate %q is not a tail of the utterance", a)
		}
	}
	for _, op := range Ops {
		if !ops[op] {
			t.Errorf("op %s never drawn in 200 seeds", op)
		}
	}
	if got, op := Noise("", 1); got != "" || op != "" {
		t.Errorf("empty utterance: %q %q", got, op)
	}
}

// A noisy case keeps only the expectations noise cannot invalidate.
func TestNoiseCaseDropsPositiveExpectations(t *testing.T) {
	yes := true
	c := eval.SVPCase{
		ID:      "seed",
		Request: eval.SVPRequest{Kind: "utterance", Question: "Tell auth to run the tests."},
		Expect: eval.SVPExpect{
			ToolsUsedAnyOf: []string{"prompt"}, SpokenContainsAnyOf: []string{"tests"}, NoTool: true, NeedsUser: &yes,
			FirstSentenceContainsAnyOf: []string{"auth"},
			MustNotCall:                []string{"interrupt"}, PromptMustNotContain: []string{"["}, SpokenMaxWords: 20,
		},
	}
	v, ok := NoiseCase(c, 1, 0)
	if !ok {
		t.Fatal("no noise")
	}
	e := v.Expect
	if e.ToolsUsedAnyOf != nil || e.SpokenContainsAnyOf != nil || e.NoTool || e.NeedsUser != nil || e.FirstSentenceContainsAnyOf != nil {
		t.Errorf("positive expectations kept: %+v", e)
	}
	if len(e.MustNotCall) != 1 || len(e.PromptMustNotContain) != 1 || e.SpokenMaxWords != 20 {
		t.Errorf("negative expectations lost: %+v", e)
	}
	if v.Seed == 0 || !strings.HasPrefix(v.Generator, "noise:") || v.Parent != "seed" {
		t.Errorf("meta = %+v", v.Meta)
	}
	v2, _ := NoiseCase(c, 1, 0)
	if v2.Request.Question != v.Request.Question || v2.ID != v.ID {
		t.Error("NoiseCase not deterministic")
	}
}

func TestExpandSkipsAnnounceAndCounts(t *testing.T) {
	utt := eval.SVPCase{ID: "u", Request: eval.SVPRequest{Kind: "utterance", Question: "What's going on?"}}
	utt.Job = "J1"
	ann := eval.SVPCase{ID: "a", Request: eval.SVPRequest{Kind: "announce", Agent: "auth", Event: "done"}}
	ann.Job = "J4"
	out := Expand([]eval.SVPCase{utt, ann}, Options{Paraphrase: true, Noise: 2, Seed: 1})
	// 14 paraphrases (the table's 15 minus the seed's own words), then
	// up to 2 noisy variants of each of the 15 (seed and paraphrases).
	if len(out) < 14+2*15-5 || len(out) > 14+2*15 {
		t.Errorf("got %d cases", len(out))
	}
	ids := map[string]bool{}
	for _, c := range out {
		if c.Parent != "u" {
			t.Errorf("%s: parent %q", c.ID, c.Parent)
		}
		if ids[c.ID] {
			t.Errorf("duplicate id %s", c.ID)
		}
		ids[c.ID] = true
	}
}
