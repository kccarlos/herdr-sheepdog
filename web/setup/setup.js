// The setup page (/setup): the five steps of `sheepdog setup`. It talks
// only to /api/setup/*, /api/pair and /api/devices (loopback only); keys are posted once
// and never come back.
import { datalistOptions, modelsStatus, probeStatus } from './models.js';
import { voiceChoice, vertexFields, savedNote, validateBody, checkLine } from './vertex.js';
import { voiceCards, family, pickOf, talkLine, pickReady, modelsLine, modelOptions, changed, vertexModel } from './voice.js';
import { cardRadios, initialChoice, nextCard, cardFor, summary } from './start.js';
import { presetById, savedPreset, orchValue, panel, keyLabel, savedAs, autoFind, localLine, modelToFill, pickGroups, geminiStatus, anthropicModelsLine } from './presets.js';
import { shellSwitch, shellSaved, countdown, deviceKind, lastSeen } from './phone.js';

const $ = (id) => document.getElementById(id);
let state = null;
let demoTimer = null;
let renderedBoot = null; // the choices are set from the daemon once per boot, not on every poll
let vertexOK = false; // the Vertex block's last check passed for the voice
let shownPreset = null; // the preset whose fields are on screen
const modelBy = {}; // the model typed or filled per preset: { value, auto }
let compatKeyValid = false; // the typed compat key checked out
let geminiValid = false; // the typed Gemini key checked out
let openaiValid = false; // the typed OpenAI key checked out
let anthropicValid = false; // the typed Anthropic key checked out
const chosen = {}; // the model and voice picked per family (gemini, openai)
const voiceLists = {}; // the last model list per card and key

async function api(path, body) {
  const opts = body === undefined ? {} : {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
  };
  const r = await fetch('/api/setup/' + path, opts);
  let data = {};
  try { data = await r.json(); } catch (_) { /* not JSON */ }
  if (!r.ok && !data.checks && !data.error) data.error = r.status + ' ' + r.statusText;
  data.httpStatus = r.status;
  return data;
}

function setResult(el, status, text) {
  el.className = 'result ' + status;
  el.textContent = text;
}

function checkText(c) {
  const icon = c.status === 'ok' ? '✓ ' : c.status === 'fail' ? '✗ ' : c.status === 'warn' ? '! ' : '';
  return icon + c.detail + (c.fix && c.status !== 'ok' ? ' — ' + c.fix : '');
}

async function loadState() {
  try {
    state = await api('state');
    if (state.httpStatus !== 200) throw new Error(state.error);
    $('offline').classList.add('hidden');
  } catch (e) {
    $('offline').classList.remove('hidden');
    return null;
  }
  render();
  return state;
}

// waitForReload resolves once the daemon answers with another boot id
// (it reloaded its settings) or after the timeout.
async function waitForReload(oldBoot, timeoutMs) {
  const end = Date.now() + (timeoutMs || 20000);
  while (Date.now() < end) {
    await new Promise((r) => setTimeout(r, 500));
    try {
      const s = await api('state');
      if (s.httpStatus === 200 && s.boot !== oldBoot) { state = s; render(); return true; }
    } catch (_) { /* restarting */ }
  }
  return false;
}

function render() {
  const s = state;
  $('env-file').textContent = s.env_file;
  $('models-doc').href = s.links.models_doc;
  $('which-doc').href = s.links.models_doc;
  $('get-key').href = s.links.get_key;
  $('vertex-doc').href = s.links.models_doc + '#vertex-ai';
  $('ios-repo').href = s.links.ios_repo;

  // Header badge: the voice.
  const badge = $('voice-badge');
  if (s.voice.status === 'connected') {
    badge.className = 'badge ok'; badge.textContent = 'voice connected';
  } else if (s.voice.problem) {
    badge.className = 'badge fail'; badge.textContent = s.voice.setup_needed ? 'setup needed' : 'voice not connected';
  } else {
    badge.className = 'badge'; badge.textContent = 'voice ' + s.voice.status;
  }

  // 1. Models
  const ms = $('models-state');
  $('gemini-saved').classList.toggle('hidden', !s.keys.gemini);
  $('openai-saved').classList.toggle('hidden', !s.keys.openai);
  const note = savedNote(s);
  $('vertex-saved').textContent = note;
  $('vertex-saved').classList.toggle('hidden', !note);
  if (s.voice.status === 'connected') {
    ms.className = 'state ok'; ms.textContent = '✓ voice connected (' + s.voice.provider + ')';
  } else if (s.voice.setup_needed) {
    ms.className = 'state'; ms.textContent = 'needs a key';
  } else if (s.voice.problem) {
    ms.className = 'state fail'; ms.textContent = s.voice.problem;
  } else {
    ms.className = 'state'; ms.textContent = s.voice.status;
  }
  if (renderedBoot !== s.boot) {
    renderedBoot = s.boot;
    const init = initialChoice(s, voiceChoice(s.voice), savedPreset(s));
    const voice = init.voice;
    renderVoiceGrid(s, voice);
    for (const k of Object.keys(chosen)) delete chosen[k];
    for (const k of Object.keys(voiceLists)) delete voiceLists[k];
    const orch = init.orch;
    renderStartGrid(init.card);
    renderGrid(s.presets, orch);
    rememberSaved(s, orch);
    renderVertex(s, voice, orch);
    showFields();
  }
  $('deepseek-saved').classList.toggle('hidden', !s.keys.deepseek);
  $('anthropic-saved').classList.toggle('hidden', !s.keys.anthropic);

  // 3. Service
  const svc = s.service;
  if (!svc.supported) {
    $('service-state').textContent = '';
    $('service-on').checked = false; $('service-on').disabled = true;
    $('save-service').disabled = true;
    $('service-msg').textContent = 'Not available on this system. Start it yourself with sheepdog start.';
  } else if (svc.installed) {
    $('service-state').className = 'state ok'; $('service-state').textContent = '✓ on';
    $('service-on').checked = true; $('service-on').disabled = true; $('save-service').disabled = true;
    $('service-msg').textContent = 'Installed. To turn it off: sheepdog service uninstall';
  }

  // 4. Phone
  const p = s.phone;
  $('phone-off').classList.toggle('hidden', p.enabled);
  $('phone-on').classList.toggle('hidden', !p.enabled);
  if (p.enabled) {
    $('phone-state').className = p.connected ? 'state ok' : 'state';
    $('phone-state').textContent = p.connected ? '✓ connected' : 'on';
    if (!$('qr').getAttribute('src') || $('qr').dataset.boot !== s.boot) {
      $('qr').dataset.boot = s.boot;
      showCode(false);
    }
    setResult($('phone-connected'), p.connected ? 'ok' : 'wait', p.connected ? 'Connected ✓ (' + p.remote + ')' : 'Waiting for the phone…');
    $('addresses').textContent = p.addresses.length ? p.explain + ' (' + p.addresses.map((a) => a.url.replace('https://', '')).join(', ') + ')' : 'No network address found: is this computer on Wi-Fi?';
    const tf = $('testflight');
    tf.textContent = '';
    if (s.links.testflight) {
      const a = document.createElement('a');
      a.href = s.links.testflight; a.target = '_blank'; a.rel = 'noopener'; a.textContent = 'Get the app on TestFlight';
      tf.append(a);
    }
    const hint = $('remote-hint');
    hint.textContent = '';
    const doc = document.createElement('a');
    doc.href = s.links.remote_doc; doc.target = '_blank'; doc.rel = 'noopener'; doc.textContent = 'docs/remote-access.md';
    if (p.tailscale) {
      hint.append('Install Tailscale on your phone too (same account) to use the app away from home. More: ', doc);
    } else {
      hint.append('To use the app away from home, install Tailscale on this computer and your phone. More: ', doc);
    }
    const sh = shellSwitch(p);
    $('shell-box').classList.toggle('hidden', !sh.show);
    $('shell-on').checked = sh.checked;
    if (!$('shell-on').disabled) $('shell-note').textContent = sh.note;
    loadDevices();
  } else {
    $('phone-state').textContent = '';
  }

  // 5. Talk
  const herdrNote = s.herdr.ok
    ? (s.herdr.agents ? 'sheepdog sees ' + s.herdr.agents + ' agent' + (s.herdr.agents === 1 ? '' : 's') + ' in herdr. Press Connect on the voice page and ask "what\'s going on?"' : 'herdr is running but has no agents yet. Start one in herdr, or try the demo below.')
    : 'herdr is not reachable: ' + s.herdr.detail + (s.herdr.fix ? ' — ' + s.herdr.fix : '');
  $('talk-note').textContent = s.voice.setup_needed ? 'Save a key in step 1 first; then the voice can answer.' : herdrNote;
  $('talk').classList.toggle('primary', !s.voice.setup_needed);
  if ($('checks').children.length === 0 && s.voice.setup_needed) $('checks-state').textContent = 'run after step 1';
  const canDemo = s.demo && (s.voice.status === 'connected' || s.keys.gemini);
  $('demo').classList.toggle('hidden', !(canDemo && (!s.herdr.ok || !s.herdr.agents)));
  if (s.demo) renderDemo(s.demo);
}

function renderDemo(d) {
  $('demo-start').classList.toggle('hidden', d.running);
  $('demo-open').classList.toggle('hidden', !d.running);
  $('demo-stop').classList.toggle('hidden', !d.running);
  if (d.running) {
    $('demo-open').href = d.url + '/voice';
    $('demo-msg').textContent = 'The demo runs until you stop it, close this page, or for 20 minutes.';
    if (!demoTimer) demoTimer = setInterval(() => api('demo', { action: 'ping' }).then((r) => r.demo && renderDemo(r.demo)), 10000);
  } else {
    if (demoTimer) { clearInterval(demoTimer); demoTimer = null; }
    $('demo-msg').textContent = d.stopped ? 'The demo ended (' + d.stopped + ').' : '';
  }
}

// renderVertex fills the Vertex block from the saved settings (once per boot).
function renderVertex(s, voice, orch) {
  const v = s.vertex;
  if (!$('vertex-project').value) $('vertex-project').value = v.project || '';
  if (!$('vertex-location').value) $('vertex-location').value = v.location || v.default_location;
  const list = $('vertex-models');
  list.textContent = '';
  for (const m of v.models) {
    const opt = document.createElement('option');
    opt.value = m;
    list.appendChild(opt);
  }
  if (!$('vertex-model').value) $('vertex-model').value = orch === 'vertex' && s.orchestrator.model ? s.orchestrator.model : v.default_model;
  // Saved and in use: the voice line shows without a new check.
  vertexOK = voice === 'vertex' && s.voice.backend === 'vertex' && s.keys.vertex && !s.voice.problem;
}

// renderStartGrid builds the four starting-point cards: one radio group.
// A card only sets the voice and orchestrator radios below; Save is what
// saves. Arrow keys move between the cards (nextCard).
function renderStartGrid(selected) {
  const grid = $('start-grid');
  grid.textContent = '';
  const inputs = [];
  const cards = state.start.cards;
  cards.forEach((c, i) => {
    const label = document.createElement('label');
    label.className = 'preset';
    const input = document.createElement('input');
    input.type = 'radio'; input.name = 'start'; input.value = c.id;
    input.checked = c.id === selected;
    input.addEventListener('change', () => { if (input.checked) applyCard(c.id); });
    input.addEventListener('keydown', (e) => {
      const j = nextCard(e.key, i, cards.length);
      if (j < 0) return;
      e.preventDefault();
      inputs[j].focus();
      inputs[j].checked = true;
      inputs[j].dispatchEvent(new Event('change'));
    });
    inputs.push(input);
    const name = document.createElement('span');
    name.className = 'p-label'; name.textContent = c.label;
    const note = document.createElement('span');
    note.className = 'p-note'; note.textContent = c.note;
    label.append(input, name, note);
    if (c.need) {
      const need = document.createElement('span');
      need.className = 'p-need'; need.textContent = c.need;
      label.append(need);
    }
    grid.append(label);
  });
}

// applyCard sets the voice and orchestrator radios for a card and shows
// their fields. Custom leaves them as they are.
function applyCard(id) {
  const r = cardRadios(state.start, id);
  if (r) {
    for (const [name, value] of [['voice', r.voice], ['orch', r.orch]]) {
      const el = document.querySelector('input[name=' + name + '][value="' + value + '"]');
      if (el) el.checked = true;
    }
    const { voice } = choices();
    if (voice === 'vertex' || $('vertex-result').textContent) checkVertex();
  }
  showFields();
}

// syncCard keeps the starting point honest after a hand change of the
// radios: the card that matches them, else Custom.
function syncCard() {
  const { voice, orch } = choices();
  const el = document.querySelector('input[name=start][value="' + cardFor(state.start, voice, orch) + '"]');
  if (el && !el.checked) el.checked = true;
}

// orchModelShown is the model the chosen orchestrator will run, as the
// page shows it.
function orchModelShown(p) {
  if (!p) return '';
  if (p.id === 'anthropic') return $('anthropic-model').value.trim() || p.default_model || '';
  if (p.id === 'gemini') return $('gemini-model').value.trim() || p.default_model || '';
  if (p.id === 'vertex') return $('vertex-model').value.trim();
  if (p.kind === 'native') return p.default_model || '';
  return $('compat-model').value.trim();
}

// renderSummary updates the bar above Save.
function renderSummary() {
  if (!state) return;
  const { voice } = choices();
  const p = currentPreset();
  const c = chosen[family(voice)];
  const shown = !$('voice-pick').classList.contains('hidden') && c;
  $('summary').textContent = summary(state.start, {
    voice, voiceModel: shown ? c.model : pickOf(state, voice).model,
    preset: p, orchModel: orchModelShown(p), otherName: $('compat-name').value.trim(),
  });
}

// renderVoiceGrid builds the three voice cards: native radios in one
// group (arrow keys move between them), styled like the presets.
function renderVoiceGrid(s, selected) {
  const grid = $('voice-grid');
  grid.textContent = '';
  for (const c of voiceCards(s.voice.openai_price)) {
    const label = document.createElement('label');
    label.className = 'preset';
    const input = document.createElement('input');
    input.type = 'radio'; input.name = 'voice'; input.value = c.value;
    input.checked = c.value === selected;
    input.addEventListener('change', () => {
      // The two Gemini cards share the pick, but Vertex serves fewer models.
      if (c.value === 'vertex' && chosen.gemini) chosen.gemini.model = vertexModel(s.vertex, chosen.gemini.model);
      syncCard();
      showFields();
      if (c.value === 'vertex' || $('vertex-result').textContent) checkVertex();
    });
    const name = document.createElement('span');
    name.className = 'p-label'; name.textContent = c.label;
    const note = document.createElement('span');
    note.className = 'p-note'; note.textContent = c.note;
    label.append(input, name, note);
    grid.append(label);
  }
}

// renderGrid builds the orchestrator preset radios from the daemon's
// table: native radios in one group, so arrow keys move between them.
function renderGrid(presets, selected) {
  const grid = $('preset-grid');
  grid.textContent = '';
  for (const p of presets || []) {
    const label = document.createElement('label');
    label.className = 'preset';
    const input = document.createElement('input');
    input.type = 'radio'; input.name = 'orch'; input.value = p.id;
    input.checked = p.id === selected;
    input.addEventListener('change', () => { syncCard(); showFields(); });
    const name = document.createElement('span');
    name.className = 'p-label'; name.textContent = p.label;
    const note = document.createElement('span');
    note.className = 'p-note'; note.textContent = p.note;
    label.append(input, name, note);
    grid.append(label);
  }
}

// rememberSaved fills the model inputs from the saved orchestrator (once
// per boot), so switching presets and back keeps them.
function rememberSaved(s, orch) {
  const p = presetById(s.presets, orch);
  if (p && p.kind !== 'native' && s.orchestrator.model && !modelBy[orch]) modelBy[orch] = { value: s.orchestrator.model, auto: false };
  if (orch === 'other' && !$('compat-name').value) {
    $('compat-name').value = s.orchestrator.provider;
    $('compat-base').value = s.orchestrator.base_url || '';
  }
  const g = presetById(s.presets, 'gemini');
  const a = presetById(s.presets, 'anthropic');
  if (a && !$('anthropic-model').value) $('anthropic-model').value = orch === 'anthropic' && s.orchestrator.model ? s.orchestrator.model : a.default_model || '';
  if (!$('gemini-model').value) $('gemini-model').value = orch === 'gemini' && s.orchestrator.model ? s.orchestrator.model : (g && g.default_model) || '';
  if (p && p.reuse_offered && p.reuse_active) $('openai-reuse').checked = true;
}

function choices() {
  const o = document.querySelector('input[name=orch]:checked');
  const v = document.querySelector('input[name=voice]:checked');
  return { voice: v ? v.value : 'gemini', orch: o ? o.value : 'gemini' };
}

function currentPreset() {
  return state ? presetById(state.presets, choices().orch) : null;
}

function showFields() {
  const { voice, orch } = choices();
  const f = vertexFields(voice, orch);
  const pn = panel(currentPreset(), $('openai-reuse').checked);
  $('openai-field').classList.toggle('hidden', voice !== 'openai');
  $('gemini-field').classList.toggle('hidden', !f.geminiNeeded);
  $('gemini-orch-only').classList.toggle('hidden', voice === 'gemini' || orch !== 'gemini');
  $('vertex-voice-wait').classList.toggle('hidden', voice !== 'vertex' || vertexOK);
  $('gemini-orch-field').classList.toggle('hidden', pn.field !== 'gemini');
  $('deepseek-field').classList.toggle('hidden', pn.field !== 'deepseek');
  $('anthropic-field').classList.toggle('hidden', pn.field !== 'anthropic');
  $('compat-field').classList.toggle('hidden', pn.field !== 'compat');
  $('vertex-orch-field').classList.toggle('hidden', !f.orchModel);
  $('vertex-block').classList.toggle('hidden', !f.block);
  $('vertex-location-field').classList.toggle('hidden', !f.location);
  $('gemini-not-needed').classList.toggle('hidden', f.geminiNeeded);
  refreshVoicePick();
  if (orch !== shownPreset) switchPreset(orch);
  renderSummary();
}

// switchPreset shows the fields of the newly selected preset: its model
// comes back from modelBy, and its models are listed when they can be.
function switchPreset(orch) {
  const prev = shownPreset;
  shownPreset = orch;
  const pp = state && presetById(state.presets, prev);
  if (pp && pp.kind !== 'native') modelBy[prev] = { value: $('compat-model').value, auto: $('compat-model').dataset.auto === '1' };
  const p = currentPreset();
  if (!p) return;
  if (p.id === 'gemini') { loadGeminiModels(); return; }
  if (p.id === 'anthropic') { if (state.keys.anthropic || anthropicValid) loadAnthropicModels(); return; }
  if (p.kind === 'native') return;
  const m = modelBy[orch] || { value: '', auto: false };
  $('compat-model').value = m.value;
  $('compat-model').dataset.auto = m.auto ? '1' : '';
  $('compat-key').value = '';
  compatKeyValid = false;
  findSeq++;
  fillModels([], false);
  for (const id of ['compat-models-status', 'probe-result', 'compat-result', 'local-status']) setResult($(id), '', '');
  $('probe-tools').disabled = !m.value;
  renderCompatPanel();
  if (autoFind(p, { reuse: $('openai-reuse').checked })) findModels();
}

// renderCompatPanel shows only what the selected OpenAI-compatible
// preset needs: a key or nothing, the base URL read-only under Details.
function renderCompatPanel() {
  const p = currentPreset();
  if (!p || p.kind === 'native') return;
  const reuse = $('openai-reuse').checked;
  const pn = panel(p, reuse);
  const intro = $('preset-intro');
  intro.textContent = '';
  if (!pn.other) {
    intro.append(p.label + ': ' + p.note + (p.local ? '. Small local models may not handle tool calling well: use Test tool calling. ' : '. '));
    if (p.docs_url) {
      const a = document.createElement('a');
      a.href = p.docs_url; a.target = '_blank'; a.rel = 'noopener';
      a.textContent = p.local ? 'Get ' + p.label + ' →' : 'Get a key →';
      intro.append(a);
    }
  } else {
    intro.append('Any endpoint that speaks the OpenAI chat completions API. Pick a model that supports tool calling.');
  }
  $('reuse-field').classList.toggle('hidden', !pn.reuseOffered);
  $('compat-other').classList.toggle('hidden', !pn.other);
  $('compat-key-field').classList.toggle('hidden', !pn.askKey);
  $('compat-key-label').textContent = keyLabel(p);
  $('compat-key-saved').classList.toggle('hidden', !pn.keySaved);
  $('compat-details').classList.toggle('hidden', !pn.details);
  $('compat-base-shown').textContent = p.base_url || '';
  $('compat-saved-as').textContent = savedAs(p, reuse);
  $('find-models').textContent = pn.findLabel;
  $('local-status').classList.toggle('hidden', !pn.local);
}

// Gemini orchestrator: the models the Gemini key can run.
let geminiSeq = 0;
async function loadGeminiModels() {
  if (!state || (!state.keys.gemini && !$('gemini-key').value.trim())) {
    setResult($('gemini-models-status'), '', '');
    return;
  }
  const mine = ++geminiSeq;
  setResult($('gemini-models-status'), 'wait', 'Listing the models this key can use…');
  const r = await api('gemini-models', { key: $('gemini-key').value.trim() }).catch(() => null);
  if (mine !== geminiSeq) return;
  const st = geminiStatus(r);
  setResult($('gemini-models-status'), st.kind, st.text);
  const ids = (r && r.models) || [];
  fillPick($('gemini-model-pick'), [{ label: '', ids }], $('gemini-model').value, 'Choose from this key\'s models');
  const list = $('gemini-models');
  list.textContent = '';
  for (const id of ids) { const o = document.createElement('option'); o.value = id; list.appendChild(o); }
}

// Anthropic orchestrator: the models the key can use (free), and the
// tool-calling test (one small billed request, only on a click).
let anthropicSeq = 0;
async function loadAnthropicModels() {
  const status = $('anthropic-models-status');
  if (!state || (!state.keys.anthropic && !$('anthropic-key').value.trim())) {
    setResult(status, 'fail', 'Paste the Anthropic key above first. You can still type the model name.');
    return;
  }
  const mine = ++anthropicSeq;
  $('anthropic-find').disabled = true;
  setResult(status, 'wait', 'Listing the models this key can use…');
  const r = await api('anthropic-models', { key: $('anthropic-key').value.trim() }).catch(() => null);
  if (mine !== anthropicSeq) return;
  $('anthropic-find').disabled = false;
  const st = anthropicModelsLine(r);
  setResult(status, st.kind, st.text);
  const ids = (r && !r.error && r.models ? r.models : []).map((m) => m.id);
  const list = $('anthropic-models');
  list.textContent = '';
  for (const id of ids) { const o = document.createElement('option'); o.value = id; list.appendChild(o); }
  if (!$('anthropic-model').value.trim() && r && r.suggested) $('anthropic-model').value = r.suggested;
  fillPick($('anthropic-model-pick'), [{ label: '', ids }], $('anthropic-model').value, 'Choose from this key\'s models');
  $('anthropic-probe').disabled = !$('anthropic-model').value.trim();
  renderSummary();
}

async function probeAnthropic() {
  const btn = $('anthropic-probe');
  const out = $('anthropic-probe-result');
  btn.disabled = true;
  setResult(out, 'wait', 'Asking the model to call a tool…');
  const r = await api('anthropic-probe', { key: $('anthropic-key').value.trim(), model: $('anthropic-model').value.trim() }).catch(() => null);
  const st = probeStatus(r);
  setResult(out, st.kind, st.text);
  btn.disabled = !$('anthropic-model').value.trim();
}

// fillPick fills a model <select> (hidden when empty); picking one puts
// it in the text input, which stays editable.
function fillPick(sel, groups, current, prompt) {
  sel.textContent = '';
  const n = groups.reduce((k, g) => k + g.ids.length, 0);
  sel.classList.toggle('hidden', n === 0);
  if (!n) return;
  const first = document.createElement('option');
  first.value = ''; first.textContent = prompt + ' (' + n + ')';
  sel.appendChild(first);
  for (const g of groups) {
    const parent = g.label ? document.createElement('optgroup') : sel;
    if (g.label) { parent.label = g.label; sel.appendChild(parent); }
    for (const id of g.ids) {
      const o = document.createElement('option');
      o.value = id; o.textContent = id;
      parent.appendChild(o);
    }
  }
  sel.value = groups.some((g) => g.ids.includes(current)) ? current : '';
}

// The voice pickers: once the chosen card's key checks out, "You'll
// talk to …" with the model (from the key's Live models) and the voice.
let voiceSeq = 0;
function voiceCtx() {
  return {
    vertexOK, geminiValid, openaiValid,
    geminiSaved: !!(state && state.keys.gemini), geminiTyped: !!$('gemini-key').value.trim(),
    openaiSaved: !!(state && state.keys.openai), openaiTyped: !!$('openai-key').value.trim(),
  };
}

async function refreshVoicePick() {
  const voice = choices().voice;
  const box = $('voice-pick');
  $('vertex-voice-wait').classList.toggle('hidden', voice !== 'vertex' || vertexOK);
  if (!state || !pickReady(voice, voiceCtx())) { box.classList.add('hidden'); voiceSeq++; return; }
  const fam = family(voice);
  const pick = pickOf(state, voice);
  if (!chosen[fam]) chosen[fam] = { model: pick.model, voice: pick.voice };
  fillVoiceNames(pick.voices, chosen[fam].voice);
  box.classList.remove('hidden');
  showTalkLine();
  const key = voice === 'gemini' ? $('gemini-key').value.trim() : voice === 'openai' ? $('openai-key').value.trim() : '';
  const cacheKey = voice + '|' + key;
  let r = voiceLists[cacheKey];
  if (!r) {
    const mine = ++voiceSeq;
    fillVoiceModels([chosen[fam].model], chosen[fam].model);
    setResult($('voice-models-status'), 'wait', 'Listing the Live models…');
    r = await api('voice-models', { provider: voice, key }).catch(() => null);
    if (mine !== voiceSeq || choices().voice !== voice) return;
    if (r && !r.error) voiceLists[cacheKey] = r;
  }
  fillVoiceModels(modelOptions(r, chosen[fam].model), chosen[fam].model);
  const st = modelsLine(r);
  setResult($('voice-models-status'), st.kind, st.text);
}

function fillVoiceModels(ids, current) {
  const sel = $('voice-model');
  sel.textContent = '';
  for (const id of ids) {
    const o = document.createElement('option');
    o.value = id; o.textContent = id;
    sel.appendChild(o);
  }
  sel.value = current;
}

function fillVoiceNames(names, current) {
  const sel = $('voice-name');
  sel.textContent = '';
  for (const n of names) {
    const o = document.createElement('option');
    o.value = n; o.textContent = n;
    sel.appendChild(o);
  }
  sel.value = current;
}

function showTalkLine() {
  const voice = choices().voice;
  const c = chosen[family(voice)];
  if (c) $('voice-line').textContent = talkLine(voice, c.model, c.voice);
  renderSummary();
}

// Live check of the Vertex block: the key (or the saved one, for its own
// project), the project and, for the voice, the location.
let vertexTimer = null;
let vertexSeq = 0;
function checkVertex(keepPick) {
  clearTimeout(vertexTimer);
  const out = $('vertex-result');
  const voice = choices().voice === 'vertex';
  const c = chosen.gemini;
  const body = validateBody({ key: $('vertex-key').value, project: $('vertex-project').value, location: $('vertex-location').value, voice, model: c && c.model }, state);
  const mine = ++vertexSeq;
  // A new model is checked with the pickers left in place.
  if (!keepPick) { vertexOK = false; refreshVoicePick(); }
  if (!body) { setResult(out, '', ''); return; }
  if (body.wait) { setResult(out, 'wait', body.wait); return; }
  setResult(out, 'wait', voice ? 'Checking the key, the project and the location…' : 'Checking the key and the project…');
  vertexTimer = setTimeout(async () => {
    const r = await api('validate', body).catch(() => null);
    if (mine !== vertexSeq) return;
    const st = checkLine(r);
    setResult(out, st.kind, st.text);
    const ok = voice && r && r.status !== 'fail';
    if (keepPick && !ok) {
      // The picked model failed at this location: the pickers stay, so
      // another model can be picked.
      setResult($('voice-models-status'), 'fail', st.text);
      return;
    }
    vertexOK = ok;
    refreshVoicePick();
  }, 600);
}

// Live validation: a pasted key is checked (debounced) before saving.
function liveValidate(input, result, kind, extra, onOk) {
  let timer = null;
  let seq = 0;
  input.addEventListener('input', () => {
    clearTimeout(timer);
    const key = input.value.trim();
    if (!key) { setResult(result, '', ''); return; }
    if (key.length < 20 && kind !== 'compat') { setResult(result, 'wait', 'Keep pasting…'); return; }
    setResult(result, 'wait', 'Checking the key…');
    const mine = ++seq;
    timer = setTimeout(async () => {
      const body = Object.assign({ kind, key }, extra ? extra() : {});
      const r = await api('validate', body).catch(() => ({ status: 'fail', detail: 'sheepdog did not answer' }));
      if (mine !== seq) return;
      setResult(result, r.status === 'fail' ? 'fail' : 'ok', r.status === 'fail' ? checkText(r) : '✓ Valid key');
      if (r.status !== 'fail' && onOk) onOk();
    }, 600);
  });
}

async function saveModels() {
  const btn = $('save-models');
  const { voice, orch } = choices();
  const fam = family(voice);
  const pickBody = chosen[fam] && !$('voice-pick').classList.contains('hidden') ? changed(pickOf(state, voice), chosen[fam].model, chosen[fam].voice) : {};
  const body = {
    ...pickBody,
    gemini_key: $('gemini-key').value.trim(), voice, openai_key: $('openai-key').value.trim(),
    orchestrator: orchValue(orch), deepseek_key: $('deepseek-key').value.trim(),
    anthropic_key: $('anthropic-key').value.trim(), anthropic_model: $('anthropic-model').value.trim(), gemini_model: $('gemini-model').value.trim(),
    compat: { name: $('compat-name').value.trim(), base_url: $('compat-base').value.trim(), model: $('compat-model').value.trim(), key: $('compat-key').value.trim(), reuse_key: $('openai-reuse').checked },
    vertex: { key: $('vertex-key').value.trim(), project: $('vertex-project').value.trim(), location: $('vertex-location').value.trim(), model: $('vertex-model').value.trim() },
  };
  btn.disabled = true;
  $('models-msg').textContent = 'Checking the keys…';
  const old = state.boot;
  const r = await api('keys', body).catch((e) => ({ error: String(e) }));
  if (!r.ok) {
    btn.disabled = false;
    $('models-msg').textContent = r.error || 'Not saved.';
    const byEnv = { GEMINI_API_KEY: 'gemini-result', OPENAI_API_KEY: voice === 'openai' ? 'openai-result' : 'compat-result', DEEPSEEK_API_KEY: 'deepseek-result', VERTEX_API_KEY: 'vertex-result', ANTHROPIC_API_KEY: 'anthropic-result' };
    const p = currentPreset();
    if (p && p.kind !== 'native' && p.key_env) byEnv[p.key_env] = 'compat-result';
    for (const c of r.checks || []) {
      const id = byEnv[c.name] || (/gemini/.test(c.name) ? 'gemini-result' : /openai/.test(c.name) ? 'openai-result' : /deepseek/.test(c.name) ? 'deepseek-result' : /anthropic/.test(c.name) ? 'anthropic-result' : 'compat-result');
      if (c.status === 'fail') setResult($(id), 'fail', checkText(c));
    }
    return;
  }
  for (const id of ['gemini-key', 'openai-key', 'deepseek-key', 'anthropic-key', 'compat-key', 'vertex-key']) $(id).value = '';
  geminiValid = false; openaiValid = false; anthropicValid = false;
  $('models-msg').textContent = 'Saved. Restarting sheepdog with your keys…';
  const reloaded = await waitForReload(old, 20000);
  btn.disabled = false;
  if (!reloaded) { $('models-msg').textContent = 'Saved, but sheepdog did not come back yet. Run `sheepdog status` in a terminal.'; return; }
  // The voice connects in the background: give it a few seconds.
  for (let i = 0; i < 16 && state.voice.status !== 'connected' && !state.voice.problem; i++) {
    await new Promise((res) => setTimeout(res, 500));
    await loadState();
  }
  $('models-msg').textContent = state.voice.status === 'connected' ? 'Saved ✓ The voice is connected.' : 'Saved.';
  runChecks();
}

async function runChecks() {
  const list = $('checks');
  list.textContent = '';
  $('checks-state').className = 'state'; $('checks-state').textContent = 'running…';
  const r = await api('checks').catch(() => ({ checks: [] }));
  let fails = 0;
  for (const c of r.checks || []) {
    if (c.status === 'skip') continue;
    if (c.status === 'fail') fails++;
    const li = document.createElement('li');
    li.className = c.status;
    const icon = document.createElement('span');
    icon.className = 'icon'; icon.setAttribute('aria-label', c.status);
    icon.textContent = c.status === 'ok' ? '✓' : c.status === 'fail' ? '✗' : '!';
    const text = document.createElement('span');
    text.textContent = c.name + ': ' + c.detail;
    li.append(icon, text);
    if (c.fix && c.status !== 'ok') {
      const fix = document.createElement('span');
      fix.className = 'fix'; fix.textContent = 'Fix: ' + c.fix;
      li.append(fix);
    }
    list.append(li);
  }
  $('checks-state').className = fails ? 'state fail' : 'state ok';
  $('checks-state').textContent = fails ? fails + ' to fix' : '✓ all good';
}

async function saveService() {
  if (!$('service-on').checked) { $('service-msg').textContent = 'Not installed. Start it yourself with sheepdog start.'; return; }
  $('save-service').disabled = true;
  $('service-msg').textContent = 'Handing sheepdog over to the login service (about 10 seconds)…';
  const old = state.boot;
  const r = await api('service', { install: true }).catch((e) => ({ error: String(e) }));
  if (r.error) { $('service-msg').textContent = r.error; $('save-service').disabled = false; return; }
  if (r.handoff) await waitForReload(old, 30000);
  await loadState();
  if (!state.service.installed) { $('service-msg').textContent = 'The service did not install: see `sheepdog logs`.'; $('save-service').disabled = false; }
}

// setShell saves the "Shell from your phone" switch (web.shell); it
// applies at once, with no restart.
async function setShell(on) {
  const box = $('shell-on');
  box.disabled = true;
  const r = await api('shell', { enable: on }).catch((e) => ({ error: String(e) }));
  box.disabled = false;
  if (r.error) { box.checked = !on; $('shell-note').textContent = shellSaved(r); return; }
  await loadState();
}

async function setPhone(enable, msgEl) {
  msgEl.textContent = enable ? 'Turning phone access on…' : 'Saving…';
  const old = state.boot;
  const r = await api('phone', { enable }).catch((e) => ({ error: String(e) }));
  if (r.error) { msgEl.textContent = r.error; return; }
  await waitForReload(old, 20000);
  msgEl.textContent = '';
}

// The one-time pairing code: GET /api/pair says when it expires, the QR
// shows the same code; fresh asks for a new one.
let codeExpires = '';
async function showCode(fresh) {
  const r = await fetch('/api/pair' + (fresh ? '?new=1' : ''), { cache: 'no-store' }).then((x) => x.json()).catch(() => ({}));
  codeExpires = r.expires || '';
  $('qr').src = '/api/pair/qr.svg?t=' + Date.now();
  tickCode();
}

function tickCode() {
  const left = countdown(codeExpires, Date.now());
  const el = $('code-expiry');
  if (!el) return;
  el.textContent = left ? 'Works once, for ' + left + '.' : 'This code has expired.';
  $('qr').classList.toggle('expired', !left);
}

// Devices: the paired phones and browsers, each with rename and revoke.
async function loadDevices() {
  const r = await fetch('/api/devices', { cache: 'no-store' }).then((x) => x.json()).catch(() => null);
  if (!r) return;
  const list = $('devices');
  const key = JSON.stringify(r.devices.map((d) => [d.id, d.name, d.last_seen]));
  if (list.dataset.key === key) return;
  list.dataset.key = key;
  list.textContent = '';
  for (const d of r.devices) {
    const li = document.createElement('li');
    const name = document.createElement('span');
    name.className = 'name'; name.textContent = d.name;
    const meta = document.createElement('span');
    meta.className = 'meta'; meta.textContent = deviceKind(d) + ' · ' + lastSeen(d, Date.now());
    const rename = document.createElement('button');
    rename.textContent = 'Rename'; rename.setAttribute('aria-label', 'Rename ' + d.name);
    rename.addEventListener('click', async () => {
      const n = prompt('Name for this device', d.name);
      if (!n || !n.trim()) return;
      await deviceCall('PATCH', '/api/devices/' + encodeURIComponent(d.id), { name: n.trim() });
    });
    const revoke = document.createElement('button');
    revoke.textContent = 'Revoke'; revoke.setAttribute('aria-label', 'Revoke ' + d.name);
    revoke.addEventListener('click', async () => {
      if (!confirm('Revoke ' + d.name + '? It disconnects now and must scan a new code to come back.')) return;
      await deviceCall('DELETE', '/api/devices/' + encodeURIComponent(d.id), {});
    });
    li.append(name, meta, rename, revoke);
    list.append(li);
  }
  $('devices-empty').classList.toggle('hidden', r.devices.length > 0);
}

async function deviceCall(method, path, body) {
  const r = await fetch(path, { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }).then((x) => x.json()).catch((e) => ({ error: String(e) }));
  $('phone-msg2').textContent = r.error || '';
  $('devices').dataset.key = '';
  await loadDevices();
  return r;
}

function stopDemoOnClose() {
  if (state && state.demo && state.demo.running) {
    fetch('/api/setup/demo', { method: 'POST', keepalive: true, headers: { 'Content-Type': 'application/json' }, body: '{"action":"stop"}' });
  }
}

// Find models: GET {base}/models through the daemon, and fill the Model
// input's datalist. Free; it also runs once after the compat key checks out.
let findSeq = 0;
// compatBody is the preset, name, base URL, key and reuse choice of a
// models, probe or validate request.
function compatBody() {
  const p = currentPreset();
  const other = !p || p.kind === 'other';
  return {
    preset: other ? '' : p.id, name: other ? $('compat-name').value.trim() : p.id,
    base_url: other ? $('compat-base').value.trim() : p.base_url,
    key: $('compat-key').value.trim(), reuse: !other && !!p.reuse_offered && $('openai-reuse').checked,
  };
}

async function findModels() {
  const status = $('compat-models-status');
  const p = currentPreset();
  const body = compatBody();
  if (!body.base_url) { setResult(status, 'fail', 'Type the Base URL first (it usually ends in /v1). You can still type the model name.'); return; }
  const mine = ++findSeq;
  $('find-models').disabled = true;
  setResult(status, 'wait', p && p.local ? 'Checking whether ' + p.label + ' is running…' : 'Looking for models…');
  const r = await api('models', body).catch(() => null);
  if (mine !== findSeq) return;
  $('find-models').disabled = false;
  if (p && p.local) {
    const ll = localLine(p, r);
    setResult($('local-status'), ll.kind, ll.text);
  }
  const st = modelsStatus(r);
  const ok = st.kind === 'ok';
  fillModels(ok ? r.models : [], ok && r.tool_info);
  if (p && p.local && !ok) setResult(status, '', '');
  else setResult(status, st.kind, st.text);
  const fill = ok ? modelToFill($('compat-model').value, $('compat-model').dataset.auto === '1', r) : null;
  if (fill) {
    $('compat-model').value = fill;
    $('compat-model').dataset.auto = '1';
    $('compat-model-pick').value = fill;
    $('probe-tools').disabled = false;
  }
  renderSummary();
}

function fillModels(models, toolInfo) {
  const list = $('compat-models');
  list.textContent = '';
  for (const o of datalistOptions(models)) {
    const opt = document.createElement('option');
    opt.value = o.value;
    if (o.label) opt.label = o.label;
    list.appendChild(opt);
  }
  fillPick($('compat-model-pick'), pickGroups(models, toolInfo), $('compat-model').value, 'Choose from the models found');
}

// Test tool calling: one small request, only on this click.
async function probeTools() {
  const btn = $('probe-tools');
  const out = $('probe-result');
  btn.disabled = true;
  setResult(out, 'wait', 'Asking the model to call a tool…');
  const r = await api('probe', Object.assign(compatBody(), { model: $('compat-model').value.trim() })).catch(() => null);
  const st = probeStatus(r);
  setResult(out, st.kind, st.text);
  btn.disabled = !$('compat-model').value.trim();
}

function init() {
  for (const id of ['anthropic-model', 'gemini-model', 'vertex-model', 'compat-model', 'compat-name']) $(id).addEventListener('input', renderSummary);
  for (const id of ['anthropic-model-pick', 'gemini-model-pick', 'compat-model-pick']) $(id).addEventListener('change', renderSummary);
  $('find-models').addEventListener('click', findModels);
  $('probe-tools').addEventListener('click', probeTools);
  $('compat-model').addEventListener('input', () => {
    $('compat-model').dataset.auto = '';
    $('probe-tools').disabled = !$('compat-model').value.trim();
    setResult($('probe-result'), '', '');
  });
  $('compat-model-pick').addEventListener('change', () => {
    const v = $('compat-model-pick').value;
    if (!v) return;
    $('compat-model').value = v;
    $('compat-model').dataset.auto = '';
    $('probe-tools').disabled = false;
    setResult($('probe-result'), '', '');
  });
  $('gemini-model-pick').addEventListener('change', () => {
    if ($('gemini-model-pick').value) $('gemini-model').value = $('gemini-model-pick').value;
  });
  $('openai-reuse').addEventListener('change', () => {
    renderCompatPanel();
    findSeq++;
    fillModels([], false);
    setResult($('compat-models-status'), '', '');
    if (autoFind(currentPreset(), { reuse: $('openai-reuse').checked, keyValid: compatKeyValid })) findModels();
  });
  for (const id of ['compat-base', 'compat-name']) {
    $(id).addEventListener('input', () => {
      findSeq++;
      $('find-models').disabled = false;
      fillModels([], false);
      setResult($('compat-models-status'), '', '');
      setResult($('probe-result'), '', '');
    });
  }
  liveValidate($('gemini-key'), $('gemini-result'), 'gemini', null, () => { geminiValid = true; refreshVoicePick(); if (choices().orch === 'gemini') loadGeminiModels(); });
  liveValidate($('openai-key'), $('openai-result'), 'openai', null, () => { openaiValid = true; refreshVoicePick(); });
  $('gemini-key').addEventListener('input', () => { geminiValid = false; refreshVoicePick(); });
  $('openai-key').addEventListener('input', () => { openaiValid = false; refreshVoicePick(); });
  $('voice-model').addEventListener('change', () => {
    const voice = choices().voice;
    chosen[family(voice)].model = $('voice-model').value;
    showTalkLine();
    if (voice === 'vertex') checkVertex(true);
  });
  $('voice-name').addEventListener('change', () => {
    chosen[family(choices().voice)].voice = $('voice-name').value;
    showTalkLine();
  });
  liveValidate($('deepseek-key'), $('deepseek-result'), 'deepseek');
  liveValidate($('anthropic-key'), $('anthropic-result'), 'anthropic', null, () => { anthropicValid = true; loadAnthropicModels(); });
  $('anthropic-key').addEventListener('input', () => { anthropicValid = false; });
  $('anthropic-find').addEventListener('click', loadAnthropicModels);
  $('anthropic-probe').addEventListener('click', probeAnthropic);
  $('anthropic-model').addEventListener('input', () => {
    $('anthropic-probe').disabled = !$('anthropic-model').value.trim();
    setResult($('anthropic-probe-result'), '', '');
  });
  $('anthropic-model-pick').addEventListener('change', () => {
    const v = $('anthropic-model-pick').value;
    if (!v) return;
    $('anthropic-model').value = v;
    $('anthropic-probe').disabled = false;
    setResult($('anthropic-probe-result'), '', '');
  });
  liveValidate($('compat-key'), $('compat-result'), 'compat', () => { const b = compatBody(); return { base_url: b.base_url, name: b.name, preset: b.preset }; }, () => { compatKeyValid = true; findModels(); });
  $('compat-key').addEventListener('input', () => { compatKeyValid = false; });
  for (const id of ['vertex-key', 'vertex-project', 'vertex-location']) $(id).addEventListener('input', () => checkVertex(false));
  $('save-models').addEventListener('click', saveModels);
  $('run-checks').addEventListener('click', runChecks);
  $('save-service').addEventListener('click', saveService);
  $('phone-enable').addEventListener('click', () => setPhone(true, $('phone-msg')));
  $('phone-disable').addEventListener('click', () => setPhone(false, $('phone-msg2')));
  $('shell-on').addEventListener('change', (e) => setShell(e.target.checked));
  $('new-code').addEventListener('click', () => showCode(true));
  $('disconnect-all').addEventListener('click', async () => {
    if (!confirm('Disconnect all devices? Every phone and browser will need to scan the code again.')) return;
    const r = await deviceCall('POST', '/api/devices/disconnect-all', {});
    if (!r.error) $('phone-msg2').textContent = 'Disconnected ' + r.count + '.';
  });
  $('reset-pairing').addEventListener('click', async () => {
    if (!confirm('Reset pairing? Every phone and browser will need to scan the code again, and each will show a security warning until it does.')) return;
    const r = await deviceCall('POST', '/api/devices/reset-pairing', {});
    if (!r.error) { $('phone-msg2').textContent = 'Pairing reset: new certificate, new code.'; showCode(false); }
  });
  setInterval(tickCode, 1000);
  $('demo-start').addEventListener('click', async () => {
    $('demo-start').disabled = true;
    $('demo-msg').textContent = 'Starting the demo…';
    const r = await api('demo', { action: 'start' }).catch((e) => ({ error: String(e) }));
    $('demo-start').disabled = false;
    if (r.error) { $('demo-msg').textContent = r.error; return; }
    state.demo = r.demo;
    renderDemo(r.demo);
    window.open(r.demo.url + '/voice', '_blank', 'noopener');
  });
  $('demo-stop').addEventListener('click', async () => {
    const r = await api('demo', { action: 'stop' });
    if (r.demo) { state.demo = r.demo; renderDemo(r.demo); }
  });
  window.addEventListener('pagehide', stopDemoOnClose);
  loadState().then((s) => { if (s && (s.keys.gemini || !s.voice.setup_needed)) runChecks(); });
  // Keep the page current (the phone connecting, the voice coming up).
  setInterval(loadState, 3000);
}

init();
