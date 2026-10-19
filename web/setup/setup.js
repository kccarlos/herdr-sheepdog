// The setup page (/setup): the five steps of `sheepdog setup`. It talks
// only to /api/setup/* (loopback only, no token); keys are posted once
// and never come back.
import { datalistOptions, modelsStatus, probeStatus } from './models.js';
import { voiceChoice, orchChoice, vertexFields, voiceLine, savedNote, validateBody, checkLine } from './vertex.js';

const $ = (id) => document.getElementById(id);
let state = null;
let demoTimer = null;
let renderedBoot = null; // the choices are set from the daemon once per boot, not on every poll
let vertexOK = false; // the Vertex block's last check passed for the voice

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
    const voice = voiceChoice(s.voice);
    for (const r of document.querySelectorAll('input[name=voice]')) r.checked = r.value === voice;
    const orch = orchChoice(s.orchestrator.provider);
    for (const r of document.querySelectorAll('input[name=orch]')) r.checked = r.value === orch;
    if (orch === 'compat' && !$('compat-name').value) $('compat-name').value = s.orchestrator.provider;
    renderVertex(s, voice, orch);
    showAdvancedFields();
  }

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
      $('qr').src = '/api/setup/qr.svg?b=' + encodeURIComponent(s.boot);
      $('qr').dataset.boot = s.boot;
    }
    setResult($('phone-connected'), p.connected ? 'ok' : 'wait', p.connected ? 'Connected ✓ (' + p.remote + ')' : 'Waiting for the phone…');
    $('addresses').textContent = p.addresses.length ? p.explain + ' (' + p.addresses.map((a) => a.url).join(', ') + ')' : 'No network address found: is this computer on Wi-Fi?';
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

function choices() {
  return { voice: document.querySelector('input[name=voice]:checked').value, orch: document.querySelector('input[name=orch]:checked').value };
}

function showAdvancedFields() {
  const { voice, orch } = choices();
  const f = vertexFields(voice, orch);
  $('openai-field').classList.toggle('hidden', voice !== 'openai');
  $('deepseek-field').classList.toggle('hidden', orch !== 'deepseek');
  $('compat-field').classList.toggle('hidden', orch !== 'compat');
  $('vertex-orch-field').classList.toggle('hidden', !f.orchModel);
  $('vertex-block').classList.toggle('hidden', !f.block);
  $('vertex-location-field').classList.toggle('hidden', !f.location);
  $('gemini-not-needed').classList.toggle('hidden', f.geminiNeeded);
  showVoiceLine();
  if (voice !== 'gemini' || orch !== 'gemini') $('advanced').open = true;
}

function showVoiceLine() {
  const el = $('vertex-voice-line');
  const on = vertexOK && choices().voice === 'vertex' && state;
  el.classList.toggle('hidden', !on);
  if (on) setResult(el, 'ok', voiceLine(state.vertex));
}

// Live check of the Vertex block: the key (or the saved one, for its own
// project), the project and, for the voice, the location.
let vertexTimer = null;
let vertexSeq = 0;
function checkVertex() {
  clearTimeout(vertexTimer);
  const out = $('vertex-result');
  const voice = choices().voice === 'vertex';
  const body = validateBody({ key: $('vertex-key').value, project: $('vertex-project').value, location: $('vertex-location').value, voice }, state);
  const mine = ++vertexSeq;
  vertexOK = false;
  showVoiceLine();
  if (!body) { setResult(out, '', ''); return; }
  if (body.wait) { setResult(out, 'wait', body.wait); return; }
  setResult(out, 'wait', voice ? 'Checking the key, the project and the location…' : 'Checking the key and the project…');
  vertexTimer = setTimeout(async () => {
    const r = await api('validate', body).catch(() => null);
    if (mine !== vertexSeq) return;
    const st = checkLine(r);
    setResult(out, st.kind, st.text);
    vertexOK = voice && r && r.status !== 'fail';
    showVoiceLine();
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
  const voice = document.querySelector('input[name=voice]:checked').value;
  const orch = document.querySelector('input[name=orch]:checked').value;
  const body = {
    gemini_key: $('gemini-key').value.trim(), voice, openai_key: $('openai-key').value.trim(),
    orchestrator: orch, deepseek_key: $('deepseek-key').value.trim(),
    compat: { name: $('compat-name').value.trim(), base_url: $('compat-base').value.trim(), model: $('compat-model').value.trim(), key: $('compat-key').value.trim() },
    vertex: { key: $('vertex-key').value.trim(), project: $('vertex-project').value.trim(), location: $('vertex-location').value.trim(), model: $('vertex-model').value.trim() },
  };
  btn.disabled = true;
  $('models-msg').textContent = 'Checking the keys…';
  const old = state.boot;
  const r = await api('keys', body).catch((e) => ({ error: String(e) }));
  if (!r.ok) {
    btn.disabled = false;
    $('models-msg').textContent = r.error || 'Not saved.';
    const byEnv = { GEMINI_API_KEY: 'gemini-result', OPENAI_API_KEY: 'openai-result', DEEPSEEK_API_KEY: 'deepseek-result', VERTEX_API_KEY: 'vertex-result' };
    for (const c of r.checks || []) {
      const id = byEnv[c.name] || (/gemini/.test(c.name) ? 'gemini-result' : /openai/.test(c.name) ? 'openai-result' : /deepseek/.test(c.name) ? 'deepseek-result' : 'compat-result');
      if (c.status === 'fail') setResult($(id), 'fail', checkText(c));
    }
    return;
  }
  for (const id of ['gemini-key', 'openai-key', 'deepseek-key', 'compat-key', 'vertex-key']) $(id).value = '';
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

async function setPhone(enable, newToken, msgEl) {
  msgEl.textContent = enable ? 'Turning phone access on…' : 'Saving…';
  const old = state.boot;
  const r = await api('phone', { enable, new_token: newToken }).catch((e) => ({ error: String(e) }));
  if (r.error) { msgEl.textContent = r.error; return; }
  await waitForReload(old, 20000);
  await api('login', {});
  msgEl.textContent = r.token_created ? 'A new token was created (inside the code; never shown).' : '';
}

function stopDemoOnClose() {
  if (state && state.demo && state.demo.running) {
    fetch('/api/setup/demo', { method: 'POST', keepalive: true, headers: { 'Content-Type': 'application/json' }, body: '{"action":"stop"}' });
  }
}

// Find models: GET {base}/models through the daemon, and fill the Model
// input's datalist. Free; it also runs once after the compat key checks out.
let findSeq = 0;
async function findModels() {
  const status = $('compat-models-status');
  const base = $('compat-base').value.trim();
  if (!base) { setResult(status, 'fail', 'Type the Base URL first (it usually ends in /v1). You can still type the model name.'); return; }
  const mine = ++findSeq;
  $('find-models').disabled = true;
  setResult(status, 'wait', 'Looking for models…');
  const r = await api('models', { name: $('compat-name').value.trim(), base_url: base, key: $('compat-key').value.trim() }).catch(() => null);
  if (mine !== findSeq) return;
  $('find-models').disabled = false;
  const st = modelsStatus(r);
  fillModels(st.kind === 'ok' ? r.models : []);
  setResult(status, st.kind, st.text);
}

function fillModels(models) {
  const list = $('compat-models');
  list.textContent = '';
  for (const o of datalistOptions(models)) {
    const opt = document.createElement('option');
    opt.value = o.value;
    if (o.label) opt.label = o.label;
    list.appendChild(opt);
  }
}

// Test tool calling: one small request, only on this click.
async function probeTools() {
  const btn = $('probe-tools');
  const out = $('probe-result');
  btn.disabled = true;
  setResult(out, 'wait', 'Asking the model to call a tool…');
  const r = await api('probe', { name: $('compat-name').value.trim(), base_url: $('compat-base').value.trim(), key: $('compat-key').value.trim(), model: $('compat-model').value.trim() }).catch(() => null);
  const st = probeStatus(r);
  setResult(out, st.kind, st.text);
  btn.disabled = !$('compat-model').value.trim();
}

function init() {
  $('find-models').addEventListener('click', findModels);
  $('probe-tools').addEventListener('click', probeTools);
  $('compat-model').addEventListener('input', () => {
    $('probe-tools').disabled = !$('compat-model').value.trim();
    setResult($('probe-result'), '', '');
  });
  for (const id of ['compat-base', 'compat-name']) {
    $(id).addEventListener('input', () => {
      findSeq++;
      $('find-models').disabled = false;
      fillModels([]);
      setResult($('compat-models-status'), '', '');
      setResult($('probe-result'), '', '');
    });
  }
  liveValidate($('gemini-key'), $('gemini-result'), 'gemini');
  liveValidate($('openai-key'), $('openai-result'), 'openai');
  liveValidate($('deepseek-key'), $('deepseek-result'), 'deepseek');
  liveValidate($('compat-key'), $('compat-result'), 'compat', () => ({ base_url: $('compat-base').value.trim(), name: $('compat-name').value.trim() }), () => findModels());
  for (const r of document.querySelectorAll('input[name=voice], input[name=orch]')) r.addEventListener('change', showAdvancedFields);
  for (const r of document.querySelectorAll('input[name=voice]')) r.addEventListener('change', () => { if (choices().voice === 'vertex' || $('vertex-result').textContent) checkVertex(); });
  for (const id of ['vertex-key', 'vertex-project', 'vertex-location']) $(id).addEventListener('input', checkVertex);
  $('save-models').addEventListener('click', saveModels);
  $('run-checks').addEventListener('click', runChecks);
  $('save-service').addEventListener('click', saveService);
  $('phone-enable').addEventListener('click', () => setPhone(true, false, $('phone-msg')));
  $('phone-disable').addEventListener('click', () => setPhone(false, false, $('phone-msg2')));
  $('new-token').addEventListener('click', () => {
    if (confirm('Make a new token? A phone paired with the old one must scan the new code.')) setPhone(true, true, $('phone-msg2'));
  });
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
  // The token cookie for /voice (when phone access set a token).
  api('login', {}).catch(() => {});
  loadState().then((s) => { if (s && (s.keys.gemini || !s.voice.setup_needed)) runChecks(); });
  // Keep the page current (the phone connecting, the voice coming up).
  setInterval(loadState, 3000);
}

init();
