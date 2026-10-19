// Pure helpers of the setup page's voice part (no DOM): `make test-web`.
// The lists come from the daemon (/api/setup/state voice.gemini and
// voice.openai, /api/setup/voice-models).

// voiceCards are the three voice choices, in the order the page shows
// them. price is GPT-Live's price line from the state.
export function voiceCards(price) {
  return [
    { value: 'gemini', label: 'Gemini Live', note: 'Google AI Studio · free key' },
    { value: 'vertex', label: 'Gemini Live on Vertex AI', note: 'Google Cloud · billed to your project' },
    { value: 'openai', label: 'GPT-Live', note: 'OpenAI · ' + (price || '$0.05 per minute of open session') },
  ];
}

// family is the settings a voice card uses: Gemini Live on AI Studio and
// on Vertex AI share gemini.model and gemini.voice.
export function family(voice) {
  return voice === 'openai' ? 'openai' : 'gemini';
}

// pickOf is the configured pick ({ model, voice, voices, … }) for a card.
export function pickOf(state, voice) {
  const v = state && state.voice;
  return (v && v[family(voice)]) || { model: '', voice: '', voices: [] };
}

// talkLine is "You'll talk to …" for a card, model and voice.
export function talkLine(voice, model, name) {
  const where = voice === 'vertex' ? ' on Vertex AI' : '';
  return "You'll talk to " + model + where + ', voice ' + name + '.';
}

// pickReady says whether the model and voice pickers show for a card: once
// its key checked out (typed and valid, or saved), or for Vertex once the
// Google Cloud block's check passed.
export function pickReady(voice, ctx) {
  const c = ctx || {};
  if (voice === 'vertex') return !!c.vertexOK;
  if (voice === 'openai') return !!(c.openaiValid || (c.openaiSaved && !c.openaiTyped));
  return !!(c.geminiValid || (c.geminiSaved && !c.geminiTyped));
}

// modelsLine is the line under the pickers after a model list.
export function modelsLine(r) {
  if (!r) return { kind: 'fail', text: 'sheepdog did not answer, so only the configured model is offered.' };
  if (r.error) return { kind: 'fail', text: "Couldn't list the models (" + r.error + '), so only the configured model is offered.' };
  if (r.note) return { kind: 'muted', text: r.note };
  const n = (r.models || []).length;
  return { kind: 'muted', text: n + (n === 1 ? ' Live model' : ' Live models') + ' this key can use. The default is ' + r.default + '.' };
}

// modelOptions are the model select's options: the list, with the
// current model kept in it.
export function modelOptions(r, current) {
  const list = (r && r.models && r.models.length) ? [...r.models] : [];
  if (current && !list.includes(current)) list.unshift(current);
  return list;
}

// changed is what a save sends for the voice: only a model or voice that
// differs from the configured one, so the defaults are not pinned in the
// env file.
export function changed(pick, model, name) {
  const out = {};
  if (model && model !== pick.model) out.voice_model = model;
  if (name && name !== pick.voice) out.voice_name = name;
  return out;
}
