// Pure helpers of the setup page's starting-point cards and summary bar
// (no DOM): `make test-web`. A card only pre-fills the voice and
// orchestrator radios; nothing is saved until Save.

// The cards, the cost lines and the summary template come from the daemon
// (/api/setup/state, start; internal/onboard/start.go), the same table the
// terminal setup prints, so the two never disagree. `info` below is that
// object.

// cardRadios is the voice and orchestrator a card selects, or null for
// Custom (and unknown ids).
export function cardRadios(info, id) {
  const c = info.cards.find((x) => x.id === id);
  return c && c.voice ? { voice: c.voice, orch: c.orch } : null;
}

// cardFor is the card matching a voice and orchestrator choice, or
// 'custom'. The models inside a choice do not matter: they stay editable.
export function cardFor(info, voice, orch) {
  const c = info.cards.find((x) => x.voice && x.voice === voice && x.orch === orch);
  return c ? c.id : 'custom';
}

// initialChoice is what the page selects on load: with nothing saved
// (the voice has no credentials) the Recommended card, else the saved
// voice and orchestrator, and the card that matches them.
export function initialChoice(state, savedVoice, savedOrch) {
  const info = state.start;
  const hasAnthropic = ((state && state.presets) || []).some((p) => p.id === 'anthropic');
  const fresh = !!(state && state.voice && state.voice.setup_needed);
  if (fresh && hasAnthropic) return { card: 'recommended', voice: 'gemini', orch: 'anthropic' };
  return { card: cardFor(info, savedVoice, savedOrch), voice: savedVoice, orch: savedOrch };
}

// nextCard is the card index an arrow key moves to (wrapping), or -1 for
// other keys. Home and End jump to the ends.
export function nextCard(key, index, count) {
  if (count < 1) return -1;
  switch (key) {
    case 'ArrowRight': case 'ArrowDown': return (index + 1) % count;
    case 'ArrowLeft': case 'ArrowUp': return (index - 1 + count) % count;
    case 'Home': return 0;
    case 'End': return count - 1;
    default: return -1;
  }
}

// voiceCost is the voice's cost line.
export function voiceCost(info, voice) {
  return info.voice_costs[voice] || info.pay_per_use;
}

// orchCost is the orchestrator's cost line for a preset and model.
export function orchCost(info, preset, model) {
  if (!preset) return info.pay_per_use;
  if (preset.local) return info.free_local;
  if (preset.id === 'gemini') return info.free_daily;
  if (preset.id === 'vertex') return info.billed;
  if (preset.id === 'anthropic') {
    const hit = info.anthropic_costs.find((r) => (model || '').includes(r.contains));
    if (hit) return hit.cost;
  }
  return info.pay_per_use;
}

// summary is the bar's text. f: voice, voiceModel, preset (the preset
// object or null), orchModel, otherName (the typed name for Other URL).
export function summary(info, f) {
  const p = f.preset;
  const provider = p && p.kind === 'other' ? (f.otherName || info.other_fallback) : (p ? p.label : 'Gemini');
  const vals = {
    voice_model: f.voiceModel || info.voice_model_fallback,
    voice_provider: info.voice_providers[f.voice] || info.voice_providers.gemini,
    orch_model: f.orchModel || info.orch_model_fallback,
    orch_provider: provider,
    voice_cost: voiceCost(info, f.voice),
    orch_cost: orchCost(info, p, f.orchModel),
  };
  return info.template.replace(/\{(\w+)\}/g, (_, k) => vals[k]);
}
