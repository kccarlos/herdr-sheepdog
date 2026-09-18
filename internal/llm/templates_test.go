package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/prompts"
)

func TestTemplatesRender(t *testing.T) {
	for _, name := range []string{TemplateSummarizeBlock, TemplateResolveInstruction, TemplateSpokenAnswer} {
		s, err := prompts.Render(name, map[string]string{"alias": "auth", "screen": "S", "user_text": "U", "agents": "A", "hint": "", "question": "Q", "raw": "R"})
		if strings.Contains(s, "{") && strings.Contains(s[strings.Index(s, "{"):], "{alias}") {
			t.Errorf("%s: alias not rendered", name)
		}
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.Contains(s, "{alias}") || strings.Contains(s, "{screen}") || strings.Contains(s, "{user_text}") || strings.Contains(s, "{agents}") || strings.Contains(s, "{hint}") || strings.Contains(s, "{question}") || strings.Contains(s, "{raw}") {
			t.Errorf("%s: unrendered placeholder in %q", name, s)
		}
		if !strings.Contains(s, "JSON") {
			t.Errorf("%s: no JSON contract", name)
		}
	}
}

func TestSummarizeBlockParsesAndNormalises(t *testing.T) {
	screen := fixture(t, "codex-approval-prompt.screen.txt")
	reply := "```json\n" + `{"spoken":"auth is asking whether it can run a printf command to create hello.txt at /tmp/x/repo/hello.txt.","question":"whether it can create hello.txt","is_question":true,"needs_decision":true,"options":[{"label":"Yes, proceed","key":"Enter"},{"label":"Yes, and don't ask again","key":"p"},{"label":"No, tell Codex what to do differently","key":"ESC"},{"label":"","key":"x"}],"answer_keys":{"yes":["enter"],"no":[]}}` + "\n```"
	f := newFake(t, ok(reply))
	c := newClient(t, f, []string{"k"}, nil)
	got, err := c.SummarizeBlock(context.Background(), screen, "auth")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsQuestion || !got.NeedsDecision || got.Question != "whether it can create hello.txt" {
		t.Errorf("flags: %+v", got)
	}
	if strings.Contains(got.Spoken, "/tmp") {
		t.Errorf("path leaked: %q", got.Spoken)
	}
	if len(got.Options) != 3 || got.Options[0].Key != "enter" || got.Options[2].Key != "esc" {
		t.Errorf("options: %+v", got.Options)
	}
	if strings.Join(got.AnswerKeys.Yes, " ") != "enter" || strings.Join(got.AnswerKeys.No, " ") != "esc" {
		t.Errorf("answer keys not filled from options: %+v", got.AnswerKeys)
	}
	// The prompt carried the alias and the screen tail.
	req := f.Requests()[0]
	if !strings.Contains(req.user, "Would you like to run the following command?") || !strings.Contains(req.user, `"auth"`) {
		t.Errorf("prompt missing screen or alias: %q", req.user[:200])
	}
}

func TestSummarizeBlockNotAQuestion(t *testing.T) {
	f := newFake(t, ok(`{"spoken":"auth is idle at its prompt","question":"nothing","is_question":false,"needs_decision":true,"options":[{"label":"Yes","key":"enter"}],"answer_keys":{"yes":["enter"],"no":["esc"]}}`))
	c := newClient(t, f, []string{"k"}, nil)
	got, err := c.SummarizeBlock(context.Background(), fixture(t, "codex-after-ctrl-c.screen.txt"), "auth")
	if err != nil {
		t.Fatal(err)
	}
	if got.IsQuestion || got.NeedsDecision || got.Question != "" || len(got.AnswerKeys.Yes) != 0 || got.Spoken != "auth is idle at its prompt." {
		t.Errorf("non-question should clear decision fields: %+v", got)
	}
}

func TestSummarizeBlockTruncatesInput(t *testing.T) {
	big := strings.Repeat("old line that should be dropped\n", 1000) + "NEWEST LINE\n"
	f := newFake(t, ok(`{"spoken":"x","is_question":false}`))
	c := newClient(t, f, []string{"k"}, nil)
	if _, err := c.SummarizeBlock(context.Background(), big, "auth"); err != nil {
		t.Fatal(err)
	}
	user := f.Requests()[0].user
	if !strings.Contains(user, "NEWEST LINE") {
		t.Error("newest line dropped")
	}
	if len(user) > DefaultScreenBytes+4000 {
		t.Errorf("prompt is %d bytes; screen was not truncated", len(user))
	}
}

func TestSummarizeBlockBadReply(t *testing.T) {
	f := newFake(t, ok(`I cannot help with that.`))
	c := newClient(t, f, []string{"k"}, nil)
	if _, err := c.SummarizeBlock(context.Background(), "screen", "auth"); err == nil {
		t.Error("prose reply accepted")
	}
	f = newFake(t, ok(`{"spoken":"","is_question":true}`))
	c = newClient(t, f, []string{"k"}, nil)
	if _, err := c.SummarizeBlock(context.Background(), "screen", "auth"); err == nil {
		t.Error("empty spoken accepted")
	}
}

var twoAgents = []AgentSummary{
	{Alias: "auth", PaneID: "w5:p1", Kind: "codex", State: "working", Task: "run the auth tests"},
	{Alias: "docs", PaneID: "w6:p1", Kind: "claude", State: "blocked", Question: "whether it can create claude.txt"},
}

func TestResolveInstructionUnambiguous(t *testing.T) {
	f := newFake(t, ok(`{"pane_id":"auth","instruction_text":"skip the flaky test and rerun","is_answer":false,"answer":"","echo":"Telling auth to skip the flaky test and rerun","ambiguous":false,"clarify_question":""}`))
	c := newClient(t, f, []string{"k"}, nil)
	got, err := c.ResolveInstruction(context.Background(), "tell the auth one to skip the flaky test and rerun", twoAgents, "auth")
	if err != nil {
		t.Fatal(err)
	}
	if got.Ambiguous || got.PaneID != "w5:p1" || got.Echo != "Telling auth to skip the flaky test and rerun." {
		t.Errorf("got %+v", got)
	}
	user := f.Requests()[0].user
	if !strings.Contains(user, "- auth (pane w5:p1, codex, working): run the auth tests") || !strings.Contains(user, "asking whether it can create claude.txt") || !strings.Contains(user, `named the agent as "auth"`) {
		t.Errorf("agent list not rendered: %s", user)
	}
}

func TestResolveInstructionAmbiguous(t *testing.T) {
	f := newFake(t, ok(`{"pane_id":null,"instruction_text":"continue","ambiguous":true,"clarify_question":"Do you mean auth or docs?"}`))
	c := newClient(t, f, []string{"k"}, nil)
	got, err := c.ResolveInstruction(context.Background(), "tell it to continue", twoAgents, "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Ambiguous || got.PaneID != "" || got.ClarifyQuestion != "Do you mean auth or docs?" {
		t.Errorf("got %+v", got)
	}
	// An unknown pane id is treated as ambiguous with a default question.
	f = newFake(t, ok(`{"pane_id":"w9:p9","instruction_text":"continue","ambiguous":false}`))
	c = newClient(t, f, []string{"k"}, nil)
	got, err = c.ResolveInstruction(context.Background(), "tell it to continue", twoAgents, "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Ambiguous || got.ClarifyQuestion != "Do you mean auth or docs?" {
		t.Errorf("unknown pane: %+v", got)
	}
	if _, err := c.ResolveInstruction(context.Background(), "  ", twoAgents, ""); err == nil {
		t.Error("empty instruction accepted")
	}
}

func TestResolveInstructionAnswerEcho(t *testing.T) {
	f := newFake(t, ok(`{"pane_id":"w6:p1","instruction_text":"yes","is_answer":true,"answer":"yes","echo":"Telling docs to yes.","ambiguous":false}`))
	c := newClient(t, f, []string{"k"}, nil)
	got, err := c.ResolveInstruction(context.Background(), "yes go ahead", twoAgents, "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsAnswer || got.PaneID != "w6:p1" || got.Echo != "Telling docs yes." {
		t.Errorf("got %+v", got)
	}
}

func TestSpokenAnswerTwoSentencesNoPaths(t *testing.T) {
	f := newFake(t, ok(`{"spoken":"The tests in /repo/auth/login_test.go failed on a nil pointer. Auth is retrying now. It also printed a lot more that nobody needs.","can_answer":true}`))
	c := newClient(t, f, []string{"k"}, nil)
	got, err := c.SpokenAnswer(context.Background(), "screen text", "why did auth fail", "auth")
	if err != nil {
		t.Fatal(err)
	}
	if got.Spoken != "The tests in a file failed on a nil pointer. Auth is retrying now." || !got.CanAnswer {
		t.Errorf("got %+v", got)
	}
}
