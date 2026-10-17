// The update banner shared by /setup and /voice: a small dismissible line under the header when
// /api/state says a newer sheepdog exists or an upgrade is installed but not running yet
// (docs/dev/update.md). Never spoken; dismissing hides it for that version only.

export const UPDATE_DOC = 'https://github.com/kccarlos/herdr-sheepdog/blob/main/docs/quick-start.md#update';

/** What the banner says for /api/state's `update` object, or null for no banner. */
export function bannerModel(u) {
  if (!u) return null;
  if (u.restart_pending) {
    const to = u.restart_to || 'the new version';
    return {
      key: 'restart:' + (u.restart_to || ''),
      text: u.auto_restart
        ? `sheepdog ${to} is installed. It restarts itself when idle; or run `
        : `sheepdog ${to} is installed. To finish, run `,
      code: 'sheepdog restart',
      link: null,
    };
  }
  if (u.available && u.latest) {
    return { key: 'available:' + u.latest, text: `sheepdog ${u.latest} is available. `, code: null, link: { href: UPDATE_DOC, label: 'How to update' } };
  }
  return null;
}

function dismissed(key) {
  try { return localStorage.getItem('sheepdog.update.dismissed') === key; } catch { return false; }
}
function dismiss(key) {
  try { localStorage.setItem('sheepdog.update.dismissed', key); } catch { /* private window: it just comes back */ }
}

/** Builds the banner element for a model. */
export function buildBanner(doc, m, onDismiss) {
  const el = doc.createElement('div');
  el.id = 'update-banner';
  el.className = 'update-banner';
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
  const b = doc.createElement('button');
  b.type = 'button';
  b.className = 'update-dismiss';
  b.setAttribute('aria-label', 'Dismiss the update notice');
  b.textContent = '×';
  b.addEventListener('click', onDismiss);
  el.append(p, b);
  return el;
}

async function init() {
  let u;
  try {
    const res = await fetch('/api/state', { cache: 'no-store' });
    if (!res.ok) return;
    u = (await res.json()).update;
  } catch { return; }
  const m = bannerModel(u);
  const old = document.getElementById('update-banner');
  if (!m || dismissed(m.key)) { if (old) old.remove(); return; }
  if (old) return;
  const el = buildBanner(document, m, () => { dismiss(m.key); el.remove(); });
  document.querySelector('header')?.after(el);
}

if (typeof document !== 'undefined') {
  init();
  setInterval(init, 5 * 60 * 1000);
}
