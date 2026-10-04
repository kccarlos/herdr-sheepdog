package gemini

// The heard watch and the re-queue of unheard lines (#89, #101, #108,
// #115) judged from word coverage whether the CEO heard a push and put
// owed lines back in the queue. In the dual-brain design (ablation G3,
// docs/design/dual-brain.md) the Gemini session only reports what
// physically happened — the CEO cut a line off after N s and heard only
// part of it (briefing.go noteCutLocked; the brain's SaidCut) — and the
// fast brain decides whether to say it again. internal/voice keeps the
// tracker for the GPT-Live adapter, which this experiment does not
// change.
