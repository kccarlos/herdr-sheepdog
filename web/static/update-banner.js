// The update banner shared by /setup and /voice: a small dismissible line under the header when
// /api/state says a newer sheepdog exists or an upgrade is installed but not running yet
// (docs/dev/update.md). Its Update button is POST /api/update (what `sheepdog update` does); the
// page reloads once the daemon runs the new version. A source checkout gets the command instead.
// Never spoken; dismissing hides it for that version only.

export const UPDATE_DOC = 'https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/quick-start.md#update';

/** What the banner says for /api/state's `update` object, or null for no banner. */
export function bannerModel(u) {
  if (!u) return null;
  const to = u.restart_to || u.latest || 'the new version';
  if (u.state === 'installing') return { key: 'installing:' + to, text: `Installing sheepdog ${to}… Sheepdog restarts when it is done.` };
  if (u.state === 'failed') return { key: 'failed:' + to, text: `The update to ${to} did not finish: ${u.error || 'no reason given'}. `, action: 'Try again' };
  if (u.restart_pending) return { key: 'restart:' + (u.restart_to || ''), text: `sheepdog ${to} is installed. `, action: 'Restart now' };
  if (!u.available || !u.latest) return null;
  if (u.can_install) return { key: 'available:' + u.latest, text: `sheepdog ${u.latest} is available. `, action: 'Update' };
  // A source checkout: the command, or the docs.
  return { key: 'available:' + u.latest, text: `sheepdog ${u.latest} is available. ${u.how ? 'To update, run ' : ''}`, code: u.how || null, link: u.how ? null : { href: UPDATE_DOC, label: 'How to update' } };
}

/** The button: POST /api/update, then follow /api/state until the daemon runs a new version. */
export async function startUpdate(doc, current, { fetchFn = fetch, reload = () => location.reload(), wait = (ms) => new Promise((r) => setTimeout(r, ms)), tries = 300 } = {}) {
  let u;
  try {
    const res = await fetchFn('/api/update', { method: 'POST' });
    const body = await res.json().catch(() => ({}));
    u = res.ok ? body : { state: 'failed', error: body.error || res.statusText };
  } catch (e) { u = { state: 'failed', error: String(e.message || e) }; }
  current = current || u.current;
  applyUpdateBanner(doc, { current, ...u });
  for (let i = 0; i < tries && u.state === 'installing'; i++) {
    await wait(2000);
    try {
      const res = await fetchFn('/api/state', { cache: 'no-store' });
      if (!res.ok) continue;
      u = (await res.json()).update || {};
    } catch { continue; } // restarting
    if (u.current && u.current !== current) return reload();
    if (u.state !== 'installing') applyUpdateBanner(doc, u);
  }
}

function dismissed(key, storage = typeof localStorage !== 'undefined' ? localStorage : null) {
  try { return Boolean(storage && storage.getItem('sheepdog.update.dismissed') === key); } catch { return false; }
}
function dismiss(key, storage = typeof localStorage !== 'undefined' ? localStorage : null) {
  try { if (storage) storage.setItem('sheepdog.update.dismissed', key); } catch { /* private window: it just comes back */ }
}

/** Builds the banner element for a model. */
export function buildBanner(doc, m, onDismiss, onUpdate) {
  const el = doc.createElement('div');
  el.id = 'update-banner';
  el.className = 'update-banner';
  el.dataset.key = m.key;
  el.setAttribute('role', 'status');
  const p = doc.createElement('span');
  p.append(m.text);
  if (m.code) {
    const c = doc.createElement('code');
    c.textContent = m.code;
    p.append(c, '.');
  }
  if (m.link) {
    const a = doc.createElement('a');
    a.href = m.link.href;
    a.target = '_blank';
    a.rel = 'noopener';
    a.textContent = m.link.label;
    p.append(a);
  }
  if (m.action) {
    const u = doc.createElement('button');
    u.type = 'button';
    u.className = 'update-now';
    u.textContent = m.action;
    u.addEventListener('click', () => { u.disabled = true; onUpdate(); });
    p.append(u);
  }
  const b = doc.createElement('button');
  b.type = 'button';
  b.className = 'update-dismiss';
  b.setAttribute('aria-label', 'Dismiss the update notice');
  b.textContent = '×';
  b.addEventListener('click', onDismiss);
  el.append(p, b);
  return el;
}

/** Applies /api/state's `update` object immediately, replacing or clearing an older banner. */
export function applyUpdateBanner(doc, u, storage) {
  const m = bannerModel(u);
  const old = doc.getElementById('update-banner');
  if (!m || dismissed(m.key, storage)) {
    if (old) old.remove();
    return;
  }
  if (old && old.dataset && old.dataset.key === m.key) return;
  if (old) old.remove();
  const el = buildBanner(doc, m, () => { dismiss(m.key, storage); el.remove(); }, () => startUpdate(doc, u.current));
  doc.querySelector('header')?.after(el);
}

export async function refreshUpdateBanner() {
  if (typeof document === 'undefined') return;
  let u;
  try {
    const res = await fetch('/api/state', { cache: 'no-store' });
    if (!res.ok) return;
    u = (await res.json()).update;
  } catch { return; }
  applyUpdateBanner(document, u);
}

if (typeof document !== 'undefined') {
  refreshUpdateBanner();
  setInterval(refreshUpdateBanner, 5 * 60 * 1000);
}
