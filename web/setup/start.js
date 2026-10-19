// Pure helpers of the setup page's starting-point cards and summary bar
// (no DOM): `make test-web`. A card only pre-fills the voice and
// orchestrator radios; nothing is saved until Save.

// startCards are the four starting points, in the order the page shows
// them. voice and orch are the radio values a card selects ('' = Custom,
// which changes nothing).
export const startCards = [
  { id: 'recommended', label: 'Recommended', voice: 'gemini', orch: 'anthropic',
    note: 'The strongest orchestrator: understands your agents best.', need: 'Needs an Anthropic API key (paid).' },
  { id: 'cost', label: 'Cost-effective', voice: 'gemini', orch: 'deepseek',
    note: 'Cost-effective, and it did well in our tests.', need: '' },
  { id: 'free', label: 'Free quick start', voice: 'gemini', orch: 'gemini',
    note: 'One free key for both. Good for trying; can be slow or busy. Upgrade any time.', need: '' },
  { id: 'custom', label: 'Custom', voice: '', orch: '',
    note: 'Pick each part yourself.', need: '' },
];

// cardRadios is the voice and orchestrator a card selects, or null for
// Custom (and unknown ids).
export function cardRadios(id) {
  const c = startCards.find((x) => x.id === id);
  return c && c.voice ? { voice: c.voice, orch: c.orch } : null;
}

// cardFor is the card matching a voice and orchestrator choice, or
// 'custom'. The models inside a choice do not matter: they stay editable.
export function cardFor(voice, orch) {
  const c = startCards.find((x) => x.voice && x.voice === voice && x.orch === orch);
  return c ? c.id : 'custom';
}

// initialChoice is what the page selects on load: with nothing saved
// (the voice has no credentials) the Recommended card, else the saved
// voice and orchestrator, and the card that matches them.
export function initialChoice(state, savedVoice, savedOrch) {
  const hasAnthropic = ((state && state.presets) || []).some((p) => p.id === 'anthropic');
  const fresh = !!(state && state.voice && state.voice.setup_needed);
  if (fresh && hasAnthropic) return { card: 'recommended', voice: 'gemini', orch: 'anthropic' };
  return { card: cardFor(savedVoice, savedOrch), voice: savedVoice, orch: savedOrch };
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

const VOICE_PROVIDER = { gemini: 'Google AI Studio', vertex: 'Google Vertex AI', openai: 'OpenAI' };
const VOICE_COST = {
  gemini: 'free with daily limits',
  vertex: 'billed to your Google Cloud project',
  openai: '$0.05/min of open session',
};
// Anthropic prices per million input/output tokens (docs/models.md).
const ANTHROPIC_COST = [
  [/opus-5-5/, '$4/$20 per million tokens'],
  [/sonnet-5-5/, '$2/$10 per million tokens'],
  [/haiku-5-5/, '$0.10/$0.50 per million tokens'],
];

// voiceCost is the voice's cost line.
export function voiceCost(voice) {
  return VOICE_COST[voice] || 'pay per use';
}

// orchCost is the orchestrator's cost line for a preset and model.
export function orchCost(preset, model) {
  if (!preset) return 'pay per use';
  if (preset.local) return 'free on this computer';
  if (preset.id === 'gemini') return 'free with daily limits';
  if (preset.id === 'vertex') return 'billed to your Google Cloud project';
  if (preset.id === 'anthropic') {
    const hit = ANTHROPIC_COST.find(([re]) => re.test(model || ''));
    return hit ? hit[1] : 'pay per use';
  }
  return 'pay per use';
}

// summary is the bar's text. in: voice, voiceModel, preset (the preset
// object or null), orchModel, otherName (the typed name for Other URL).
export function summary(f) {
  const vModel = f.voiceModel || 'a Live model';
  const p = f.preset;
  const provider = p && p.kind === 'other' ? (f.otherName || 'your server') : (p ? p.label : 'Gemini');
  const oModel = f.orchModel || 'a model of your choice';
  return 'You talk to ' + vModel + ' (' + (VOICE_PROVIDER[f.voice] || 'Google AI Studio') + '). '
    + 'Your agents are handled by ' + oModel + ' (' + provider + '). '
    + 'Cost: ' + voiceCost(f.voice) + ' · ' + orchCost(p, f.orchModel);
}
