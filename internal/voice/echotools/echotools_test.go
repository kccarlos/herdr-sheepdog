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
		{voice.ToolCall{Name: voice.ToolStop}, "Echo test: the stop tool was called with no arguments."},
		{voice.ToolCall{Name: voice.ToolToSupervisor, Args: map[string]any{"text": "tell auth to rerun"}}, "Echo test: the to_supervisor tool was called with text tell auth to rerun."},
		{voice.ToolCall{Name: voice.ToolStop, Args: map[string]any{}}, "Echo test: the stop tool was called with no arguments."},
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
