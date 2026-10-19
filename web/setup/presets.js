// Pure helpers of the setup page's orchestrator presets (no DOM): `make
// test-web`. The preset table itself comes from the daemon
// (/api/setup/state presets, internal/onboard/presets.go).
import { sortModels } from './models.js';

// presetById finds a preset in the state's list.
export function presetById(presets, id) {
  return (presets || []).find((p) => p.id === id) || null;
}

// savedPreset is the preset to select for the saved orchestrator.
export function savedPreset(state) {
  const id = state && state.orchestrator && state.orchestrator.preset;
  return presetById(state && state.presets, id) ? id : 'gemini';
}

// orchValue is the request's orchestrator for a preset id (Other URL is
// the generic compat entry).
export function orchValue(id) {
  return id === 'other' ? 'compat' : id;
}

// panel says what shows for preset p. reuse: the "same OpenAI key" box is
// ticked. field: which block (gemini, deepseek, vertex or compat).
export function panel(p, reuse) {
  if (!p) return { field: 'gemini' };
  if (p.kind === 'native') return { field: p.id };
  const other = p.kind === 'other';
  const reuseOffered = !other && !!p.reuse_offered;
  const reusing = reuseOffered && !!reuse;
  return {
    field: 'compat',
    other,
    details: !other,
    reuseOffered,
    askKey: other || (!!p.key_needed && !reusing),
    keySaved: !other && !!p.key_saved,
    local: !!p.local,
    findLabel: p.local ? 'Check again' : 'Find models',
  };
}

// keyLabel names the key input.
export function keyLabel(p) {
  if (!p || p.kind === 'other') return 'API key (empty for a local server)';
  return p.label + ' API key';
}

// keyEnv is where the orchestrator's key is saved for preset p.
export function keyEnv(p, reuse) {
  if (!p) return '';
  return reuse && p.reuse_offered && p.reuse_env ? p.reuse_env : p.key_env || '';
}

// savedAs is the Details line: the config entry and the key's place.
export function savedAs(p, reuse) {
  if (!p || p.kind !== 'compat') return '';
  const where = 'Saved as the [[brain.openai_compat]] entry "' + p.id + '" in config.toml';
  if (!p.key_needed) return where + '; no key needed.';
  const env = keyEnv(p, reuse);
  return where + '; the key is read from ' + env + (reuse ? ' (the voice key).' : '.');
}

// autoFind tells whether selecting preset p (or a key check) should list
// its models at once: a local server always (that is the running check);
// a keyed preset once a key is there (valid, saved, or the reused one).
export function autoFind(p, ctx) {
  if (!p || p.kind !== 'compat') return false;
  const c = ctx || {};
  if (p.local) return true;
  if (c.reuse && p.reuse_offered) return true;
  if (c.keyValid) return true;
  return !!p.key_saved && !c.keyTyped;
}

// localLine is the running line of a local preset after a model list.
export function localLine(p, r) {
  const name = (p && p.label) || 'The server';
  if (!r) return { kind: 'fail', text: 'sheepdog did not answer.' };
  if (r.running === false) return { kind: 'fail', text: 'Not running: start ' + name + ', then press Check again.' };
  if (r.error) return { kind: 'fail', text: [r.error, r.fix].filter(Boolean).join(' ') };
  const n = (r.models || []).length;
  return { kind: 'ok', text: name + ' is running, ' + n + (n === 1 ? ' model' : ' models') + '.' };
}

// modelToFill is the value to put in the model input after a list, or
// null to leave it: what the user typed (or the saved model) stays; an
// empty or auto-filled input takes the suggestion.
export function modelToFill(current, auto, r) {
  if ((current || '').trim() && !auto) return null;
  return r && r.suggested ? r.suggested : null;
}

// pickGroups are the model select's option groups: tool-capable first
// when the endpoint says, else one group.
export function pickGroups(models, toolInfo) {
  const sorted = sortModels(models);
  if (!toolInfo) return [{ label: '', ids: sorted.map((m) => m.id) }];
  return [
    { label: 'Supports tool calling', ids: sorted.filter((m) => m.tools === true).map((m) => m.id) },
    { label: 'Other models', ids: sorted.filter((m) => m.tools !== true).map((m) => m.id) },
  ].filter((g) => g.ids.length);
}

// geminiStatus is the line under the Gemini model input.
export function geminiStatus(r) {
  if (!r) return { kind: 'fail', text: 'sheepdog did not answer. You can still type the model name.' };
  if (r.error) return { kind: 'fail', text: [r.error, r.fix].filter(Boolean).join(' — ') };
  const n = (r.models || []).length;
  return { kind: 'ok', text: n + (n === 1 ? ' model' : ' models') + ' this key can use. The default is ' + r.default + '.' };
}
