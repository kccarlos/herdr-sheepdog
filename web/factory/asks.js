// Factory ask cards, on-demand short-code browser pairing, and automatic
// WebCrypto re-authentication after a daemon restart or 401 (design.md 7.1).
import { enroll, signIn, keyStore } from '../voice/pairing.js';

export const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
export const isOwnerAsk = (a) => Boolean(a && a.gated !== false && a.kind !== 'decision');
export const PAIR_HINT = 'Pair this browser to answer asks: open the Sheepdog menu-bar app on this Mac for a short pairing code, then enter it below.';

export function renderAskCardsHTML(asks, state = {}) {
  const list = Array.isArray(asks) ? asks.filter(isOwnerAsk) : [];
  let html = list.length ? list.map((a) => {
    const busy = state.busyId === a.id;
    const meta = [a.kind && `<span class="chip">${esc(a.kind)}</span>`, a.task && `<code>${esc(a.task)}</code>`].filter(Boolean).join('');
    const cmd = a.command ? `<pre class="ask-cmd"><code>${esc(a.command)}</code></pre>` : '';
    const opts = (a.options || []).map((opt) => {
      const rec = opt === a.recommend;
      return `<button type="button" class="ask-opt${rec ? ' rec' : ''}" data-ask-id="${esc(a.id)}" data-option="${esc(opt)}" aria-label="${esc(rec ? `${opt} (Recommended)` : opt)}"${busy ? ' disabled' : ''}>${esc(opt)}${rec ? ' <span class="rec-tag">Recommended</span>' : ''}</button>`;
    }).join('');
    const err = state.error && (!state.errorAskId || state.errorAskId === a.id) ? `<p class="ask-err" role="alert">${esc(state.error)}</p>` : '';
    return `<article class="ask-card" data-ask-id="${esc(a.id)}" aria-label="${esc(a.question)}">${meta ? `<div class="meta">${meta}</div>` : ''}<p class="ask-q">${esc(a.question)}</p>${cmd}<div class="ask-opts" role="group" aria-label="Answer options">${opts}</div>${err}</article>`;
  }).join('') : '<p class="muted">No open asks right now.</p>';

  if (state.needsPairing) {
    html += `<div class="pair-box" role="region" aria-label="Pair this browser"><p>${esc(state.pairMessage || PAIR_HINT)}</p><form class="pair-form" data-pair-form="1"><label class="muted" for="pair-code">Pairing code</label><div class="pair-row"><input id="pair-code" name="code" type="text" autocomplete="one-time-code" placeholder="XXXX-XXXX" aria-label="Short pairing code"><button type="submit" class="ask-opt rec"${state.pairingBusy ? ' disabled' : ''}>Pair</button></div>${state.pairError ? `<p class="ask-err" role="alert">${esc(state.pairError)}</p>` : ''}</form></div>`;
  }
  return html;
}

export function createAskFlow(deps) {
  let daemonId = '', reauthInFlight = null;
  const resolveId = async () => {
    const r = await deps.fetch('/api/hello', { cache: 'no-store' }).catch(() => null);
    const b = r && r.ok ? await r.json().catch(() => ({})) : {};
    return (daemonId = (b && b.computer && b.computer.id) || daemonId);
  };
  const reauth = () => (reauthInFlight ||= (async () => {
    const did = await resolveId();
    if (!did) return { ok: false, needsPairing: true, message: PAIR_HINT };
    const auth = await signIn(did, deps).catch(() => ({ ok: false, reason: 'error' }));
    if (auth.ok) return { ok: true, daemonId: did };
    if (auth.reason === 'device_revoked' || auth.reason === 'unknown_device') return { ok: false, reason: auth.reason, needsPairing: true, message: 'This browser was removed. ' + PAIR_HINT };
    if (auth.reason === 'no_key') return { ok: false, reason: 'no_key', needsPairing: true, message: PAIR_HINT };
    return { ok: false, reason: auth.reason || 'error' };
  })().finally(() => { reauthInFlight = null; }));
  const postAnswer = async (id, option) => {
    const r = await deps.fetch(`/api/factory/asks/${encodeURIComponent(id)}/answer`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ option }),
    });
    return { r, body: await r.json().catch(() => ({})) };
  };
  return {
    reauth,
    async pairWithCode(code) {
      const clean = String(code || '').trim();
      if (!clean) return { ok: false, message: 'Enter the pairing code from the Sheepdog menu-bar app.' };
      const id = await resolveId();
      const enr = await enroll({ code: clean, daemonId: id }, deps);
      if (!enr.ok) return enr;
      daemonId = enr.daemonId || id;
      const auth = await signIn(daemonId, deps);
      return auth.ok ? { ok: true, daemonId } : { ok: false, message: `Paired, but sign-in failed (${auth.reason || 'error'}).` };
    },
    async answer(ask, option) {
      const id = typeof ask === 'string' ? ask : ask && ask.id;
      let { r, body } = await postAnswer(id, option);
      if (r.ok) return { ok: true, ...body };
      const authNeeded = r.status === 401 || (r.status === 403 && (body.code === 'proof_required' || body.error === 'proof_required'));
      if (!authNeeded) return { ok: false, status: r.status, code: body.code, message: body.error || `Could not answer (${r.status}).` };
      const auth = await reauth();
      if (auth.ok) {
        ({ r, body } = await postAnswer(id, option));
        return r.ok ? { ok: true, ...body } : { ok: false, status: r.status, code: body.code, message: body.error || `Could not answer (${r.status}).` };
      }
      return { ok: false, needsPairing: true, message: auth.message || PAIR_HINT };
    },
  };
}

export function browserAskDeps() {
  return {
    fetch: globalThis.fetch.bind(globalThis), subtle: globalThis.crypto.subtle, now: () => Date.now(),
    ua: globalThis.navigator?.userAgent || '', touch: globalThis.navigator?.maxTouchPoints || 0, ...keyStore(globalThis.indexedDB),
  };
}
