package scorecard

import (
	"sort"
	"strings"
	"time"

	"github.com/kccarlos/herdr-sheepdog/internal/eventlog"
)

// ResumeReport is the Gemini Live resume-retention measure (work item
// "Gemini Live context retention across resumes", docs/handover-2026-10-04.md):
// at every resumed connection, the prompt token count before the resume
// against the first one after it, per UTC day. Tokens are not recall: a
// smaller prompt may or may not have lost facts (docs/research/
// live-resume-context-2026-10-04.md). Measurement only.
//
// Rows come from voice.resume_context where the log has it (with
// per-modality numbers); older logs are derived from voice.resume
// (resumed:true) with the last voice.usage before it and the first one
// after it, and voice.handle_update's prompt_tokens for the accepted
// handle where present.
type ResumeReport struct {
	Resumes int         `json:"resumes"`
	Days    []ResumeDay `json:"days"`
	Rows    []ResumeRow `json:"rows"`
}

// ResumeDay is one UTC day's line.
type ResumeDay struct {
	Day     string `json:"day"`
	Resumes int    `json:"resumes"`
	// Before and After are the summed prompt tokens over the day's
	// resumes; Ratio is After/Before; MinRatio the worst resume.
	Before   int64   `json:"before"`
	After    int64   `json:"after"`
	Ratio    float64 `json:"ratio"`
	MinRatio float64 `json:"min_ratio"`
	// Modality sums, only over rows that carry them (voice.resume_context).
	ModalityBefore map[string]int64 `json:"modality_before,omitempty"`
	ModalityAfter  map[string]int64 `json:"modality_after,omitempty"`
	Derived        int              `json:"derived"` // rows derived from voice.usage, not voice.resume_context
	// First* is the day's resumes of a session that was never resumed
	// before (generation 1), Later* the resumes of an already resumed
	// session (generation 2 and up): the canary found that a handle
	// issued by a resumed connection restores only that connection's own
	// content (docs/research/live-resume-context-2026-10-04.md).
	// FreshAfterResume counts the reconnects that opened a fresh session
	// instead of resuming a resumed one (voice.handle_dropped reason
	// resumed_twice, the CEO's option 1): re-seeded, not resumed, so not
	// rows here.
	FreshAfterResume int     `json:"fresh_after_resume"`
	FirstN           int     `json:"first_n"`
	FirstRatio  float64 `json:"first_ratio"`
	LaterN      int     `json:"later_n"`
	LaterRatio  float64 `json:"later_ratio"`
	firstBefore int64
	firstAfter  int64
	laterBefore int64
	laterAfter  int64
}

// ResumeRow is one resumed connection.
type ResumeRow struct {
	TS           time.Time `json:"ts"`
	Source       string    `json:"source"` // resume_context | derived
	Before       int64     `json:"before"`
	HandlePrompt int64     `json:"handle_prompt,omitempty"`
	After        int64     `json:"after"`
	Ratio        float64   `json:"ratio"`
	// Generation counts the resumes of the session so far: 1 for the
	// first resume of a connection that was opened fresh.
	Generation  int              `json:"generation"`
	BeforeByMod map[string]int64 `json:"before_by_modality,omitempty"`
	AfterByMod  map[string]int64 `json:"after_by_modality,omitempty"`
	Backend     string           `json:"backend,omitempty"`
	Model       string           `json:"model,omitempty"`
}

type usagePayload struct {
	Prompt int64 `json:"prompt"`
}

type handleUpdatePayload struct {
	Accepted     bool  `json:"accepted"`
	PromptTokens int64 `json:"prompt_tokens"`
}

type resumePayload struct {
	Resumed    *bool `json:"resumed"`
	Generation *int  `json:"generation"` // logged since the resumed_twice rail
}

type handleDroppedPayload struct {
	Reason string `json:"reason"`
}

type resumeContextPayload struct {
	Backend      string           `json:"backend"`
	Model        string           `json:"model"`
	Before       int64            `json:"before_prompt"`
	BeforeBy     map[string]int64 `json:"before_prompt_by_modality"`
	HandlePrompt int64            `json:"handle_prompt"`
	After        int64            `json:"after_prompt"`
	AfterBy      map[string]int64 `json:"after_prompt_by_modality"`
	AfterMissing bool             `json:"after_missing"`
	Generation   *int             `json:"generation"`
}

func ratio(after, before int64) float64 {
	if before <= 0 {
		return 0
	}
	return float64(after) / float64(before)
}

// computeResumeRetention implements the resume-retention section.
func computeResumeRetention(events []eventlog.Event) ResumeReport {
	var rows []ResumeRow
	var lastUsage, handlePrompt int64
	gen := 0
	var pending *ResumeRow // a resumed connection waiting for its first usage
	derivedLast := false   // the last row came from derivation and may be replaced
	fresh := map[string]int{}
	for _, ev := range events {
		if ev.Type == "voice.handle_dropped" && decode[handleDroppedPayload](ev.Payload).Reason == "resumed_twice" {
			fresh[ev.TS.UTC().Format("2006-01-02")]++
		}
		switch {
		case ev.Source == "daemon" && ev.Type == "daemon.start", ev.Type == "voice.handle_dropped", ev.Type == "voice.session_refreshed":
			// A fresh session from here on.
			lastUsage, handlePrompt, pending, derivedLast, gen = 0, 0, nil, false, 0
		case ev.Type == "voice.usage":
			p := decode[usagePayload](ev.Payload)
			if pending != nil {
				pending.After = p.Prompt
				pending.Ratio = ratio(p.Prompt, pending.Before)
				rows = append(rows, *pending)
				pending, derivedLast = nil, true
			}
			lastUsage = p.Prompt
		case ev.Type == "voice.handle_update":
			if p := decode[handleUpdatePayload](ev.Payload); p.Accepted {
				handlePrompt = p.PromptTokens
			}
		case ev.Type == "voice.resume":
			p := decode[resumePayload](ev.Payload)
			if p.Resumed != nil && !*p.Resumed {
				// A fresh session: nothing to compare.
				lastUsage, handlePrompt, pending, derivedLast, gen = 0, 0, nil, false, 0
				continue
			}
			derivedLast = false
			gen++
			if p.Generation != nil {
				gen = *p.Generation // the daemon's own count, where logged
			}
			if lastUsage > 0 {
				pending = &ResumeRow{TS: ev.TS, Source: "derived", Before: lastUsage, HandlePrompt: handlePrompt, Generation: gen}
			}
		case ev.Type == "voice.resume_context":
			p := decode[resumeContextPayload](ev.Payload)
			if p.AfterMissing {
				continue
			}
			if p.Generation != nil {
				gen = *p.Generation
			}
			row := ResumeRow{
				TS: ev.TS, Source: "resume_context", Before: p.Before, HandlePrompt: p.HandlePrompt, After: p.After,
				Ratio: ratio(p.After, p.Before), Generation: gen, BeforeByMod: p.BeforeBy, AfterByMod: p.AfterBy, Backend: p.Backend, Model: p.Model,
			}
			if derivedLast && len(rows) > 0 {
				rows[len(rows)-1] = row // the same resume, now with modality detail
			} else {
				rows = append(rows, row)
			}
			derivedLast = false
		}
	}

	byDay := map[string]*ResumeDay{}
	for _, r := range rows {
		day := r.TS.UTC().Format("2006-01-02")
		d := byDay[day]
		if d == nil {
			d = &ResumeDay{Day: day, MinRatio: r.Ratio}
			byDay[day] = d
		}
		d.Resumes++
		d.Before += r.Before
		d.After += r.After
		if r.Ratio < d.MinRatio {
			d.MinRatio = r.Ratio
		}
		if r.Source == "derived" {
			d.Derived++
		}
		if r.Generation == 1 {
			d.FirstN++
			d.firstBefore += r.Before
			d.firstAfter += r.After
		} else if r.Generation > 1 {
			d.LaterN++
			d.laterBefore += r.Before
			d.laterAfter += r.After
		}
		if r.BeforeByMod != nil || r.AfterByMod != nil {
			if d.ModalityBefore == nil {
				d.ModalityBefore, d.ModalityAfter = map[string]int64{}, map[string]int64{}
			}
			for k, v := range r.BeforeByMod {
				d.ModalityBefore[strings.ToUpper(k)] += v
			}
			for k, v := range r.AfterByMod {
				d.ModalityAfter[strings.ToUpper(k)] += v
			}
		}
	}
	for day, n := range fresh {
		d := byDay[day]
		if d == nil {
			d = &ResumeDay{Day: day}
			byDay[day] = d
		}
		d.FreshAfterResume = n
	}
	rep := ResumeReport{Resumes: len(rows), Rows: rows}
	for _, d := range byDay {
		d.Ratio = ratio(d.After, d.Before)
		d.FirstRatio = ratio(d.firstAfter, d.firstBefore)
		d.LaterRatio = ratio(d.laterAfter, d.laterBefore)
		rep.Days = append(rep.Days, *d)
	}
	sort.Slice(rep.Days, func(i, j int) bool { return rep.Days[i].Day < rep.Days[j].Day })
	return rep
}
