package main

import (
	"os"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/brain"
	"github.com/kccarlos/herdr-sheepdog/internal/eval"
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
)

// The judge every eval layer uses (tier 1 eval sim, tier 2 bench run,
// tier 3, bench report's rescoring, bench rejudge, eval calib, eval
// --live): Claude Sonnet through the Claude Code CLI unless --judge, or
// SHEEPDOG_JUDGE for every command at once, says otherwise.

// judgeHelp is the --judge flag's help line.
const judgeHelp = "  --judge KIND          judge backend: claude (default: Sonnet via `claude -p`, the\n" +
	"                        subscription, no API key), vertex (gemini-3.1-flash-lite), ollama\n" +
	"                        or fake; SHEEPDOG_JUDGE=kind[:model] sets the default"

// judgeChoice is the judge kind and model: the flags when given, else
// SHEEPDOG_JUDGE ("claude", "vertex:gemini-3.1-flash-lite"), else
// eval.DefaultJudgeKind with the backend's default model.
func judgeChoice(kind, model string) (string, string) {
	if kind != "" {
		return kind, model
	}
	if env := strings.TrimSpace(os.Getenv("SHEEPDOG_JUDGE")); env != "" {
		k, m, _ := strings.Cut(env, ":")
		if model == "" {
			model = m
		}
		return strings.TrimSpace(k), model
	}
	return eval.DefaultJudgeKind, model
}

// judgeModelID is the model id a judge choice resolves to, for setup
// lines and help.
func judgeModelID(kind, model string) string {
	switch {
	case model != "":
		return model
	case kind == eval.BackendClaude:
		return eval.DefaultClaudeModel
	case kind == eval.BackendOllama:
		return eval.DefaultOllamaModel
	case kind == eval.BackendVertex:
		return eval.DefaultVertexModel
	}
	return kind
}

// claudeJudgeSem bounds the Claude CLI judge's processes across one
// command: each call is a `claude -p` process on the subscription, and
// the 5-hour usage window is shared with the user's own sessions.
var claudeJudgeSem = make(chan struct{}, 3)

// judgeVertex is the Vertex credentials a judge backend may need.
type judgeVertex struct{ key, project, ollamaURL string }

// newJudgeModel builds a judge's (or simulated user's) model: Patient on
// sem for vertex and ollama, on claudeJudgeSem for the Claude judge. A
// fake backend returns a nil model.
func newJudgeModel(kind, model string, v judgeVertex, sem chan struct{}, log logging.Logger) (brain.Model, string, error) {
	m, id, err := eval.NewBackend(eval.BackendOptions{Kind: kind, Model: model, VertexAPIKey: v.key, VertexProject: v.project, OllamaURL: v.ollamaURL, DeepSeekAPIKey: os.Getenv("DEEPSEEK_API_KEY")}, log)
	if err != nil || m == nil {
		return m, id, err
	}
	tries := 8
	if kind == eval.BackendClaude {
		sem, tries = claudeJudgeSem, 3
	}
	return eval.Patient(m, sem, tries, altFor(kind, id)), id, nil
}

// newLLMJudge is the judge over m, metered under "judge". The Claude
// judge skips the rationale call (a no carries its evidence instead).
func newLLMJudge(m brain.Model, id, kind string, meter *eval.Meter) *eval.LLMJudge {
	return &eval.LLMJudge{Model: eval.Metered(m, meter, "judge"), ModelID: id, NoRationale: kind == eval.BackendClaude}
}
