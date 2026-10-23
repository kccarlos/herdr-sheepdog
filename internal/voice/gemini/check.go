package gemini

import (
	"context"
	"errors"

	"google.golang.org/genai"
)

// CheckVertexLive opens one Gemini Live session on Vertex AI with apiKey
// in project at location, waits for setupComplete and closes it: no audio
// or text is sent. It proves that the location serves model, which the
// REST side cannot tell (docs/dev/research/vertex-live-spike-2026-09-29.md).
// A location without the model fails with a close 1008 "Publisher model
// … was not found". ctx bounds the whole check.
func CheckVertexLive(ctx context.Context, apiKey, project, location, model string) error {
	return checkLive(ctx, newSDKDialer(&Vertex{Project: project, Location: location}), apiKey, model)
}

func checkLive(ctx context.Context, d Dialer, apiKey, model string) error {
	conn, err := d.Dial(ctx, apiKey, model, &genai.LiveConnectConfig{ResponseModalities: []genai.Modality{genai.ModalityAudio}})
	if err != nil {
		return err
	}
	type result struct {
		msg *genai.LiveServerMessage
		err error
	}
	ch := make(chan result, 1)
	go func() {
		m, err := conn.Receive()
		ch <- result{m, err}
	}()
	select {
	case r := <-ch:
		_ = conn.Close()
		if r.err != nil {
			return r.err
		}
		if r.msg == nil || r.msg.SetupComplete == nil {
			return errors.New("gemini: the Live session did not confirm its setup")
		}
		return nil
	case <-ctx.Done():
		_ = conn.Close()
		return ctx.Err()
	}
}
