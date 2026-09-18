package echotools

import (
	"context"
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

func TestHandler(t *testing.T) {
	h := Handler()
	cases := []struct {
		call voice.ToolCall
		want string
	}{
		{voice.ToolCall{Name: "briefing"}, "briefing called with no arguments"},
		{voice.ToolCall{Name: "propose", Args: map[string]any{"instruction": "rerun", "agent": "auth"}}, "propose called with agent auth, instruction rerun"},
		{voice.ToolCall{Name: "confirm", Args: map[string]any{}}, "confirm called with no arguments"},
	}
	for _, tc := range cases {
		res, err := h(context.Background(), tc.call)
		if err != nil {
			t.Fatalf("%s: %v", tc.call.Name, err)
		}
		if res.Spoken != tc.want || res.Scheduling != voice.UrgencyInterrupt {
			t.Errorf("%s: got %+v", tc.call.Name, res)
		}
	}
	if _, err := h(context.Background(), voice.ToolCall{Name: "rm_rf"}); err == nil {
		t.Error("unknown tool accepted")
	}
}
