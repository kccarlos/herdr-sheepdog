package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/kccarlos/herdr-sheepdog/internal/onboard"
	"github.com/kccarlos/herdr-sheepdog/internal/voice/gemini"
)

// `sheepdog init`'s voice step (init_flow.go).

func voiceLabel(v string) string {
	switch v {
	case onboard.VoiceVertex:
		return "Gemini Live on Vertex AI"
	case onboard.VoiceOpenAI:
		return "GPT-Live"
	}
	return "Gemini Live on AI Studio"
}

// voiceStep is step 2: the voice, its key (or Vertex credentials) and the
// optional model and voice. It returns the voice id.
func (f *initFlow) voiceStep(card onboard.StartCard) (string, error) {
	voice := card.Voice
	if voice == "" {
		ids := []string{onboard.VoiceGemini, onboard.VoiceVertex, onboard.VoiceOpenAI}
		labels := []string{
			"Gemini Live on AI Studio: Google AI Studio, one free key",
			"Gemini Live on Vertex AI: Google Cloud, billed to your project",
			"GPT-Live: OpenAI, " + onboard.GPTLivePrice,
		}
		var err error
		voice, err = f.in.chooseDef("Step 2 of 3. Voice", ids, labels, f.savedVoice(), f.o.voice)
		if err != nil {
			return "", err
		}
	}
	gp, op := onboard.VoicePicks(f.cur)
	pick := gp
	var key string
	var err error
	var models []string
	switch voice {
	case onboard.VoiceGemini:
		if key, err = f.key(f.geminiKey()); err != nil {
			return "", err
		}
		if !f.o.skipValidate && !f.in.nonInteractive {
			ids, r := onboard.ListLiveModels(f.ctx, f.ep, key)
			models = f.listNote(ids, r, "Live models")
		}
	case onboard.VoiceOpenAI:
		pick = op
		if key, err = f.key(initKey{env: "OPENAI_API_KEY", label: "OpenAI API key for GPT-Live (https://platform.openai.com/api-keys)",
			check: func(ctx context.Context, k string) onboard.Result {
				return resultOf(onboard.ValidateKey(ctx, f.ep, f.cur, f.presets, onboard.ValidateRequest{Kind: "openai", Key: k}))
			}}); err != nil {
			return "", err
		}
		if !f.o.skipValidate && !f.in.nonInteractive {
			ids, r := onboard.ListGPTLiveModels(f.ctx, f.ep, key)
			models = f.listNote(ids, r, "GPT-Live models")
		}
	case onboard.VoiceVertex:
		if err := f.vertex(true); err != nil {
			return "", err
		}
		models = gemini.VertexModels
		if f.cur.Gemini.Backend == onboard.VoiceVertex {
			models = onboard.WithFirst(gemini.VertexModels, f.cur.Gemini.Model)
		}
	default:
		return "", fmt.Errorf("--voice %q: want gemini, vertex or openai", voice)
	}
	f.input.Voice = voice
	model, name := f.o.voiceModel, f.o.voiceName
	if !f.in.nonInteractive {
		if model == "" {
			if model, err = f.in.pickName("Live model", onboard.WithFirst(models, pick.Model), pick.Model, nil); err != nil {
				return "", err
			}
		}
		if name == "" {
			fprintln(f.out, "Voices: "+strings.Join(onboard.VoiceNames(voice, pick.Voice), ", "))
			if name, err = f.in.ask("Voice (Enter keeps the default)", "", pick.Voice); err != nil {
				return "", err
			}
		}
	}
	// Only a choice that differs from the configured one is saved, so the
	// defaults are not pinned in the env file.
	if model != pick.Model {
		f.input.VoiceModel = model
	}
	if name != "" && !strings.EqualFold(name, pick.Voice) {
		f.input.VoiceName = name
	}
	if voice == onboard.VoiceVertex && !f.o.skipValidate && f.input.VoiceModel != "" {
		// A new Live model must be served at the location.
		c := onboard.ValidateKey(f.ctx, f.ep, f.cur, f.presets, onboard.ValidateRequest{Kind: "vertex", Key: f.have["VERTEX_API_KEY"], Project: f.input.Vertex.Project,
			Location: onboard.OrDefault(f.input.Vertex.Location, onboard.DefaultVertexLocation), Voice: true, LiveModel: f.input.VoiceModel})
		if c.Status == "fail" {
			return "", fmt.Errorf("the voice model %s: %s — fix: %s", f.input.VoiceModel, c.Detail, c.Fix)
		}
		fprintln(f.out, "  "+resultOf(c).Line())
	}
	return voice, nil
}

// listNote prints a failed list as a note (the default is still
// offered) and returns the ids.
func (f *initFlow) listNote(ids []string, r onboard.Result, what string) []string {
	if r.Status != onboard.OK {
		fprintf(f.out, "  could not list the %s (%s), so only the default is offered\n", what, r.Detail)
		return nil
	}
	return ids
}

func (f *initFlow) geminiKey() initKey {
	return initKey{env: "GEMINI_API_KEY", label: "Gemini API key (" + onboard.GeminiKeyURL + ")",
		check: func(ctx context.Context, k string) onboard.Result {
			return resultOf(onboard.ValidateKey(ctx, f.ep, f.cur, f.presets, onboard.ValidateRequest{Kind: "gemini", Key: k}))
		}}
}

// vertex asks for the Google Cloud project, the location (for the
// voice) and the key, and checks them live.
func (f *initFlow) vertex(forVoice bool) error {
	if f.input.Vertex.Project != "" {
		return nil
	}
	saved := f.cur.Gemini.Vertex
	defProject := onboard.OrDefault(os.Getenv("VERTEX_PROJECT"), saved.Project)
	project, err := f.in.ask("Google Cloud project id for Vertex AI", f.o.vertexProject, defProject)
	if err != nil {
		return err
	}
	f.input.Vertex.Project = project
	if forVoice {
		loc, err := f.in.ask("Location for the voice", f.o.vertexLocation, onboard.OrDefault(saved.Location, onboard.DefaultVertexLocation))
		if err != nil {
			return err
		}
		f.input.Vertex.Location = loc
	}
	// A saved key is kept only for the project it was saved for.
	existing := f.existing
	if saved.Project != project {
		existing = map[string]string{}
	}
	k := initKey{env: "VERTEX_API_KEY", label: "Vertex AI API key (" + onboard.VertexKeyDocURL + ")",
		check: func(ctx context.Context, key string) onboard.Result {
			req := onboard.ValidateRequest{Kind: "vertex", Key: key, Project: project}
			if forVoice {
				req.Location, req.Voice = f.input.Vertex.Location, true
			}
			return resultOf(onboard.ValidateKey(ctx, f.ep, f.cur, f.presets, req))
		}}
	v, err := initAskKey(f.ctx, k, existing, f.in, f.o.skipValidate, f.out)
	if err != nil {
		return err
	}
	f.have[k.env] = v
	f.keys[k.env] = v
	return nil
}
