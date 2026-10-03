package inject

import (
	"github.com/kccarlos/herdr-sheepdog/internal/ctxcap"
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
	// Context, when set, records each push into the voice context log
	// (the /debug Context tab's text-router session), marked not sent.
	Context *ctxcap.VoiceSession
	// Heard, when set, is told of every spoken (non-silent) push, as if
	// a voice session had spoken it (app.Options.HeardEcho).
	Heard func(text string, u voice.Urgency)
}

var _ supervisor.Pusher = SkippedPusher{}

// Push implements supervisor.Pusher.
func (p SkippedPusher) Push(text string, u voice.Urgency) error {
	return p.PushTagged(text, u, nil)
}

// PushTagged implements supervisor.TagPusher: the same skipped push,
// with its tags (answers_job) on the event, so the /debug views link an
// investigation's answer to its acknowledgement with --voice=off too.
// There is no live model to speak while, so scheduling_sent always
// matches the requested urgency.
func (p SkippedPusher) PushTagged(text string, u voice.Urgency, tags map[string]any) error {
	if p.Log != nil {
		payload := map[string]any{"text": text, "urgency": u.String(), "scheduling_sent": u.SchedulingName(), "skipped": true}
		for k, v := range tags {
			payload[k] = v
		}
		p.Log.Log("voice", "voice.push", "", payload)
	}
	it := ctxcap.VoiceItem{Kind: ctxcap.VKindPush, Class: ctxcap.ClassPush, Role: "user", Label: "push (" + u.SchedulingName() + ", not sent: --voice=off)", Text: text, Links: ctxcap.LinksFromTags(tags),
		Raw: ctxcap.Raw(map[string]any{"text": text, "urgency": u.String(), "tags": tags})}
	if u == voice.UrgencySilent {
		it.Kind, it.Label = ctxcap.VKindContext, "silent context (not sent: --voice=off)"
	}
	p.Context.Item(it)
	if p.Heard != nil && u != voice.UrgencySilent && text != "" {
		p.Heard(text, u)
	}
	return nil
}
