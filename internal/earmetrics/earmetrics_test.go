package earmetrics

import (
	"reflect"
	"testing"
)

// contextTabLine is the real merged-PRs line from the 2026-09-23 log that
// docs/dev/iteration-2.md's WP0a brief quotes for this test (roughly 98
// words by strings.Fields, 4 em dashes, at least 2 PR numbers, several
// jargon hits, ending in neither a question nor a closer). The brief's
// "~101 words, dashes 5" is a rough hand count from reading it aloud, not
// an exact token count; strings.Fields on the literal text gives 98
// words and 4 " — " occurrences (verified independently with Python).
const contextTabLine = `Both landed: the Context tab is merged as daemon #49 — a new /debug tab beside Supervisor showing the brain's exact request and the voice model's context side by side, linked, with a scrubber and diffs against the previous step — and the first app refactor went in as #12, dead code out and duplicated state merged, behaviour unchanged with 245 tests green. It's asking whether to carry on with the next two in parallel — the daemon's dead-code and docs cleanup then removing the legacy tool surface, and the app's testable SessionCore — and I'd say yes.`

const twoAgentsLine = `Two of the four are still going: discoursecopilot-claude has been at its release work for about 56 minutes.`

const versionLine = `Version 2.1.0 is out on GitHub and marked Latest. Shall we delete it?`

func TestMeasure(t *testing.T) {
	tests := []struct {
		name string
		text string
		want Metrics
	}{
		{
			name: "context tab merged PRs line",
			text: contextTabLine,
			want: Metrics{
				Words:            98,
				Sentences:        2,
				Dashes:           4,
				Semicolons:       0,
				Technical:        2, // #49, #12
				EndsWithQuestion: false,
				EndsWithCloser:   false,
			},
		},
		{
			name: "two agents status, one sentence",
			text: twoAgentsLine,
			want: Metrics{
				Words:            18,
				Sentences:        1,
				Dashes:           0,
				Semicolons:       0,
				Technical:        0,
				EndsWithQuestion: false,
				EndsWithCloser:   false,
			},
		},
		{
			name: "version line ends with a question",
			text: versionLine,
			want: Metrics{
				Words:            13,
				Sentences:        2,
				Dashes:           0,
				Semicolons:       0,
				Technical:        1, // 2.1.0
				EndsWithQuestion: true,
				EndsWithCloser:   false,
			},
		},
		{
			name: "empty text",
			text: "",
			want: Metrics{},
		},
		{
			name: "closer with no question",
			text: "The fix is in and the tests pass. Nothing needs you.",
			want: Metrics{
				Words:            11,
				Sentences:        2,
				EndsWithQuestion: false,
				EndsWithCloser:   true,
			},
		},
		{
			name: "bare done closer",
			text: "Pushed the fix. Done.",
			want: Metrics{
				Words:          4,
				Sentences:      2,
				EndsWithCloser: true,
			},
		},
		{
			name: "semicolon and double-hyphen dash",
			text: "It failed once -- flaky; I reran it and it passed.",
			want: Metrics{
				Words:      11,
				Sentences:  1,
				Dashes:     1,
				Semicolons: 1,
			},
		},
		{
			name: "ms value and file path are technical",
			text: "It took 1400 ms and touched internal/scorecard/report.go.",
			want: Metrics{
				Words:     7,
				Sentences: 1,
				Technical: 2, // 1400 ms, the file path
			},
		},
		{
			name: "compact ms value",
			text: "That step took 150ms.",
			want: Metrics{
				Words:     4,
				Sentences: 1,
				Technical: 1,
			},
		},
		{
			name: "git hash is technical but a plain hex-alphabet word is not",
			text: "The commit is a1b2c3d and the cafe is closed.",
			want: Metrics{
				Words:     9,
				Sentences: 1,
				Technical: 1, // a1b2c3d only; "cafe" has no digit
			},
		},
		{
			name: "trailing clause with no terminal punctuation still counts as one sentence",
			text: "Still waiting on the build",
			want: Metrics{
				Words:     5,
				Sentences: 1,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Measure(tt.text)
			got.Jargon = nil // checked separately below
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Measure(%q) = %+v, want %+v", tt.text, got, tt.want)
			}
		})
	}
}

func TestMeasureJargon(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "no jargon in a plain sentence",
			text: "The tests pass and everything is green.",
			want: nil,
		},
		{
			name: "single-word jargon, case-insensitive",
			text: "The daemon hit a race in the socket buffer.",
			want: []string{"buffer", "race", "daemon", "socket"},
		},
		{
			name: "multi-word jargon phrase",
			text: "It's dead code, cleaned off the tool surface, no stack trace left.",
			want: []string{"tool surface", "dead code", "stack trace"},
		},
		{
			name: "PR does not fire inside spring, CI does not fire inside specifically",
			text: "It's spring, and this is specifically about the release.",
			want: nil,
		},
		{
			name: "the real context tab line has several jargon hits",
			text: contextTabLine,
			want: nil, // checked as a minimum count below, not an exact list
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findJargon(tt.text)
			if tt.name == "the real context tab line has several jargon hits" {
				if len(got) < 4 {
					t.Fatalf("findJargon(contextTabLine) = %v, want at least 4 hits", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("findJargon(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestAggregate(t *testing.T) {
	ms := []Metrics{
		{Words: 10, Dashes: 0, EndsWithQuestion: false, Jargon: nil},
		{Words: 20, Dashes: 1, EndsWithQuestion: true, Jargon: []string{"daemon"}},
		{Words: 40, Dashes: 1, EndsWithCloser: true, Jargon: []string{"daemon", "race"}},
		{Words: 100, Dashes: 0, Jargon: nil},
	}
	s := Aggregate(ms)
	if s.N != 4 {
		t.Fatalf("N = %d, want 4", s.N)
	}
	wantMean := (10.0 + 20 + 40 + 100) / 4
	if s.MeanWords != wantMean {
		t.Fatalf("MeanWords = %v, want %v", s.MeanWords, wantMean)
	}
	if s.ShareOverThirtyWords != 0.5 { // 40 and 100 are > 30
		t.Fatalf("ShareOverThirtyWords = %v, want 0.5", s.ShareOverThirtyWords)
	}
	if s.ShareWithDash != 0.5 {
		t.Fatalf("ShareWithDash = %v, want 0.5", s.ShareWithDash)
	}
	if s.ShareEndingClosed != 0.5 { // one question, one closer
		t.Fatalf("ShareEndingClosed = %v, want 0.5", s.ShareEndingClosed)
	}
	wantJargonMean := (0.0 + 1 + 2 + 0) / 4
	if s.MeanJargonPerLine != wantJargonMean {
		t.Fatalf("MeanJargonPerLine = %v, want %v", s.MeanJargonPerLine, wantJargonMean)
	}
}

func TestAggregateEmpty(t *testing.T) {
	if s := Aggregate(nil); s.N != 0 {
		t.Fatalf("Aggregate(nil).N = %d, want 0", s.N)
	}
}

func TestWordOverlap(t *testing.T) {
	tests := []struct {
		a, b string
		want float64
	}{
		{"discoursecopilot-claude finished.", "discoursecopilot-claude finished the whole job.", 1}, // "discoursecopilot claude finished" all in the shorter
		{"", "anything", 0},
		{"totally different text here", "nothing shared at all", 0},
	}
	for _, tt := range tests {
		got := WordOverlap(tt.a, tt.b)
		if got != tt.want {
			t.Fatalf("WordOverlap(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
