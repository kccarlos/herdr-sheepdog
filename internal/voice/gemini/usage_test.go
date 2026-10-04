package gemini

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

func usageMsg(prompt, resp int32, pText, pAudio int32) *genai.LiveServerMessage {
	return &genai.LiveServerMessage{UsageMetadata: &genai.UsageMetadata{
		PromptTokenCount: prompt, ResponseTokenCount: resp, TotalTokenCount: prompt + resp,
		PromptTokensDetails: []*genai.ModalityTokenCount{
			{Modality: genai.MediaModalityText, TokenCount: pText},
			{Modality: genai.MediaModalityAudio, TokenCount: pAudio},
		},
		ResponseTokensDetails: []*genai.ModalityTokenCount{{Modality: genai.MediaModalityAudio, TokenCount: resp}},
	}}
}

func waitPayloads(t *testing.T, h *harness, typ string, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p := h.log.payloads(typ); len(p) >= n {
			return p
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("%s: want %d, got %d", typ, n, len(h.log.payloads(typ)))
	return nil
}

// voice.usage carries the per-modality prompt and response token details.
func TestUsageLogsTokenDetailsPerModality(t *testing.T) {
	c1 := newFakeConn("c1")
	h := newHarness(t, Config{}, &fakeDialer{results: []func() (Conn, error){ok(c1)}})
	c1.serve(usageMsg(1000, 40, 300, 700))
	u := waitPayloads(t, h, "voice.usage", 1)[0]
	pm, _ := u["prompt_by_modality"].(map[string]int32)
	rm, _ := u["response_by_modality"].(map[string]int32)
	if u["prompt"] != int32(1000) || pm["TEXT"] != 300 || pm["AUDIO"] != 700 || rm["AUDIO"] != 40 {
		t.Errorf("voice.usage = %v", u)
	}
}

// One voice.resume_context per resumed connection: the prompt size
// before the resume (and at the accepted handle) against the first usage
// report after it, per modality.
func TestResumeContextComparesBeforeAndAfter(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{ReconnectBackoff: 5 * time.Millisecond, Model: "m-test"}, d)

	c1.serve(usageMsg(900, 10, 200, 700))
	c1.serve(handleMsg("handle-A-0123456789"))
	waitHandleUpdates(t, h, 1)
	c1.serve(usageMsg(1200, 10, 250, 950)) // after the handle, before the drop
	waitPayloads(t, h, "voice.usage", 2)
	c1.fail(errors.New("websocket: close 1006 (abnormal closure)"))
	h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	if n := len(h.log.payloads("voice.resume_context")); n != 0 {
		t.Fatalf("voice.resume_context before any usage after the resume: %d", n)
	}
	c2.serve(usageMsg(500, 5, 100, 400))
	rc := waitPayloads(t, h, "voice.resume_context", 1)[0]
	bm, _ := rc["before_prompt_by_modality"].(map[string]int32)
	hm, _ := rc["handle_prompt_by_modality"].(map[string]int32)
	am, _ := rc["after_prompt_by_modality"].(map[string]int32)
	if rc["before_prompt"] != int32(1200) || rc["handle_prompt"] != int32(900) || rc["after_prompt"] != int32(500) ||
		bm["AUDIO"] != 950 || hm["AUDIO"] != 700 || am["AUDIO"] != 400 || am["TEXT"] != 100 ||
		rc["delta_prompt"] != int32(-700) || rc["backend"] != "aistudio" || rc["model"] != "m-test" {
		t.Errorf("voice.resume_context = %v", rc)
	}
	// A second usage report on the same connection adds nothing.
	c2.serve(usageMsg(600, 5, 150, 450))
	waitPayloads(t, h, "voice.usage", 4)
	if n := len(h.log.payloads("voice.resume_context")); n != 1 {
		t.Errorf("voice.resume_context events = %d, want 1", n)
	}
}

// A fresh (not resumed) reconnect has nothing to compare: no event.
func TestResumeContextNotLoggedForAFreshSession(t *testing.T) {
	c1, c2 := newFakeConn("c1"), newFakeConn("c2")
	d := &fakeDialer{results: []func() (Conn, error){ok(c1), ok(c2)}}
	h := newHarness(t, Config{ReconnectBackoff: 5 * time.Millisecond}, d)
	c1.serve(usageMsg(900, 10, 200, 700))
	waitPayloads(t, h, "voice.usage", 1)
	c1.fail(errors.New("websocket: close 1006 (abnormal closure)"))
	h.waitEvent("resumed", func(ev voice.Event) bool { _, ok := ev.(voice.EventResumed); return ok })
	c2.serve(usageMsg(300, 5, 100, 200))
	waitPayloads(t, h, "voice.usage", 2)
	if n := len(h.log.payloads("voice.resume_context")); n != 0 {
		t.Errorf("voice.resume_context without a handle: %d", n)
	}
}
