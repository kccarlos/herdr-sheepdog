// Pure keyboard shortcut mapping for /voice. Tested under node via
// shortcuts.test.mjs; imported by app.js.

export function voiceKeyAction(
  event,
  { inField = false, cardOpen = false, shortcutsOpen = false, canStop = false, canMute = false } = {},
) {
  const modified = Boolean(event.metaKey || event.ctrlKey || event.altKey);
  if (cardOpen) return null;
  if (shortcutsOpen) {
    if (!modified && (event.key === '?' || event.key === 'Escape')) return { type: 'close-shortcuts' };
    return null;
  }
  if (event.key === 'Escape') {
    return canStop ? { type: 'stop' } : null;
  }
  if (inField || modified) return null;
  if (event.key === '?') return { type: 'open-shortcuts' };
  if (event.key === ' ') return { type: canStop ? 'stop' : 'ignore', preventDefault: true };
  if (event.key === 'm' || event.key === 'M') return canMute ? { type: 'toggle-mute' } : null;
  return null;
}
