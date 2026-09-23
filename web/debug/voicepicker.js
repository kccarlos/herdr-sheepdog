// The /debug header's voice-provider picker: pure helpers over the
// GET /api/voice/providers body ({provider, model, providers: [{id,
// label, model, available, reason?, billing?}]}), unit-tested with
// `make test-web`. app.js owns the DOM and the PUT.

// voiceOptions turns the providers into <option> descriptors: an
// unavailable provider is disabled with the reason in its tooltip, a
// per-minute one says it is billed per minute.
export function voiceOptions(data) {
  const providers = (data && Array.isArray(data.providers)) ? data.providers : [];
  return providers.map((p) => {
    const bits = [`${p.label || p.id} (${p.model || '?'})`];
    if (p.billing === 'per_minute') bits.push('billed per minute of open session');
    if (!p.available) bits.push(p.reason || 'not configured');
    return {
      value: p.id,
      text: p.billing === 'per_minute' ? `${p.label || p.id} ($/min)` : (p.label || p.id),
      disabled: !p.available,
      title: bits.join(' · '),
    };
  });
}

// voiceHint is the note beside the picker for the current provider.
export function voiceHint(data) {
  if (!data || !data.provider || data.provider === 'off') return 'voice off';
  const cur = (data.providers || []).find((p) => p.id === data.provider);
  if (cur && cur.billing === 'per_minute') return `${data.model} · billed per minute (closed when idle)`;
  return data.model || '';
}

// billedText renders the running billed seconds of a web.client.stats
// payload ("" when the provider does not bill per minute).
export function billedText(stats) {
  if (!stats || typeof stats.voice_billed_seconds !== 'number' || stats.voice_billed_seconds <= 0) return '';
  const s = stats.voice_billed_seconds;
  const cost = (s / 60) * 0.05;
  return `${s.toFixed(0)} s billed (~$${cost.toFixed(3)})${stats.voice_session_open ? ' · live' : ''}`;
}
