// Factory ask cards, on-demand short-code browser pairing, and automatic
// WebCrypto re-authentication after a daemon restart or 401 (design.md 7.1).
import { enroll, signIn, keyStore } from '../voice/pairing.js';

export const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);
export const isOwnerAsk = (a) => Boolean(a && a.gated !== false && a.kind !== 'decision');
export const PAIR_HINT = 'Changes from this page need a paired browser, once. On this Mac, open the Sheepdog menu-bar app (its icon is in the menu bar), choose Pair browser…, and type the code it shows.';
const PAIR_WRONG = 'That code did not work. It was mistyped, already used, or older than 5 minutes. Choose Pair browser… again for a new code.';
export const ERR_ICO = '<svg class="ico" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 4l9 16H3z"></path><path d="M12 10v4"></path><path d="M12 17v.5"></path></svg>';
export const OK_ICO = '<svg class="ico" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 12l5 5 9-10"></path></svg>';

export function pairBoxHTML(state = {}, id = 'pair-code') {
  if (state.pairDone) return `<section class="pair-box" role="region" aria-label="pair this browser"><h3>pair this browser</h3><p class="set-ok" role="status">${OK_ICO}<span>Paired. Sending what you asked for…</span></p></section>`;
  const cid = esc(id), err = state.pairError ? `<p class="ask-err" role="alert">${ERR_ICO}<span>${esc(state.pairError)}</span></p>` : '';
  return `<section class="pair-box" role="region" aria-label="pair this browser"><h3>pair this browser</h3><p>${esc(state.pairMessage || PAIR_HINT)}</p>` +
    `<p class="muted">What you just asked for is kept. It is sent as soon as this browser is paired.</p>` +
    `<form class="pair-form" data-pair-form="1"><label class="pair-lbl" for="${cid}">pairing code</label><div class="pair-row">` +
    `<input id="${cid}" name="code" type="text"${state.pairError ? ' class="bad"' : ''} autocomplete="one-time-code" placeholder="XXXX-XXXX" value="${esc(state.pairCode || '')}">` +
    `<button type="submit" class="ask-opt rec"${state.pairingBusy ? ' disabled' : ''}>${state.pairingBusy ? 'Pairing…' : 'Pair'}</button></div>${err}</form>` +
    `<details><summary>No Sheepdog menu-bar app?</summary><p>The code comes from the Sheepdog menu-bar app. It is not installed on this Mac, or it is not open.</p>` +
    `<a class="ask-opt" href="https://github.com/kccarlos/herdr-sheepdog/releases/latest">Download the Sheepdog menu-bar app</a>` +
    `<p>Open it, then choose Pair browser… from its icon in the menu bar.</p></details></section>`;
}

export function renderAskCardsHTML(asks, state = {}) {
  const list = Array.isArray(asks) ? asks.filter(isOwnerAsk) : [];
  const inCard = state.needsPairing && list.some((a) => a.id === state.pending?.askId);
  const html = list.length ? list.map((a) => {
    const busy = state.busyId === a.id;
    const meta = a.set ? 'setting · the engine asks' : [a.kind && `<span class="chip">${esc(a.kind)}</span>`, a.task && `<code>${esc(a.task)}</code>`].filter(Boolean).join('');
    const cmd = a.command ? `<pre class="ask-cmd"><code>${esc(a.command)}</code></pre>` : '';
    const opts = (a.options || []).map((opt) => {
      const rec = opt === a.recommend;
      return `<button type="button" class="ask-opt${rec ? ' rec' : ''}" data-ask-id="${esc(a.id)}" data-option="${esc(opt)}" aria-label="${esc(rec ? `${opt} (Recommended)` : opt)}"${busy ? ' disabled' : ''}>${esc(opt)}${rec ? ' <span class="rec-tag">Recommended</span>' : ''}</button>`;
    }).join('');
    const err = state.error && (!state.errorAskId || state.errorAskId === a.id) ? `<p class="ask-err" role="alert">${ERR_ICO}<span>${esc(state.error)}</span></p>` : '';
    return `<article class="ask-card" data-ask-id="${esc(a.id)}" aria-label="${esc(a.question)}">${meta ? `<div class="meta">${meta}</div>` : ''}<p class="ask-q">${esc(a.question)}</p>${cmd}<div class="ask-opts" role="group" aria-label="Answer options">${opts}</div>${err}${inCard && state.pending?.askId === a.id ? pairBoxHTML(state) : ''}</article>`;
  }).join('') : '<p class="muted">No open asks right now.</p>';
  return state.needsPairing && !inCard ? html + pairBoxHTML(state) : html;
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
  const post = async (url, payload) => {
    const r = await deps.fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
    return { r, body: await r.json().catch(() => ({})) };
  };
  const fail = (r, b) => ({ ok: false, status: r.status, code: b.code, name: b.name || '', fix: b.fix || '', results: b.results, message: b.error || b.message || `Could not complete request (${r.status}).` });
  const send = async (url, payload) => {
    let { r, body } = await post(url, payload);
    if (r.ok) return { ok: true, ...body };
    const authNeeded = r.status === 401 || (r.status === 403 && (body.code === 'proof_required' || body.error === 'proof_required'));
    if (!authNeeded) return fail(r, body);
    const auth = await reauth();
    if (!auth.ok) return { ok: false, needsPairing: true, message: auth.message || PAIR_HINT };
    ({ r, body } = await post(url, payload));
    return r.ok ? { ok: true, ...body } : fail(r, body);
  };
  return {
    reauth, send,
    async pairWithCode(code) {
      const clean = String(code || '').trim();
      if (!clean) return { ok: false, message: 'Enter the pairing code from the Sheepdog menu-bar app.' };
      let st = 0, id = await resolveId();
      const enr = await enroll({ code: clean, daemonId: id }, { ...deps, fetch: async (u, i) => { const r = await deps.fetch(u, i); st = r.status; return r; } });
      if (!enr.ok) return { ok: false, status: st, message: st === 410 ? PAIR_WRONG : enr.message };
      daemonId = enr.daemonId || id;
      const auth = await signIn(daemonId, deps);
      return auth.ok ? { ok: true, daemonId } : { ok: false, message: `Paired, but sign-in failed (${auth.reason || 'error'}).` };
    },
    answer: (ask, option) => send(`/api/factory/asks/${encodeURIComponent(typeof ask === 'string' ? ask : ask && ask.id)}/answer`, { option }),
  };
}

export function browserAskDeps() {
  return {
    fetch: globalThis.fetch.bind(globalThis), subtle: globalThis.crypto.subtle, now: () => Date.now(),
    ua: globalThis.navigator?.userAgent || '', touch: globalThis.navigator?.maxTouchPoints || 0, ...keyStore(globalThis.indexedDB),
  };
}
