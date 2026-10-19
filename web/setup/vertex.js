// Pure helpers of the setup page's Vertex AI block (no DOM): `make test-web`.

// voiceChoice is the Voice radio the saved settings select.
export function voiceChoice(voice) {
  if (!voice) return 'gemini';
  if (voice.provider === 'openai') return 'openai';
  return voice.backend === 'vertex' ? 'vertex' : 'gemini';
}

// orchChoice is the Orchestrator radio for a saved provider: the built-in
// ones by name, any other name is an OpenAI-compatible entry.
export function orchChoice(provider) {
  if (['gemini', 'deepseek', 'vertex'].includes(provider)) return provider;
  return provider ? 'compat' : 'gemini';
}

// vertexFields says which parts of the Vertex block show for the chosen
// voice and orchestrator. The location is the voice's only: the
// orchestrator always calls Vertex at global.
export function vertexFields(voice, orch) {
  const v = voice === 'vertex';
  const o = orch === 'vertex';
  return { block: v || o, location: v, orchModel: o, geminiNeeded: voice === 'gemini' || orch === 'gemini' };
}

// voiceLine is what the Vertex voice will be, once the credentials check out.
export function voiceLine(vertex) {
  const model = (vertex && vertex.live_model) || 'gemini-3.8-live';
  const name = (vertex && vertex.voice) || 'Puck';
  return "You'll talk to " + model + ' (Vertex), voice ' + name;
}

// savedKeyFor tells whether the saved Vertex key may be used for project:
// only the project it was saved for.
export function savedKeyFor(state, project) {
  return !!(state && state.keys && state.keys.vertex && state.vertex && project && state.vertex.project === project);
}

// savedNote is the line under the key field ('' when no key is saved).
export function savedNote(state) {
  if (!state || !state.keys || !state.keys.vertex) return '';
  const p = state.vertex && state.vertex.project;
  return p
    ? 'A key is saved for project ' + p + '. Paste a new one only to replace it; another project needs its own key.'
    : 'A key is saved. Paste a new one only to replace it.';
}

// validateBody is the /api/setup/validate request for the block, or null
// when there is nothing to check yet ({ wait } says what is missing).
export function validateBody(f, state) {
  const key = (f.key || '').trim();
  const project = (f.project || '').trim();
  if (!project) return key ? { wait: 'Type the project id to check the key.' } : null;
  if (!key && !savedKeyFor(state, project)) return null;
  if (key && key.length < 20) return { wait: 'Keep pasting…' };
  const body = { kind: 'vertex', key, project };
  if (f.voice) {
    body.voice = true;
    body.location = (f.location || '').trim() || (state && state.vertex && state.vertex.default_location) || 'us-central1';
  }
  return body;
}

// checkLine is the result line of a Vertex check: { kind, text }.
export function checkLine(r) {
  if (!r) return { kind: 'fail', text: '✗ sheepdog did not answer.' };
  if (r.status === 'fail') return { kind: 'fail', text: '✗ ' + r.detail + (r.fix ? ' — ' + r.fix : '') };
  if (r.status === 'warn') return { kind: 'ok', text: '! ' + r.detail + (r.fix ? ' — ' + r.fix : '') };
  return { kind: 'ok', text: '✓ ' + r.detail };
}
