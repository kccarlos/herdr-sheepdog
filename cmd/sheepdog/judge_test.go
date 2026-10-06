package main

import (
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/eval"
)

// The judge is Claude unless a flag or SHEEPDOG_JUDGE says otherwise.
func TestJudgeChoice(t *testing.T) {
	t.Setenv("SHEEPDOG_JUDGE", "")
	if k, m := judgeChoice("", ""); k != eval.BackendClaude || judgeModelID(k, m) != "sonnet" {
		t.Errorf("default %s %s", k, m)
	}
	t.Setenv("SHEEPDOG_JUDGE", "vertex:gemini-3.1-flash-lite")
	if k, m := judgeChoice("", ""); k != eval.BackendVertex || m != "gemini-3.1-flash-lite" {
		t.Errorf("env %s %s", k, m)
	}
	if k, m := judgeChoice("fake", ""); k != eval.BackendFake || m != "" {
		t.Errorf("flag %s %s", k, m)
	}
	t.Setenv("SHEEPDOG_JUDGE", "vertex")
	if k, m := judgeChoice("", ""); judgeModelID(k, m) != eval.DefaultVertexModel {
		t.Errorf("vertex default model %s %s", k, m)
	}
}
