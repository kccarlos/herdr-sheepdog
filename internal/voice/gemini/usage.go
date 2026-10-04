package gemini

import (
	"time"

	"google.golang.org/genai"
)

// Resume-context observability (work item "Gemini Live context retention
// across resumes", docs/handover-2026-10-04.md). After a resume the prompt
// token count has been seen to drop sharply. This file only measures: it
// records the usage report's per-modality token details and, once per
// resumed connection, one voice.resume_context event comparing the prompt
// size before the resume with the first usage report after it. Nothing
// here changes what the session sends or when.

// usageSnap is one usage report: prompt tokens in total and per modality
// (TEXT, AUDIO, ...), and the response side the same way.
type usageSnap struct {
	at       time.Time
	prompt   int32
	response int32
	promptBy map[string]int32
	respBy   map[string]int32
}

// modalityTotals sums token counts per modality; nil when none reported.
func modalityTotals(d []*genai.ModalityTokenCount) map[string]int32 {
	if len(d) == 0 {
		return nil
	}
	m := map[string]int32{}
	for _, x := range d {
		if x != nil {
			m[string(x.Modality)] += x.TokenCount
		}
	}
	return m
}

// copyTokens returns an independent copy, so a snapshot kept for later is
// not shared with a logged payload.
func copyTokens(m map[string]int32) map[string]int32 {
	if m == nil {
		return nil
	}
	c := make(map[string]int32, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func snapOf(at time.Time, u *genai.UsageMetadata) usageSnap {
	return usageSnap{
		at: at, prompt: u.PromptTokenCount, response: u.ResponseTokenCount,
		promptBy: modalityTotals(u.PromptTokensDetails), respBy: modalityTotals(u.ResponseTokensDetails),
	}
}

// resumeCtx is a resumed connection waiting for its first usage report.
type resumeCtx struct {
	resumedAt time.Time
	atHandle  usageSnap // last usage when the accepted handle was issued
	before    usageSnap // last usage before the resume
	handleAt  time.Time
}

func (u usageSnap) fields(prefix string, p map[string]any) {
	if u.at.IsZero() {
		return
	}
	p[prefix+"prompt"] = u.prompt
	if u.promptBy != nil {
		p[prefix+"prompt_by_modality"] = copyTokens(u.promptBy)
	}
	p[prefix+"at"] = u.at.UTC().Format(time.RFC3339Nano)
}

// noteUsageLocked records a usage report of the live connection and, when
// a resume is waiting for its first report, returns the
// voice.resume_context payload. Caller holds s.mu.
func (s *Session) noteUsageLocked(u *genai.UsageMetadata, live bool) map[string]any {
	snap := snapOf(s.now(), u)
	s.lastPromptTokens = u.PromptTokenCount
	var out map[string]any
	if live && s.resumeCtx != nil {
		out = s.resumeContextPayloadLocked(&snap)
	}
	if live {
		s.lastUsage = snap
	}
	return out
}

// startResumeContextLocked is called when a resumed connection is
// installed. A previous resume that never saw a usage report is
// returned as an event payload (without "after_*" fields). Caller holds
// s.mu.
func (s *Session) startResumeContextLocked() map[string]any {
	var stale map[string]any
	if s.resumeCtx != nil {
		stale = s.resumeContextPayloadLocked(nil)
	}
	s.resumeCtx = &resumeCtx{resumedAt: s.now(), atHandle: s.handleUsage, before: s.lastUsage, handleAt: s.handleAt}
	return stale
}

// resumeContextPayloadLocked builds the payload and clears the pending
// resume. after is the first usage report after the resume, if any.
func (s *Session) resumeContextPayloadLocked(after *usageSnap) map[string]any {
	r := s.resumeCtx
	s.resumeCtx = nil
	model := s.cfg.Model
	p := map[string]any{
		"backend":    s.cfg.Backend(),
		"model":      model,
		"resumed_at": r.resumedAt.UTC().Format(time.RFC3339Nano),
	}
	if !r.handleAt.IsZero() {
		p["handle_at"] = r.handleAt.UTC().Format(time.RFC3339Nano)
	}
	r.before.fields("before_", p)
	r.atHandle.fields("handle_", p)
	if after != nil {
		after.fields("after_", p)
		if r.before.prompt > 0 {
			p["delta_prompt"] = after.prompt - r.before.prompt
			p["ratio_prompt"] = float64(after.prompt) / float64(r.before.prompt)
		}
		p["after_ms"] = after.at.Sub(r.resumedAt).Milliseconds()
	} else {
		p["after_missing"] = true
	}
	return p
}
