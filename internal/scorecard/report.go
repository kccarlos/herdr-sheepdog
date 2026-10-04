package scorecard

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/earmetrics"
	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// Report is every docs/iteration-2.md scorecard metric computed over one
// slice of the event log (a session, or the whole loaded range).
type Report struct {
	Label         string            `json:"label"`
	Events        int               `json:"events"`
	From          time.Time         `json:"from,omitempty"`
	To            time.Time         `json:"to,omitempty"`
	Latency       LatencyReport     `json:"latency"`
	TurnLatency   TurnLatencyReport `json:"turn_latency"`
	Ear           EarReport         `json:"ear"`
	RepeatAsk     RepeatAskReport   `json:"repeat_ask"`
	Lost          LostReport        `json:"lost_utterance"`
	Drop          DropReport        `json:"brain_line_drop"`
	CtrlC         CtrlCReport       `json:"ctrl_c"`
	Comprehension int               `json:"comprehension_follow_ups"`
	Duplicate     DuplicateReport   `json:"duplicate_speech"`
	Resume        ResumeReport      `json:"resume_retention"`
}

// Analyze computes every metric over events, labelling the result label.
func Analyze(label string, events []eventlog.Event) Report {
	r := Report{Label: label, Events: len(events)}
	if len(events) > 0 {
		r.From, r.To = events[0].TS, events[len(events)-1].TS
	}
	r.Latency = computeLatency(events)
	r.TurnLatency = computeTurnLatency(events)
	r.Ear = computeEar(events)
	r.RepeatAsk = computeRepeatAsk(events)
	r.Lost = computeLost(events)
	r.Drop = computeDrop(events)
	r.CtrlC = computeCtrlC(events)
	r.Comprehension = computeComprehension(events)
	r.Duplicate = computeDuplicateSpeech(events)
	r.Resume = computeResumeRetention(events)
	return r
}

// WriteJSON writes reports as a JSON array, one Report per session (or a
// single "total" element when sessions were not split).
func WriteJSON(w io.Writer, reports []Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(reports)
}

// WriteMarkdown writes reports as one markdown report, one section per
// session, each followed by its own tables. cmd is the exact command line
// used to produce the report, recorded so the report is reproducible.
func WriteMarkdown(w io.Writer, reports []Report, cmd string) error {
	var b strings.Builder
	b.WriteString("# Scorecard\n\n")
	if cmd != "" {
		fmt.Fprintf(&b, "Command: `%s`\n\n", cmd)
	}
	for _, r := range reports {
		writeMarkdownSection(&b, r)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeMarkdownSection(b *strings.Builder, r Report) {
	fmt.Fprintf(b, "## %s\n\n", r.Label)
	fmt.Fprintf(b, "%d events", r.Events)
	if !r.From.IsZero() {
		fmt.Fprintf(b, ", %s to %s", r.From.Format(time.RFC3339), r.To.Format(time.RFC3339))
	}
	b.WriteString("\n\n")

	b.WriteString("### Latency\n\n")
	b.WriteString("| metric | n | mean | median | p90 |\n|---|---|---|---|---|\n")
	writeStatsRow(b, "answer latency (ms)", r.Latency.AnswerLatencyMS)
	writeStatsRow(b, "reply latency incl. late pushes (ms)", r.Latency.ReplyLatencyMS)
	writeStatsRow(b, "time to first word (ms)", r.Latency.TimeToFirstWordMS)
	writeStatsRow(b, "brain.step prompt_tokens", r.Latency.BrainStepPromptTokens)
	for _, kind := range sortedKeys(r.Latency.BrainTurnDurByKind) {
		writeStatsRow(b, "brain.turn dur_ms ("+kind+")", r.Latency.BrainTurnDurByKind[kind])
	}
	b.WriteString("\n")

	writeTurnLatency(b, r.TurnLatency)

	b.WriteString("### Ear metrics\n\n")
	b.WriteString("| pool | n | mean words | median words | p90 words | share > 30 words | share with dash | share ending closed | mean jargon/line |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	writeEarRow(b, "spoken (tool_result + push)", r.Ear.Spoken)
	writeEarRow(b, "brain lines dropped (push_late, added:false)", r.Ear.DroppedBrainLines)
	b.WriteString("\n")

	fmt.Fprintf(b, "### Repeat-ask rate\n\n%d / %d utterances (%.1f%%)\n\n", r.RepeatAsk.Repeats, r.RepeatAsk.Total, r.RepeatAsk.Rate*100)

	fmt.Fprintf(b, "### Lost-utterance rate\n\n%d / %d utterance boundaries (%.1f%%). voice_initiative_ignored: %d. echo_ignored: %d.\n\n",
		r.Lost.Lost, r.Lost.Total, r.Lost.Rate*100, r.Lost.VoiceInitiativeIgnored, r.Lost.EchoIgnored)

	fmt.Fprintf(b, "### Brain-line drop rate\n\n%d / %d brain.push_late dropped (%.1f%%): %d needs_user, %d not, %d with empty spoken text.\n\n",
		r.Drop.Dropped, r.Drop.Total, r.Drop.DropRate*100, r.Drop.DroppedNeedsUser, r.Drop.DroppedNotNeedsUser, r.Drop.DroppedEmptySpoken)
	if len(r.Drop.FallbackByReason) > 0 {
		b.WriteString("brain.fallback by reason:\n\n")
		for _, reason := range sortedKeys(r.Drop.FallbackByReason) {
			fmt.Fprintf(b, "- %s: %d\n", reason, r.Drop.FallbackByReason[reason])
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(b, "### Ctrl-C sends\n\n%d sends, %d preceded by a stop word within %s.\n\n", r.CtrlC.Total, r.CtrlC.PrecededByStopCount, stopWindow)
	for _, s := range r.CtrlC.Sends {
		fmt.Fprintf(b, "- %s pane=%s preceded_by_stop=%t trigger=%q\n", s.TS.Format(time.RFC3339), s.PaneID, s.PrecededByStop, s.TriggerText)
	}
	if len(r.CtrlC.Sends) > 0 {
		b.WriteString("\n")
	}

	fmt.Fprintf(b, "### Comprehension follow-ups\n\n%d\n\n", r.Comprehension)

	fmt.Fprintf(b, "### Duplicate speech\n\n%d instance(s) within %s sharing ≥ %.0f%% of words.\n\n", r.Duplicate.Count, duplicateWindow, duplicateOverlapThreshold*100)
	for _, d := range r.Duplicate.Instances {
		fmt.Fprintf(b, "- push %q at %s / tool_result %q at %s\n", d.PushText, d.PushTS.Format(time.RFC3339), d.Spoken, d.ToolResultTS.Format(time.RFC3339))
	}
	if len(r.Duplicate.Instances) > 0 {
		b.WriteString("\n")
	}

	writeResumeRetention(b, r.Resume)
}

// writeResumeRetention prints the daily resume-retention lines: prompt
// tokens after / before a Gemini Live resume (tokens, not recall).
func writeResumeRetention(b *strings.Builder, r ResumeReport) {
	fmt.Fprintf(b, "### Resume retention (Gemini Live)\n\n%d resumed connection(s). Prompt tokens after / before the resume; a drop is not proof of lost facts.\n\n", r.Resumes)
	if r.Resumes == 0 {
		return
	}
	b.WriteString("| day | resumes | after / before | ratio | worst | per modality after / before | derived |\n|---|---|---|---|---|---|---|\n")
	for _, d := range r.Days {
		var mods []string
		for _, k := range sortedKeys(d.ModalityBefore) {
			mods = append(mods, fmt.Sprintf("%s %d/%d", k, d.ModalityAfter[k], d.ModalityBefore[k]))
		}
		m := strings.Join(mods, ", ")
		if m == "" {
			m = "n/a"
		}
		fmt.Fprintf(b, "| %s | %d | %d / %d | %.2f | %.2f | %s | %d |\n", d.Day, d.Resumes, d.After, d.Before, d.Ratio, d.MinRatio, m, d.Derived)
	}
	b.WriteString("\n")
}

func writeTurnLatency(b *strings.Builder, r TurnLatencyReport) {
	fmt.Fprintf(b, "### Turn latency\n\n%d CEO utterances (turn.latency, %s)", r.N, r.Source)
	if len(r.Outcomes) > 0 {
		b.WriteString(":")
		for i, o := range sortedKeys(r.Outcomes) {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(b, " %s %d", o, r.Outcomes[o])
		}
	}
	b.WriteString(".\n\n")
	if r.N == 0 {
		return
	}
	b.WriteString("| segment | n | p50 | p90 | mean |\n|---|---|---|---|---|\n")
	for _, name := range TurnLatencySegments {
		if s, ok := r.Segments[name]; ok {
			fmt.Fprintf(b, "| %s | %d | %.0f | %.0f | %.0f |\n", name, s.N, s.Median, s.P90, s.Mean)
		}
	}
	b.WriteString("\n")
}

func writeStatsRow(b *strings.Builder, name string, s Stats) {
	fmt.Fprintf(b, "| %s | %d | %.0f | %.0f | %.0f |\n", name, s.N, s.Mean, s.Median, s.P90)
}

func writeEarRow(b *strings.Builder, name string, s earmetrics.Summary) {
	fmt.Fprintf(b, "| %s | %d | %.1f | %.1f | %.1f | %.1f%% | %.1f%% | %.1f%% | %.2f |\n",
		name, s.N, s.MeanWords, s.MedianWords, s.P90Words,
		s.ShareOverThirtyWords*100, s.ShareWithDash*100, s.ShareEndingClosed*100, s.MeanJargonPerLine)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
