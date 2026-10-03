package inject

import (
	"testing"

	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// SkippedPusher tells Heard of every spoken push and never of silent
// context (serve --heard-echo).
func TestSkippedPusherHeard(t *testing.T) {
	var heard []string
	p := SkippedPusher{Heard: func(text string, _ voice.Urgency) { heard = append(heard, text) }}
	_ = p.Push("names line", voice.UrgencySilent)
	_ = p.Push("Claude is done.", voice.UrgencyIdle)
	_ = p.PushTagged("Claude asks to run go test.", voice.UrgencyInterrupt, map[string]any{"agent": "bench-claude"})
	if len(heard) != 2 || heard[0] != "Claude is done." || heard[1] != "Claude asks to run go test." {
		t.Fatalf("heard = %v", heard)
	}
	_ = SkippedPusher{}.Push("no callback", voice.UrgencyIdle) // nil Heard is fine
}
