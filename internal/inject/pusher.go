package inject

import (
	"github.com/kccarlos/herdr-sheepdog/internal/logging"
	"github.com/kccarlos/herdr-sheepdog/internal/supervisor"
	"github.com/kccarlos/herdr-sheepdog/internal/voice"
)

// SkippedPusher stands in for the voice session when `--voice=off`: every
// push is logged as voice.push with skipped:true and goes nowhere, so the
// blocked chain (herdr.state → llm.call → pending.created → voice.push)
// stays visible in the log.
type SkippedPusher struct {
	Log logging.Logger
}

var _ supervisor.Pusher = SkippedPusher{}

// Push implements supervisor.Pusher.
func (p SkippedPusher) Push(text string, u voice.Urgency) error {
	if p.Log != nil {
		p.Log.Log("voice", "voice.push", "", map[string]any{"text": text, "urgency": u.String(), "skipped": true})
	}
	return nil
}
